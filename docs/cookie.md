# Cookie 托管系统

## 概述

Cifera 实现了服务器端 Cookie 托管系统，完全替代浏览器原生 Cookie 机制。核心目标：

- **隔离**：源站 Cookie 不接触浏览器，避免跨站 Cookie 污染和泄露
- **托管**：Cifera 服务器为每个用户维护独立的 Cookie Jar，通过会话 ID 关联
- **持久化**：Cookie Jar 可持久化到磁盘（Badger），重启后恢复
- **JS 兼容**：通过 hook `document.cookie` 读写，JS 代码无感知

## 架构

```
浏览器（JS Shadow Jar）          Cifera 服务器（Cookie Jar Manager）        源站
┌──────────────────┐         ┌───────────────────────────┐         ┌──────────┐
│ document.cookie  │         │  Session ID → Cookie Jar  │         │          │
│   ↕ hook         │         │       ↕ AddCookie         │         │          │
│ Shadow Jar       │  sync   │  In-memory + Badger       │  proxy  │ Set-Cookie│
│ (JS Object)      │────────▶│                           │────────▶│          │
│ dirty flag       │  ACK    │  stripProxyPort           │◀────────│          │
└──────────────────┘         └───────────────────────────┘         └──────────┘
```

## 工作流程

### 1. 会话建立

1. 用户首次请求代理服务器
2. 服务器生成 UUID v4 作为会话 ID，创建空 Cookie Jar
3. 通过 `Set-Cookie: _cifera_sid=<uuid>` 将会话 ID 传递给客户端
4. 后续请求携带 `_cifera_sid` cookie，服务器据此查找对应 Jar
5. 若客户端提供的 ID 符合 UUID 格式但服务器无对应 Jar，沿用该 ID 自动创建

### 2. 请求拦截（浏览器 → 源站）

- 客户端请求中的 `Cookie` 头被**完全替换**为 Jar 中匹配当前 host/path 的 cookie
- `_cifera_sid` cookie 从转发请求中移除，不泄漏到源站

### 3. 响应拦截（源站 → 浏览器）

- 源站的 `Set-Cookie` 头被拦截，存入 Cookie Jar
- `Domain` 属性移除（使 cookie 在代理域名下生效）
- 响应中不再包含源站的 `Set-Cookie`，避免浏览器直接存储
- Jar 变更后触发异步持久化

### 4. HTML 注入（全量同步）

- HTML 响应中注入 `__CIFERA__.c`（JSON 数组），包含当前 Jar 中所有 cookie
- 客户端 TS 从 `__CIFERA__.c` 初始化 Shadow Cookie Jar（JS 内存对象）
- 每次 HTML 页面加载都进行全量同步，确保 Shadow Jar 与服务器一致

### 5. JS Cookie 操作（document.cookie hook）

- `document.cookie = "foo=bar"`：写入 Shadow Jar + 标记脏
- `document.cookie`（读取）：从 Shadow Jar 匹配当前 path 的 cookie（过滤 HttpOnly）
- 删除操作使用墓碑标记（`e = -1`），保留在 Shadow Jar 中直到 ACK 确认

### 6. 增量同步（Cifera-Cookie-Sync / Cifera-Cookie-Ack）

**客户端 → 服务器**：
- fetch/XHR 请求自动添加 `Cifera-Cookie-Sync` 头
- 值为 `base64(JSON array of CookieEntry)`，仅包含脏 cookie（修改/删除）
- 墓碑条目（`e = -1`）表示删除操作

**服务器 → 客户端**：
- 响应添加 `Cifera-Cookie-Ack` 头
- 格式：`name1|path1,name2|path2`（逗号分隔的 cookie key 列表）
- 客户端收到 ACK 后：清除对应脏标记，移除已确认的墓碑

**重试机制**：
- 未确认的脏 cookie 保留在 dirtySet，下次请求自动重新同步

## 数据格式

### CookieEntry（TS ↔ Go 共享）

```typescript
interface CookieEntry {
    n: string;   // cookie name
    v: string;   // cookie value
    p: string;   // path（默认 "/"）
    e: number;   // expires（Unix 时间戳，0 = 会话 cookie，-1 = 删除墓碑）
    s: boolean;  // secure
    h: boolean;  // httpOnly
}
```

### Cifera-Cookie-Sync 示例

```
Cifera-Cookie-Sync: W3sibiI6InNlc3Npb24iLCJ2IjoiYWJjMTIzIiwicCI6Ii8iLCJlIjowLCJzIjpmYWxzZSwiaCI6ZmFsc2V9XQ==
```

解码后：`[{"n":"session","v":"abc123","p":"/","e":0,"s":false,"h":false}]`

### Cifera-Cookie-Ack 示例

```
Cifera-Cookie-Ack: session|/,token|/api
```

## 配置

```toml
[cookies]
enabled = true                  # 是否启用 Cookie 托管
jar_capacity = 500              # 每个 Jar 最大 cookie 数量
persist_path = "./data/cookies" # Badger 持久化目录（空则不持久化）
cleanup_interval = 300          # 过期清理间隔（秒）
```

### 配置项说明

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `enabled` | bool | `true` | 启用 Cookie 托管系统 |
| `jar_capacity` | int | `500` | 每个 Jar 最大 cookie 数，超出时淘汰最早过期的 |
| `persist_path` | string | `""` | Badger 数据目录，为空则仅内存存储 |
| `cleanup_interval` | int | `300` | 定期清理间隔（秒），移除过期 cookie 和空 Jar |

## 持久化

- 使用 [BadgerDB](https://github.com/dgraph-io/badger)（LSM + WAL）作为持久化引擎
- 每个 Cookie Jar 以 session ID 为 key，序列化的 `[]CookieEntry` 为 value
- 写入通过异步通道（缓冲 4096），不阻塞请求处理
- 通道满时丢弃本次写入（cleanup 定期兜底）
- 启动时自动加载所有已持久化的 Jar
- 关闭时排空通道剩余任务确保数据完整

## 元信息设计

Cookie 托管使用以下 Cifera 元信息：

| 类型 | 名称 | 方向 | 说明 |
|------|------|------|------|
| Cookie | `_cifera_sid` | 服务器 → 客户端 | 会话 ID（UUID v4） |
| Header | `Cifera-Cookie-Sync` | 客户端 → 服务器 | 脏 cookie 增量同步 |
| Header | `Cifera-Cookie-Ack` | 服务器 → 客户端 | 同步确认 |

这些元信息在转发源站时被剔除，不会泄漏到源站。
