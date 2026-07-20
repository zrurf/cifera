package addon

import (
	"fmt"
	"regexp"
)

// 用于定位 HTML 注入位置的正则（与 rewriter/inject.go 保持一致）
var (
	reHeadOpen  = regexp.MustCompile(`(?i)<head[^>]*>`)
	reHeadClose = regexp.MustCompile(`(?i)</head>`)
	reBodyOpen  = regexp.MustCompile(`(?i)<body[^>]*>`)
	reBodyClose = regexp.MustCompile(`(?i)</body>`)
)

// InjectAddons 在 HTML 中按 position 注入 addon 的 JS/CSS
// 注入顺序：按 InjectItem 在列表中的顺序依次注入
// JS 资源用 <script>...</script> 包裹，CSS 资源用 <style>...</style> 包裹
func InjectAddons(html []byte, injects []InjectItem) []byte {
	if len(injects) == 0 {
		return html
	}

	// 按 position 分组，保持同 position 内的顺序
	grouped := make(map[InjectPosition][][]byte)
	// 记录所有出现过的 position，保持插入顺序
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

	// 按 position 优先级依次注入
	// 优先级：head_start → head_end → body_start → body_end
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

		// 合并同一 position 的所有注入内容
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
		// 其他类型不包裹，直接返回
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
