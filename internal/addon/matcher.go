package addon

import "sync"

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

// MatchInject 匹配所有 inject 规则，返回所有匹配项（可叠加注入）
// addon 数 >= 3 时并发匹配，否则顺序匹配，避免小规模下的 goroutine 开销
func MatchInject(addons []*LoadedAddon, originalURL string) []InjectItem {
	if len(addons) < 3 {
		return matchInjectSequential(addons, originalURL)
	}
	return matchInjectConcurrent(addons, originalURL)
}

// matchInjectSequential 顺序匹配 inject 规则
func matchInjectSequential(addons []*LoadedAddon, originalURL string) []InjectItem {
	var items []InjectItem
	for _, a := range addons {
		for i := range a.Manifest.Rules {
			rule := &a.Manifest.Rules[i]
			if rule.Action == ActionInject && rule.Match(originalURL) {
				items = append(items, InjectItem{
					Position:     rule.Position,
					Content:      rule.resourceContent,
					ResourceType: rule.resourceType,
					AddonID:      a.Manifest.Addon.ID,
				})
			}
		}
	}
	return items
}

// matchInjectConcurrent 并发匹配 inject 规则
func matchInjectConcurrent(addons []*LoadedAddon, originalURL string) []InjectItem {
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
			for i := range a.Manifest.Rules {
				rule := &a.Manifest.Rules[i]
				if rule.Action == ActionInject && rule.Match(originalURL) {
					items = append(items, InjectItem{
						Position:     rule.Position,
						Content:      rule.resourceContent,
						ResourceType: rule.resourceType,
						AddonID:      a.Manifest.Addon.ID,
					})
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
