// Package worker — 全局 ASR mutex。
//
// whisper-cli 子进程在单 GPU 上不能并发：显存上限 + addon 不保证 thread-safe。
// 用一个 channel 容量 1 模拟 mutex，所有 runner.runAsr 调用必须先 Acquire。
//
// 与 TS asrMutex 行为一致：进入前 enterAsrQueue 让 poller 看到队列深度；
// 退出时 exitAsrRun + exitAsrQueue。失败时也会 release（defer）。
package worker

// asrMutex 单例 mutex。同一进程内只允许一个 whisper-cli 跑。
type asrMutex struct {
	ch chan struct{}
}

var globalASRMutex = newASRMutex()

func newASRMutex() *asrMutex {
	m := &asrMutex{ch: make(chan struct{}, 1)}
	m.ch <- struct{}{} // 初始可用
	return m
}

// Acquire 阻塞直到拿到锁。
func (m *asrMutex) Acquire() {
	<-m.ch
}

// Release 释放锁。
func (m *asrMutex) Release() {
	m.ch <- struct{}{}
}

// AcquireASR 公开 API：runner 调用。
func AcquireASR() { globalASRMutex.Acquire() }

// ReleaseASR 公开 API：runner defer 调用。
func ReleaseASR() { globalASRMutex.Release() }
