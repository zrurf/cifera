package addon

import (
	"regexp"
	"testing"
)

func newTestAddon(t *testing.T, id string, rules []Rule) *LoadedAddon {
	t.Helper()
	for i := range rules {
		compiled := make([]*regexp.Regexp, 0, len(rules[i].Patterns))
		for _, p := range rules[i].Patterns {
			re, err := regexp.Compile(p)
			if err != nil {
				t.Fatal(err)
			}
			compiled = append(compiled, re)
		}
		rules[i].compiledPatterns = compiled
	}
	return &LoadedAddon{Manifest: AddonManifest{Addon: AddonMeta{ID: id}, Rules: rules}}
}

func TestMatchBlock(t *testing.T) {
	addons := []*LoadedAddon{
		newTestAddon(t, "a", []Rule{{Action: ActionBlock, Patterns: []string{`^https://example\.com/forbidden`}}}),
	}
	if MatchBlock(addons, "https://example.com/forbidden/page") == nil {
		t.Error("block 规则应命中")
	}
	if MatchBlock(addons, "https://example.com/ok") != nil {
		t.Error("不匹配的 URL 不应命中")
	}
}

func TestMatchReplaceNotBlock(t *testing.T) {
	addons := []*LoadedAddon{
		newTestAddon(t, "a", []Rule{{Action: ActionReplace, Patterns: []string{`^https://example\.com/replace`}}}),
	}
	if MatchReplace(addons, "https://example.com/replace") == nil {
		t.Error("replace 规则应命中")
	}
	if MatchBlock(addons, "https://example.com/replace") != nil {
		t.Error("replace 规则不应被 block 匹配命中")
	}
}

func TestMatchInject(t *testing.T) {
	addons := []*LoadedAddon{
		newTestAddon(t, "a", []Rule{
			{Action: ActionInject, Position: PositionHeadEnd, Patterns: []string{`/page`}},
			{Action: ActionInject, Position: PositionBodyStart, Patterns: []string{`/page`}},
		}),
	}
	items := MatchInject(addons, "https://example.com/page")
	if len(items) != 2 {
		t.Fatalf("应匹配 2 个注入项，实际 %d", len(items))
	}
	if items[0].Position != PositionHeadEnd || items[0].AddonID != "a" {
		t.Errorf("注入项内容异常: %+v", items[0])
	}
}

func testInjectAddon(id string, content string) *LoadedAddon {
	rule := Rule{
		Action: ActionInject, Position: PositionHeadEnd, Patterns: []string{`/page`},
		rawContent:      []byte(content),
		resourceContent: []byte(content),
		resourceType:    ResourceTypeJS,
		tenantCache:     &ruleTenantCache{},
	}
	compiled := make([]*regexp.Regexp, 0, len(rule.Patterns))
	compiled = append(compiled, regexp.MustCompile(rule.Patterns[0]))
	rule.compiledPatterns = compiled
	return &LoadedAddon{
		Manifest: AddonManifest{Addon: AddonMeta{ID: id}, Rules: []Rule{rule}},
		Params:   map[string]any{"theme": "global", "flag": "g"},
	}
}

// 租户参数解析器：注册租户应使用租户级参数重渲染 inject 内容
func TestMatchInjectTenantParams(t *testing.T) {
	a := testInjectAddon("a", `var theme = "{{ param "theme" }}"`)
	items := MatchInjectFor([]*LoadedAddon{a}, "https://example.com/page", func(addonID string) (bool, string, map[string]any) {
		if addonID == "a" {
			return true, "acme", map[string]any{"theme": "light"}
		}
		return true, "", nil
	})
	if len(items) != 1 {
		t.Fatalf("应匹配 1 个注入项，实际 %d", len(items))
	}
	want := `var theme = "light"`
	if string(items[0].Content) != want {
		t.Errorf("租户参数渲染异常: %s, 期望 %s", items[0].Content, want)
	}
	// 缓存应命中同一租户，结果一致
	cached := a.Manifest.Rules[0].ResourceContentFor("acme", map[string]any{"theme": "light"})
	if string(cached) != want {
		t.Errorf("租户渲染缓存异常: %s", cached)
	}
}

// enabled_addons 过滤：resolver 返回 include=false 时跳过该 addon
func TestMatchInjectTenantFiltered(t *testing.T) {
	a1 := testInjectAddon("keep", `x=1`)
	a2 := testInjectAddon("drop", `y=2`)
	items := MatchInjectFor([]*LoadedAddon{a1, a2}, "https://example.com/page", func(addonID string) (bool, string, map[string]any) {
		if addonID == "drop" {
			return false, "", nil
		}
		return true, "", nil
	})
	if len(items) != 1 {
		t.Fatalf("应仅匹配可用 addon，实际 %d", len(items))
	}
	if items[0].AddonID != "keep" {
		t.Errorf("应保留 keep，得到 %s", items[0].AddonID)
	}
}

// 并发匹配（>=3 addon）时，租户解析器返回的只读映射不应产生竞态
func TestMatchInjectTenantParamsConcurrent(t *testing.T) {
	makeAddon := func(id string, theme string) *LoadedAddon {
		return testInjectAddon(id, `var t = "{{ param "theme" }}"`)
	}
	addons := []*LoadedAddon{
		makeAddon("a", "1"),
		makeAddon("b", "2"),
		makeAddon("c", "3"),
	}
	for name, f := range map[string]func() []InjectItem{
		"concurrent": func() []InjectItem {
			return MatchInjectFor(addons, "https://example.com/page", func(id string) (bool, string, map[string]any) {
				return true, "t1", map[string]any{"theme": "t-" + id}
			})
		},
		"sequential": func() []InjectItem {
			return matchInjectSequential(addons, "https://example.com/page", func(id string) (bool, string, map[string]any) {
				return true, "t1", map[string]any{"theme": "t-" + id}
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			items := f()
			if len(items) != 3 {
				t.Fatalf("应匹配 3 个注入项，实际 %d", len(items))
			}
			for _, it := range items {
				want := `var t = "t-` + it.AddonID + `"`
				if string(it.Content) != want {
					t.Errorf("addon %s 租户渲染异常: %s, 期望 %s", it.AddonID, it.Content, want)
				}
			}
		})
	}
}
