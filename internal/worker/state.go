// Package worker — 全局运行时状态，单例。
//
// 与原 TS workerState 字段对齐。线程安全：所有字段读写经 sync.RWMutex。
//
// 多维并发槽位（IO/ASR/Translate）：状态机由 runner 在阶段切换时维护，
// poller 用 CanClaimNew() 决定能否再拉一个任务。
package worker

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/broker"
)

// HistoryCapacity FIFO 容量上限；超出丢最旧。
const HistoryCapacity = 50

// CurrentJob 当前 in-flight 任务，poller / runner / heartbeat / UI 共享。
type CurrentJob struct {
	JobID       string    `json:"jobId"`
	MediaID     string    `json:"mediaId"`
	MediaTitle  string    `json:"mediaTitle,omitempty"`
	DisplayName string    `json:"displayName"`
	Stage       string    `json:"stage"`
	Progress    int       `json:"progress"`
	StartedAt   int64     `json:"startedAt"` // ms epoch
	Category    string    `json:"category,omitempty"` // io/asr/translate/upload/idle
	Attempt     int       `json:"attempt,omitempty"`
	MaxAttempts int       `json:"maxAttempts,omitempty"`
}

// HistoryJob 已结束任务快照。持久化到 history.json。
type HistoryJob struct {
	JobID        string           `json:"jobId"`
	DisplayName  string           `json:"displayName"`
	MediaTitle   string           `json:"mediaTitle,omitempty"`
	FinalStage   string           `json:"finalStage"` // completed / failed / lost
	ErrorMessage string           `json:"errorMessage,omitempty"`
	EndedAt      int64            `json:"endedAt"`
	ErrorKind    broker.ErrorKind `json:"errorKind,omitempty"`
}

// SubtitleSlots 多维并发槽位。前端展示用。
type SubtitleSlots struct {
	IOMax             int `json:"ioMax"`
	ASRMax            int `json:"asrMax"`
	TranslateMax      int `json:"translateMax"`
	IOInflight        int `json:"ioInflight"`
	ASRInflight       int `json:"asrInflight"`
	ASRQueueDepth     int `json:"asrQueueDepth"`
	TranslateInflight int `json:"translateInflight"`
}

// Stats 累计计数。
type Stats struct {
	Completed int    `json:"completed"`
	Failed    int    `json:"failed"`
	LastError string `json:"lastError,omitempty"`
}

// RuntimeStatus 推给前端的实时快照。字段与原 WorkerRuntimeStatus 一致。
type RuntimeStatus struct {
	Registered         bool           `json:"registered"`
	PollingActive      bool           `json:"pollingActive"`
	StaleThresholdSec  int            `json:"staleThresholdSec"`
	MaxConcurrentTasks int            `json:"maxConcurrentTasks"`
	Slots              *SubtitleSlots `json:"slots,omitempty"`
	WorkerID           string         `json:"workerId"`
	UptimeSec          int            `json:"uptimeSec"`
	CurrentJobs        []CurrentJob   `json:"currentJobs"`
	HistoryJobs        []HistoryJob   `json:"historyJobs"`
	Stats              Stats          `json:"stats"`
}

// State 单例运行时状态。NewState() 构造，跨整个进程生命周期共享。
type State struct {
	mu sync.RWMutex

	registered    bool
	pollingActive bool

	staleThresholdSec       int
	maxConcurrentTasks      int
	serverMaxConcurrent     int
	localMaxConcurrent      int
	startedAt               time.Time

	currentJobs map[string]*CurrentJob
	historyJobs []HistoryJob
	stats       Stats

	// 多维并发槽位
	ioMax             int
	asrMax            int
	translateMax      int
	ioInflight        int
	asrInflight       int
	asrQueueDepth     int
	translateInflight int

	// 持久化
	historyPath string

	// 订阅状态变更（用于 WS 广播）
	subscribers map[chan RuntimeStatus]struct{}
}

// NewState 构造并从 historyPath 加载历史快照。
func NewState(historyPath string) *State {
	s := &State{
		staleThresholdSec:  60,
		maxConcurrentTasks: 1,
		serverMaxConcurrent: 1,
		startedAt:          time.Now(),
		currentJobs:        map[string]*CurrentJob{},
		ioMax:              4,
		asrMax:             1,
		translateMax:       2,
		historyPath:        historyPath,
		subscribers:        map[chan RuntimeStatus]struct{}{},
	}
	s.loadHistory()
	return s
}

// --- 注册 / polling 标志 ---

func (s *State) SetRegistered(yes bool) {
	s.mu.Lock()
	s.registered = yes
	if !yes {
		s.pollingActive = false
	}
	s.mu.Unlock()
	s.broadcast()
}

func (s *State) SetPollingActive(yes bool) {
	s.mu.Lock()
	s.pollingActive = yes
	s.mu.Unlock()
	s.broadcast()
}

func (s *State) SetStaleThreshold(sec int) {
	s.mu.Lock()
	if sec < 15 {
		sec = 60
	}
	s.staleThresholdSec = sec
	s.mu.Unlock()
}

// SetServerMaxConcurrent 从 register 响应回填；本地 override 优先。
func (s *State) SetServerMaxConcurrent(n int) {
	s.mu.Lock()
	if n < 1 {
		n = 1
	}
	s.serverMaxConcurrent = n
	s.applyEffectiveLocked()
	s.mu.Unlock()
	s.broadcast()
}

func (s *State) SetLocalMaxConcurrent(n int) {
	s.mu.Lock()
	if n < 0 {
		n = 0
	}
	s.localMaxConcurrent = n
	s.applyEffectiveLocked()
	s.mu.Unlock()
	s.broadcast()
}

func (s *State) applyEffectiveLocked() {
	eff := s.serverMaxConcurrent
	if s.localMaxConcurrent > 0 {
		eff = s.localMaxConcurrent
	}
	if eff < 1 {
		eff = 1
	}
	s.maxConcurrentTasks = eff
	s.ioMax = eff
}

// CanClaimNew 多维并发判定。
//
// 规则：
//  1. currentJobs.size < maxConcurrentTasks
//  2. ioInflight < ioMax
//  3. asrQueueDepth < maxConcurrentTasks
func (s *State) CanClaimNew() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.currentJobs) >= s.maxConcurrentTasks {
		return false
	}
	if s.ioInflight >= s.ioMax {
		return false
	}
	if s.asrQueueDepth >= s.maxConcurrentTasks {
		return false
	}
	return true
}

// --- 多维并发计数器 ---

func (s *State) EnterIO()    { s.mu.Lock(); s.ioInflight++; s.mu.Unlock(); s.broadcast() }
func (s *State) ExitIO()     { s.mu.Lock(); if s.ioInflight > 0 { s.ioInflight-- }; s.mu.Unlock(); s.broadcast() }
func (s *State) EnterAsrQ()  { s.mu.Lock(); s.asrQueueDepth++; s.mu.Unlock(); s.broadcast() }
func (s *State) ExitAsrQ()   { s.mu.Lock(); if s.asrQueueDepth > 0 { s.asrQueueDepth-- }; s.mu.Unlock(); s.broadcast() }
func (s *State) EnterAsrRun() { s.mu.Lock(); s.asrInflight++; s.mu.Unlock(); s.broadcast() }
func (s *State) ExitAsrRun()  { s.mu.Lock(); if s.asrInflight > 0 { s.asrInflight-- }; s.mu.Unlock(); s.broadcast() }
func (s *State) EnterTr()    { s.mu.Lock(); s.translateInflight++; s.mu.Unlock(); s.broadcast() }
func (s *State) ExitTr()     { s.mu.Lock(); if s.translateInflight > 0 { s.translateInflight-- }; s.mu.Unlock(); s.broadcast() }

// --- 当前任务 ---

func (s *State) UpsertJob(job CurrentJob) {
	s.mu.Lock()
	cp := job
	s.currentJobs[job.JobID] = &cp
	s.mu.Unlock()
	s.broadcast()
}

func (s *State) UpdateProgress(jobID, stage string, progress int) {
	s.mu.Lock()
	if j, ok := s.currentJobs[jobID]; ok {
		j.Stage = stage
		j.Progress = progress
	}
	s.mu.Unlock()
	s.broadcast()
}

func (s *State) SetCategory(jobID, category string) {
	s.mu.Lock()
	if j, ok := s.currentJobs[jobID]; ok {
		j.Category = category
	}
	s.mu.Unlock()
}

func (s *State) RemoveJob(jobID string) {
	s.mu.Lock()
	delete(s.currentJobs, jobID)
	s.mu.Unlock()
	s.broadcast()
}

// --- 历史快照 ---

func (s *State) PushHistory(h HistoryJob) {
	s.mu.Lock()
	s.historyJobs = append([]HistoryJob{h}, s.historyJobs...)
	if len(s.historyJobs) > HistoryCapacity {
		s.historyJobs = s.historyJobs[:HistoryCapacity]
	}
	s.persistHistoryLocked()
	s.mu.Unlock()
	s.broadcast()
}

func (s *State) ClearHistory() {
	s.mu.Lock()
	s.historyJobs = nil
	s.persistHistoryLocked()
	s.mu.Unlock()
	s.broadcast()
}

// --- 统计 ---

func (s *State) RecordSuccess() {
	s.mu.Lock()
	s.stats.Completed++
	s.stats.LastError = ""
	s.mu.Unlock()
	s.broadcast()
}

func (s *State) RecordFailure(err string) {
	s.mu.Lock()
	s.stats.Failed++
	if len(err) > 500 {
		err = err[:500]
	}
	s.stats.LastError = err
	s.mu.Unlock()
	s.broadcast()
}

// Snapshot 返回当前状态的深拷贝。
func (s *State) Snapshot() RuntimeStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cur := make([]CurrentJob, 0, len(s.currentJobs))
	for _, j := range s.currentJobs {
		cur = append(cur, *j)
	}
	hist := make([]HistoryJob, len(s.historyJobs))
	copy(hist, s.historyJobs)
	slots := SubtitleSlots{
		IOMax:             s.ioMax,
		ASRMax:            s.asrMax,
		TranslateMax:      s.translateMax,
		IOInflight:        s.ioInflight,
		ASRInflight:       s.asrInflight,
		ASRQueueDepth:     s.asrQueueDepth,
		TranslateInflight: s.translateInflight,
	}
	return RuntimeStatus{
		Registered:         s.registered,
		PollingActive:      s.pollingActive,
		StaleThresholdSec:  s.staleThresholdSec,
		MaxConcurrentTasks: s.maxConcurrentTasks,
		Slots:              &slots,
		WorkerID:           "", // 由 lifecycle 在 Snapshot 后补
		UptimeSec:          int(time.Since(s.startedAt).Seconds()),
		CurrentJobs:        cur,
		HistoryJobs:        hist,
		Stats:              s.stats,
	}
}

// --- 订阅（WS 广播）---

func (s *State) Subscribe() (<-chan RuntimeStatus, func()) {
	ch := make(chan RuntimeStatus, 16)
	s.mu.Lock()
	s.subscribers[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subscribers, ch)
		s.mu.Unlock()
		close(ch)
	}
}

func (s *State) broadcast() {
	snap := s.Snapshot()
	s.mu.RLock()
	subs := make([]chan RuntimeStatus, 0, len(s.subscribers))
	for ch := range s.subscribers {
		subs = append(subs, ch)
	}
	s.mu.RUnlock()
	for _, ch := range subs {
		select {
		case ch <- snap:
		default:
			// 慢消费者：丢
		}
	}
}

// --- history 持久化 ---

func (s *State) loadHistory() {
	raw, err := os.ReadFile(s.historyPath)
	if err != nil {
		return
	}
	var entries []HistoryJob
	if err := json.Unmarshal(raw, &entries); err == nil {
		if len(entries) > HistoryCapacity {
			entries = entries[:HistoryCapacity]
		}
		s.historyJobs = entries
	}
}

func (s *State) persistHistoryLocked() {
	raw, err := json.MarshalIndent(s.historyJobs, "", "  ")
	if err != nil {
		return
	}
	tmp := s.historyPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, s.historyPath)
}
