// Package compress 提供响应压缩功能，支持 gzip、brotli、zstd
// 压缩优先级：zstd > brotli > gzip（基于客户端 Accept-Encoding 协商）
package compress

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

// Algorithm 压缩算法类型
type Algorithm string

const (
	Gzip   Algorithm = "gzip"
	Brotli Algorithm = "br"
	Zstd   Algorithm = "zstd"
)

// AlgorithmConfig 单个压缩算法的配置
type AlgorithmConfig struct {
	Enabled bool `mapstructure:"enabled" toml:"enabled"`
	Level   int  `mapstructure:"level" toml:"level"`
}

// Config 压缩模块配置
type Config struct {
	Enabled bool                          `mapstructure:"enabled" toml:"enabled"`
	Algos   map[Algorithm]AlgorithmConfig `mapstructure:"algos" toml:"algos"`
}

// DefaultConfig 返回默认压缩配置
func DefaultConfig() Config {
	return Config{
		Enabled: true,
		Algos: map[Algorithm]AlgorithmConfig{
			Gzip:   {Enabled: true, Level: 5},
			Brotli: {Enabled: true, Level: 4},
			Zstd:   {Enabled: true, Level: 3},
		},
	}
}

// Negotiator 压缩协商器，根据客户端 Accept-Encoding 选择最佳压缩算法
type Negotiator struct {
	enabled bool
	algos   map[Algorithm]AlgorithmConfig
	// 压缩 writer 池
	gzipWriters   sync.Pool
	brotliWriters sync.Pool
	zstdEncoders  sync.Pool
	// 缓冲区池
	bufPool sync.Pool
}

// NewNegotiator 创建压缩协商器
func NewNegotiator(cfg Config) *Negotiator {
	n := &Negotiator{
		enabled: cfg.Enabled,
		algos:   cfg.Algos,
	}

	if !n.enabled {
		return n
	}

	// 确保 algos 不为 nil
	if n.algos == nil {
		n.algos = DefaultConfig().Algos
	}

	// 初始化 writer 池
	n.gzipWriters = sync.Pool{
		New: func() any {
			w, _ := gzip.NewWriterLevel(io.Discard, n.getLevel(Gzip))
			return w
		},
	}
	n.brotliWriters = sync.Pool{
		New: func() any {
			w := brotli.NewWriterLevel(io.Discard, n.getLevel(Brotli))
			return w
		},
	}
	n.zstdEncoders = sync.Pool{
		New: func() any {
			w, _ := zstd.NewWriter(io.Discard, zstd.WithEncoderLevel(zstd.EncoderLevel(n.getLevel(Zstd))))
			return w
		},
	}

	n.bufPool = sync.Pool{
		New: func() any {
			return bytes.NewBuffer(make([]byte, 0, 32*1024))
		},
	}

	return n
}

func (n *Negotiator) getLevel(algo Algorithm) int {
	if cfg, ok := n.algos[algo]; ok && cfg.Level > 0 {
		return cfg.Level
	}
	switch algo {
	case Gzip:
		return 5
	case Brotli:
		return 4
	case Zstd:
		return 3
	}
	return 5
}

func (n *Negotiator) isAlgoEnabled(algo Algorithm) bool {
	if !n.enabled {
		return false
	}
	cfg, ok := n.algos[algo]
	return ok && cfg.Enabled
}

func (n *Negotiator) getBuf() *bytes.Buffer {
	v := n.bufPool.Get()
	if v != nil {
		buf := v.(*bytes.Buffer)
		buf.Reset()
		return buf
	}
	return bytes.NewBuffer(make([]byte, 0, 32*1024))
}

func (n *Negotiator) putBuf(buf *bytes.Buffer) {
	if buf.Cap() > 4*1024*1024 { // >4MB 不回收
		return
	}
	buf.Reset()
	n.bufPool.Put(buf)
}

// Negotiate 根据客户端 Accept-Encoding 协商最佳压缩算法
// 优先级：zstd > brotli > gzip
func (n *Negotiator) Negotiate(acceptEncoding string) Algorithm {
	if !n.enabled || acceptEncoding == "" {
		return ""
	}

	// 解析 Accept-Encoding，检查各算法的支持情况
	// Accept-Encoding 格式：gzip, deflate, br;q=1.0, zstd;q=0.9
	bestAlgo := Algorithm("")
	bestQ := float64(-1)

	for _, part := range strings.Split(acceptEncoding, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// 分离算法名和 q 值
		var algoName string
		q := 1.0
		if before, after, ok := strings.Cut(part, ";"); ok {
			algoName = strings.TrimSpace(before)
			// 解析 q 值
			params := after
			if strings.Contains(params, "q=") {
				qStart := strings.Index(params, "q=") + 2
				qEnd := strings.IndexByte(params[qStart:], ';')
				qStr := params[qStart:]
				if qEnd >= 0 {
					qStr = params[qStart : qStart+qEnd]
				}
				if parsed, err := strconv.ParseFloat(strings.TrimSpace(qStr), 64); err == nil {
					q = parsed
				}
			}
		} else {
			algoName = part
		}

		// 将客户端算法名映射到我们的 Algorithm 类型
		var algo Algorithm
		switch algoName {
		case "zstd":
			algo = Zstd
		case "br":
			algo = Brotli
		case "gzip":
			algo = Gzip
		default:
			continue
		}

		if !n.isAlgoEnabled(algo) {
			continue
		}

		// 相同 q 值时按优先级选择：zstd > brotli > gzip
		if q > bestQ || (q == bestQ && algoPriority(algo) > algoPriority(bestAlgo)) {
			bestQ = q
			bestAlgo = algo
		}
	}

	return bestAlgo
}

// algoPriority 返回算法优先级（值越大越优先）
func algoPriority(algo Algorithm) int {
	switch algo {
	case Zstd:
		return 3
	case Brotli:
		return 2
	case Gzip:
		return 1
	default:
		return 0
	}
}

// Compress 使用指定算法压缩数据
func (n *Negotiator) Compress(body []byte, algo Algorithm) ([]byte, error) {
	if !n.enabled || len(body) == 0 {
		return body, nil
	}

	switch algo {
	case Gzip:
		return n.compressGzip(body)
	case Brotli:
		return n.compressBrotli(body)
	case Zstd:
		return n.compressZstd(body)
	}
	return body, nil
}

func (n *Negotiator) compressGzip(body []byte) ([]byte, error) {
	buf := n.getBuf()
	defer n.putBuf(buf)

	w, ok := n.gzipWriters.Get().(*gzip.Writer)
	if !ok {
		w, _ = gzip.NewWriterLevel(buf, n.getLevel(Gzip))
	} else {
		w.Reset(buf)
	}

	if _, err := w.Write(body); err != nil {
		w.Reset(io.Discard)
		n.gzipWriters.Put(w)
		return nil, err
	}
	if err := w.Close(); err != nil {
		w.Reset(io.Discard)
		n.gzipWriters.Put(w)
		return nil, err
	}
	w.Reset(io.Discard)
	n.gzipWriters.Put(w)

	result := make([]byte, buf.Len())
	copy(result, buf.Bytes())
	return result, nil
}

func (n *Negotiator) compressBrotli(body []byte) ([]byte, error) {
	buf := n.getBuf()
	defer n.putBuf(buf)

	w, ok := n.brotliWriters.Get().(*brotli.Writer)
	if !ok {
		w = brotli.NewWriterLevel(buf, n.getLevel(Brotli))
	} else {
		w.Reset(buf)
	}

	if _, err := w.Write(body); err != nil {
		n.brotliWriters.Put(w)
		return nil, err
	}
	if err := w.Flush(); err != nil {
		n.brotliWriters.Put(w)
		return nil, err
	}
	w.Reset(nil)
	n.brotliWriters.Put(w)

	result := make([]byte, buf.Len())
	copy(result, buf.Bytes())
	return result, nil
}

func (n *Negotiator) compressZstd(body []byte) ([]byte, error) {
	buf := n.getBuf()
	defer n.putBuf(buf)

	w, ok := n.zstdEncoders.Get().(*zstd.Encoder)
	if !ok {
		var err error
		w, err = zstd.NewWriter(buf, zstd.WithEncoderLevel(zstd.EncoderLevel(n.getLevel(Zstd))))
		if err != nil {
			return nil, err
		}
	} else {
		w.Reset(buf)
	}

	if _, err := w.Write(body); err != nil {
		w.Reset(io.Discard)
		n.zstdEncoders.Put(w)
		return nil, err
	}
	if err := w.Close(); err != nil {
		w.Reset(io.Discard)
		n.zstdEncoders.Put(w)
		return nil, err
	}
	w.Reset(io.Discard)
	n.zstdEncoders.Put(w)

	result := make([]byte, buf.Len())
	copy(result, buf.Bytes())
	return result, nil
}

// CompressResponse 压缩 HTTP 响应体
// 根据原始请求的 Accept-Encoding 选择最佳算法
// 不会压缩已有 Content-Encoding 的响应
func (n *Negotiator) CompressResponse(resp *http.Response, origReq *http.Request) error {
	if !n.enabled {
		return nil
	}

	// 已有 Content-Encoding，跳过（可能来自上游的压缩透传）
	if resp.Header.Get("Content-Encoding") != "" {
		return nil
	}

	// 无 body，跳过
	if resp.Body == nil {
		return nil
	}

	// 协商压缩算法
	acceptEncoding := origReq.Header.Get("Accept-Encoding")
	algo := n.Negotiate(acceptEncoding)
	if algo == "" {
		return nil
	}

	// 读取 body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	resp.Body.Close()

	// 小于 1400 字节不压缩（压缩反而可能增大，且节省有限）
	if len(body) < 1400 {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	}

	// 压缩
	compressed, err := n.Compress(body, algo)
	if err != nil {
		// 压缩失败，回退到未压缩
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return err
	}

	// 压缩后更大则不使用压缩
	if len(compressed) >= len(body) {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	}

	// 更新响应
	resp.Body = io.NopCloser(bytes.NewReader(compressed))
	resp.ContentLength = int64(len(compressed))
	resp.Header.Set("Content-Length", strconv.Itoa(len(compressed)))
	resp.Header.Set("Content-Encoding", string(algo))
	resp.Header.Add("Vary", "Accept-Encoding")
	// 删除可能冲突的头部
	resp.Header.Del("Transfer-Encoding")

	return nil
}

// ContentEncoding 返回算法对应的 Content-Encoding 值
func (algo Algorithm) ContentEncoding() string {
	return string(algo)
}

// Decode 解压数据，根据 Content-Encoding 值选择解压算法
// 支持 gzip、brotli(br)、zstd
func Decode(data []byte, encoding string) ([]byte, error) {
	switch strings.ToLower(encoding) {
	case "gzip":
		return decodeGzip(data)
	case "br", "brotli":
		return decodeBrotli(data)
	case "zstd":
		return decodeZstd(data)
	default:
		return data, nil // 未知编码，返回原始数据
	}
}

func decodeGzip(data []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func decodeBrotli(data []byte) ([]byte, error) {
	r := brotli.NewReader(bytes.NewReader(data))
	return io.ReadAll(r)
}

func decodeZstd(data []byte) ([]byte, error) {
	decoder, err := zstd.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer decoder.Close()
	return io.ReadAll(decoder)
}
