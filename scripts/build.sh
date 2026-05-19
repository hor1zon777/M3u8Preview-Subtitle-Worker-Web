#!/usr/bin/env bash
# scripts/build.sh
#
# 一键构建：
#   1) cd web && pnpm install --frozen-lockfile （首次）
#   2) pnpm build → 静态站点输出到 web/out
#   3) cp web/out → internal/web/dist
#   4) go build → ./subtitle-worker
#
# 用法：
#   ./scripts/build.sh                生产构建
#   ./scripts/build.sh --no-frontend  跳过前端（只 go build）

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

SKIP_FRONTEND=0
for arg in "$@"; do
  case "$arg" in
    --no-frontend) SKIP_FRONTEND=1 ;;
  esac
done

if [[ $SKIP_FRONTEND -eq 0 ]]; then
  echo "==> building frontend"
  (
    cd web
    if [[ ! -d node_modules ]]; then
      pnpm install --frozen-lockfile
    fi
    pnpm build
  )
  echo "==> copying web/out → internal/web/dist"
  rm -rf internal/web/dist
  cp -r web/out internal/web/dist
else
  echo "==> skipping frontend build"
  if [[ ! -d internal/web/dist ]]; then
    mkdir -p internal/web/dist
    printf '<!doctype html><html><body><h1>frontend not built</h1></body></html>' > internal/web/dist/index.html
  fi
fi

echo "==> building Go binary"
go build -ldflags "-s -w" -o subtitle-worker ./cmd/worker
echo "==> done: $(ls -la subtitle-worker)"
