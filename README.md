# Cifera
**一个基于 Golang 的正向代理服务器，可以代理访问 http(s) 站点。**

## 特性
- **JS 运行时注入**：自动向 HTML 页面注入 Cifera 的 TS 编译运行时，拦截并改写页面内的 fetch、XHR、DOM、导航、Worker 等请求
- **Addon 系统**：通过 addon.toml 配置规则，支持 replace（替换响应）、replace_content（替换响应体）、block（拦截请求）、inject（注入 JS/CSS）四种动作，无需修改代码
- **虚拟主机**：支持本地目录托管和远程代理两种类型，override/fallback 优先级策略，元信息可透传（`pass_meta`）
- **Cookie 托管**：服务器端 Cookie Jar 隔离源站 Cookie，避免跨站污染和泄露；支持 NutsDB 持久化、JS `document.cookie` 兼容、增量同步 + ACK 确认
- **响应压缩**：gzip / brotli / zstd，HTML 解压→改写→再压缩，非 HTML 透传上游压缩
- **缓存**：LRU 缓存，可配置容量
- **元信息设计**：`_cifera_*` 参数和 `Cifera-*` 头受控流通，转发源站时自动剔除，不泄漏

## 使用
### 启动服务器
```bash
cifera --config config.toml
```

查看所有参数：
```bash
cifera --help
```

### 访问代理站点
假设你的服务器启动在 `127.0.0.1:8080`，你可以通过浏览器测试访问：
```
http://127.0.0.1:8080/s?_cifera_h=www.baidu.com&_cifera_s=https&wd=golang
```
这会显示 **Golang** 这个关键词的百度搜索页面。

URL 和参数的详细说明参见 [代理 URL 和参数](docs/proxy_url.md)。

## 配置

完整的 `config.toml` 示例：

```toml
[server]
listen = ":8080"            # 监听地址

[log]
level = "info"              # 日志级别：debug, info, warn, error, fatal
persistent = true           # 是否持久化日志文件
path = "./log/latest.log"   # 日志文件路径
compression = true          # 是否压缩日志文件
max_size = 50               # 单个日志文件最大大小（MB）
max_age = 7                 # 日志文件保留天数
max_backups = 3             # 最大备份文件数量

[addons]
dir = "./addons"            # addon 加载目录
# enabled = []              # 仅加载指定 addon（按 id），缺省或为空则加载全部

[compression]
enabled = true              # 是否启用响应压缩

[compression.gzip]
enabled = true
level = 5                   # 压缩级别 (1-9)

[compression.brotli]
enabled = true
level = 4                   # 压缩级别 (0-11)

[compression.zstd]
enabled = true
level = 4                   # 压缩级别 (1-22)

[cache]
enabled = true
max_size = 268435456        # 最大缓存大小（字节），默认 256MB

[cookies]
enabled = true              # 是否启用 Cookie 托管
jar_capacity = 500          # 每个会话最大 Cookie 数量
persist_path = "./data/cookies"  # NutsDB 持久化目录（空则不持久化）
cleanup_interval = 300      # 过期 Cookie 清理间隔（秒）

# [[hosts]]                 # 虚拟主机配置（可定义多个）
# name = "cdn.example.com"  # 虚拟主机名称（即目标 host）
# type = "local"            # 类型：local（本地目录）或 remote（远程代理）
# path = "./vhost/cdn/"     # local 类型的本地目录路径
# remote = "https://cdn.example.com"  # remote 类型的远程 URL
# priority = "override"     # 优先级：override 或 fallback
# fallback_status = "4..|5.."  # fallback 状态码正则（支持 ! 前缀取反）
# pass_meta = false         # 是否透传元信息到 vhost
```

### Addon 配置

每个 addon 是 `addons/` 目录下的一个子目录，包含 `addon.toml` 清单和资源文件：

```toml
# addon.toml
[addon]
id = "com.example.my-addon"   # 唯一标识（建议反向域名格式）
name = "My Addon"             # 名称
version = "1.0.0"             # 版本号
description = "示例 addon"     # 描述（可选）
author = "author"             # 作者（可选）

[[rules]]
pattern = ["https://example\\.com/.*"]  # URL 正则（或关系，匹配任一即可）
action = "inject"                       # 动作：block / replace / replace_content / inject
resource = "inject.js"                  # 资源文件路径（相对于 addon 目录）
position = "head_end"                   # inject 位置：head_start / head_end / body_start / body_end

[[rules]]
pattern = ["https://cdn\\.example\\.com/.*"]
action = "replace"
resource = "local-file.js"              # 替换响应的资源文件

[[rules]]
pattern = ["https://blocked\\.example\\.com/.*"]
action = "block"
status_code = 403                       # block 的状态码，默认 404

# addon 也可以注册虚拟主机
# [[hosts]]
# name = "vhost.example.com"
# type = "local"
# path = "./public/"
# priority = "override"
```

## 编译
### 1. 环境
- Golang 1.26+
- BunJS 1.3.10+ *(不可使用 Node.js)*

### 2. 编译
由于 Cifera 依赖于注入 JS 代码实现对 HTML 页面的代理，所以需要先编译 scripts 资源：
```bash
cd scripts && bun run build
```
再编译 cifera：
```bash
cd .. && go build -o cifera
```

## 文档
- [架构](docs/arch.md)
- [代理 URL 和参数](docs/proxy_url.md)
- [Cookie 托管系统](docs/cookie.md)

## 兼容性警告
项目处于早期开发阶段，对部分网站和请求暂时没有适配，可能有因为资源请求失败、跨域等问题导致的页面异常或访问失败。

## 许可证
[MIT License](LICENSE)
