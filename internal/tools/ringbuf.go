// internal/tools/ringbuf.go
// RingBuffer 是一个线程安全的定长环形字节缓冲区。
// 借鉴操作系统内核中 kfifo 的设计理念:当缓冲区写满时,
// 最旧的数据会被新数据静默覆盖,从而保证内存使用始终有上界。
// 用于后台常驻进程的 stdout/stderr 日志捕获,防止 OOM。
package tools

import "sync"

// RingBuffer 线程安全的环形字节缓冲区
type RingBuffer struct {
	buf   []byte // 底层字节数组
	size  int    // 总容量(字节)
	start int    // 当前有效数据的起始索引
	count int    // 当前有效数据的字节数(0 <= count <= size)
	mu    sync.RWMutex
}

// NewRingBuffer 创建一个容量为 size 字节的环形缓冲区
func NewRingBuffer(size int) *RingBuffer {
	return &RingBuffer{
		buf:  make([]byte, size),
		size: size,
	}
}

// Write 实现 io.Writer 接口。
// 写入的数据追加到缓冲区尾部;若缓冲区已满,最旧的数据被覆盖。
// 此方法线程安全,多个 goroutine 可并发写入。
func (rb *RingBuffer) Write(p []byte) (n int, err error) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	for _, b := range p {
		pos := (rb.start + rb.count) % rb.size
		rb.buf[pos] = b
		if rb.count < rb.size {
			rb.count++
		} else {
			// 缓冲区已满,移动 start 指针"丢弃"最旧的一个字节
			rb.start = (rb.start + 1) % rb.size
		}
	}
	return len(p), nil
}

// String 返回缓冲区中当前所有有效数据的字符串表示。
// 数据按写入顺序排列(先进先出),不修改缓冲区状态。
func (rb *RingBuffer) String() string {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	if rb.count == 0 {
		return ""
	}

	result := make([]byte, rb.count)
	for i := 0; i < rb.count; i++ {
		result[i] = rb.buf[(rb.start+i)%rb.size]
	}
	return string(result)
}

// Len 返回缓冲区中当前有效数据的字节数
func (rb *RingBuffer) Len() int {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	return rb.count
}

// Reset 清空缓冲区,保留底层数组容量
func (rb *RingBuffer) Reset() {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.start = 0
	rb.count = 0
}
