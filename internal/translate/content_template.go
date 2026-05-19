// Package translate — 内容模板 + 批量大小常量 + 正则。
//
// 与原 TS constants/index.ts 对齐。
package translate

import "regexp"

// CONTENT_TEMPLATES 输出 SRT 内容拼接模板。
//
// onlyTranslate         译文只保留
// sourceAndTranslate    源在上、译在下
// translateAndSource    译在上、源在下
var contentTemplates = map[string]string{
	"onlyTranslate":      "%s\n\n",
	"sourceAndTranslate": "{src}\n{tgt}\n\n",
	"translateAndSource": "{tgt}\n{src}\n\n",
}

// RenderContent 按模板渲染单条字幕内容。
func RenderContent(template, src, tgt string) string {
	switch template {
	case "onlyTranslate":
		return tgt + "\n\n"
	case "sourceAndTranslate":
		return src + "\n" + tgt + "\n\n"
	case "translateAndSource":
		return tgt + "\n" + src + "\n\n"
	default:
		return tgt + "\n\n"
	}
}

// DefaultBatchSize 各模式批次大小。
var DefaultBatchSize = struct {
	AI  int
	API int
}{AI: 10, API: 1}

// Regex 与原 TS 同名常量。
var (
	ThinkTagRegex   = regexp.MustCompile(`(?s)<think>.*?</think>\n`)
	ResultTagRegex  = regexp.MustCompile(`(?s)<result[^>]*>(.*?)</result>`)
	JSONContentRegex = regexp.MustCompile("(?s)```json\\n(.*?)\\n```")
)

// DefaultSystemPrompt 与原 TS defaultSystemPrompt 对齐。
//
// 占位符：${sourceLanguage}, ${targetLanguage}, ${content}
const DefaultSystemPrompt = `你是一位专业字幕翻译专家。请将给定的 ${sourceLanguage} 字幕翻译成 ${targetLanguage}，遵守以下要求：

1. 严格保持原始字幕的编号顺序与时间轴对应关系
2. 译文应自然流畅，符合目标语言的表达习惯
3. 保留原文中的语气词、感叹号、问号等情感符号
4. 对人名、专有名词、品牌名采用通行译法（如不确定则音译）
5. 在保持原意基础上适度本地化，避免逐字直译
6. 输出格式必须是 JSON 对象，键为字幕 ID，值为翻译后的内容；不要输出额外说明文字。`

// DefaultUserPrompt 与原 TS defaultUserPrompt 对齐。
const DefaultUserPrompt = `${content}`
