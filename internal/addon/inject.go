package addon

import (
	"fmt"
	"regexp"

	"github.com/zrurf/cifera/internal/inject"
)

// 定位 HTML 注入位置的正则（与 rewriter/inject.go 保持一致）
var (
	reHeadOpen  = regexp.MustCompile(`(?i)<head[^>]*>`)
	reHeadClose = regexp.MustCompile(`(?i)</head>`)
	reBodyOpen  = regexp.MustCompile(`(?i)<body[^>]*>`)
	reBodyClose = regexp.MustCompile(`(?i)</body>`)
)

// InjectAddons 在 HTML 中注入 addon 的 JS/CSS
// 支持两类位置：文档级 position（head_start 等）与 CSS 选择器注入（at）。
// 位置注入按列表顺序分组注入；JS 用 <script> 包裹，CSS 用 <style> 包裹。
func InjectAddons(html []byte, injects []InjectItem) []byte {
	if len(injects) == 0 {
		return html
	}

	result := html
	var selectorItems []inject.Item
	var positionInjects []InjectItem

	for _, item := range injects {
		if item.At != "" {
			selectorItems = append(selectorItems, buildSelectorItem(item))
		} else {
			positionInjects = append(positionInjects, item)
		}
	}

	if len(selectorItems) > 0 {
		out, err := inject.InjectBySelector(result, selectorItems)
		if err == nil {
			result = out
		} else {
			// 选择器注入失败（如选择器无效）时降级：作为 body_end 位置注入，避免返回损坏页面
			for _, s := range selectorItems {
				positionInjects = append(positionInjects, InjectItem{
					Position:     PositionBodyEnd,
					Content:      s.Content,
					ResourceType: selectorResourceType(s.IsCSS),
				})
			}
		}
	}

	return injectByPosition(result, positionInjects)
}

// buildSelectorItem 将 InjectItem 转换为 inject.Item
func buildSelectorItem(item InjectItem) inject.Item {
	return inject.Item{
		Selector: item.At,
		Relation: inject.Relation(item.Relation),
		Scope:    inject.Scope(item.Scope),
		Content:  item.Content,
		IsCSS:    item.ResourceType == ResourceTypeCSS,
	}
}

// selectorResourceType 将 inject.Item.IsCSS 还原为 addon 资源类型
func selectorResourceType(isCSS bool) ResourceType {
	if isCSS {
		return ResourceTypeCSS
	}
	return ResourceTypeJS
}

// injectByPosition 按文档级 position 分组并注入
func injectByPosition(html []byte, injects []InjectItem) []byte {
	if len(injects) == 0 {
		return html
	}

	// 按 position 分组，保持同 position 内顺序
	grouped := make(map[InjectPosition][][]byte)
	var positions []InjectPosition
	seen := make(map[InjectPosition]bool)

	for _, item := range injects {
		wrapped := wrapResource(item.Content, item.ResourceType)
		if !seen[item.Position] {
			positions = append(positions, item.Position)
			seen[item.Position] = true
		}
		grouped[item.Position] = append(grouped[item.Position], wrapped)
	}

	result := html

	// 按位置优先级（head_start → head_end → body_start → body_end）依次注入
	positionOrder := []InjectPosition{
		PositionHeadStart,
		PositionHeadEnd,
		PositionBodyStart,
		PositionBodyEnd,
	}

	for _, pos := range positionOrder {
		chunks, ok := grouped[pos]
		if !ok {
			continue
		}

		// 合并同一 position 的内容
		var combined []byte
		for _, chunk := range chunks {
			combined = append(combined, chunk...)
		}

		result = injectAtPosition(result, combined, pos)
	}

	return result
}

// wrapResource 将资源内容用 HTML 元素包裹
func wrapResource(content []byte, resType ResourceType) []byte {
	switch resType {
	case ResourceTypeJS:
		return fmt.Appendf(nil, "<script>%s</script>", string(content))
	case ResourceTypeCSS:
		return fmt.Appendf(nil, "<style>%s</style>", string(content))
	default:
		return content
	}
}

// injectAtPosition 在指定位置注入内容
func injectAtPosition(html []byte, content []byte, position InjectPosition) []byte {
	switch position {
	case PositionHeadStart:
		// 在 <head> 标签后注入
		if loc := reHeadOpen.FindIndex(html); loc != nil {
			pos := loc[1]
			return append(html[:pos], append(content, html[pos:]...)...)
		}

	case PositionHeadEnd:
		// 在 </head> 标签前注入
		if loc := reHeadClose.FindIndex(html); loc != nil {
			pos := loc[0]
			return append(html[:pos], append(content, html[pos:]...)...)
		}

	case PositionBodyStart:
		// 在 <body> 标签后注入
		if loc := reBodyOpen.FindIndex(html); loc != nil {
			pos := loc[1]
			return append(html[:pos], append(content, html[pos:]...)...)
		}

	case PositionBodyEnd:
		// 在 </body> 标签前注入
		if loc := reBodyClose.FindIndex(html); loc != nil {
			pos := loc[0]
			return append(html[:pos], append(content, html[pos:]...)...)
		}
	}

	// 无法定位，在文档末尾追加
	return append(html, content...)
}
