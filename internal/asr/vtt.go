// Package asr — SRT → VTT 转换。
//
// 与原 TS convertSrtToVtt 行为一致：
//   - 顶部加 "WEBVTT\n\n"
//   - 时间戳 "HH:MM:SS,mmm" → "HH:MM:SS.mmm"
//   - 保留 cue id
package asr

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

var vttTimestampRe = regexp.MustCompile(`(\d{2}:\d{2}:\d{2}),(\d{3})\s*-->\s*(\d{2}:\d{2}:\d{2}),(\d{3})`)

// SrtTextToVTT 转换 SRT 字符串为 VTT 字符串。
func SrtTextToVTT(srt string) string {
	normalized := strings.ReplaceAll(strings.ReplaceAll(srt, "\r\n", "\n"), "\r", "\n")
	replaced := vttTimestampRe.ReplaceAllString(normalized, "$1.$2 --> $3.$4")
	return "WEBVTT\n\n" + replaced
}

// SrtFileToVTT 读 srtPath 转成 VTT 字节。
func SrtFileToVTT(srtPath string) ([]byte, error) {
	raw, err := os.ReadFile(srtPath)
	if err != nil {
		return nil, fmt.Errorf("read srt: %w", err)
	}
	return []byte(SrtTextToVTT(string(raw))), nil
}
