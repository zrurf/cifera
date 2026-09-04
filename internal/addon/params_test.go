package addon

import (
	"testing"
)

func TestResolveParamsDefaults(t *testing.T) {
	defs := []ParamDef{
		{Name: "theme", Type: "string", Default: "dark"},
		{Name: "limit", Type: "int", Default: 5},
		{Name: "flag", Type: "bool", Default: true},
		{Name: "opt", Type: "string"}, // 无默认，用类型零值
	}
	p, err := ResolveParams(defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p["theme"] != "dark" || p["limit"] != 5 || p["flag"] != true || p["opt"] != "" {
		t.Errorf("默认值解析异常: %+v", p)
	}
}

func TestResolveParamsOverride(t *testing.T) {
	defs := []ParamDef{
		{Name: "limit", Type: "int", Default: 5},
	}
	p, err := ResolveParams(defs, map[string]any{"limit": int64(100)})
	if err != nil {
		t.Fatal(err)
	}
	if p["limit"] != 100 {
		t.Errorf("覆盖值解析异常: %+v", p)
	}
}

func TestResolveParamsTypeError(t *testing.T) {
	defs := []ParamDef{{Name: "limit", Type: "int", Default: 5}}
	if _, err := ResolveParams(defs, map[string]any{"limit": "abc"}); err == nil {
		t.Error("类型不符应报错")
	}
}

func TestResolveParamsBadType(t *testing.T) {
	defs := []ParamDef{{Name: "x", Type: "unknown", Default: 1}}
	if _, err := ResolveParams(defs, nil); err == nil {
		t.Error("不支持的类型应报错")
	}
}

func TestRenderResource(t *testing.T) {
	params := map[string]any{"theme": "light", "limit": 3}
	out, err := RenderResource([]byte(`var t = "{{ param "theme" }}"; var n = {{ param "limit" }};`), params)
	if err != nil {
		t.Fatal(err)
	}
	want := `var t = "light"; var n = 3;`
	if string(out) != want {
		t.Errorf("模板渲染异常: %s", out)
	}
}

func TestRenderResourceNoTemplate(t *testing.T) {
	out, err := RenderResource([]byte(`.x { color: red }`), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `.x { color: red }` {
		t.Errorf("无模板内容应原样返回: %s", out)
	}
}

// 参数缺失时 param func 返回空串，不报错
func TestRenderResourceMissingParam(t *testing.T) {
	out, err := RenderResource([]byte(`var t = "{{ param "nope" }}"`), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `var t = ""` {
		t.Errorf("缺失参数应渲染为空串: %s", out)
	}
}

// ParamsFor：租户覆盖叠加在全局覆盖之上，且不破坏全局结果
func TestLoadedAddonParamsFor(t *testing.T) {
	defs := []ParamDef{
		{Name: "theme", Type: "string", Default: "dark"},
		{Name: "flag", Type: "bool", Default: false},
	}
	global := map[string]any{"theme": "global"}
	a := &LoadedAddon{Manifest: AddonManifest{Params: defs}}
	gp, err := ResolveParams(defs, global)
	if err != nil {
		t.Fatal(err)
	}
	a.Params = gp
	a.globalOverrides = global

	// 无租户覆盖 → 返回全局
	got, err := a.ParamsFor(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["theme"] != "global" {
		t.Errorf("全局参数异常: %+v", got)
	}

	// 租户覆盖优先于全局，其余沿用全局
	tp, err := a.ParamsFor(map[string]any{"theme": "light"})
	if err != nil {
		t.Fatal(err)
	}
	if tp["theme"] != "light" || tp["flag"] != false {
		t.Errorf("租户参数合并异常: %+v", tp)
	}
	// 全局参数不应被租户覆盖改变
	if a.Params["theme"] != "global" {
		t.Errorf("全局参数被污染: %+v", a.Params)
	}
}
