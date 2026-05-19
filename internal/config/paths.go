// Package config — 路径解析。
//
// 在 Linux 上按 XDG 规范：
//   - $XDG_CONFIG_HOME 默认 $HOME/.config
//   - $XDG_DATA_HOME   默认 $HOME/.local/share
//
// env 覆盖：
//   - MWS_CONFIG_DIR  override 配置目录（含 config.json）
//   - MWS_DATA_DIR    override 数据目录（含模型 / 历史 / 临时 SRT）
package config

import (
	"os"
	"path/filepath"
)

const appDir = "m3u8-subtitle-worker"

// ResolveDirs 返回 (configDir, dataDir)。两个目录都会被 Store 在 New 时自动 mkdir。
func ResolveDirs() (string, string) {
	cfg := os.Getenv("MWS_CONFIG_DIR")
	dat := os.Getenv("MWS_DATA_DIR")
	if cfg == "" {
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			if home, err := os.UserHomeDir(); err == nil {
				base = filepath.Join(home, ".config")
			} else {
				base = filepath.Join(os.TempDir(), ".config")
			}
		}
		cfg = filepath.Join(base, appDir)
	}
	if dat == "" {
		base := os.Getenv("XDG_DATA_HOME")
		if base == "" {
			if home, err := os.UserHomeDir(); err == nil {
				base = filepath.Join(home, ".local", "share")
			} else {
				base = filepath.Join(os.TempDir(), ".local", "share")
			}
		}
		dat = filepath.Join(base, appDir)
	}
	return cfg, dat
}
