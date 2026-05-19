// Package models — Whisper 模型清单。
//
// 对应 renderer/lib/utils 的 modelCategories：tiny / base / small / medium / large 等。
// 量化变体（q5_0 / q5_1 / q8_0）通过 BaseModel 关联。
//
// 文件名约定：ggml-<name>.bin，与 whisper.cpp 标准一致。
// 下载源：huggingface.co 或镜像 hf-mirror.com（每个 model entry 暴露相对 path）。
package models

// ModelEntry 单个 whisper 模型。
type ModelEntry struct {
	Name       string `json:"name"`        // 不含 ggml- 前缀与 .bin 后缀
	Size       string `json:"size"`        // 人类可读：39MB / 1.5GB
	Speed      int    `json:"speed"`       // 1-5 评分
	Quality    int    `json:"quality"`     // 1-5 评分
	MinRAMGB   int    `json:"minRamGb"`    // 推荐最小内存
	Quantized  bool   `json:"quantized"`
	EnglishOnly bool  `json:"englishOnly"`
}

// ModelCategory 模型分组（用于 UI 展示）。
type ModelCategory struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Models      []ModelEntry `json:"models"`
}

// Catalog 内置模型清单。涵盖原 SmartSub modelCategories 主流条目 + 量化变体。
var Catalog = []ModelCategory{
	{
		Name:        "tiny",
		Description: "最小最快；适合在 CPU 或低配 GPU 上做粗略转录。",
		Models: []ModelEntry{
			{Name: "tiny", Size: "75MB", Speed: 5, Quality: 1, MinRAMGB: 1},
			{Name: "tiny.en", Size: "75MB", Speed: 5, Quality: 1, MinRAMGB: 1, EnglishOnly: true},
			{Name: "tiny-q5_1", Size: "32MB", Speed: 5, Quality: 1, MinRAMGB: 1, Quantized: true},
		},
	},
	{
		Name:        "base",
		Description: "速度仍快；准确率比 tiny 明显提升。",
		Models: []ModelEntry{
			{Name: "base", Size: "142MB", Speed: 5, Quality: 2, MinRAMGB: 1},
			{Name: "base.en", Size: "142MB", Speed: 5, Quality: 2, MinRAMGB: 1, EnglishOnly: true},
			{Name: "base-q5_1", Size: "57MB", Speed: 5, Quality: 2, MinRAMGB: 1, Quantized: true},
		},
	},
	{
		Name:        "small",
		Description: "中速 / 中等准确率，CPU 也能跑。",
		Models: []ModelEntry{
			{Name: "small", Size: "466MB", Speed: 4, Quality: 3, MinRAMGB: 2},
			{Name: "small.en", Size: "466MB", Speed: 4, Quality: 3, MinRAMGB: 2, EnglishOnly: true},
			{Name: "small-q5_1", Size: "181MB", Speed: 4, Quality: 3, MinRAMGB: 2, Quantized: true},
		},
	},
	{
		Name:        "medium",
		Description: "推荐 GPU 使用；中英文都不错。",
		Models: []ModelEntry{
			{Name: "medium", Size: "1.5GB", Speed: 3, Quality: 4, MinRAMGB: 4},
			{Name: "medium.en", Size: "1.5GB", Speed: 3, Quality: 4, MinRAMGB: 4, EnglishOnly: true},
			{Name: "medium-q5_0", Size: "514MB", Speed: 3, Quality: 4, MinRAMGB: 3, Quantized: true},
		},
	},
	{
		Name:        "large",
		Description: "最高准确率；强烈推荐 GPU（≥ 6GB 显存）。",
		Models: []ModelEntry{
			{Name: "large-v3", Size: "3.1GB", Speed: 2, Quality: 5, MinRAMGB: 8},
			{Name: "large-v3-turbo", Size: "1.5GB", Speed: 4, Quality: 5, MinRAMGB: 6},
			{Name: "large-v3-q5_0", Size: "1.1GB", Speed: 3, Quality: 5, MinRAMGB: 6, Quantized: true},
			{Name: "large-v3-turbo-q5_0", Size: "574MB", Speed: 4, Quality: 5, MinRAMGB: 4, Quantized: true},
		},
	},
}

// AllModels 平铺所有 ModelEntry，方便检索。
func AllModels() []ModelEntry {
	var all []ModelEntry
	for _, c := range Catalog {
		all = append(all, c.Models...)
	}
	return all
}

// FindModel 按名查找；找不到返回 nil。
func FindModel(name string) *ModelEntry {
	for _, m := range AllModels() {
		if m.Name == name {
			cp := m
			return &cp
		}
	}
	return nil
}
