#!/usr/bin/env bash
# scripts/dev.sh — 并行开发：next dev (3000) + go run（8089）
#
# 浏览器访问 http://localhost:3000，前端 fetch /api/* 会被 next rewrites 代理到 :8089。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# 1) 启 Go 后端
(MWS_ADDR=":8089" go run ./cmd/worker) &
GO_PID=$!
trap "kill $GO_PID 2>/dev/null || true" EXIT

# 2) 启前端
cd web
if [[ ! -d node_modules ]]; then
  pnpm install
fi
MWS_DEV=1 pnpm dev
