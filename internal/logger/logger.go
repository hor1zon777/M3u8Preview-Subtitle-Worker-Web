// Package logger 提供进程级日志记录 + 内存环形缓冲区 + 订阅广播。
//
// 设计：
//   - 主进程通过 logger.Info/Warn/Error/Debug 写日志，输出到 stderr 并落入环形缓冲区
//   - Web 层订阅 Subscribe() 拿到 channel，每条新日志推过来 → WebSocket 转发到浏览器
//   - 环形缓冲区容量 1000 条，超出按 FIFO 丢最旧
//   - Debug 级别受全局开关控制：关闭时 Debug() 直接 no-op，避免噪音与内存浪费
//
// 与原 helpers/logger.ts 行为对齐：UI 上有"最近日志"列表 + 错误显示。
package logger

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Level 日志级别。和原 SmartSub 的 'info' | 'warning' | 'error' 对齐，并扩展 'debug'。
type Level string

const (
	LevelDebug   Level = "debug"
	LevelInfo    Level = "info"
	LevelWarning Level = "warning"
	LevelError   Level = "error"
)

// Entry 单条日志记录。结构与原 logs store 一致，便于前端直接消费。
type Entry struct {
	Timestamp int64  `json:"timestamp"` // ms epoch
	Message   string `json:"message"`
	Type      Level  `json:"type"`
}

const ringCapacity = 1000

var (
	mu          sync.Mutex
	ring        = make([]Entry, 0, ringCapacity)
	subscribers = map[chan Entry]struct{}{}

	// debugEnabled 控制 Debug 级别是否实际输出。原子读写避免每次日志都拿锁。
	debugEnabled atomic.Bool
)

// SetDebug 切换全局 Debug 开关。配置加载或保存时调用。
func SetDebug(enabled bool) {
	prev := debugEnabled.Swap(enabled)
	if prev == enabled {
		return
	}
	// 开关变化本身记一条 info 方便排查"为什么没看到 debug 日志"
	if enabled {
		Info("[logger] debug mode enabled")
	} else {
		Info("[logger] debug mode disabled")
	}
}

// IsDebug 是否启用 Debug 输出。
func IsDebug() bool { return debugEnabled.Load() }

// Log 写一条日志。Debug 级别在开关关闭时被丢弃。
func Log(level Level, format string, args ...any) {
	if level == LevelDebug && !debugEnabled.Load() {
		return
	}
	msg := fmt.Sprintf(format, args...)
	entry := Entry{
		Timestamp: time.Now().UnixMilli(),
		Message:   msg,
		Type:      level,
	}
	// 输出到 stderr，docker logs / journalctl 都能看到
	fmt.Fprintf(os.Stderr, "[%s] %s\n", level, msg)

	mu.Lock()
	if len(ring) >= ringCapacity {
		// FIFO 丢最旧：拷贝避免底层切片无限增长
		ring = append(ring[:0], ring[1:]...)
	}
	ring = append(ring, entry)
	subs := make([]chan Entry, 0, len(subscribers))
	for ch := range subscribers {
		subs = append(subs, ch)
	}
	mu.Unlock()

	// 非阻塞广播：subscriber 满了就丢；避免一个慢消费者卡住 logger
	for _, ch := range subs {
		select {
		case ch <- entry:
		default:
		}
	}
}

// Debug Info Warn Error 是 Log 的便捷封装。
func Debug(format string, args ...any) { Log(LevelDebug, format, args...) }
func Info(format string, args ...any)  { Log(LevelInfo, format, args...) }
func Warn(format string, args ...any)  { Log(LevelWarning, format, args...) }
func Error(format string, args ...any) { Log(LevelError, format, args...) }

// Snapshot 返回当前环形缓冲区的快照（拷贝）。
func Snapshot() []Entry {
	mu.Lock()
	defer mu.Unlock()
	cp := make([]Entry, len(ring))
	copy(cp, ring)
	return cp
}

// Subscribe 返回一个 channel，每条新日志推过来。
// buffer=64 应对短时突发；调用方应及时消费。
// 返回的 cancel 调用以注销订阅。
func Subscribe() (<-chan Entry, func()) {
	ch := make(chan Entry, 64)
	mu.Lock()
	subscribers[ch] = struct{}{}
	mu.Unlock()
	return ch, func() {
		mu.Lock()
		delete(subscribers, ch)
		mu.Unlock()
		close(ch)
	}
}

// Clear 清空环形缓冲区（用于 UI"清空日志"按钮）。
func Clear() {
	mu.Lock()
	ring = ring[:0]
	mu.Unlock()
}
