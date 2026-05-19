// Package models — 已安装 whisper 模型 + 删除 + 导入。
//
// 支持两种后端：
//   - whisper.cpp（ggml-*.bin 文件在 modelsPath）
//   - faster-whisper（Python；模型缓存在 ~/.cache/huggingface/hub/）
//
// List() 会合并两个来源的结果。
package models

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Installer 已安装模型管理器。
type Installer struct {
	ModelsPath string
}

// List 返回所有已安装/已缓存的模型名（去前缀/后缀后的可识别名称）。
// 包含两类来源：
//  1. modelsPath 下的 ggml-*.bin（whisper.cpp）
//  2. ~/.cache/huggingface/hub/ 下的 models--* 目录（faster-whisper HF 缓存）
func (i *Installer) List() ([]string, error) {
	seen := map[string]bool{}

	// 1) ggml-*.bin
	if err := os.MkdirAll(i.ModelsPath, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir modelsPath: %w", err)
	}
	entries, err := os.ReadDir(i.ModelsPath)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasPrefix(name, "ggml-") && strings.HasSuffix(name, ".bin") {
				n := strings.TrimSuffix(strings.TrimPrefix(name, "ggml-"), ".bin")
				seen[n] = true
			}
		}
	}

	// 2) HF cache（faster-whisper 模型）
	// 缓存目录结构：~/.cache/huggingface/hub/models--<org>--<name>/snapshots/<hash>/
	//   例：models--Systran--faster-whisper-large-v3
	//       models--guillaumeklay--faster-whisper-large-v3-turbo
	home, err := os.UserHomeDir()
	if err == nil {
		hfDir := filepath.Join(home, ".cache", "huggingface", "hub")
		entries, err := os.ReadDir(hfDir)
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() || !strings.HasPrefix(e.Name(), "models--") {
					continue
				}
				// models--org--name → org/name
				parts := strings.SplitN(strings.TrimPrefix(e.Name(), "models--"), "--", 2)
				modelID := strings.ReplaceAll(e.Name()[len("models--"):], "--", "/")
				_ = modelID // 完整 HF id（如 Systran/faster-whisper-large-v3）
				// 提取末尾 size 名（large-v3, small, …）
				if len(parts) >= 2 {
					seen[parts[len(parts)-1]] = true
				}
				// 也加带 org 前缀的完整 id 作为可选项
				seen[modelID] = true
			}
		}
	}

	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	return names, nil
}

// Delete 删除模型文件（仅 ggml .bin；HF 缓存通过 faster-whisper 自身管理）。
func (i *Installer) Delete(model string) error {
	if model == "" {
		return fmt.Errorf("model name empty")
	}
	path := filepath.Join(i.ModelsPath, "ggml-"+model+".bin")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete %s: %w", path, err)
	}
	return nil
}

// Import 从 io.Reader 拷贝到 modelsPath/ggml-<name>.bin。已存在覆盖。
func (i *Installer) Import(name string, r io.Reader) error {
	if name == "" {
		return fmt.Errorf("model name empty")
	}
	if err := os.MkdirAll(i.ModelsPath, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(i.ModelsPath, "ggml-"+name+".bin")
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("copy: %w", err)
	}
	return os.Rename(tmp, dst)
}
