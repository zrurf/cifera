// Package inject 实现基于 CSS 选择器的 HTML 注入。
// 与 rewriter/addon 现有的正则文档级位置注入不同，本模块将 HTML 解析为 DOM，
// 用 CSS 选择器定位元素，在元素的前/后/子位置插入 JS/CSS，再序列化回 HTML。
// 仅当存在选择器注入规则时才启用，避免常规请求的额外解析开销。
package inject

import (
	"bytes"
	"fmt"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"
)

// Relation 相对命中元素的插入位置
type Relation string

const (
	RelationBefore  Relation = "before"  // 元素之前
	RelationAfter   Relation = "after"   // 元素之后
	RelationPrepend Relation = "prepend" // 元素内最前一个子节点
	RelationAppend  Relation = "append"  // 元素内最后一个子节点
)

// Scope 命中元素的插入范围
type Scope string

const (
	ScopeFirst Scope = "first" // 仅首个命中元素
	ScopeAll   Scope = "all"   // 全部命中元素
)

// Item 一条选择器注入项
type Item struct {
	Selector string   // CSS 选择器
	Relation Relation // 相对位置
	Scope    Scope    // 命中范围
	Content  []byte   // 待注入的原始内容
	IsCSS    bool     // true 用 <style> 包裹，false 用 <script> 包裹
}

// InjectBySelector 按选择器把多条注入项插入 HTML 并返回序列化结果。
// 任意选择器无法解析或注入失败时返回错误，由调用方决定回退策略。
func InjectBySelector(input []byte, items []Item) ([]byte, error) {
	if len(items) == 0 {
		return input, nil
	}

	doc, err := html.Parse(bytes.NewReader(input))
	if err != nil {
		return nil, fmt.Errorf("解析 HTML 失败: %w", err)
	}

	for _, item := range items {
		if err := applyItem(doc, item); err != nil {
			return nil, err
		}
	}

	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		return nil, fmt.Errorf("序列化 HTML 失败: %w", err)
	}
	return buf.Bytes(), nil
}

// applyItem 对一条注入项执行选择、构造与插入
func applyItem(doc *html.Node, item Item) error {
	sel, err := cascadia.Parse(item.Selector)
	if err != nil {
		return fmt.Errorf("无效的 CSS 选择器 %q: %w", item.Selector, err)
	}

	nodes := cascadia.QueryAll(doc, sel)
	if len(nodes) == 0 {
		return nil
	}

	if item.Scope == ScopeFirst {
		nodes = nodes[:1]
	}

	content := string(item.Content)
	for _, target := range nodes {
		node := buildContentNode(item.IsCSS, content)
		if err := insertNode(target, node, item.Relation); err != nil {
			return err
		}
	}
	return nil
}

// insertNode 按 relation 把节点插入到 target 的指定位置
func insertNode(target, node *html.Node, relation Relation) error {
	switch relation {
	case RelationBefore:
		if target.Parent == nil {
			return nil
		}
		target.Parent.InsertBefore(node, target)
	case RelationAfter:
		if target.Parent == nil {
			return nil
		}
		if target.NextSibling == nil {
			target.Parent.AppendChild(node)
		} else {
			target.Parent.InsertBefore(node, target.NextSibling)
		}
	case RelationPrepend:
		if target.FirstChild == nil {
			target.AppendChild(node)
		} else {
			target.InsertBefore(node, target.FirstChild)
		}
	case RelationAppend:
		target.AppendChild(node)
	default:
		return fmt.Errorf("无效的 relation: %s", relation)
	}
	return nil
}

// buildContentNode 把内容构造成 script/style 元素节点。
// 内容用 RawNode 承载，避免序列化时实体转义破坏 JS/CSS。
func buildContentNode(isCSS bool, content string) *html.Node {
	tag := "script"
	if isCSS {
		tag = "style"
	}
	wrapper := &html.Node{Type: html.ElementNode, Data: tag}
	inner := &html.Node{Type: html.RawNode, Data: content}
	wrapper.AppendChild(inner)
	return wrapper
}
