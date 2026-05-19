// Package asr — SRT 解析 / 格式化 / 校验。
//
// 与原 TS `main/translate/utils/subtitle.ts` 行为一致：
//   - parseSubtitles：把 SRT 文本切成 Subtitle 数组（id / 时间戳 / 内容多行）
//   - format：拼回 SRT 文本（id + 时间戳 + 内容）
//
// 一个段落形如：
//
//	1
//	00:00:01,000 --> 00:00:04,000
//	Hello China
//	（空行）
package asr

import (
	"fmt"
	"os"
	"strings"
)

// Subtitle 单条字幕（与 TS 端结构对齐）。
type Subtitle struct {
	ID           string   `json:"id"`
	StartEndTime string   `json:"startEndTime"`
	Content      []string `json:"content"`
}

// ParseSubtitles 解析 SRT 字符串。容错点：
//   - 允许 \r\n / \n
//   - id 行后接空行再到时间行（一些工具会这么生成）
//   - 时间行匹配 "-->"
//   - 末尾不强制空行
func ParseSubtitles(text string) []Subtitle {
	normalized := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(normalized, "\n")

	var out []Subtitle
	for i := 0; i < len(lines); {
		// 跳过空行
		for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
			i++
		}
		if i >= len(lines) {
			break
		}
		idLine := strings.TrimSpace(lines[i])
		i++
		// 容错：id 后可能直接是空行
		for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
			i++
		}
		if i >= len(lines) {
			break
		}
		timeLine := strings.TrimSpace(lines[i])
		if !strings.Contains(timeLine, "-->") {
			// 不像时间行 — 跳过
			continue
		}
		i++
		var content []string
		for i < len(lines) && strings.TrimSpace(lines[i]) != "" {
			content = append(content, lines[i])
			i++
		}
		if idLine == "" || len(content) == 0 {
			continue
		}
		out = append(out, Subtitle{
			ID:           idLine,
			StartEndTime: timeLine,
			Content:      content,
		})
	}
	return out
}

// FormatSRT 把 Subtitle 数组转成 SRT 文本。
func FormatSRT(subs []Subtitle) string {
	var b strings.Builder
	for _, s := range subs {
		b.WriteString(s.ID)
		b.WriteByte('\n')
		b.WriteString(s.StartEndTime)
		b.WriteByte('\n')
		for _, line := range s.Content {
			b.WriteString(line)
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// ReadSRT 读 SRT 文件返回 Subtitle 数组。
func ReadSRT(path string) ([]Subtitle, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read srt: %w", err)
	}
	return ParseSubtitles(string(raw)), nil
}

// WriteSRT 写 SRT 文件。
func WriteSRT(path string, subs []Subtitle) error {
	return os.WriteFile(path, []byte(FormatSRT(subs)), 0o644)
}
