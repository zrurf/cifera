package addon

import "sync"

// TenantParamResolver 供租户级个性化注入时的参数解析回调。
// 入参为 addon ID；返回：
//   - include：该 addon 是否对当前租户可用（配合 enabled_addons 过滤，为 false 时跳过该 addon）；
//   - tenantID：租户 ID（渲染缓存键）；
//   - params：该租户下的个性化参数；为 nil 表示使用全局渲染结果。
type TenantParamResolver func(addonID string) (include bool, tenantID string, params map[string]any)

// MatchBlock 匹配 block 规则，返回首个匹配项（短路）
func MatchBlock(addons []*LoadedAddon, originalURL string) *MatchResult {
	for _, a := range addons {
		for i := range a.Manifest.Rules {
			rule := &a.Manifest.Rules[i]
			if rule.Action == ActionBlock && rule.Match(originalURL) {
				return &MatchResult{
					Rule:    rule,
					AddonID: a.Manifest.Addon.ID,
				}
			}
		}
	}
	return nil
}

// MatchReplace 匹配 replace 或 replace_content 规则，返回首个匹配项（短路）
func MatchReplace(addons []*LoadedAddon, originalURL string) *MatchResult {
	for _, a := range addons {
		for i := range a.Manifest.Rules {
			rule := &a.Manifest.Rules[i]
			if (rule.Action == ActionReplace || rule.Action == ActionReplaceContent) && rule.Match(originalURL) {
				return &MatchResult{
					Rule:    rule,
					AddonID: a.Manifest.Addon.ID,
				}
			}
		}
	}
	return nil
}

// MatchInject 匹配所有 inject 规则，返回所有匹配项（可叠加注入）。
// 使用全局参数渲染。addon 数 >= 3 时并发匹配。
func MatchInject(addons []*LoadedAddon, originalURL string) []InjectItem {
	return MatchInjectFor(addons, originalURL, nil)
}

// MatchInjectFor 匹配所有 inject 规则；resolver 非空时，对命中的资源按租户参数重渲染。
func MatchInjectFor(addons []*LoadedAddon, originalURL string, resolver TenantParamResolver) []InjectItem {
	if len(addons) < 3 {
		return matchInjectSequential(addons, originalURL, resolver)
	}
	return matchInjectConcurrent(addons, originalURL, resolver)
}

// buildInjectItem 由规则构造注入项，并按租户参数决定渲染内容
func buildInjectItem(a *LoadedAddon, rule *Rule, resolver TenantParamResolver) InjectItem {
	content := rule.resourceContent
	if resolver != nil {
		if _, tenantID, params := resolver(a.Manifest.Addon.ID); params != nil {
			content = rule.ResourceContentFor(tenantID, params)
		}
	}
	return InjectItem{
		Position:     rule.Position,
		Content:      content,
		ResourceType: rule.resourceType,
		AddonID:      a.Manifest.Addon.ID,
		At:           rule.At,
		Relation:     rule.Relation,
		Scope:        rule.Scope,
	}
}

// matchInjectSequential 顺序匹配 inject 规则
func matchInjectSequential(addons []*LoadedAddon, originalURL string, resolver TenantParamResolver) []InjectItem {
	var items []InjectItem
	for _, a := range addons {
		if !addonAvailable(a, resolver) {
			continue
		}
		for i := range a.Manifest.Rules {
			rule := &a.Manifest.Rules[i]
			if rule.Action == ActionInject && rule.Match(originalURL) {
				items = append(items, buildInjectItem(a, rule, resolver))
			}
		}
	}
	return items
}

// matchInjectConcurrent 并发匹配 inject 规则
func matchInjectConcurrent(addons []*LoadedAddon, originalURL string, resolver TenantParamResolver) []InjectItem {
	type addonResult struct {
		items []InjectItem
	}

	results := make([]addonResult, len(addons))
	var wg sync.WaitGroup
	wg.Add(len(addons))

	for idx, a := range addons {
		go func(idx int, a *LoadedAddon) {
			defer wg.Done()
			var items []InjectItem
			if !addonAvailable(a, resolver) {
				results[idx] = addonResult{items: items}
				return
			}
			for i := range a.Manifest.Rules {
				rule := &a.Manifest.Rules[i]
				if rule.Action == ActionInject && rule.Match(originalURL) {
					items = append(items, buildInjectItem(a, rule, resolver))
				}
			}
			results[idx] = addonResult{items: items}
		}(idx, a)
	}

	wg.Wait()

	// 合并结果（保持 addon 顺序）
	var all []InjectItem
	for _, r := range results {
		all = append(all, r.items...)
	}
	return all
}

// addonAvailable 判断 addon 是否对当前解析器上下文可用。
// 无解析器时全部可用；有解析器且返回 include=false 表示该 addon 被租户过滤。
func addonAvailable(a *LoadedAddon, resolver TenantParamResolver) bool {
	if resolver == nil {
		return true
	}
	include, _, _ := resolver(a.Manifest.Addon.ID)
	return include
}
