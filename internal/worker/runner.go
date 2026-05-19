// Package worker — 单个 job 的完整流水线，对齐原 TS runner.ts。
//
// 流程：
//
//	1. 拉 FLAC          asr 5%~25%
//	2. ffmpeg → WAV     asr 25%~40%
//	3. whisper ASR      asr 40%~75%（受 globalASRMutex 串行）
//	4. 翻译（可选）      translate 75%~95%
//	5. SRT → VTT + 上传  writing 95%~100%
//
// 错误分类：runner 把 error 走 classifyError 映射成 broker.ErrorKind，
// 让服务端按 retriable/permanent/neutral 策略分流。
package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/asr"
	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/audio"
	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/broker"
	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/config"
	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/logger"
)

// TranslateFunc 翻译接口。由 lifecycle 注入实际实现（避免 worker 直依赖 translate 包）。
//
//   - srtPath：源 SRT 文件
//   - providerID：worker 配置选定的 provider id
//   - 返回：译文 SRT 文件绝对路径
type TranslateFunc func(ctx context.Context, srtPath, providerID, sourceLang, targetLang string, onProgress func(percent int)) (string, error)

// RunnerDeps runner 的外部依赖。
type RunnerDeps struct {
	State       *State
	Client      *broker.Client
	WorkerID    string
	GetSettings func() config.SystemSettings
	GetWorker   func() config.WorkerSettings
	Translate   TranslateFunc // 可空（=不翻译）；通常 lifecycle 在 Phase 3 之后注入
	WorkRoot    string        // 工作目录根（默认 os.TempDir/m3u8-subtitle-worker）
}

// Runner 单 job 流水线执行器。
type Runner struct {
	deps RunnerDeps
}

// NewRunner 构造。WorkRoot 为空则用 $TMPDIR/m3u8-subtitle-worker。
func NewRunner(deps RunnerDeps) *Runner {
	if deps.WorkRoot == "" {
		deps.WorkRoot = filepath.Join(os.TempDir(), "m3u8-subtitle-worker")
	}
	return &Runner{deps: deps}
}

// Run 跑一个 ClaimedJob。成功 nil，失败抛 error（runner 已自行调 broker.Fail）。
func (r *Runner) Run(ctx context.Context, job *broker.ClaimedJob) (retErr error) {
	if job.Stage != "" && job.Stage != "asr_subtitle" {
		return fmt.Errorf("subtitle worker received unexpected stage: %s (expected asr_subtitle)", job.Stage)
	}
	if job.AudioArtifactURL == "" {
		return fmt.Errorf("missing audioArtifactUrl in claimed job (server protocol mismatch?)")
	}

	workDir := filepath.Join(r.deps.WorkRoot, sanitizeFs(job.JobID))
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return fmt.Errorf("mkdir workdir: %w", err)
	}
	logger.Debug("[worker] [job %s] workDir=%s", job.JobID, workDir)

	displayName := deriveDisplayName(job)
	state := r.deps.State
	state.UpsertJob(CurrentJob{
		JobID:       job.JobID,
		MediaID:     job.MediaID,
		MediaTitle:  job.MediaTitle,
		DisplayName: displayName,
		Stage:       "asr",
		Progress:    5,
		StartedAt:   time.Now().UnixMilli(),
		Category:    "io",
		Attempt:     job.Attempt,
		MaxAttempts: job.MaxAttempts,
	})
	state.EnterIO()
	inIO := true

	// 终态记录：finally 推 historyJobs 时使用
	finalStage := "failed"
	var finalError string
	var finalErrKind broker.ErrorKind

	cleanup := true

	defer func() {
		if inIO {
			state.ExitIO()
		}
		state.PushHistory(HistoryJob{
			JobID:        job.JobID,
			DisplayName:  displayName,
			MediaTitle:   job.MediaTitle,
			FinalStage:   finalStage,
			ErrorMessage: finalError,
			EndedAt:      time.Now().UnixMilli(),
			ErrorKind:    finalErrKind,
		})
		state.RemoveJob(job.JobID)
		if cleanup {
			_ = os.RemoveAll(workDir)
		}
	}()

	defer func() {
		if retErr == nil {
			return
		}
		// 分类 + 上报 fail
		errMsg := retErr.Error()
		if errors_isJobLost(retErr) {
			finalStage = "lost"
			finalError = "job lost"
			cleanup = false
			state.RecordFailure("job lost")
			logger.Warn("[worker] [job %s] lost (410 Gone) — abandoning", job.JobID)
			return
		}
		kind := ClassifyError(errMsg)
		finalErrKind = kind
		finalError = errMsg
		state.RecordFailure(errMsg)
		logger.Error("[worker] [job %s] failed (kind=%s): %s", job.JobID, kind, errMsg)
		failCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := r.deps.Client.Fail(failCtx, job.JobID, r.deps.WorkerID, errMsg, kind); err != nil {
			logger.Error("[worker] [job %s] additionally failed to report fail: %v", job.JobID, err)
		}
		cancel()
	}()

	if err := r.reportPhase(ctx, job.JobID, "asr", 5); err != nil {
		return err
	}

	// ---- 1. 拉 FLAC ----
	logger.Info("[worker] [job %s] pulling FLAC ...", job.JobID)
	t0 := time.Now()
	fetch, err := r.deps.Client.AudioFetch(ctx, job.AudioArtifactURL, r.deps.WorkerID)
	if err != nil {
		return err
	}
	flac, err := audio.DownloadAndVerify(fetch.Body, workDir, job.AudioArtifactSha256, job.AudioArtifactSize)
	fetch.Body.Close()
	if err != nil {
		return err
	}
	logger.Info("[worker] FLAC downloaded: %d bytes, sha=%s…", flac.Size, flac.Sha256[:12])
	logger.Debug("[worker] [job %s] FLAC stage took=%s", job.JobID, time.Since(t0))
	if err := r.reportPhase(ctx, job.JobID, "asr", 25); err != nil {
		return err
	}

	// ---- 2. ffmpeg 解码 ----
	logger.Info("[worker] [job %s] decoding FLAC -> WAV ...", job.JobID)
	t1 := time.Now()
	settings := r.deps.GetSettings()
	wavPath, err := audio.DecodeFlacToWav(ctx, settings.FFmpegPath, flac.FlacPath, workDir)
	if err != nil {
		return err
	}
	logger.Debug("[worker] [job %s] FFmpeg decode took=%s wav=%s", job.JobID, time.Since(t1), wavPath)
	if err := r.reportPhase(ctx, job.JobID, "asr", 40); err != nil {
		return err
	}

	// ---- 3. ASR ----
	state.ExitIO()
	inIO = false
	state.SetCategory(job.JobID, "asr")
	logger.Info("[worker] [job %s] running ASR ...", job.JobID)

	state.EnterAsrQ()
	logger.Debug("[worker:asr-lock] queueing job %s", job.JobID)
	AcquireASR()
	logger.Debug("[worker:asr-lock] acquired by job %s", job.JobID)
	state.EnterAsrRun()
	asrStarted := time.Now()
	runner := &asr.WhisperRunner{Settings: settings}
	worker := r.deps.GetWorker()
	srtPath, asrErr := runner.Run(ctx, workDir, asr.WhisperOptions{
		WavPath:        wavPath,
		Model:          firstNonEmpty(worker.WhisperModel, "large-v3"),
		SourceLanguage: firstNonEmpty(job.SourceLang, worker.SourceLanguage, "auto"),
		Prompt:         worker.WhisperPrompt,
		MaxContext:     worker.WhisperMaxContext,
	}, func(percent int) {
		// 40-75 区间映射
		mapped := 40 + percent*35/100
		if mapped < 40 {
			mapped = 40
		}
		if mapped > 75 {
			mapped = 75
		}
		state.UpdateProgress(job.JobID, "asr", mapped)
	})
	state.ExitAsrRun()
	ReleaseASR()
	state.ExitAsrQ()
	logger.Info("[worker:asr-lock] released by job %s (held=%ds)", job.JobID, int(time.Since(asrStarted).Seconds()))
	if asrErr != nil {
		return asrErr
	}
	// ASR 完成后统计段数，便于和翻译阶段对照
	if asrSubs, e := asr.ReadSRT(srtPath); e == nil {
		logger.Info("[worker] [job %s] ASR done: %d segments", job.JobID, len(asrSubs))
	}
	logger.Debug("[worker] [job %s] ASR stage took=%s srt=%s", job.JobID, time.Since(asrStarted), srtPath)
	if err := r.reportPhase(ctx, job.JobID, "asr", 75); err != nil {
		return err
	}

	// ---- 4. 翻译 ----
	finalSrtPath := srtPath
	didTranslate := false
	if r.shouldTranslate(job, worker) {
		state.EnterTr()
		state.SetCategory(job.JobID, "translate")
		logger.Info("[worker] [job %s] running translation (provider=%s, %s → %s) ...",
			job.JobID, worker.TranslateProviderID,
			firstNonEmpty(job.SourceLang, worker.SourceLanguage, "auto"),
			firstNonEmpty(job.TargetLang, worker.TargetLanguage))
		tTranslate := time.Now()
		translated, terr := r.deps.Translate(ctx, srtPath, worker.TranslateProviderID,
			firstNonEmpty(job.SourceLang, worker.SourceLanguage, "auto"),
			firstNonEmpty(job.TargetLang, worker.TargetLanguage),
			func(percent int) {
				mapped := 75 + percent*20/100
				if mapped > 95 {
					mapped = 95
				}
				state.UpdateProgress(job.JobID, "translate", mapped)
			})
		state.ExitTr()
		if terr != nil {
			return terr
		}
		logger.Debug("[worker] [job %s] translate stage took=%s out=%s", job.JobID, time.Since(tTranslate), translated)
		finalSrtPath = translated
		didTranslate = true
	} else {
		ws := worker
		logger.Info("[worker] [job %s] skipping translation (translateProviderId=%q, target=%q, source=%q)",
			job.JobID, ws.TranslateProviderID,
			firstNonEmpty(job.TargetLang, ws.TargetLanguage),
			firstNonEmpty(job.SourceLang, ws.SourceLanguage, "auto"))
	}
	if err := r.reportPhase(ctx, job.JobID, "translate", 95); err != nil {
		return err
	}

	// ---- 5. SRT → VTT + 上传 ----
	logger.Info("[worker] [job %s] uploading VTT ...", job.JobID)
	tUpload := time.Now()
	state.SetCategory(job.JobID, "upload")
	vtt, err := asr.SrtFileToVTT(finalSrtPath)
	if err != nil {
		return err
	}
	hasher := sha256.New()
	hasher.Write(vtt)
	sha := hex.EncodeToString(hasher.Sum(nil))
	logger.Debug("[worker] [job %s] VTT size=%d sha=%s…", job.JobID, len(vtt), sha[:12])
	// dump VTT 头部，便于诊断"播放器只显示一条字幕"等问题
	if len(vtt) > 0 {
		logger.Debug("[worker] [job %s] VTT head:\n%s", job.JobID, vttHead(vtt, 10))
	}
	completeCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := r.deps.Client.Complete(completeCtx, job.JobID, broker.CompleteMeta{
		WorkerID: r.deps.WorkerID,
		Size:     int64(len(vtt)),
		Sha256:   sha,
	}, vtt); err != nil {
		return err
	}
	state.UpdateProgress(job.JobID, "writing", 100)
	state.RecordSuccess()
	finalStage = "completed"
	logger.Info("[worker] [job %s] DONE (translated=%v)", job.JobID, didTranslate)
	logger.Debug("[worker] [job %s] upload stage took=%s total=%s",
		job.JobID, time.Since(tUpload), time.Since(t0))
	return nil
}

// shouldTranslate 与原 TS 同等优先级：
//  1. worker.translateProviderId 空 → 不翻译
//  2. target 空 → 不翻译
//  3. source == target → 不翻译
func (r *Runner) shouldTranslate(job *broker.ClaimedJob, ws config.WorkerSettings) bool {
	if r.deps.Translate == nil {
		return false
	}
	if strings.TrimSpace(ws.TranslateProviderID) == "" {
		return false
	}
	target := strings.TrimSpace(firstNonEmpty(job.TargetLang, ws.TargetLanguage))
	if target == "" {
		return false
	}
	source := strings.TrimSpace(firstNonEmpty(job.SourceLang, ws.SourceLanguage))
	return target != source
}

// reportPhase 推阶段进度到 broker。
// 返回 ErrJobLost 时上层应该立即 return 进 cleanup；其它错误只记日志不阻塞。
func (r *Runner) reportPhase(ctx context.Context, jobID, stage string, progress int) error {
	r.deps.State.UpdateProgress(jobID, stage, progress)
	hbCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := r.deps.Client.Heartbeat(hbCtx, jobID, r.deps.WorkerID, stage, progress)
	if err == nil {
		return nil
	}
	if errors_isJobLost(err) {
		return broker.ErrJobLost
	}
	// 单次心跳失败不致命：定时心跳会兜底
	logger.Warn("[worker] heartbeat failed for job %s: %v", jobID, err)
	return nil
}

// errors_isJobLost 区分 broker.ErrJobLost 和包装它的 wrap error。
func errors_isJobLost(err error) bool {
	if err == nil {
		return false
	}
	if err == broker.ErrJobLost {
		return true
	}
	// 也匹配包装错误
	return strings.Contains(err.Error(), "job lost")
}

func deriveDisplayName(job *broker.ClaimedJob) string {
	if job.AudioArtifactURL != "" {
		u, err := url.Parse(job.AudioArtifactURL)
		if err == nil {
			segs := strings.Split(strings.TrimRight(u.Path, "/"), "/")
			if len(segs) > 0 {
				last := segs[len(segs)-1]
				decoded, derr := url.QueryUnescape(last)
				if derr == nil && hasExtension(decoded) {
					return decoded
				}
				if hasExtension(last) {
					return last
				}
			}
		}
	}
	return job.JobID
}

var extRe = regexp.MustCompile(`\.[A-Za-z0-9]{1,8}$`)

func hasExtension(name string) bool { return extRe.MatchString(name) }

func sanitizeFs(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
		if b.Len() >= 64 {
			break
		}
	}
	return b.String()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v != "" {
			return v
		}
	}
	return ""
}

// vttHead 抽取 VTT 字节流的前 N 个 cue（按空行分隔），用于 debug。
// 按 cue 块边界截断，避免 UTF-8 字符被切半产生乱码。
func vttHead(vtt []byte, maxCues int) string {
	s := string(vtt)
	// 找到第 maxCues 个空行后的位置。VTT 中 cue 之间用空行分隔；
	// 第一段 "WEBVTT\n\n" 后是第 1 个 cue。
	pos := 0
	emptyLineSeen := 0
	for i := 0; i < len(s)-1; i++ {
		if s[i] == '\n' && s[i+1] == '\n' {
			emptyLineSeen++
			if emptyLineSeen > maxCues {
				pos = i + 2
				break
			}
		}
	}
	if pos == 0 || pos >= len(s) {
		return s
	}
	return s[:pos] + "…"
}
