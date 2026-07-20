package addon

// MatchBlock 匹配 block 规则，返回首个匹配项
// 若无匹配返回 nil
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

// MatchReplace 匹配 replace 或 replace_content 规则，返回首个匹配项
// 若无匹配返回 nil
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
func MatchInject(addons []*LoadedAddon, originalURL string) []InjectItem {
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
