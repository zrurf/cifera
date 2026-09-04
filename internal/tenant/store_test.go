package tenant

import (
	"testing"
	"time"
)

func testStore() *Store {
	return NewStore([]Registration{
		{ID: "acme", Name: "Acme", Enabled: true, TokenTTL: time.Hour, Secret: "s3cret"},
		{ID: "open", Name: "Open", Enabled: true, TokenTTL: time.Hour}, // 无 secret
		{ID: "off", Name: "Off", Enabled: false, TokenTTL: time.Hour},
	})
}

func TestStoreAuthSecret(t *testing.T) {
	s := testStore()
	// 配置了 secret 的租户必须提供正确 secret
	if _, err := s.Auth("acme", "wrong"); err == nil {
		t.Error("错误 secret 应拒绝")
	}
	tok, err := s.Auth("acme", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if !s.Valid("acme", tok) {
		t.Error("有效 token 应通过校验")
	}
	if s.Valid("open", tok) {
		t.Error("token 不应匹配其他租户")
	}
	// 撤销后失效
	s.Revoke(tok)
	if s.Valid("acme", tok) {
		t.Error("撤销后 token 应失效")
	}
}

func TestStoreNoSecret(t *testing.T) {
	s := testStore()
	tok, err := s.Auth("open", "")
	if err != nil {
		t.Fatal(err)
	}
	if !s.Valid("open", tok) {
		t.Error("无 secret 租户应能签发并校验 token")
	}
}

func TestStoreDisabled(t *testing.T) {
	s := testStore()
	if _, err := s.Auth("off", ""); err == nil {
		t.Error("停用租户应拒绝鉴权")
	}
}