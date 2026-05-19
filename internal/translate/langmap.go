// Package translate — provider 语言码映射。
//
// 与原 TS convertLanguageCode 行为一致：把 ISO/whisper 风格码（en/zh/ja/...）
// 转换为各 provider 期望的形式（zh-CN / zh-Hans / Chinese 等）。
package translate

import "strings"

// LanguageMap 每个 provider 一份映射表。
// key 是源标准码（小写），value 是 provider 端期望码。
var languageMaps = map[string]map[string]string{
	"baidu": {
		"auto": "auto",
		"zh":   "zh",
		"en":   "en",
		"ja":   "jp",
		"ko":   "kor",
		"fr":   "fra",
		"es":   "spa",
		"de":   "de",
		"ru":   "ru",
		"it":   "it",
		"pt":   "pt",
		"th":   "th",
		"vi":   "vie",
		"ar":   "ara",
	},
	"google": {
		"auto": "",
		"zh":   "zh-CN",
		"zh-cn": "zh-CN",
		"zh-tw": "zh-TW",
		"en":   "en",
		"ja":   "ja",
		"ko":   "ko",
	},
	"azure": {
		"auto": "",
		"zh":   "zh-Hans",
		"zh-cn": "zh-Hans",
		"zh-tw": "zh-Hant",
		"en":   "en",
		"ja":   "ja",
		"ko":   "ko",
	},
	"aliyun": {
		"auto": "auto",
		"zh":   "zh",
		"en":   "en",
		"ja":   "ja",
		"ko":   "ko",
	},
	"volc": {
		"auto": "",
		"zh":   "zh",
		"en":   "en",
		"ja":   "ja",
		"ko":   "ko",
	},
	"doubao": {
		"auto": "",
		"zh":   "Chinese",
		"en":   "English",
		"ja":   "Japanese",
		"ko":   "Korean",
		"fr":   "French",
		"es":   "Spanish",
		"de":   "German",
	},
	"deeplx": {
		"auto": "auto",
		"zh":   "ZH",
		"en":   "EN",
		"ja":   "JA",
		"ko":   "KO",
		"fr":   "FR",
		"es":   "ES",
		"de":   "DE",
		"ru":   "RU",
	},
}

// ConvertLanguageCode 转换语言码。providerType 不在表中或语言不在表中 → 原样返回。
func ConvertLanguageCode(code, providerType string) string {
	c := strings.ToLower(strings.TrimSpace(code))
	if c == "" {
		return ""
	}
	m, ok := languageMaps[strings.ToLower(providerType)]
	if !ok {
		return c
	}
	if v, ok := m[c]; ok {
		return v
	}
	return c
}
