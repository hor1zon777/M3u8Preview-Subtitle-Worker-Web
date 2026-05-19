// Package audio — ffmpeg 子进程解码 FLAC → 16kHz mono PCM WAV。
//
// 与原 audioFetcher.decodeFlacToWav 行为一致：
//   - -ar 16000 -ac 1 -c:a pcm_s16le
//   - 已存在 wav 且非空则跳过
//   - 失败抛错（含 stderr 前 2000 字符）
package audio

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// DecodeFlacToWav 用 ffmpeg 把 flacPath 转成 workDir/audio.wav。
func DecodeFlacToWav(ctx context.Context, ffmpegPath, flacPath, workDir string) (string, error) {
	wavPath := filepath.Join(workDir, "audio.wav")
	if s, err := os.Stat(wavPath); err == nil && s.Size() > 0 {
		return wavPath, nil
	}
	args := []string{
		"-y",                     // overwrite
		"-vn",                    // no video stream
		"-i", flacPath,
		"-ar", "16000",
		"-ac", "1",
		"-c:a", "pcm_s16le",
		wavPath,
	}
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("ffmpeg stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("ffmpeg start: %w", err)
	}
	stderrBuf, _ := io.ReadAll(stderr)
	if err := cmd.Wait(); err != nil {
		tail := string(stderrBuf)
		if len(tail) > 2000 {
			tail = tail[len(tail)-2000:]
		}
		return "", fmt.Errorf("ffmpeg decode failed: %w; stderr: %s", err, tail)
	}
	if s, err := os.Stat(wavPath); err != nil || s.Size() == 0 {
		return "", fmt.Errorf("ffmpeg succeeded but wav is empty: %s", wavPath)
	}
	return wavPath, nil
}
