# Dockerfile for m3u8-preview-subtitle-worker-web
#
# 多阶段构建：
#   1. node:20  → pnpm build 前端
#   2. golang:1.23 → go build Go 二进制（嵌入前端 dist）
#   3. debian:bookworm-slim → 最小运行时（ffmpeg + faster-whisper Python）
#
# 运行：
#   docker build -t subtitle-worker:latest .
#   docker run --gpus all -p 8089:8089 -v $PWD/data:/data subtitle-worker:latest
#
# 数据卷：/data 挂载到 ~/.config / ~/.local/share 的合成路径（通过 env 覆盖）。
# 模型缓存（HF）在 /home/appuser/.cache/huggingface/，如需持久化也挂出来。

# ---------------- frontend ----------------
FROM node:20-bookworm-slim AS frontend
WORKDIR /web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml* ./
RUN pnpm install --frozen-lockfile || pnpm install
COPY web/ ./
RUN pnpm build

# ---------------- backend ----------------
FROM golang:1.23-bookworm AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# 拷贝前端构建产物到 embed 位置
COPY --from=frontend /web/out ./internal/web/dist
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "-s -w" -o /out/subtitle-worker ./cmd/worker

# ---------------- runtime ----------------
FROM debian:bookworm-slim AS runtime
ENV DEBIAN_FRONTEND=noninteractive

# 基础工具 + ffmpeg + Python 3 + venv
RUN apt-get update && apt-get install -y --no-install-recommends \
        ffmpeg ca-certificates curl tini \
        python3 python3-venv python3-pip \
    && rm -rf /var/lib/apt/lists/*

# faster-whisper Python 依赖
RUN python3 -m venv /opt/whisper-venv && \
    /opt/whisper-venv/bin/pip install --no-cache-dir faster-whisper nvidia-cudnn-cu12 nvidia-cublas-cu12 || \
    /opt/whisper-venv/bin/pip install --no-cache-dir faster-whisper

# 安装 Python wrapper 脚本
COPY scripts/whisper-py-wrapper.py /opt/whisper-py-wrapper.py

# trampoline: /usr/local/bin/whisper-cli 指向 Python wrapper
RUN printf '#!/usr/bin/env bash\nexec /opt/whisper-venv/bin/python3 /opt/whisper-py-wrapper.py "$@"\n' \
        > /usr/local/bin/whisper-cli && chmod +x /usr/local/bin/whisper-cli

# 应用用户（非 root）
RUN useradd -m -u 1000 appuser

WORKDIR /app
COPY --from=backend /out/subtitle-worker /app/subtitle-worker

# 数据卷：模型 + 配置 + 历史 + 工作临时
ENV MWS_CONFIG_DIR=/data/config \
    MWS_DATA_DIR=/data \
    MWS_WORK_ROOT=/data/work \
    MWS_ADDR=:8089
VOLUME ["/data"]
EXPOSE 8089

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -fsS http://127.0.0.1:8089/api/health || exit 1

USER appuser
ENTRYPOINT ["/usr/bin/tini", "--", "/app/subtitle-worker"]
