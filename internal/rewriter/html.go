package rewriter

import (
	"bytes"
	"regexp"
	"strings"
	"sync"

	"github.com/zrurf/cifera/internal/utils"
	"golang.org/x/net/html"
)

// attrRegexCache 缓存已编译的属性匹配正则，避免每次请求重新编译
var attrRegexCache sync.Map

// getAttrRegex 获取或编译属性名对应的正则
func getAttrRegex(attrName string) *regexp.Regexp {
	if v, ok := attrRegexCache.Load(attrName); ok {
		return v.(*regexp.Regexp)
	}
	pattern := `(?i)(\b` + regexp.QuoteMeta(attrName) + `\s*=\s*)(?:"([^"]*)"|'([^']*)')`
	re := regexp.MustCompile(pattern)
	actual, _ := attrRegexCache.LoadOrStore(attrName, re)
	return actual.(*regexp.Regexp)
}

// reCSSURL 用于改写 CSS 中的 url()
var reCSSURL = regexp.MustCompile(`url\(\s*['"]?([^'"\)\s]+)['"]?\s*\)`)

// urlAttrNames 需要改写 URL 的属性名集合（适用于所有标签）
var urlAttrNames = map[string]bool{
	"src":        true,
	"href":       true,
	"action":     true,
	"poster":     true,
	"srcset":     true,
	"cite":       true,
	"longdesc":   true,
	"profile":    true,
	"usemap":     true,
	"codebase":   true,
	"archive":    true,
	"background": true,
}

// tagSpecificURLAttrs 标签特定的 URL 属性
// 某些属性（如 data）仅在特定标签中才是 URL，其他标签中是普通数据
var tagSpecificURLAttrs = map[string]map[string]bool{
	"object":     {"data": true},
	"applet":     {"data": true},
	"embed":      {"data": true},
	"source":     {"src": true, "srcset": true},
	"track":      {"src": true},
	"input":      {"src": true},
	"img":        {"src": true, "srcset": true},
	"iframe":     {"src": true},
	"frame":      {"src": true},
	"script":     {"src": true},
	"link":       {"href": true},
	"a":          {"href": true},
	"area":       {"href": true},
	"base":       {"href": true},
	"form":       {"action": true},
	"video":      {"src": true, "poster": true},
	"audio":      {"src": true},
	"body":       {"background": true},
	"table":      {"background": true},
	"td":         {"background": true},
	"th":         {"background": true},
	"tr":         {"background": true},
	"blockquote": {"cite": true},
	"q":          {"cite": true},
	"ins":        {"cite": true},
	"del":        {"cite": true},
}

// RewriteHTMLUrls 使用 html.Tokenizer 逐 token 遍历改写 HTML 中的 URL
// 始终输出 raw 原始字节，通过替换 raw 中的属性值来改写 URL，
// 绝不使用 token.String() 重新序列化，避免破坏原始 HTML 结构
func RewriteHTMLUrls(htmlBytes []byte, proxyBase, currentPath, host, schema, referer string) []byte {
	r := bytes.NewReader(htmlBytes)
	tokenizer := html.NewTokenizer(r)
	// 预分配缓冲区：HTML 改写后通常比原始大 20-50%（因代理参数追加）
	buf := bytes.NewBuffer(make([]byte, 0, len(htmlBytes)+len(htmlBytes)/2))

	// 跟踪当前是否在 <style> 标签内
	inStyle := false

	for {
		tt := tokenizer.Next()
		if tt == html.ErrorToken {
			buf.Write(tokenizer.Buffered())
			break
		}

		raw := tokenizer.Raw()

		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			token := tokenizer.Token()
			tagName := strings.ToLower(token.Data)

			// 跟踪 <style> 标签
			if tagName == "style" {
				inStyle = true
			}

			if needsRewrite(token) {
				// 构建 属性名→新值 的映射，传入标签名用于标签特定属性判断
				replacements := buildAttrReplacements(token, proxyBase, currentPath, host, schema, referer)
				if len(replacements) > 0 {
					modified := applyAttrReplacements(raw, replacements)
					buf.Write(modified)
				} else {
					buf.Write(raw)
				}
			} else {
				buf.Write(raw)
			}

		case html.EndTagToken:
			token := tokenizer.Token()
			tagName := strings.ToLower(token.Data)
			if tagName == "style" {
				inStyle = false
			}
			buf.Write(raw)

		case html.TextToken:
			if inStyle {
				// <style> 标签内文本：改写 CSS url()
				cssContent := string(raw)
				rewritten := rewriteCSSURLs(cssContent, proxyBase, currentPath, host, schema, referer)
				buf.WriteString(rewritten)
			} else {
				// 所有其他文本（包括 <script> 内容）直接透传
				buf.Write(raw)
			}

		default:
			// CommentToken, DoctypeToken 等直接透传
			buf.Write(raw)
		}
	}

	return buf.Bytes()
}

// attrReplacement 表示一个属性值的替换
type attrReplacement struct {
	attrName string
	oldVal   string // token.Attr 中的解码值
	newVal   string // 替换后的新值
}

// isURLAttr 判断在指定标签中，某个属性是否为 URL 属性
// 结合通用 urlAttrNames 和标签特定 tagSpecificURLAttrs 判断
func isURLAttr(tagName, attrKey string) bool {
	// data-* 自定义属性永远不是 URL
	if strings.HasPrefix(attrKey, "data-") {
		return false
	}

	// 如果标签有特定的属性白名单，只改写白名单中的属性
	// 例如 iframe 只有 src 是 URL，data 属性不是 URL
	if tagAttrs, ok := tagSpecificURLAttrs[tagName]; ok {
		return tagAttrs[attrKey]
	}

	// 标签没有特定白名单，使用通用规则
	return urlAttrNames[attrKey]
}

// needsRewrite 判断一个标签是否包含需要改写的属性
func needsRewrite(token html.Token) bool {
	tagName := strings.ToLower(token.Data)
	for _, attr := range token.Attr {
		attrKey := strings.ToLower(attr.Key)
		if isURLAttr(tagName, attrKey) {
			return true
		}
		if attrKey == "style" && strings.Contains(attr.Val, "url(") {
			return true
		}
		if attrKey == "http-equiv" && strings.EqualFold(attr.Val, "refresh") {
			return true
		}
	}
	return false
}

// buildAttrReplacements 构建需要替换的属性映射
func buildAttrReplacements(token html.Token, proxyBase, currentPath, host, schema, referer string) []attrReplacement {
	var replacements []attrReplacement
	isMetaRefresh := false
	tagName := strings.ToLower(token.Data)

	// 检查是否为 meta refresh
	for _, attr := range token.Attr {
		if attr.Key == "http-equiv" && strings.EqualFold(attr.Val, "refresh") {
			isMetaRefresh = true
			break
		}
	}

	for _, attr := range token.Attr {
		attrKey := strings.ToLower(attr.Key)
		switch {
		case attrKey == "srcset":
			newVal := rewriteSrcsetValue(attr.Val, proxyBase, currentPath, host, schema, referer)
			if newVal != attr.Val {
				replacements = append(replacements, attrReplacement{
					attrName: attrKey,
					oldVal:   attr.Val,
					newVal:   newVal,
				})
			}

		case attrKey == "style":
			newVal := rewriteCSSURLs(attr.Val, proxyBase, currentPath, host, schema, referer)
			if newVal != attr.Val {
				replacements = append(replacements, attrReplacement{
					attrName: attrKey,
					oldVal:   attr.Val,
					newVal:   newVal,
				})
			}

		case attrKey == "content" && isMetaRefresh:
			newVal := rewriteMetaRefreshContent(attr.Val, proxyBase, currentPath, host, schema, referer)
			if newVal != attr.Val {
				replacements = append(replacements, attrReplacement{
					attrName: attrKey,
					oldVal:   attr.Val,
					newVal:   newVal,
				})
			}

		case isURLAttr(tagName, attrKey):
			newVal := utils.BuildProxyUrl(proxyBase, attr.Val, currentPath, host, schema, referer)
			if newVal != attr.Val {
				replacements = append(replacements, attrReplacement{
					attrName: attrKey,
					oldVal:   attr.Val,
					newVal:   newVal,
				})
			}
		}
	}

	return replacements
}

// applyAttrReplacements 在 raw 字节中替换属性值
// 通过查找 attr="oldVal" 或 attr='oldVal' 模式，将 oldVal 替换为 newVal
// 直接操作原始字节，不重新序列化整个标签
func applyAttrReplacements(raw []byte, replacements []attrReplacement) []byte {
	result := string(raw)

	for _, rep := range replacements {
		// 尝试双引号格式：attr="oldVal"
		doubleQuoted := rep.attrName + `="` + rep.oldVal + `"`
		doubleReplacement := rep.attrName + `="` + rep.newVal + `"`

		// 尝试单引号格式：attr='oldVal'
		singleQuoted := rep.attrName + `='` + rep.oldVal + `'`
		singleReplacement := rep.attrName + `='` + rep.newVal + `'`

		if strings.Contains(result, doubleQuoted) {
			result = strings.Replace(result, doubleQuoted, doubleReplacement, 1)
		} else if strings.Contains(result, singleQuoted) {
			result = strings.Replace(result, singleQuoted, singleReplacement, 1)
		} else {
			// 属性值可能包含 HTML 实体编码（如 &amp;），尝试解码后匹配
			// html.Tokenizer 会将 &amp; 解码为 &，但 raw 中仍是 &amp;
			decodedOldVal := htmlEntityReplacer(rep.oldVal)
			decodedDoubleQuoted := rep.attrName + `="` + decodedOldVal + `"`
			decodedSingleQuoted := rep.attrName + `='` + decodedOldVal + `'`

			if decodedOldVal != rep.oldVal {
				if strings.Contains(result, decodedDoubleQuoted) {
					result = strings.Replace(result, decodedDoubleQuoted, doubleReplacement, 1)
				} else if strings.Contains(result, decodedSingleQuoted) {
					result = strings.Replace(result, decodedSingleQuoted, singleReplacement, 1)
				}
			}

			// 最后尝试：逐字符宽松匹配（处理引号内 HTML 实体）
			if strings.Contains(result, rep.attrName+"=") {
				result = replaceAttrValueInRaw(result, rep.attrName, rep.newVal)
			}
		}
	}

	return []byte(result)
}

// htmlEntityReplacer 将 URL 中的特殊字符替换为可能的 HTML 实体编码形式
func htmlEntityReplacer(val string) string {
	// URL 中 & 在 HTML 属性中会被编码为 &amp;
	return strings.ReplaceAll(val, "&", "&amp;")
}

// replaceAttrValueInRaw 在原始 HTML 中查找指定属性并替换其值
// 使用正则匹配来处理各种引号和编码情况
var reAttrValue = regexp.MustCompile(`(\bATTR\s*=\s*)(?:"([^"]*)"|'([^']*)')`)

func replaceAttrValueInRaw(raw, attrName, newVal string) string {
	// 使用缓存的正则，避免每次请求编译
	re := getAttrRegex(attrName)

	return re.ReplaceAllStringFunc(raw, func(match string) string {
		submatches := re.FindStringSubmatch(match)
		if len(submatches) < 4 {
			return match
		}
		prefix := submatches[1]
		if submatches[2] != "" {
			// 双引号值
			return prefix + `"` + newVal + `"`
		}
		// 单引号值
		return prefix + `'` + newVal + `'`
	})
}

// rewriteSrcsetValue 改写 srcset 属性值
func rewriteSrcsetValue(value, proxyBase, currentPath, host, schema, referer string) string {
	entries := strings.Split(value, ",")
	var rewritten []string
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, " ", 2)
		u := parts[0]
		descriptor := ""
		if len(parts) > 1 {
			descriptor = " " + parts[1]
		}
		newURL := utils.BuildProxyUrl(proxyBase, u, currentPath, host, schema, referer)
		rewritten = append(rewritten, newURL+descriptor)
	}
	return strings.Join(rewritten, ", ")
}

// rewriteCSSURLs 改写 CSS 内容中的 url()
func rewriteCSSURLs(css, proxyBase, currentPath, host, schema, referer string) string {
	return reCSSURL.ReplaceAllStringFunc(css, func(match string) string {
		submatches := reCSSURL.FindStringSubmatch(match)
		if len(submatches) < 2 {
			return match
		}
		originalURL := submatches[1]
		rewritten := utils.BuildProxyUrl(proxyBase, originalURL, currentPath, host, schema, referer)
		if rewritten == originalURL {
			return match
		}
		return "url('" + rewritten + "')"
	})
}

// rewriteMetaRefreshContent 改写 meta refresh 的 content 属性
func rewriteMetaRefreshContent(value, proxyBase, currentPath, host, schema, referer string) string {
	lower := strings.ToLower(value)
	idx := strings.Index(lower, "url=")
	if idx < 0 {
		return value
	}

	prefix := value[:idx+4]
	originalURL := value[idx+4:]
	originalURL = strings.Trim(originalURL, " \t'\"")
	rewritten := utils.BuildProxyUrl(proxyBase, originalURL, currentPath, host, schema, referer)
	if rewritten == originalURL {
		return value
	}

	return prefix + rewritten
}
