// Package config 负责持久化所有用户配置：worker / settings / providers / customParameters。
//
// 与原 Electron 项目 store 结构对齐：每个顶层 key 一个独立 JSON 字段。
// 历史任务 history 单独写 history.json，与 config.json 解耦（history 频繁写）。
//
// 落盘路径：
//   - $XDG_CONFIG_HOME/m3u8-subtitle-worker/config.json   配置
//   - $XDG_DATA_HOME/m3u8-subtitle-worker/history.json    历史任务
//   - $XDG_DATA_HOME/m3u8-subtitle-worker/whisper-models  Whisper 模型默认目录
//
// 可由 env 覆盖（部署常用）：
//   - MWS_CONFIG_DIR：覆盖配置目录
//   - MWS_DATA_DIR：覆盖数据目录（含模型）
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/google/uuid"
)

// WorkerSettings 对接 m3u8-preview-go v4 broker 协议；与原 TS WorkerSettings 1:1。
type WorkerSettings struct {
	BaseURL                 string `json:"baseUrl"`
	Token                   string `json:"token"`
	PollIntervalSec         int    `json:"pollIntervalSec"`
	HeartbeatIntervalSec    int    `json:"heartbeatIntervalSec"`
	ErrorBackoffSec         int    `json:"errorBackoffSec"`
	VerifyTLS               bool   `json:"verifyTls"`
	WorkerName              string `json:"workerName"`
	WorkerID                string `json:"workerId"`
	Enabled                 bool   `json:"enabled"`
	WhisperModel            string `json:"whisperModel"`
	SourceLanguage          string `json:"sourceLanguage"`
	TargetLanguage          string `json:"targetLanguage"`
	TranslateProviderID     string `json:"translateProviderId"`
	WhisperPrompt           string `json:"whisperPrompt"`
	WhisperMaxContext       int    `json:"whisperMaxContext"`
	LocalMaxConcurrentTasks int    `json:"localMaxConcurrentTasks"`
}

// SystemSettings 沿用 SmartSub Settings 全局配置（VAD/CUDA/路径）。
type SystemSettings struct {
	Language             string  `json:"language"`             // "zh" / "en"
	UseCuda              bool    `json:"useCuda"`              // -ng=false（即用 GPU）
	ModelsPath           string  `json:"modelsPath"`           // Whisper 模型目录
	WhisperCliPath       string  `json:"whisperCliPath"`       // whisper-cli 可执行路径
	FFmpegPath           string  `json:"ffmpegPath"`           // ffmpeg 可执行路径
	AssetsPath           string  `json:"assetsPath"`           // VAD silero 模型等附带资源目录
	UseVAD               bool    `json:"useVAD"`
	VadThreshold         float64 `json:"vadThreshold"`
	VadMinSpeechDuration int     `json:"vadMinSpeechDuration"` // ms
	VadMinSilenceDuration int    `json:"vadMinSilenceDuration"` // ms
	VadMaxSpeechDuration int     `json:"vadMaxSpeechDuration"`  // ms; 0 = 无上限
	VadSpeechPad         int     `json:"vadSpeechPad"`          // ms
	VadSamplesOverlap    float64 `json:"vadSamplesOverlap"`     // 0-1
	Debug                bool    `json:"debug"`                 // 调试模式：输出每一步骤的详细日志
	WebToken             string  `json:"webToken"`              // Web UI 访问令牌；空 = 不强制认证
}

// Provider 翻译服务条目。字段尽量与原 TS Provider 对齐（前端直接消费同一 JSON）。
type Provider struct {
	ID                  string         `json:"id"`
	Name                string         `json:"name"`
	Type                string         `json:"type"` // openai / volc / baidu / aliyun / doubao / google / azure / azureopenai / ollama / deeplx
	IsAi                bool           `json:"isAi"`
	APIURL              string         `json:"apiUrl,omitempty"`
	APIKey              string         `json:"apiKey,omitempty"`
	APISecret           string         `json:"apiSecret,omitempty"`
	ModelName           string         `json:"modelName,omitempty"`
	Prompt              string         `json:"prompt,omitempty"`
	SystemPrompt        string         `json:"systemPrompt,omitempty"`
	UseBatchTranslation bool           `json:"useBatchTranslation,omitempty"`
	BatchSize           int            `json:"batchSize,omitempty"`
	Concurrency         int            `json:"concurrency,omitempty"`
	RequestInterval     float64        `json:"requestInterval,omitempty"` // 秒
	StructuredOutput    string         `json:"structuredOutput,omitempty"` // disabled / json_object / json_schema
	UseJsonMode         *bool          `json:"useJsonMode,omitempty"`
	Endpoint            string         `json:"endpoint,omitempty"`         // aliyun
	ProviderType        string         `json:"providerType,omitempty"`
	CustomParameters    map[string]any `json:"customParameters,omitempty"`
}

// Config 顶层配置，整体 JSON 落盘到 config.json。
type Config struct {
	Worker               WorkerSettings        `json:"worker"`
	Settings             SystemSettings        `json:"settings"`
	TranslationProviders []Provider            `json:"translationProviders"`
	CustomParameters     map[string]any        `json:"customParameters,omitempty"`
}

// DefaultWorkerSettings 与原 DEFAULT_WORKER_SETTINGS 一致。
func DefaultWorkerSettings() WorkerSettings {
	return WorkerSettings{
		BaseURL:                 "",
		Token:                   "",
		PollIntervalSec:         5,
		HeartbeatIntervalSec:    30,
		ErrorBackoffSec:         5,
		VerifyTLS:               true,
		WorkerName:              hostname(),
		WorkerID:                "", // ensureWorkerID 时填
		Enabled:                 false,
		WhisperModel:            "large-v3",
		SourceLanguage:          "auto",
		TargetLanguage:          "",
		TranslateProviderID:     "",
		WhisperPrompt:           "",
		WhisperMaxContext:       -1,
		LocalMaxConcurrentTasks: 0,
	}
}

// DefaultSystemSettings 与原 SmartSub Settings defaults 对齐。
func DefaultSystemSettings(dataDir string) SystemSettings {
	return SystemSettings{
		Language:             "zh",
		UseCuda:              true,
		ModelsPath:           filepath.Join(dataDir, "whisper-models"),
		WhisperCliPath:       "whisper-cli", // 假定在 PATH 中
		FFmpegPath:           "ffmpeg",
		AssetsPath:           filepath.Join(dataDir, "assets"),
		UseVAD:               true,
		VadThreshold:         0.5,
		VadMinSpeechDuration: 250,
		VadMinSilenceDuration: 100,
		VadMaxSpeechDuration: 0,
		VadSpeechPad:         30,
		VadSamplesOverlap:    0.1,
	}
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "subtitle-worker"
	}
	return h
}

// EnsureDefaults 把 c 的零值字段补成默认值；保留用户已设置值。
// 在 Load 之后调用，保证字段完备性即使旧 config.json 没有这些 key。
func (c *Config) EnsureDefaults(dataDir string) {
	def := DefaultWorkerSettings()
	if c.Worker.PollIntervalSec == 0 {
		c.Worker.PollIntervalSec = def.PollIntervalSec
	}
	if c.Worker.HeartbeatIntervalSec == 0 {
		c.Worker.HeartbeatIntervalSec = def.HeartbeatIntervalSec
	}
	if c.Worker.ErrorBackoffSec == 0 {
		c.Worker.ErrorBackoffSec = def.ErrorBackoffSec
	}
	if c.Worker.WorkerName == "" {
		c.Worker.WorkerName = def.WorkerName
	}
	if c.Worker.WorkerID == "" {
		c.Worker.WorkerID = uuid.NewString()
	}
	if c.Worker.WhisperModel == "" {
		c.Worker.WhisperModel = def.WhisperModel
	}
	if c.Worker.SourceLanguage == "" {
		c.Worker.SourceLanguage = def.SourceLanguage
	}
	if c.Worker.WhisperMaxContext == 0 {
		// 0 不是合理默认；TS 里 -1 表示"用 whisper 默认"
		c.Worker.WhisperMaxContext = def.WhisperMaxContext
	}

	defSys := DefaultSystemSettings(dataDir)
	if c.Settings.ModelsPath == "" {
		c.Settings.ModelsPath = defSys.ModelsPath
	}
	if c.Settings.WhisperCliPath == "" {
		c.Settings.WhisperCliPath = defSys.WhisperCliPath
	}
	if c.Settings.FFmpegPath == "" {
		c.Settings.FFmpegPath = defSys.FFmpegPath
	}
	if c.Settings.AssetsPath == "" {
		c.Settings.AssetsPath = defSys.AssetsPath
	}
	if c.Settings.Language == "" {
		c.Settings.Language = defSys.Language
	}
	if c.Settings.VadThreshold == 0 {
		c.Settings.VadThreshold = defSys.VadThreshold
		c.Settings.UseVAD = defSys.UseVAD
		c.Settings.VadMinSpeechDuration = defSys.VadMinSpeechDuration
		c.Settings.VadMinSilenceDuration = defSys.VadMinSilenceDuration
		c.Settings.VadSpeechPad = defSys.VadSpeechPad
		c.Settings.VadSamplesOverlap = defSys.VadSamplesOverlap
	}
	if c.TranslationProviders == nil {
		c.TranslationProviders = []Provider{}
	}
	if c.CustomParameters == nil {
		c.CustomParameters = map[string]any{}
	}
}

// Store 提供线程安全的 Load/Save。生命周期 = 进程；不缓存除 config.Path 外的内部状态。
type Store struct {
	mu          sync.RWMutex
	configPath  string
	historyPath string
	current     Config
}

// New 打开/创建 store；不存在时落盘默认值。
func New(cfgDir, dataDir string) (*Store, error) {
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir config dir: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir data dir: %w", err)
	}
	s := &Store{
		configPath:  filepath.Join(cfgDir, "config.json"),
		historyPath: filepath.Join(dataDir, "history.json"),
	}
	if err := s.load(dataDir); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load(dataDir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.configPath)
	if errors.Is(err, os.ErrNotExist) {
		// 首次启动：写默认配置
		s.current = Config{
			Worker:               DefaultWorkerSettings(),
			Settings:             DefaultSystemSettings(dataDir),
			TranslationProviders: []Provider{},
			CustomParameters:     map[string]any{},
		}
		s.current.EnsureDefaults(dataDir)
		return s.atomicWriteLocked()
	}
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(raw, &s.current); err != nil {
		// 损坏：备份后用默认重置
		backup := s.configPath + ".broken." + fmt.Sprint(os.Getpid())
		_ = os.WriteFile(backup, raw, 0o600)
		s.current = Config{
			Worker:               DefaultWorkerSettings(),
			Settings:             DefaultSystemSettings(dataDir),
			TranslationProviders: []Provider{},
			CustomParameters:     map[string]any{},
		}
	}
	s.current.EnsureDefaults(dataDir)
	return s.atomicWriteLocked()
}

// Get 返回当前配置的快照（深拷贝），避免调用方意外改到内部状态。
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return deepCopy(s.current)
}

// GetWorker / GetSettings / GetProviders 是 Get 的便捷子视图。
func (s *Store) GetWorker() WorkerSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current.Worker
}

func (s *Store) GetSettings() SystemSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current.Settings
}

func (s *Store) GetProviders() []Provider {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := make([]Provider, len(s.current.TranslationProviders))
	copy(cp, s.current.TranslationProviders)
	return cp
}

// Update 用 mutator 改 config 然后原子落盘；mutator 内部对 *Config 修改。
func (s *Store) Update(mutate func(*Config)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	mutate(&s.current)
	return s.atomicWriteLocked()
}

func (s *Store) atomicWriteLocked() error {
	raw, err := json.MarshalIndent(s.current, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	tmp := s.configPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("write tmp config: %w", err)
	}
	if err := os.Rename(tmp, s.configPath); err != nil {
		return fmt.Errorf("rename config: %w", err)
	}
	return nil
}

// HistoryPath 返回历史 JSON 路径，让 worker 模块自己读写避免互锁。
func (s *Store) HistoryPath() string {
	return s.historyPath
}

func deepCopy(c Config) Config {
	raw, _ := json.Marshal(c)
	var out Config
	_ = json.Unmarshal(raw, &out)
	return out
}
