package addon

import (
	"bytes"
	"fmt"
	"text/template"
)

// ParamDef addon 参数声明（addon.toml 的 [[params]] 段）
type ParamDef struct {
	Name        string `toml:"name"`        // 参数名
	Type        string `toml:"type"`        // string / int / bool / json
	Default     any    `toml:"default"`     // 默认值
	Description string `toml:"description"` // 描述
}

// ResolveParams 按声明解析有效参数值。
// 优先级：调用方提供的 values（已合并租户/全局） > addon 默认值 > 类型零值。
// values 为 nil 时仅使用 addon 默认值。返回校验通过后的参数表。
func ResolveParams(defs []ParamDef, values map[string]any) (map[string]any, error) {
	resolved := make(map[string]any, len(defs))

	for _, d := range defs {
		zero, err := zeroByType(d.Type)
		if err != nil {
			return nil, fmt.Errorf("参数 %s: %w", d.Name, err)
		}

		v := zero
		if d.Default != nil {
			var ok bool
			v, ok = normalizeValue(d.Type, d.Default)
			if !ok {
				return nil, fmt.Errorf("参数 %s 默认值类型与 %s 不符", d.Name, d.Type)
			}
		}
		if values != nil {
			if uv, ok := values[d.Name]; ok {
				v, ok = normalizeValue(d.Type, uv)
				if !ok {
					return nil, fmt.Errorf("参数 %s 配置值类型与 %s 不符", d.Name, d.Type)
				}
			}
		}

		resolved[d.Name] = v
	}

	return resolved, nil
}

// zeroByType 返回参数类型的零值
func zeroByType(t string) (any, error) {
	switch t {
	case "string":
		return "", nil
	case "int":
		return 0, nil
	case "bool":
		return false, nil
	case "json":
		return nil, nil
	default:
		return nil, fmt.Errorf("不支持的类型: %s（支持 string/int/bool/json）", t)
	}
}

// normalizeValue 将配置值转换为目标类型；转换失败返回 ok=false。
// 处理 TOML/viper 解析出的 int64/float64 等数字表示。
func normalizeValue(t string, v any) (any, bool) {
	switch t {
	case "string":
		s, ok := v.(string)
		if !ok {
			// 数值可转字符串，便于配置容错
			switch n := v.(type) {
			case int, int64, float64, bool:
				return fmt.Sprint(n), true
			}
			return nil, false
		}
		return s, true
	case "int":
		switch n := v.(type) {
		case int:
			return n, true
		case int64:
			return int(n), true
		case float64:
			if n != float64(int64(n)) {
				return nil, false
			}
			return int(n), true
		}
		return nil, false
	case "bool":
		b, ok := v.(bool)
		return b, ok
	case "json":
		return v, true
	}
	return nil, false
}

// RenderResource 渲染 addon 资源模板。
// 内容不含 "{{" 时原样返回；含模板时用 text/template 渲染，占位符形如 {{ param "name" }}。
func RenderResource(content []byte, params map[string]any) ([]byte, error) {
	if !bytes.Contains(content, []byte("{{")) {
		return content, nil
	}

	tpl, err := template.New("resource").
		Funcs(template.FuncMap{
			"param": func(name string) any {
				if v, ok := params[name]; ok {
					return v
				}
				return ""
			},
		}).
		Parse(string(content))
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := tpl.Execute(&buf, nil); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
