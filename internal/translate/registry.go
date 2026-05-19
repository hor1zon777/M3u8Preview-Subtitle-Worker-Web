// Package translate — provider 注册表。
//
// defaultRegistry 列出所有内置 provider type 与对应 TranslatorFunc。
// 与原 TS TRANSLATOR_MAP 对齐：
//
//   - OpenAI 兼容：openai / deepseek / DeerAPI / Gemini / qwen / siliconflow
//   - 火山：volc（TranslateText V4 签名）
//   - 百度：baidu（MD5）
//   - 阿里云：aliyun（ACS3 V3 签名）
//   - 豆包：doubao（Bearer）
//   - 谷歌：google（API key 查询参数）
//   - Azure：azure（Cognitive Translator，Ocp-Apim-* 头）/ azureopenai（OpenAI 兼容）
//   - DeepLX：deeplx（自部署 HTTP）
//   - Ollama：ollama（本地 LLM）
package translate

import "github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/translate/providers"

func defaultRegistry() map[string]TranslatorFunc {
	openai := providers.OpenAI
	return map[string]TranslatorFunc{
		"openai":      openai,
		"deepseek":    openai,
		"deerapi":     openai,
		"gemini":      openai,
		"qwen":        openai,
		"siliconflow": openai,
		"azureopenai": providers.AzureOpenAI,
		"ollama":      providers.Ollama,
		"volc":        providers.Volcengine,
		"baidu":       providers.Baidu,
		"aliyun":      providers.Aliyun,
		"doubao":      providers.Doubao,
		"google":      providers.Google,
		"azure":       providers.AzureTranslator,
		"deeplx":      providers.DeepLX,
	}
}
