# 代理URL和参数

## 名词解释
- **代理URL**：指用户访问Cifera时，输入的URL。
- **源URL**：指用户访问的原始URL。

## 示例
例如你在[README.md](../README.md)中看到的这一串URL：
```
http://127.0.0.1:8080/s?_cifera_h=www.baidu.com&_cifera_s=https&wd=golang
```
这一行就叫做**代理URL**。将其还原为**源URL**：
```
https://www.baidu.com/s?wd=golang
```
你可以尝试访问，结果是一样的。

## 代理URL构成

一个代理URL的构成如下：
```
http://<proxy_host>/<source_path>?_cifera_h=<source_host>&_cifera_s=<source_schema>&_cifera_r=<source_referer>&<source_query>
```
- `<proxy_host>`：Cifera的主机名。
- `<source_path>`：源URL的路径。
- `_cifera_h=<source_host>`：源URL的主机名。
- `_cifera_s=<source_schema>`：源URL的协议 *（可选，默认值为`http`）*。
- `_cifera_r=<source_referer>`：代理URL的Referer头 *（可选，为空会回退使用Referer Header，当部分场景下`source_host`为空时会用于推断源主机名）*。
- `<source_query>`：源URL的查询参数。