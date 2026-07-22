# 架构

## 整体架构

Cifera 是一个基于 Go 的正向 Web 代理服务器，核心思路是将源站 URL 改写为代理 URL，在代理服务器上完成请求转发和响应改写。

```
浏览器                     Cifera 代理服务器                     源站
┌──────────────┐         ┌──────────────────────────┐         ┌──────────┐
│ JS 运行时     │         │  Go HTTP 服务器           │         │          │
│ (TS 编译注入) │  请求   │  ├─ URL 改写              │  请求   │          │
│  ├─ fetch    │────────▶│  ├─ Cookie Jar 管理       │────────▶│          │
│  ├─ XHR      │         │  ├─ Addon 匹配/注入       │         │          │
│  ├─ DOM      │  响应   │  ├─ VHost 分发            │  响应   │          │
│  ├─ 导航     │◀────────│  ├─ HTML 改写/JS 注入     │◀────────│          │
│  ├─ Worker   │         │  ├─ 压缩/缓存             │         │          │
│  └─ Cookie   │         │  └─ 元信息剥离             │         │          │
└──────────────┘         └──────────────────────────┘         └──────────┘
```

## 请求处理流程

```
请求进入
  │
  ├─ parseProxyParams：从 URL 和 Header 提取 _cifera_h/_cifera_s/Cifera-Referer
  │   ├─ URL 查询参数 → Referer 头推断 → Cifera-Referer 头 → r.Host 回退
  │   └─ stripProxyPort：剥离源站 host 中意外携带的代理端口
  │
  ├─ Cookie Jar：获取/创建会话，替换请求 Cookie 头
  │   ├─ 从 _cifera_sid cookie 查找/创建 Jar
  │   ├─ 用 Jar 中匹配 host/path 的 cookie 替换请求 Cookie 头
  │   └─ Cifera-Cookie-Sync：处理客户端脏 cookie 同步 → PersistJar
  │
  ├─ Addon block：URL 正则匹配，命中则返回指定状态码
  │
  ├─ VHost 分发：检查虚拟主机注册表
  │   ├─ override：直接由 vhost 服务响应（本地文件或远程代理）
  │   └─ fallback：存入 context，代理到源站后检查状态码
  │
  ├─ 缓存查找：GET/HEAD 请求检查 LRU 缓存
  │
  ├─ 并发控制：信号量限制（4096），排队等待
  │
  └─ httputil.ReverseProxy 转发
      │
      └─ 响应处理（processResponse）
          ├─ Addon replace/replace_content：替换响应体
          ├─ Fallback VHost：状态码命中正则 → 用 vhost 响应替换
          ├─ 重定向 Location 改写
          ├─ Cookie Jar：拦截 Set-Cookie，存入 Jar，移除 Domain 属性
          │   └─ 设置 _cifera_sid cookie（新会话时）
          ├─ Cifera-Cookie-Ack：响应中添加同步确认头
          ├─ Addon inject：注入 JS/CSS 到 HTML
          ├─ HTML 改写：改写 URL 属性，注入 __CIFERA__ 配置和 JS 运行时
          │   └─ __CIFERA__ = { h: host, s: schema, c: cookiesJSON }
          ├─ 缓存写入：成功响应存入 LRU 缓存
          └─ 压缩：gzip/brotli/zstd（根据 Accept-Encoding 协商）
```

## 模块说明

### `internal/server.go` — 核心服务器

- HTTP 代理入口，`httputil.ReverseProxy` 驱动
- `parseProxyParams`：提取代理参数，Referer 头推断，端口剥离
- `processResponse`：响应改写主流程（Cookie 拦截 → Addon → HTML 改写 → 压缩）
- `ciferaHandler`：整合 addon、vhost、代理的处理链
- 并发控制：信号量限制最多 4096 并发代理请求，超出排队等待
- 错误处理：`context canceled` 降级为 DEBUG（客户端断开连接），其他代理错误返回 502

### `internal/rewriter/` — HTML 改写

- `html.go`：基于 `golang.org/x/net/html` Tokenizer 的 HTML URL 改写
  - 标签感知属性白名单（如 iframe data 不做 URL 改写，data-* 自定义属性永不改写）
  - CSS `url()` 改写、`srcset` 改写、meta refresh 改写、`javascript:` URL 不区分大小写
- `inject.go`：JS 运行时和 `__CIFERA__` 配置注入
  - `config` 使用 `map[string]any` + `json.RawMessage` 避免双重 JSON 编码
- `rewriter.go`：改写入口，协调 HTML 改写和注入

### `internal/cookiejar/` — Cookie 托管

- `jar.go`：Cookie Jar 实现（RFC 6265 域名/路径匹配、容量淘汰、过期处理）
- `manager.go`：Manager（UUID v4 会话、NutsDB 持久化、异步写入、定期清理）
- `matcher.go`：域名匹配、路径匹配、IP 检测

详见 [Cookie 托管系统](cookie.md)

### `internal/addon/` — Addon 系统

- `addon.go`：数据结构
  - `ActionType`：`block` / `replace` / `replace_content` / `inject`
  - `InjectPosition`：`head_start` / `head_end` / `body_start` / `body_end`
  - `ResourceType`：`js` / `css` / `other`
  - `AddonManifest`：`[addon]` 元信息 + `[[rules]]` 规则列表 + `[[hosts]]` 虚拟主机
- `loader.go`：从 addon.toml 加载和验证 addon，预编译正则，预加载资源文件
- `matcher.go`：URL 正则匹配请求（`MatchBlock` / `MatchReplace` / `MatchInject`）
- `inject.go`：HTML 注入 JS/CSS（根据 `InjectPosition` 确定插入位置）

**Addon 处理流程**：

```
请求 URL
  │
  ├─ MatchBlock：正则匹配 → 命中则返回 status_code（默认 404）
  │
  └─ processResponse 中：
      ├─ MatchReplace：正则匹配 → 命中则替换响应体（replace 或 replace_content）
      └─ MatchInject：正则匹配 → 命中则在 HTML 指定位置注入 <script>/<style>
```

### `internal/vhost/` — 虚拟主机

- `vhost.go`：HostConfig / Host / Registry
  - `HostType`：`local`（本地文件） / `remote`（远程代理）
  - `Priority`：`override`（直接覆盖） / `fallback`（状态码回退）
  - `FallbackStatus`：正则匹配状态码，支持 `!` 前缀取反（如 `!200` 匹配非 200）
  - `PassMeta`：是否透传元信息（`_cifera_*` + `Cifera-*`）
  - `Registry`：以 host name 为 key 的注册表，不区分大小写，后注册覆盖先注册
- `local.go`：本地文件服务
  - 仅允许 GET/HEAD 方法，其他返回 405
  - 禁止目录列表（返回 403）
  - 路径安全校验（`path.Clean` + `strings.TrimPrefix`，跨平台一致）
- `remote.go`：远程代理
  - 仅允许 http/https 协议
  - `PassMeta = false` 时剥离元信息（`StripMetaParams` + `StripMetaHeaders`）

**VHost 分发流程**：

```
请求 host
  │
  ├─ Registry.Lookup(hostParam, requestHost)
  │
  ├─ override → 直接 Serve 响应
  │   ├─ local → 读取本地文件
  │   └─ remote → 代理到远程 URL
  │
  └─ fallback → 存入 context
      └─ 源站响应后检查 FallbackMatches(statusCode)
          ├─ 命中 → 用 vhost 响应替换
          └─ 未命中 → 使用源站原始响应
```

### `internal/compress/` — 响应压缩

- Content-Encoding 协商（gzip/brotli/zstd）
- 非 HTML 压缩响应透传（上游已压缩，无需解压-再压缩）
- HTML 响应：解压 → 改写 → 再压缩（保证改写后的内容正确压缩）
- addon replace 后的响应同样走压缩流程

### `internal/cache/` — 响应缓存

- LRU 缓存，可配置最大容量（默认 256MB）
- 缓存键：`schema + host + requestURI`
- 仅缓存 GET/HEAD 成功响应

### `internal/constant/url.go` — 常量

- 代理参数前缀（`_cifera_`）、元 header 前缀（`Cifera-`）
- 所有保留的元参数和元 header 常量
- Cookie 会话 ID 常量（`_cifera_sid`）

### `internal/utils/url.go` — URL 工具

- `ParseProxyUrl`：从代理 URL 提取源站信息（host, schema, path）
- `BuildProxyUrl`：将源 URL 转换为代理 URL
- `StripMetaParams`：剥离所有 `_cifera_*` 查询参数
- `StripMetaHeaders`：剥离所有 `Cifera-*` 请求头

## 前端 JS 运行时

编译自 `scripts/src/`（使用 BunJS），注入到每个 HTML 页面的 `<head>` 开头：

| 模块 | 文件 | 功能 |
|------|------|------|
| 入口 | `index.ts` | 初始化拦截器和 Cookie 管理 |
| URL 改写 | `rewriter.ts` | `rewriteUrl` 核心函数、`normalizeProxyHost` 端口剥离 |
| fetch 拦截 | `interceptors/fetch.ts` | 拦截 `window.fetch`，改写 URL，添加 `Cifera-Referer` + `Cifera-Cookie-Sync`，处理 ACK |
| XHR 拦截 | `interceptors/xhr.ts` | 拦截 `XMLHttpRequest.open`，改写 URL，添加元 header，处理 ACK |
| DOM 拦截 | `interceptors/dom.ts` | 拦截 `setAttribute`、property setter、MutationObserver，标签感知属性白名单 |
| 导航拦截 | `interceptors/navigation.ts` | 拦截 `location.href`、`replace`、`assign`、`window.open`、`history` API、Navigation API、`<a>` 点击 |
| Worker 拦截 | `interceptors/worker.ts` | 拦截 `Worker` / `SharedWorker` 构造函数，改写脚本 URL |
| Cookie 管理 | `cookie.ts` | Shadow Cookie Jar、`document.cookie` hook、脏标记、墓碑删除、ACK 处理 |

### URL 改写流程

```
原始 URL
  │
  ├─ shouldSkip：空白 / # / 已含 _cifera_ / 非白名单协议 → 跳过
  │
  ├─ new URL 解析
  │
  ├─ normalizeProxyHost：剥离代理端口 + 替换代理 host
  │
  ├─ 同源判断（host === window.location.host）
  │   ├─ 同源 → 追加 _cifera_h / _cifera_s 参数
  │   └─ 跨源 → 重写为代理 URL（window.location.origin + path + _cifera_h + _cifera_s）
  │
  └─ 返回改写后的 URL
```

### 协议白名单

| 拦截器 | 白名单协议 |
|--------|-----------|
| URL 改写 | `http`, `https`, `ftp`, `ftps`, `ws`, `wss` |
| 导航拦截 | `http`, `https`, `ftp`, `ftps` |

不在白名单中的协议（如 `javascript:`, `data:`, `jsBridge:`, `weixin:` 等）不会被拦截和改写。

## 元信息设计

Cifera 的元信息（`_cifera_*` 参数 + `Cifera-*` header）用于 Cifera 与客户端之间的控制和数据交换：

- **保留元**：由 Cifera 规定和使用，用户不可自定义或修改
- **自定义元**：用户可定义额外的 `_cifera_*`/`Cifera-*` 用于数据交换
- **流通规则**：元信息仅在 Cifera 和客户端之间流通，转发源站时被自动剔除
- **VHost 例外**：vhost 配置 `pass_meta = true` 时，元信息可传递给 vhost

详见 [代理 URL 和参数](proxy_url.md)

## 错误处理

| 场景 | 处理方式 |
|------|----------|
| 客户端断开连接（`context canceled`） | 降级为 DEBUG 日志，不返回错误响应 |
| 代理请求失败 | 返回 502 Bad Gateway |
| Addon 资源文件不存在 | 启动时加载失败，addon 不可用 |
| VHost 本地目录不存在 | 启动时校验失败，config/addon 加载错误 |
| VHost 路径穿越 | 运行时 `sanitizePath` 校验，非法路径返回 403 |
| Cookie 持久化通道满 | 丢弃本次持久化（cleanup 定期兜底） |
| NutsDB 启动失败 | 返回错误，服务器不启动 |
