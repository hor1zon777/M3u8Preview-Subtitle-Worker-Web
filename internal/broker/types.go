// Package broker 对接 m3u8-preview-go 服务端 v3/v4 broker 协议。
//
// 与原 TS `main/worker/types.ts` 字段一一对应。前端也直接消费这些类型的 JSON 形式，
// 因此字段名（json tag）必须与 TS 端保持完全一致。
package broker

import "errors"

// RegisterResponse — 服务端 /api/v1/worker/register 响应体（data 字段）。
type RegisterResponse struct {
	WorkerID             string   `json:"workerId"`
	ServerTime           int64    `json:"serverTime"`
	WorkerStaleThreshold int      `json:"workerStaleThreshold"`
	MaxConcurrentTasks   int      `json:"maxConcurrentTasks,omitempty"`
	AcceptedCapabilities []string `json:"acceptedCapabilities,omitempty"`
}

// ClaimedJob — /api/v1/worker/claim 200 响应中的任务 payload。
type ClaimedJob struct {
	JobID      string            `json:"jobId"`
	MediaID    string            `json:"mediaId"`
	MediaTitle string            `json:"mediaTitle,omitempty"`
	Stage      string            `json:"stage"`

	// audio_extract（subtitle worker 不应接收）
	M3U8URL string            `json:"m3u8Url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`

	// asr_subtitle 阶段字段
	AudioArtifactURL        string `json:"audioArtifactUrl,omitempty"`
	AudioArtifactSize       int64  `json:"audioArtifactSize,omitempty"`
	AudioArtifactSha256     string `json:"audioArtifactSha256,omitempty"`
	AudioArtifactFormat     string `json:"audioArtifactFormat,omitempty"`
	AudioArtifactDurationMs int64  `json:"audioArtifactDurationMs,omitempty"`

	SourceLang string `json:"sourceLang"`
	TargetLang string `json:"targetLang"`

	Attempt     int `json:"attempt,omitempty"`
	MaxAttempts int `json:"maxAttempts,omitempty"`
}

// ErrorKind — v4 错误分类，与服务端 model.ErrorKind* 一一对应。
type ErrorKind string

const (
	// retriable
	ErrKindNetworkTimeout       ErrorKind = "network_timeout"
	ErrKindNetwork5XX           ErrorKind = "network_5xx"
	ErrKindBrokerStreamTimeout  ErrorKind = "broker_stream_timeout"
	ErrKindAudioSourceTemporary ErrorKind = "audio_source_temporary"
	ErrKindWhisperOOM           ErrorKind = "whisper_oom"
	ErrKindTranslateProvider5XX ErrorKind = "translate_provider_5xx"
	ErrKindUnknown              ErrorKind = "unknown"

	// permanent
	ErrKindAuthInvalidToken         ErrorKind = "auth_invalid_token"
	ErrKindAudioSource404           ErrorKind = "audio_source_404"
	ErrKindFlacSha256Mismatch       ErrorKind = "flac_sha256_mismatch"
	ErrKindWhisperModelMissing      ErrorKind = "whisper_model_missing"
	ErrKindWhisperEmptyTranscription ErrorKind = "whisper_empty_transcription"
	ErrKindTranslateQuotaExceeded   ErrorKind = "translate_quota_exceeded"
	ErrKindConfigInvalid            ErrorKind = "config_invalid"

	// neutral
	ErrKindWorkerCapacity ErrorKind = "worker_capacity"
	ErrKindWorkerShutdown ErrorKind = "worker_shutdown"
)

// CompleteMeta — POST /api/v1/worker/jobs/:jobId/complete 的 meta multipart 字段。
type CompleteMeta struct {
	WorkerID     string `json:"workerId"`
	Size         int64  `json:"size"`
	Sha256       string `json:"sha256,omitempty"`
	SegmentCount int    `json:"segmentCount,omitempty"`
}

// APIEnvelope — 服务端通用响应包装。
type APIEnvelope[T any] struct {
	Success bool   `json:"success"`
	Data    T      `json:"data,omitempty"`
	Message string `json:"message,omitempty"`
	Code    string `json:"code,omitempty"`
}

// ErrJobLost 表示服务端已不持有该 job（HTTP 410 Gone）。pipeline 立刻放弃。
var ErrJobLost = errors.New("job lost (410 Gone)")
