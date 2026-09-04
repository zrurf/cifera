// Package pool 提供高性能对象池，减少 GC 压力
package pool

import (
	"bytes"
	"sync"
)

const (
	// 小缓冲区默认大小
	smallBufSize = 32 * 1024 // 32KB
	// 大缓冲区默认大小
	largeBufSize = 256 * 1024 // 256KB
)

var smallBufPool = sync.Pool{
	New: func() any {
		buf := bytes.NewBuffer(make([]byte, 0, smallBufSize))
		return buf
	},
}

var largeBufPool = sync.Pool{
	New: func() any {
		buf := bytes.NewBuffer(make([]byte, 0, largeBufSize))
		return buf
	},
}

// byteSlicePools 分级字节切片池（用于 io.ReadAll 等读取缓冲）
var byteSlicePools = [3]sync.Pool{
	{New: func() any { return make([]byte, 0, 4*1024) }},   // 4KB
	{New: func() any { return make([]byte, 0, 64*1024) }},  // 64KB
	{New: func() any { return make([]byte, 0, 512*1024) }}, // 512KB
}

// GetBuffer 获取一个缓冲区，hint 为预估数据大小
func GetBuffer(hint int) *bytes.Buffer {
	if hint > smallBufSize {
		if v := largeBufPool.Get(); v != nil {
			buf := v.(*bytes.Buffer)
			buf.Reset()
			return buf
		}
	}
	if v := smallBufPool.Get(); v != nil {
		buf := v.(*bytes.Buffer)
		buf.Reset()
		return buf
	}
	return bytes.NewBuffer(make([]byte, 0, min(hint, smallBufSize)))
}

// PutBuffer 归还缓冲区到对象池
func PutBuffer(buf *bytes.Buffer) {
	if buf == nil {
		return
	}
	// 超过 1MB 的缓冲区不回收，防止内存泄漏
	if buf.Cap() > 1024*1024 {
		return
	}
	buf.Reset()
	if buf.Cap() <= smallBufSize {
		smallBufPool.Put(buf)
	} else {
		largeBufPool.Put(buf)
	}
}

// GetByteSlice 获取一个字节切片，sizeClass 为大小等级 (0=4KB, 1=64KB, 2=512KB)
func GetByteSlice(sizeClass int) []byte {
	if sizeClass < 0 || sizeClass >= len(byteSlicePools) {
		return make([]byte, 0)
	}
	if v := byteSlicePools[sizeClass].Get(); v != nil {
		return v.([]byte)[:0]
	}
	return make([]byte, 0)
}

// PutByteSlice 归还字节切片到对象池
func PutByteSlice(sizeClass int, bs []byte) {
	if sizeClass < 0 || sizeClass >= len(byteSlicePools) {
		return
	}
	byteSlicePools[sizeClass].Put(bs[:0])
}
