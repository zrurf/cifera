# Cifera
**一个基于Golang的正向代理服务器，可以代理访问http(s)站点。**

## 特性
- 自动向html页面中注入Cifera的js代码，实现对页面内基本所有请求的代理。
- 支持自定义addon，可以在不修改代码的情况下，劫持代理的请求和资源，也支持向html页面中注入自定义的js代码和css样式。
- 支持vhost虚拟主机，可以实现代理自己的资源。

## 使用
### 启动服务器
```bash
cifera --config config.toml
```

查看所有参数
```bash
cifera --help
```

### 访问代理站点
假设你的服务器启动在127.0.0.1:8080，你可以通过浏览器测试访问：
```
http://127.0.0.1:8080/s?_cifera_h=www.baidu.com&_cifera_s=https&wd=golang
```
这会显示Golang这个关键词的百度搜索页面。

对于**URL和参数**，参见 [代理URL和参数](docs/proxy_url.md)。

## 编译
### 1. 环境
- Golang 1.26+
- BunJS 1.3+ *(不可使用NodeJS)*

### 2. 编译
由于Cifera依赖于注入JS代码实现对html页面的代理，所以需要先编译scripts资源：
```bash
cd scripts # 进入scripts目录
bun run build
```
再编译cifera：
```bash
cd .. # 返回上一级目录，即项目根目录
go build -o cifera
```

## 兼容性警告
项目处于早期开发阶段，对部分网站和请求暂时没有适配，可能有因为资源请求失败、跨域等问题导致的页面异常或访问失败。

## 已知问题
- 目前仅支持`http(s)`协议，不支持`ws(s)`等其他协议。
- Cookie有跨站冲突和污染问题。有Cookie安全性问题。
- 目前由于没有压缩响应体，且没有缓存，可能会有性能问题。

## 许可证
[MIT License](LICENSE)