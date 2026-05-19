// Package worker — 错误分类。完全对齐原 TS classifySubtitleError。
package worker

import (
	"strings"

	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/broker"
)

// ClassifyError 将错误字符串映射到服务端 ErrorKind 枚举。
//
// 用于上报 fail 时让服务端按 retriable/permanent/neutral 策略分流。匹配不到 → unknown（retriable）。
func ClassifyError(msg string) broker.ErrorKind {
	lower := strings.ToLower(msg)

	// permanent
	switch {
	case strings.Contains(lower, "whisper 输出 srt 为空"),
		strings.Contains(lower, "whisper output srt is empty"),
		strings.Contains(lower, "srt is empty"),
		strings.Contains(lower, "transcription=[]"),
		strings.Contains(lower, "empty transcription"),
		strings.Contains(lower, "0 segments"):
		return broker.ErrKindWhisperEmptyTranscription
	case strings.Contains(lower, "whisper 未生成 srt"),
		strings.Contains(lower, "model not found"),
		strings.Contains(lower, "invalid model size"),
		strings.Contains(lower, "modelspath"),
		strings.Contains(lower, "cannot find model"),
		strings.Contains(lower, "whispermodel init failed"),
		strings.Contains(lower, "no such file") && strings.Contains(lower, "ggml-"):
		return broker.ErrKindWhisperModelMissing
	case strings.Contains(lower, "sha256 mismatch"),
		strings.Contains(lower, "flac size mismatch"),
		strings.Contains(lower, "corrupt input"),
		strings.Contains(lower, "flate:"):
		return broker.ErrKindFlacSha256Mismatch
	case strings.Contains(lower, "401"),
		strings.Contains(lower, "403"),
		strings.Contains(lower, "unauthorized"):
		return broker.ErrKindAuthInvalidToken
	case strings.Contains(lower, "404"),
		strings.Contains(lower, "not found"):
		return broker.ErrKindAudioSource404
	case strings.Contains(lower, "translate provider") &&
		(strings.Contains(lower, "quota") ||
			strings.Contains(lower, "rate limit") ||
			strings.Contains(lower, "429")):
		return broker.ErrKindTranslateQuotaExceeded
	case strings.Contains(lower, "找不到") && strings.Contains(lower, "provider"):
		return broker.ErrKindConfigInvalid
	}

	// retriable
	switch {
	case strings.Contains(lower, "timeout"),
		strings.Contains(lower, "timed out"),
		strings.Contains(lower, "deadline exceeded"):
		return broker.ErrKindNetworkTimeout
	case strings.Contains(lower, "502"),
		strings.Contains(lower, "503"),
		strings.Contains(lower, "504"):
		return broker.ErrKindNetwork5XX
	case strings.Contains(lower, "content-length=0"),
		strings.Contains(lower, "30s 内将 flac"),
		strings.Contains(lower, "5min 内将 flac"):
		return broker.ErrKindBrokerStreamTimeout
	case strings.Contains(lower, "out of memory"),
		strings.Contains(lower, "cuda oom"):
		return broker.ErrKindWhisperOOM
	case strings.Contains(lower, "connection reset"),
		strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "econnreset"),
		strings.Contains(lower, "econnrefused"):
		return broker.ErrKindNetworkTimeout
	}

	return broker.ErrKindUnknown
}
