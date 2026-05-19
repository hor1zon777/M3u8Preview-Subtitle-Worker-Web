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
	// 缓存目录：~/.cache/huggingface/hub/models--<org>--<repo>/
	// 例：models--Systran--faster-whisper-large-v3
	//     models--deepdml--faster-whisper-large-v3-turbo-ct2
	// 提取 catalog 中的短名：large-v3, large-v3-turbo, tiny, ...
	home, err := os.UserHomeDir()
	if err == nil {
		hfDir := filepath.Join(home, ".cache", "huggingface", "hub")
		entries, err := os.ReadDir(hfDir)
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() || !strings.HasPrefix(e.Name(), "models--") {
					continue
				}
				// models--Org--faster-whisper-large-v3(-turbo)(-ct2)
				full := e.Name()[len("models--"):] // Org--faster-whisper-large-v3
				last := full
				if idx := strings.LastIndex(full, "--"); idx >= 0 {
					last = full[idx+2:] // faster-whisper-large-v3-turbo-ct2
				}
				// 加到结果集
				seen[full] = true // Org/faster-whisper-large-v3
				// 提取短名：去掉 faster-whisper- 前缀、-ct2 后缀
				short := last
				short = strings.TrimPrefix(short, "faster-whisper-")
				short = strings.TrimSuffix(short, "-ct2")
				if short != "" {
					seen[short] = true // large-v3-turbo
				}
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
