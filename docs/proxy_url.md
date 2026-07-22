# 代理 URL 和参数

## 名词解释
- **代理 URL**：指用户访问 Cifera 时，输入的 URL。
- **源 URL**：指用户实际要访问的原始 URL。

## 示例
例如你在 [README.md](../README.md) 中看到的这一串 URL：
```
http://127.0.0.1:8080/s?_cifera_h=www.baidu.com&_cifera_s=https&wd=golang
```
这一行就叫做**代理 URL**。将其还原为**源 URL**：
```
https://www.baidu.com/s?wd=golang
```
你可以尝试访问，结果是一样的。

## 代理 URL 构成

一个代理 URL 的构成如下：
```
http://<proxy_host>/<source_path>?_cifera_h=<source_host>&_cifera_s=<source_schema>&<source_query>
```
- `<proxy_host>`：Cifera 的主机名（含端口，如 `127.0.0.1:8080`）。
- `<source_path>`：源 URL 的路径。
- `_cifera_h=<source_host>`：源 URL 的主机名（可含端口，如 `example.com:443`）。
- `_cifera_s=<source_schema>`：源 URL 的协议 *（可选，默认值为 `http`）*。
- `<source_query>`：源 URL 的查询参数。

### `_cifera_h` 提取逻辑

请求进入 Cifera 时，`_cifera_h` 参数从以下位置提取（按优先级）：

1. **URL 查询参数**：`?_cifera_h=example.com`
2. **Referer 头推断**：若 URL 中无 `_cifera_h`，从 `Referer` 头中提取（页面内请求通常由浏览器自动附加 Referer）
3. **`Cifera-Referer` 头**：前端 JS 运行时在 fetch/XHR 请求中添加的元 header，值为当前页面的源 URL
4. **r.Host 回退**：若以上均无，使用请求的 Host 头（适用于直接访问 vhost 场景）

`_cifera_s` 的提取逻辑相同，但默认值为 `http`。

## 元信息

Cifera 使用前缀约定区分自身控制参数和普通参数：

### 元参数（`_cifera_` 前缀）

| 参数 | 必需 | 说明 |
|------|------|------|
| `_cifera_h` | 是 | 源站主机名 |
| `_cifera_s` | 否 | 源站协议（默认 `http`） |

用户可自定义额外的 `_cifera_*` 参数用于与 Cifera 交换数据，但保留的元参数（上表）不可被修改。

### 元 header（`Cifera-` 前缀）

| Header | 方向 | 说明 |
|--------|------|------|
| `Cifera-Referer` | 客户端 → 服务器 | 当前页面的源 URL，用于在 `_cifera_h` 缺省时推断源主机 |
| `Cifera-Cookie-Sync` | 客户端 → 服务器 | 脏 cookie 增量同步（Cookie 托管模式） |
| `Cifera-Cookie-Ack` | 服务器 → 客户端 | cookie 同步确认（Cookie 托管模式） |

用户可自定义额外的 `Cifera-*` header 用于数据交换。

### 流通规则

- 元参数和元 header **仅在 Cifera 和客户端之间流通**，转发源站时被自动剔除
- 剥离时机：
  - **元参数**：在 `Rewrite` 阶段，构建源站 URL 时调用 `StripMetaParams` 从查询参数中移除所有 `_cifera_*`
  - **元 header**：在同一阶段，调用 `StripMetaHeaders` 从请求头中移除所有 `Cifera-*`
- vhost 配置 `pass_meta = true` 时，元信息可传递给 vhost（用于 vhost 间通信）
- 保留的元参数不可被用户自定义和修改
