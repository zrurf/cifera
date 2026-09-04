package compress

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

func newTestResponse(body []byte, contentType string, contentLength int64) *http.Response {
	return &http.Response{
		StatusCode:    http.StatusOK,
		ContentLength: contentLength,
		Header:        http.Header{"Content-Type": []string{contentType}},
		Body:          io.NopCloser(bytes.NewReader(body)),
	}
}

// 流式压缩：三种算法压缩后均可解压还原为原文
func TestCompressResponseStream(t *testing.T) {
	for _, algo := range []Algorithm{Gzip, Brotli, Zstd} {
		n := NewNegotiator(DefaultConfig())
		body := bytes.Repeat([]byte("cifera 代理服务器性能测试内容 "), 500)
		resp := newTestResponse(body, "text/plain; charset=utf-8", int64(len(body)))
		req := &http.Request{Header: http.Header{"Accept-Encoding": []string{string(algo)}}}

		if err := n.CompressResponse(resp, req); err != nil {
			t.Fatalf("%s: %v", algo, err)
		}
		if resp.Header.Get("Content-Encoding") != string(algo) {
			t.Fatalf("%s: 期望 Content-Encoding=%s", algo, algo)
		}
		if resp.ContentLength != -1 {
			t.Fatalf("%s: 流式压缩后 ContentLength 应为 -1", algo)
		}
		compressed, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("%s: 读取压缩流失败: %v", algo, err)
		}
		decoded, err := Decode(compressed, string(algo))
		if err != nil {
			t.Fatalf("%s: 解压失败: %v", algo, err)
		}
		if !bytes.Equal(decoded, body) {
			t.Fatalf("%s: 解压内容与原文不一致", algo)
		}
	}
}

// 长度未知（chunked）时同样可流式压缩
func TestCompressResponseChunked(t *testing.T) {
	n := NewNegotiator(DefaultConfig())
	body := bytes.Repeat([]byte("chunked content "), 1000)
	resp := newTestResponse(body, "text/plain", -1)
	req := &http.Request{Header: http.Header{"Accept-Encoding": []string{"br"}}}

	if err := n.CompressResponse(resp, req); err != nil {
		t.Fatal(err)
	}
	compressed, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(compressed, "br")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, body) {
		t.Fatal("chunked 压缩回环不一致")
	}
}

// 已知长度过小：跳过压缩
func TestCompressResponseSkipSmall(t *testing.T) {
	n := NewNegotiator(DefaultConfig())
	body := bytes.Repeat([]byte("a"), 100)
	resp := newTestResponse(body, "text/plain", int64(len(body)))
	req := &http.Request{Header: http.Header{"Accept-Encoding": []string{"gzip"}}}

	if err := n.CompressResponse(resp, req); err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Content-Encoding") != "" {
		t.Fatal("小响应不应被压缩")
	}
}

// 明显不可压缩的内容类型：跳过压缩
func TestCompressResponseSkipIncompressible(t *testing.T) {
	n := NewNegotiator(DefaultConfig())
	body := bytes.Repeat([]byte{0xff}, 10*1024)
	resp := newTestResponse(body, "image/png", int64(len(body)))
	req := &http.Request{Header: http.Header{"Accept-Encoding": []string{"gzip"}}}

	if err := n.CompressResponse(resp, req); err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Content-Encoding") != "" {
		t.Fatal("图片等不可压缩类型不应被压缩")
	}
}

// 协商优先级：同 q 值时 zstd > brotli > gzip
func TestNegotiatePriority(t *testing.T) {
	n := NewNegotiator(DefaultConfig())
	cases := []struct {
		accept string
		want   string
	}{
		{"gzip, deflate", "gzip"},
		{"gzip, br", "br"},
		{"gzip;q=1.0, br;q=0.5", "gzip"},
		{"br;q=0.5, gzip;q=0.8, zstd;q=0.9", "zstd"},
		{"", ""},
		{"deflate", ""},
	}
	for _, c := range cases {
		if got := n.Negotiate(c.accept); got != Algorithm(c.want) {
			t.Errorf("Negotiate(%q) = %q, 期望 %q", c.accept, got, c.want)
		}
	}
}

// 已压缩响应（Content-Encoding 已存在）直接跳过
func TestCompressResponseSkipAlreadyEncoded(t *testing.T) {
	n := NewNegotiator(DefaultConfig())
	body := bytes.Repeat([]byte("x"), 10*1024)
	resp := newTestResponse(body, "application/octet-stream", int64(len(body)))
	resp.Header.Set("Content-Encoding", "gzip")
	req := &http.Request{Header: http.Header{"Accept-Encoding": []string{"br"}}}

	if err := n.CompressResponse(resp, req); err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatal("已有 Content-Encoding 的响应不应被二次压缩")
	}
}
