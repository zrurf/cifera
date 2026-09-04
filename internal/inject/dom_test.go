package inject

import (
	"strings"
	"testing"
)

const sampleHTML = `<!DOCTYPE html>
<html>
<head><title>t</title></head>
<body>
<div id="root"><span class="ad">a1</span><span class="ad">a2</span></div>
</body>
</html>`

// 选择器 after + scope=all：应在每个 .ad 实例后注入
func TestInjectBySelectorAfterAll(t *testing.T) {
	out, err := InjectBySelector([]byte(sampleHTML), []Item{
		{Selector: ".ad", Relation: RelationAfter, Scope: ScopeAll, Content: []byte(".x{}"), IsCSS: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if c := strings.Count(s, "<style>.x{}</style>"); c != 2 {
		t.Errorf("期望注入 2 个 style，实际 %d", c)
	}
}

// 选择器 prepend + scope=first：应只注入到首个命中元素
func TestInjectBySelectorPrependFirst(t *testing.T) {
	out, err := InjectBySelector([]byte(sampleHTML), []Item{
		{Selector: "#root", Relation: RelationPrepend, Scope: ScopeFirst, Content: []byte("window.x=1"), IsCSS: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if c := strings.Count(s, "<script>window.x=1</script>"); c != 1 {
		t.Errorf("期望注入 1 个 script，实际 %d", c)
	}
}

// 无效选择器应返回错误
func TestInjectBySelectorInvalidSelector(t *testing.T) {
	_, err := InjectBySelector([]byte(sampleHTML), []Item{
		{Selector: "###", Relation: RelationAfter, Scope: ScopeAll, Content: []byte("x"), IsCSS: true},
	})
	if err == nil {
		t.Fatal("无效选择器应返回错误")
	}
}