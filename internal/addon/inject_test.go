package addon

import (
	"regexp"
	"strings"
	"testing"
)

const selectorHTML = `<html><head></head><body><div class="ad">x</div><div class="ad">y</div></body></html>`

func newSelectorAddon(id string, rule Rule) *LoadedAddon {
	compiled := make([]*regexp.Regexp, 0, len(rule.Patterns))
	for _, p := range rule.Patterns {
		compiled = append(compiled, regexp.MustCompile(p))
	}
	rule.compiledPatterns = compiled
	return &LoadedAddon{Manifest: AddonManifest{Addon: AddonMeta{ID: id}, Rules: []Rule{rule}}}
}

// MatchInject 应透传选择器字段
func TestMatchInjectSelectorFields(t *testing.T) {
	a := newSelectorAddon("a", Rule{
		Action: ActionInject, At: ".ad", Relation: RelationAfter, Scope: ScopeAll,
		Patterns: []string{`/page`},
	})
	items := MatchInject([]*LoadedAddon{a}, "https://example.com/page")
	if len(items) != 1 {
		t.Fatalf("应匹配 1 个注入项，实际 %d", len(items))
	}
	if items[0].At != ".ad" || items[0].Relation != RelationAfter || items[0].Scope != ScopeAll {
		t.Errorf("选择器字段未透传: %+v", items[0])
	}
}

// 选择器注入（CSS 实例级，scope=all）
func TestInjectAddonsSelectorCSS(t *testing.T) {
	injects := []InjectItem{{
		At: ".ad", Relation: RelationAfter, Scope: ScopeAll,
		Content: []byte(".x{}"), ResourceType: ResourceTypeCSS, AddonID: "a",
	}}
	out := InjectAddons([]byte(selectorHTML), injects)
	if c := strings.Count(string(out), "<style>.x{}</style>"); c != 2 {
		t.Errorf("期望注入 2 个 style，实际 %d：%s", c, out)
	}
}

// 选择器无效时回退为 body_end 注入，不返回损坏页面
func TestInjectAddonsSelectorFallback(t *testing.T) {
	injects := []InjectItem{{
		At: "###", Relation: RelationAfter, Scope: ScopeAll,
		Content: []byte("window.ok=1"), ResourceType: ResourceTypeJS, AddonID: "a",
	}}
	out := InjectAddons([]byte(selectorHTML), injects)
	if !strings.Contains(string(out), "<script>window.ok=1</script>") {
		t.Errorf("失败应回退注入内容：%s", out)
	}
}

// position 与 at 互斥校验
func TestValidateInjectPositionXorAt(t *testing.T) {
	// 两者都缺省
	r := Rule{Action: ActionInject, resourceType: ResourceTypeJS, resourceContent: []byte("x")}
	m := &AddonManifest{Rules: []Rule{r}}
	if err := validateRules(m, nil); err == nil {
		t.Error("position 与 at 均缺省应报错")
	}
	// 两者都指定
	r2 := Rule{Action: ActionInject, Position: PositionHeadEnd, At: ".a", Relation: RelationAfter, resourceType: ResourceTypeJS, resourceContent: []byte("x")}
	m2 := &AddonManifest{Rules: []Rule{r2}}
	if err := validateRules(m2, nil); err == nil {
		t.Error("position 与 at 同时指定应报错")
	}
}