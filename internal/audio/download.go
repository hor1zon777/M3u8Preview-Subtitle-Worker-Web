// Package audio — broker FLAC 流式下载 + SHA-256 / size 校验。
//
// 与原 TS audioFetcher.downloadFlacAndVerify 行为一致：
//   - 把响应流写入磁盘
//   - 实时 update SHA-256
//   - 完成后校验 size / sha256（meta 来自 claim 响应）
package audio

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// FlacFetchResult 下载完成后的元数据。
type FlacFetchResult struct {
	FlacPath string
	Size     int64
	Sha256   string
}

// DownloadAndVerify 把 body 流式写入 workDir/audio.flac，同时计算 SHA-256。
// expectedSha 为空则跳过 hash 校验；expectedSize <= 0 则跳过大小校验。
func DownloadAndVerify(body io.Reader, workDir string, expectedSha string, expectedSize int64) (*FlacFetchResult, error) {
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir workdir: %w", err)
	}
	flacPath := filepath.Join(workDir, "audio.flac")
	f, err := os.Create(flacPath)
	if err != nil {
		return nil, fmt.Errorf("create flac: %w", err)
	}
	defer f.Close()

	hasher := sha256.New()
	w := io.MultiWriter(f, hasher)
	n, err := io.Copy(w, body)
	if err != nil {
		_ = os.Remove(flacPath)
		return nil, fmt.Errorf("copy flac: %w", err)
	}
	if n == 0 {
		_ = os.Remove(flacPath)
		return nil, fmt.Errorf("broker 返回空 body（0 字节）。" +
			"通常表示 audio worker 未在 5min 内将 FLAC 推流到服务端。" +
			"请确认 audio worker 已注册、在线，且持有该 jobId 的 FLAC 文件。")
	}
	sha := hex.EncodeToString(hasher.Sum(nil))
	if expectedSha != "" && sha != expectedSha {
		_ = os.Remove(flacPath)
		return nil, fmt.Errorf("FLAC sha256 mismatch: expect %s got %s (got %d bytes)", expectedSha, sha, n)
	}
	if expectedSize > 0 && n != expectedSize {
		_ = os.Remove(flacPath)
		return nil, fmt.Errorf("FLAC size mismatch: expect %d got %d", expectedSize, n)
	}
	return &FlacFetchResult{
		FlacPath: flacPath,
		Size:     n,
		Sha256:   sha,
	}, nil
}
