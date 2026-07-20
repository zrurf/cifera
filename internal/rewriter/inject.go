package rewriter

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/zrurf/cifera/internal/constant"
)

// 匹配注入位置的正则
var (
	reHeadOpen  = regexp.MustCompile(`(?i)<head[^>]*>`)
	reHeadClose = regexp.MustCompile(`(?i)</head>`)
	reBodyOpen  = regexp.MustCompile(`(?i)<body[^>]*>`)
	reHTMLTag   = regexp.MustCompile(`(?i)<html[^>]*>`)
)

// InjectRuntime 在 HTML 中注入代理参数和 JS 运行时
func InjectRuntime(html []byte, jsContent, host, schema, referer, pageOrigin string) []byte {
	// 构造 __CIFERA__ 配置
	config := map[string]string{
		"h": host,
		"s": schema,
	}
	if referer != "" {
		config["r"] = referer
	}
	if pageOrigin != "" {
		config["p"] = pageOrigin
	}
	configJSON, err := json.Marshal(config)
	if err != nil {
		configJSON = fmt.Appendf(nil, `{"h":"%s","s":"%s"}`, host, schema)
	}

	// 构造注入片段
	inject := fmt.Sprintf(
		`<script>var %s=%s</script><script>%s</script>`,
		constant.ProxyGlobalVar,
		string(configJSON),
		jsContent,
	)

	injectBytes := []byte(inject)

	// 查找注入位置（优先级：<head> 后 > </head> 前 > <html> 后 > <body> 前 > 文档开头）
	if loc := reHeadOpen.FindIndex(html); loc != nil {
		// 在 <head> 标签后注入
		pos := loc[1]
		return append(html[:pos], append(injectBytes, html[pos:]...)...)
	}

	if loc := reHeadClose.FindIndex(html); loc != nil {
		// 在 </head> 标签前注入
		pos := loc[0]
		return append(html[:pos], append(injectBytes, html[pos:]...)...)
	}

	if loc := reHTMLTag.FindIndex(html); loc != nil {
		// 在 <html> 标签后注入
		pos := loc[1]
		return append(html[:pos], append(injectBytes, html[pos:]...)...)
	}

	if loc := reBodyOpen.FindIndex(html); loc != nil {
		// 在 <body> 标签前注入
		pos := loc[0]
		return append(html[:pos], append(injectBytes, html[pos:]...)...)
	}

	// 兜底：在文档最开头注入
	return append(injectBytes, html...)
}
