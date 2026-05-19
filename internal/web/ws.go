// Package web — WebSocket：实时推送 workerStatus / modelProgress / log。
package web

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/logger"
	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/worker"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(_ *http.Request) bool { return true },
}

// wsMessage 单条 WS 消息。
type wsMessage struct {
	Type    string `json:"type"`    // workerStatus / modelProgress / log
	Payload any    `json:"payload"`
}

// modelProgressEvent 单条模型下载进度。
type modelProgressEvent struct {
	Model   string `json:"model"`
	Percent int    `json:"percent"`
	Error   string `json:"error,omitempty"`
}

// modelProgressHub 内嵌一个全局广播器，避免每次 handler 直接走 conn.WriteJSON 慢拖累。
var (
	wsMu     sync.Mutex
	wsConns  = map[*websocket.Conn]chan wsMessage{}
)

// broadcastModelProgress 向所有 WS 推一条模型下载进度。
func (s *Server) broadcastModelProgress(model string, pct int, errMsg string) {
	wsMu.Lock()
	defer wsMu.Unlock()
	msg := wsMessage{Type: "modelProgress", Payload: modelProgressEvent{Model: model, Percent: pct, Error: errMsg}}
	for _, ch := range wsConns {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Warn("[web:ws] upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	send := make(chan wsMessage, 64)
	wsMu.Lock()
	wsConns[conn] = send
	wsMu.Unlock()
	defer func() {
		wsMu.Lock()
		delete(wsConns, conn)
		wsMu.Unlock()
		close(send)
	}()

	// 立即推一份当前 worker status
	send <- wsMessage{Type: "workerStatus", Payload: s.engine.Status()}

	// 订阅 state / log
	statusCh, cancelStatus := s.state.Subscribe()
	defer cancelStatus()
	logCh, cancelLog := logger.Subscribe()
	defer cancelLog()

	// 启读 goroutine 检测客户端 close
	closeCh := make(chan struct{})
	go func() {
		defer close(closeCh)
		for {
			if _, _, err := conn.NextReader(); err != nil {
				return
			}
		}
	}()

	// 启状态聚合 goroutine 转 wsMessage
	go func() {
		for snap := range statusCh {
			snap := snap // capture
			select {
			case send <- wsMessage{Type: "workerStatus", Payload: enrichStatus(snap, s)}:
			case <-closeCh:
				return
			}
		}
	}()
	go func() {
		for entry := range logCh {
			e := entry
			select {
			case send <- wsMessage{Type: "log", Payload: e}:
			case <-closeCh:
				return
			}
		}
	}()

	// 主写循环
	for {
		select {
		case msg, ok := <-send:
			if !ok {
				return
			}
			if err := conn.WriteJSON(msg); err != nil {
				return
			}
		case <-closeCh:
			return
		}
	}
}

func enrichStatus(snap worker.RuntimeStatus, s *Server) worker.RuntimeStatus {
	// 把 workerId 补上（state.Snapshot 不知 workerId）
	snap.WorkerID = s.store.GetWorker().WorkerID
	return snap
}

// 占位让 json import 不被 lint 删
var _ = json.NewEncoder
