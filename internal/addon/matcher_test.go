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
