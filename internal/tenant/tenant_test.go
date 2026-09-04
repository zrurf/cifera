package tenant

import "testing"

func TestJarKeyNamespaces(t *testing.T) {
	auto := FromSession("sid-1")
	if auto.Mode != ModeAuto {
		t.Errorf("自动租户模式错误: %s", auto.Mode)
	}
	if got := auto.JarKey("sid-1"); got != "sid-1:sid-1" {
		t.Errorf("自动租户 jarKey 应为 sid:sid，实际 %s", got)
	}
	managed := Tenant{ID: "acme", Mode: ModeManaged}
	if got := managed.JarKey("sid-9"); got != "acme:sid-9" {
		t.Errorf("注册租户 jarKey 应为 acme:sid-9，实际 %s", got)
	}
}

// 无 token 校验器时，即使携带 _cifera_tid 也退化为自动租户，防止伪造
func TestResolveAutoWithoutTokenStore(t *testing.T) {
	r := NewResolver(nil) // 未启用注册租户校验
	get := func(name string) string {
		switch name {
		case CookieTID:
			return "acme"
		case CookieTok:
			return "fake"
		}
		return ""
	}
	got, err := r.Resolve(get, "sid-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != ModeAuto || got.ID != "sid-1" {
		t.Errorf("应退化为自动租户，实际 %+v", got)
	}
}

// token 校验器存在且校验通过时识别为注册租户
func TestResolveManagedValidToken(t *testing.T) {
	r := NewResolver(&fakeStore{ok: true})
	get := func(name string) string {
		switch name {
		case CookieTID:
			return "acme"
		case CookieTok:
			return "good-token"
		}
		return ""
	}
	got, err := r.Resolve(get, "sid-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != ModeManaged || got.ID != "acme" {
		t.Errorf("应识别为注册租户 acme，实际 %+v", got)
	}
}

type fakeStore struct{ ok bool }

func (f *fakeStore) Valid(tenantID, token string) bool { return f.ok }