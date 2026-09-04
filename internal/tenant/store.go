package tenant

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// Registration 一个注册租户的配置（来自 config 的 tenants.<id>）
type Registration struct {
	ID            string                    // 租户 ID
	Name          string                    // 名称
	Enabled       bool                      // 是否启用
	TokenTTL      time.Duration             // token 有效期
	Secret        string                    // 可选接入密钥（鉴权阶段）
	AddonParams   map[string]map[string]any // addon_id → 租金参数覆盖
	EnabledAddons []string                  // 可选，限定可用 addon
}

// tokenRecord 一个已签发 token
type tokenRecord struct {
	tenantID  string
	expiresAt time.Time
}

// Store 注册租户 token 的签发与校验。
// token 保存在内存账本，支持按租户下发、校验与撤销。
type Store struct {
	mu            sync.RWMutex
	registrations map[string]*Registration
	tokens        map[string]*tokenRecord
}

// NewStore 依据注册租户列表创建 store
func NewStore(regs []Registration) *Store {
	s := &Store{
		registrations: make(map[string]*Registration, len(regs)),
		tokens:        make(map[string]*tokenRecord),
	}
	for i := range regs {
		r := regs[i]
		if r.TokenTTL <= 0 {
			r.TokenTTL = 24 * time.Hour
		}
		s.registrations[r.ID] = &r
	}
	return s
}

// Registration 返回指定租户的注册信息
func (s *Store) Registration(id string) (*Registration, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.registrations[id]
	return r, ok
}

// Auth 为注册租户签发 token。
// 租户必须存在且启用；配置了 Secret 时要求提供的 secret 一致。
func (s *Store) Auth(tenantID, secret string) (string, error) {
	s.mu.RLock()
	reg, ok := s.registrations[tenantID]
	s.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("租户不存在: %s", tenantID)
	}
	if !reg.Enabled {
		return "", fmt.Errorf("租户已停用: %s", tenantID)
	}
	if reg.Secret != "" && reg.Secret != secret {
		return "", fmt.Errorf("接入密钥错误")
	}

	token, err := newToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.tokens[token] = &tokenRecord{tenantID: tenantID, expiresAt: time.Now().Add(reg.TokenTTL)}
	s.mu.Unlock()
	return token, nil
}

// Valid 校验 token 是否属于指定租户且未过期
func (s *Store) Valid(tenantID, token string) bool {
	if token == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.tokens[token]
	if !ok || rec.tenantID != tenantID {
		return false
	}
	if time.Now().After(rec.expiresAt) {
		return false
	}
	return true
}

// Revoke 撤销指定 token（使其立即失效）
func (s *Store) Revoke(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, token)
}

// RegisteredTenantCount 返回已注册租户数量
func (s *Store) RegisteredTenantCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.registrations)
}

// newToken 生成一个安全随机 token（256 bit）
func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成 token 失败: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
