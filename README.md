# m3u8-preview-subtitle-worker-web

**m3u8-preview-go 服务端 v3/v4 broker 协议的字幕 worker —— Linux 生产部署的 Web 版本。**

复刻原 Electron 版（`m3u8-preview-subtitle-worker`）的全部 worker 功能，UI 改为浏览器访问，单二进制部署：

- 拉 FLAC（broker GET stream + SHA-256 校验）
- ffmpeg 解码 → 16 kHz mono PCM WAV
- whisper.cpp CLI 子进程跑 ASR（含 silero VAD / CUDA）
- 翻译（OpenAI 兼容 / 火山 / 百度 / 阿里云 / 豆包 / Google / Azure / DeepLX / Ollama 等 10 种 provider）
- SRT → VTT 上传给服务端

UI 4 个页面：**Worker / 模型管理 / 翻译服务 / 系统设置**。

---

## 目录

- [一、快速开始](#一快速开始)
- [二、依赖准备](#二依赖准备)
  - [2.1 ffmpeg](#21-ffmpeg)
  - [2.2 whisper.cpp（whisper-cli）](#22-whispercppwhisper-cli)
  - [2.3 VAD 模型](#23-vad-模型)
- [三、安装与启动](#三安装与启动)
  - [3.1 源码构建](#31-源码构建)
  - [3.2 Docker 部署（推荐生产）](#32-docker-部署推荐生产)
  - [3.3 systemd 托管](#33-systemd-托管)
- [四、首次配置流程](#四首次配置流程)
  - [4.1 系统设置](#41-系统设置)
  - [4.2 选择 Whisper 模型](#42-选择-whisper-模型)
  - [4.3 配置翻译服务](#43-配置翻译服务)
  - [4.4 连接服务端 + 启动 Worker](#44-连接服务端--启动-worker)
- [五、各页面使用指南](#五各页面使用指南)
- [六、环境变量与命令行参数](#六环境变量与命令行参数)
- [七、运维与高级话题](#七运维与高级话题)
  - [7.1 多 worker 并发部署](#71-多-worker-并发部署)
  - [7.2 Bearer Token 认证](#72-bearer-token-认证)
  - [7.3 nginx / Caddy 反代 + HTTPS](#73-nginx--caddy-反代--https)
  - [7.4 日志与监控](#74-日志与监控)
  - [7.5 备份与迁移](#75-备份与迁移)
- [八、故障排查](#八故障排查)
- [九、API 速查](#九api-速查)
- [十、开发模式](#十开发模式)
- [十一、目录结构](#十一目录结构)

---

## 一、快速开始

最少 5 步在一台 Linux 服务器跑起来：

```bash
# 1) 装系统依赖
sudo apt update && sudo apt install -y ffmpeg python3 python3-venv python3-pip

# 2) 安装 faster-whisper（Python）+ wrapper
cd /path/to/subtitle-worker
sudo ./scripts/install-faster-whisper.sh

# 3) 构建本项目
./scripts/build.sh

# 4) 启动
./subtitle-worker --addr :8089

# 5) 浏览器打开 http://<server-ip>:8089
#    依次：系统设置 → Worker 页选模型 → 翻译服务（添加 provider）→ 填 baseUrl + token → 启动
```

> **NVIDIA GPU**：install-faster-whisper.sh 自动检测 nvidia-smi 并装 cuDNN / cuBLAS。
> **不需要 GPU**：脚本自动跳过 CUDA 包，CPU 模式仍可工作。
>
> **仍想用 whisper.cpp？** 见 [备选方案：whisper.cpp](#23-备选方案-whispercpp)。

---

## 二、依赖准备

### 2.1 ffmpeg

worker 用 ffmpeg 把 FLAC 解码成 16 kHz mono PCM WAV，再喂给 whisper。

- Debian / Ubuntu：`sudo apt install -y ffmpeg`
- CentOS / RHEL：`sudo dnf install -y ffmpeg`（需先启用 RPM Fusion）
- 验证：`ffmpeg -version` 输出版本号即可

把可执行文件放进 `$PATH`，或在「系统设置」里填绝对路径（如 `/usr/bin/ffmpeg`）。

### 2.2 faster-whisper（推荐，默认）

本项目推荐用 [faster-whisper](https://github.com/SYSTRAN/faster-whisper)（CTranslate2 后端），
比 whisper.cpp 安装更简单、速度相当甚至更快，且**无需编译 C++**。

```bash
# 一键安装（会自动创建 Python venv + pip install + trampoline 脚本）
sudo ./scripts/install-faster-whisper.sh
```

脚本做的事：

1. `python3 -m venv /opt/whisper-faster-venv`
2. `pip install faster-whisper`
3. 有 N 卡则自动 `pip install nvidia-cudnn-cu12 nvidia-cublas-cu12`
4. 创建 `/usr/local/bin/whisper-cli` trampoline → Python wrapper

**验证**：

```bash
whisper-cli --help | head -3
# 应输出 faster-whisper wrapper usage
```

Web UI 「系统设置」页面会自动检测出 `whisperEngine: "faster-whisper"`。

**模型管理**：faster-whisper **自动从 HuggingFace 下载**模型到 `~/.cache/huggingface/`：
- 在 Worker 页选一个模型名（如 `large-v3`），首次使用自动下载（约 3 GB，一次性的）
- 「模型」页面显示完整清单；已缓存的标 ✓
- 不再需要手动下载 ggml 文件

### 2.3 备选方案：whisper.cpp

如果你偏好 whisper.cpp（或 faster-whisper 不兼容你的环境），也可以编译原生 whisper-cli：

```bash
git clone https://github.com/ggerganov/whisper.cpp /opt/whisper.cpp
cd /opt/whisper.cpp

# CPU 版
cmake -B build && cmake --build build -j

# 或 CUDA 版（需 CUDA Toolkit + CMake ≥ 3.18）
cmake -B build -DGGML_CUDA=ON && cmake --build build -j

sudo ln -sf $(pwd)/build/bin/whisper-cli /usr/local/bin/whisper-cli
```

| 特性 | faster-whisper | whisper.cpp |
|---|---|---|
| 安装 | `pip install`（30 秒） | CMake 编译（需 CMake ≥ 3.18 + 编译工具链） |
| GPU 加速 | cuDNN / cuBLAS（pip 自动装） | 需 CUDA Toolkit + 编译时 `-DGGML_CUDA=ON` |
| 模型格式 | HF 自动下载 CT2 | 手动下载 ggml-*.bin |
| 速度（large-v3, 10min 音频, RTX 3060） | ~25 秒 | ~30 秒 |
| CPU 速度 | ~2 分钟 | ~5 分钟 |
| VAD | 内置 silero，无需额外文件 | 需单独下载 `ggml-silero-v6.2.0.bin` |

注意：两种后端通过同一个 `/usr/local/bin/whisper-cli` 入口调用（Go 代码自动检测首行 `#!` 判断类型）。

### 2.4 VAD 模型（可选）

**faster-whisper 自带 silero VAD**，不需要额外下载模型文件。

whisper.cpp 模式需要 VAD 模型文件：

```bash
mkdir -p ~/.local/share/m3u8-subtitle-worker/assets
curl -L -o ~/.local/share/m3u8-subtitle-worker/assets/ggml-silero-v6.2.0.bin \
  https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-silero-v6.2.0.bin
```

VAD 开关在「系统设置」页面。

---

## 三、安装与启动

### 3.1 源码构建

依赖：Go 1.23+，Node.js 20+，pnpm 9+。

```bash
git clone <repo> subtitle-worker
cd subtitle-worker
./scripts/build.sh
./subtitle-worker --addr :8089
```

`scripts/build.sh` 做的事：

1. `cd web && pnpm install`（如未装）
2. `pnpm build` → 前端静态产物写到 `web/out`
3. `cp -r web/out internal/web/dist`（供 `go:embed`）
4. `go build -ldflags "-s -w" -o subtitle-worker ./cmd/worker`

最终产物是 **一个 ~12 MB 的二进制**，前端 / 配置默认值全部内嵌。

跳过前端重新构建（只改了 Go 代码时）：

```bash
./scripts/build.sh --no-frontend
```

### 3.2 Docker 部署（推荐生产）

镜像已内置 faster-whisper（Python）——**不需要**挂载 whisper-cli 二进制：

```bash
docker compose up -d --build
docker compose logs -f subtitle-worker
```

镜像三段式构建：

| 阶段 | 基础镜像 | 责任 |
|---|---|---|
| frontend | `node:20-bookworm-slim` | `pnpm build` 产前端 |
| backend  | `golang:1.23-bookworm`   | `go build` 静态二进制 |
| runtime  | `debian:bookworm-slim`   | ffmpeg + Python 3 + faster-whisper + wrapper |

- **模型缓存**：`docker-compose.yml` 已配置 `whisper-cache` 命名卷挂到 `~/.cache/huggingface/`，重启/重建不丢模型
- **GPU 支持**：需要 [nvidia-container-toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html)
- 健康检查：`curl http://127.0.0.1:8089/api/health` 应返回 `{"ok":true,"data":{"status":"ok"}}`

### 3.3 systemd 托管

不想用 Docker 也可以直接 systemd 起。新建 `/etc/systemd/system/subtitle-worker.service`：

```ini
[Unit]
Description=m3u8 Subtitle Worker
After=network.target

[Service]
Type=simple
User=worker
Group=worker
WorkingDirectory=/opt/subtitle-worker
ExecStart=/opt/subtitle-worker/subtitle-worker --addr :8089
Restart=on-failure
RestartSec=5s
Environment="MWS_CONFIG_DIR=/etc/subtitle-worker"
Environment="MWS_DATA_DIR=/var/lib/subtitle-worker"
# 可选：开启 Bearer 认证
# Environment="WEB_UI_TOKEN=your-secret"

[Install]
WantedBy=multi-user.target
```

启用：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now subtitle-worker
sudo systemctl status subtitle-worker
journalctl -u subtitle-worker -f
```

---

## 四、首次配置流程

服务起来后浏览器访问 `http://<server-ip>:8089`，按下面顺序走一遍 4 个页面。

### 4.1 系统设置

第一步去**「设置」** 页核对底层依赖路径：

| 字段 | 说明 |
|---|---|
| whisper-cli 路径 | 默认 `whisper-cli`（在 `$PATH` 中）。docker 部署或非标位置请填绝对路径 |
| ffmpeg 路径 | 同上，默认 `ffmpeg` |
| 模型存储路径 | 默认 `$XDG_DATA_HOME/m3u8-subtitle-worker/whisper-models` |
| 资源路径 | VAD 模型（`ggml-silero-v6.2.0.bin`）所在目录 |
| 使用 CUDA | 有 N 卡时开。faster-whisper 自动用 GPU；whisper.cpp 需编译时带 `-DGGML_CUDA=ON` |

**VAD 配置**（启用后展开）：

| 字段 | 默认 | 调整建议 |
|---|---|---|
| threshold | 0.5 | 越大越严，只保留高置信度语音段；嘈杂背景可调 0.6 |
| min speech ms | 250 | 短于此的语音段被丢弃 |
| min silence ms | 100 | 短于此的静音被合并 |
| max speech ms | 0（无上限） | 长视频可调 30000 强制分段 |
| speech pad ms | 30 | 语音段前后留多少 ms 缓冲 |
| samples overlap | 0.1 | VAD 窗口重叠率，一般不动 |

保存设置后回到 Worker 页运行状态卡能看到 `whisperCliFound` / `whisperEngine` 是否显示 `faster-whisper`。

### 4.2 选择 Whisper 模型

进**「模型」** 页：

- 每个模型卡片显示大小 / 速度 / 质量 / 推荐最小内存
- 已缓存/下载的模型左侧标 ✓；hover 出现删除按钮
- 点「下载」可**手动下载**模型（两种模式各自走各自的路径）

| 模式 | 下载目标 | 说明 |
|---|---|---|
| faster-whisper | `~/.cache/huggingface/hub/` | 调 `huggingface_hub.snapshot_download`；**也可以在 Worker 页直接选模型，首次任务自动下载** |
| whisper.cpp | `modelsPath/ggml-*.bin` | 从 `hf-mirror.com` 或 `huggingface.co` 拉 `.bin` 文件 |

faster-whisper 模式下「自动下载」和「手动下载」效果相同——都是把 CT2 模型拉进 HF cache。区别只是手动下载可以提前"预热"缓存，不会在第一次 ASR 时等几分钟。

**模型选择建议**：

| 显存 / 内存 | 推荐 |
|---|---|
| ≥ 8 GB GPU | `large-v3`（最准） |
| 4–8 GB GPU | `large-v3-turbo`（推 large-v3 的 1/2 大小且只损失 5% 质量） |
| 仅 CPU，≥ 8 GB 内存 | `medium-q5_0`（量化版） |
| CPU + 4 GB 内存 | `small-q5_1` |
| 资源紧张 | `tiny` / `base`（准确率低，仅练手） |

仅识别英文可以选 `.en` 变体（同等大小下英文更准）。

### 4.3 配置翻译服务

进**「翻译服务」** 页，左栏点「+ 新增 Provider」选择类型，14 种内置 type：

| Type | 类别 | 必填字段 | 备注 |
|---|---|---|---|
| `openai` | AI | apiUrl / apiKey / modelName | OpenAI 官方或兼容厂商（DeepSeek / Together / OpenRouter…） |
| `deepseek` | AI | apiUrl / apiKey / modelName | apiUrl 填 `https://api.deepseek.com/v1` |
| `qwen` | AI | apiUrl / apiKey / modelName | DashScope OpenAI 兼容模式 |
| `siliconflow` | AI | apiUrl / apiKey / modelName | 硅基流动 |
| `Gemini` | AI | apiUrl / apiKey / modelName | Google AI Studio 的 OpenAI 兼容代理 |
| `azureopenai` | AI | apiUrl（含 deployment + api-version） / apiKey | Azure OpenAI 资源 |
| `ollama` | AI | apiUrl / modelName | 本地 LLM，无 apiKey |
| `doubao` | API | apiUrl / apiKey / modelName | 字节豆包翻译 |
| `volc` | API | apiKey（AK）/ apiSecret（SK） | 火山引擎翻译，V4 签名 |
| `baidu` | API | apiKey（appid）/ apiSecret | 百度翻译开放平台 |
| `aliyun` | API | apiKey / apiSecret / endpoint | 阿里云机器翻译，V3 签名 |
| `google` | API | apiUrl / apiKey | Google Translation API v2 |
| `azure` | API | apiUrl / apiKey / apiSecret（region） | Azure Cognitive Translator |
| `deeplx` | API | apiUrl | DeepLX 自部署，无认证 |

**AI 类**（isAi=true）会走 JSON 批量翻译，每批默认 10 条字幕；可调：

- Batch Size：单批字幕数，1–100
- 并发：同时多少批在跑（受 provider 限流约束）
- 请求间隔（秒）：相邻批次提交的最小间隔，节流
- Structured Output：`json_schema`（OpenAI 推荐）/ `json_object` / `disabled`
- System Prompt：留空用内置默认，可填私有 prompt

**API 类**（isAi=false）：每批 1 条，纯 HTTP，无 prompt。

保存后点右上「测试翻译」会用 `Hello China` → 你设置的目标语言跑一遍，成功显示 `OK: 中国你好` 之类的结果。

### 4.4 连接服务端 + 启动 Worker

回**「Worker」** 页：

**服务端配置卡**：

| 字段 | 说明 |
|---|---|
| 服务端 URL | m3u8-preview-go 的 base URL，如 `https://m3u8.example.com` |
| Worker Token | 服务端给本 worker 颁发的 token（`mwt_xxx`），在 m3u8-preview-go 后台创建 |
| Worker 名称 | 默认主机名；在服务端 worker 列表里区分多台机器用 |
| Poll 间隔 | 无任务时的轮询间隔（秒），默认 5；服务端 long-poll 25s 内有任务会立刻返回 |
| 心跳间隔 | 给服务端汇报存活的间隔（秒），默认 30 |
| 错误退避起点 | claim 失败后退避起步秒数，每次失败 ×1.7 直到封顶 60s |
| 校验 TLS 证书 | 服务端是自签证书的话关掉 |

**ASR & 翻译配置卡**：

| 字段 | 说明 |
|---|---|
| Whisper 模型 | 从「模型」页下载好的模型里选 |
| 翻译 Provider | 从「翻译服务」页配好的里选；选「不翻译」则只生成单语 SRT |
| 默认源语言 | `auto` 让 whisper 自动检测；服务端任务携带的 sourceLang 优先 |
| 默认目标语言 | 不指定时跟随任务；任务也没指定则跳过翻译 |
| Whisper Max Context | `-1` 用 whisper 默认值，正常不动 |
| Whisper Prompt | 可填专有名词 / 人名 / 术语帮助识别 |

填好后点**「测试连接」**，绿色 `OK` 表示 token 有效；再点**「启动 Worker」**。

启动成功后：

- 运行状态卡：「注册」+「Polling」都变绿
- 并发槽位卡：实时显示 IO / ASR / 翻译三类资源池占用
- 当前任务卡：服务端派活后会实时显示进度

---

## 五、各页面使用指南

### Worker dashboard（首页 `/`）

`运行状态` 卡是诊断核心：

- **注册** + **Polling** 都是绿钩才算正常工作
- **maxConcurrent**：当前生效的最大并发任务数（来自服务端 `register` 响应或本地 override）
- **本地并发上限 override**：服务端没返回或你想强制更高时填，`0 = 跟随服务端`。改完失焦自动保存生效，无需重启 worker
- **IO / ASR / 翻译槽**：三维资源池实时占用
  - IO 槽：拉 FLAC + ffmpeg 解码 + 上传 VTT，可中等并发
  - ASR 槽：whisper.cpp，**全局 mutex 强制串行**（GPU mutex / addon thread-safety）
  - 翻译槽：受 provider 配额约束，低并发

`当前任务` 卡显示每个 in-flight job 的 jobId / displayName / stage / 进度条。`历史任务` 卡持久化 50 条已结束任务（completed / failed / lost），重启后仍可见。

底部 3 个按钮：

- **保存设置**：仅保存配置不重启 worker
- **测试连接**：验证 baseUrl + token，无副作用
- **启动 Worker / 停止 Worker**：注册到服务端 + 起 poll loop / 优雅停止

### 模型管理（`/models/`）

- 按 tiny / base / small / medium / large 分类展示
- **两种模式都支持手动下载**：faster-whisper 预热 HF cache；whisper.cpp 拉 ggml-*.bin
  - whisper.cpp 模式：顶部下拉切换下载源（`hf-mirror.com` 或 `huggingface.co`）
  - faster-whisper 模式：下载源下拉**不显示**（始终走 HF），但下载按钮同样可用
- faster-whisper 模式下也可以**跳过手动下载**——Worker 页选模型后首次任务自动下载
- 已缓存/下载的模型左侧标 ✓，hover 出现删除按钮
- 下载进度通过 WebSocket 实时推送

### 翻译服务（`/providers/`）

- 左栏列表：点条目编辑，hover 出现垃圾桶图标可删
- 右栏表单：所有字段改动**不会**自动保存，必须点底部「保存」
- 右上「测试翻译」按 `en → zh` 跑一条样例

### 系统设置（`/settings/`）

- **外观**：浅色 / 深色 / 跟随系统
- **Web UI Bearer Token**：仅本地浏览器有效，保存到 localStorage。后端启用 `WEB_UI_TOKEN` env 时必填
- **可执行文件 / 模型路径**：见 [4.1](#41-系统设置)
- **GPU 加速 / VAD 配置**：见 [4.1](#41-系统设置)

---

## 六、环境变量与命令行参数

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `MWS_ADDR` | `:8089` | HTTP 监听地址，也可用 `--addr` 命令行参数覆盖 |
| `MWS_CONFIG_DIR` | `$XDG_CONFIG_HOME/m3u8-subtitle-worker` | 配置目录（含 `config.json`） |
| `MWS_DATA_DIR` | `$XDG_DATA_HOME/m3u8-subtitle-worker` | 数据目录（模型 / 历史 / 临时工作目录默认根） |
| `MWS_WORK_ROOT` | `$TMPDIR/m3u8-subtitle-worker` | 单任务工作目录根（FLAC / WAV / SRT 落盘） |
| `WEB_UI_TOKEN` | （空） | 设置后所有 `/api/*` 请求需 `Authorization: Bearer <token>` |

命令行参数（`--help` 看全部）：

```
--addr string        HTTP 监听地址 (default ":8089")
--work-root string   worker 临时工作目录
```

---

## 七、运维与高级话题

### 7.1 多 worker 并发部署

服务端可以同时挂多台 worker。每台 worker 用**独立** token 注册（在 m3u8-preview-go 后台为每台机器创建一个 token），服务端会按可用容量公平分发任务。

- 每台 worker 拉自己一份 FLAC + 跑自己的 whisper，互不影响
- 单卡 GPU 上同台机器的多任务**仍然串行**（ASR mutex），所以多任务并发收益来自不同机器 / 不同卡
- 想在一台机器同时跑多 worker（罕见，比如做对照测试）：用不同的 `MWS_CONFIG_DIR` + `MWS_DATA_DIR` + `MWS_ADDR` 跑多个进程

### 7.2 Bearer Token 认证

默认无认证（适合 LAN 内网）。生产暴露公网建议开启：

```bash
WEB_UI_TOKEN="$(openssl rand -hex 32)" ./subtitle-worker --addr :8089
```

打开后所有 `/api/*` 请求必须带 `Authorization: Bearer <token>` 头，WebSocket 走 `?token=<token>` 查询参数。

浏览器端：进「设置 → Web UI Bearer Token」填入相同 token，保存到 localStorage。所有 fetch 请求自动带头。

### 7.3 nginx / Caddy 反代 + HTTPS

worker 本身**不做 TLS 终端**。生产建议放反代后面。

**Caddy**（最简）：

```caddy
worker.example.com {
    reverse_proxy 127.0.0.1:8089
    # WebSocket 自动支持，无需额外配置
}
```

**nginx**：

```nginx
server {
    listen 443 ssl http2;
    server_name worker.example.com;
    ssl_certificate     /etc/letsencrypt/live/worker.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/worker.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8089;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        # WebSocket 升级
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        # 模型下载可能很久
        proxy_read_timeout 30m;
        proxy_send_timeout 30m;
    }
}
```

### 7.4 日志与监控

- **stderr** 直接打日志，docker logs / journalctl 都能拿到
- **内存环形缓冲区**：最近 1000 条日志，HTTP 端点 `GET /api/logs` 拉出来
- **WebSocket `log` 事件**：日志实时推送给前端
- **健康检查**：`GET /api/health` 返回 `{"ok":true,"data":{"status":"ok"}}`，Docker / k8s liveness probe 可直接用

服务端 m3u8-preview-go 也会记录每个 worker 的最近心跳 + 任务历史，互为参考。

### 7.5 备份与迁移

需要备份的关键文件：

```
$MWS_CONFIG_DIR/config.json                # 全部配置（worker / settings / providers）
$MWS_DATA_DIR/history.json                 # 历史任务（可丢）
$MWS_DATA_DIR/whisper-models/ggml-*.bin    # 模型（按需重新下载即可）
$MWS_DATA_DIR/assets/ggml-silero-v6.2.0.bin # VAD 模型
```

迁移到新机器：复制 config.json + 模型即可，**不需要**复制 history。

---

## 八、故障排查

| 症状 | 可能原因 / 解决 |
|---|---|
| Worker 启动报「请先填 server URL 和 token」 | 还没保存设置就点了启动 |
| 「测试连接」返回 `connect: connection refused` | 服务端不在线 / 防火墙 / 端口写错 |
| 「测试连接」HTTP 401 | token 无效或已过期 |
| 「测试连接」HTTP 403 | token 类型不匹配（确认是 worker token，不是 user token） |
| `whisper-cli not found` | PATH 里没有 / 路径写错；到「设置」填绝对路径 |
| `cannot find model ggml-large-v3.bin` | whisper.cpp 模式没下载；faster-whisper 模式不会出现（自动下载） |
| `whisper 输出 SRT 为空` | VAD 阈值过严 / 音频静音；调低 vadThreshold 或暂时关 VAD 验证 |
| `ModuleNotFoundError: No module named 'faster_whisper'` | Python venv 未激活或未安装；重跑 `install-faster-whisper.sh` |
| `CUDA out of memory` | 模型太大；换 `large-v3-turbo` 或调低本地并发上限到 1 |
| HF 下载模型很慢 / 超时 | 在宿主机设 `HF_ENDPOINT=https://hf-mirror.com` 再重启 worker |
| FLAC 下载报 `Content-Length=0` | audio worker 未在 5 min 内上传 FLAC 到服务端 broker；检查 audio worker 那边 |
| `flac sha256 mismatch` | 服务端给的 sha 和实际拉到的不符；网络中间人 / 文件被改 |
| `translate provider id=xxx 在列表中找不到` | 在「Worker」页选了的 provider 后来被删了 |
| 翻译 quota_exceeded | provider 限流 / 余额不足；换 provider 或降低并发 |
| 浏览器打开是空白 / 404 | 前端没构建；跑 `./scripts/build.sh` |
| 浏览器 `Application error: a client-side exception` | 后端 API 返回了意外结构；先 `curl /api/worker/status` 看响应是否正常 |
| GPU 跑 ASR 报 OOM | 模型太大 / 并发太高；换量化模型或把本地并发上限调到 1 |
| Docker 容器无 GPU | nvidia-container-toolkit 没装 / `--gpus all` 没传 |

更深的诊断：`journalctl -u subtitle-worker -f` 或 `docker compose logs -f` 看完整 stderr。

---

## 九、API 速查

所有响应统一 envelope：

```json
{"ok": true, "data": {...}}
{"ok": false, "message": "..."}
```

| Method | Path | 说明 |
|---|---|---|
| GET | `/api/health` | 健康检查 |
| GET | `/api/version` | 版本 / Go 信息 |
| GET | `/api/system/info` | whisper-cli 是否存在 / 模型路径 / 已安装模型列表 |
| GET | `/api/logs` | 最近 1000 条日志 |
| GET | `/api/settings` | 系统设置 |
| PUT | `/api/settings` | 更新系统设置 |
| GET | `/api/providers` | 翻译服务列表 |
| PUT | `/api/providers` | 整体替换翻译服务列表 |
| POST | `/api/providers/{id}/test` | 测试翻译 |
| GET | `/api/worker/settings` | worker 配置 |
| PUT | `/api/worker/settings` | 更新 worker 配置 |
| GET | `/api/worker/status` | 实时运行状态 |
| POST | `/api/worker/start` | 启动 worker |
| POST | `/api/worker/stop` | 停止 worker |
| POST | `/api/worker/test` | ping 服务端 |
| GET | `/api/models` | catalog + installed + modelsPath |
| POST | `/api/models/{name}/download?source=hf-mirror` | 下载（异步，进度走 WS） |
| DELETE | `/api/models/{name}` | 删除模型 |
| POST | `/api/models/import` | multipart 导入本地 .bin |
| GET | `/api/ws/status` | WebSocket：`workerStatus` / `modelProgress` / `log` 事件流 |

curl 示例（无认证）：

```bash
curl http://127.0.0.1:8089/api/worker/status | jq
curl -X POST http://127.0.0.1:8089/api/worker/start
curl -X PUT -H 'Content-Type: application/json' \
  -d '{"baseUrl":"https://m3u8.example.com","token":"mwt_xxx",...}' \
  http://127.0.0.1:8089/api/worker/settings
```

带 token 时所有请求加 `-H "Authorization: Bearer $TOKEN"`。

---

## 十、开发模式

并行起前端 dev server + Go 后端：

```bash
./scripts/dev.sh
# 浏览器 http://localhost:3000
# /api/* 自动 proxy 到 :8089
# 改 Go 代码：Ctrl-C 后重跑 go run ./cmd/worker
# 改前端：热重载
```

只跑后端：

```bash
MWS_ADDR=:8089 go run ./cmd/worker
```

只跑前端（连远程后端）：

```bash
cd web && MWS_DEV=1 pnpm dev
```

跑测试 + vet：

```bash
go test ./...
go vet ./...
```

---

## 十一、目录结构

```
m3u8-preview-subtitle-worker-web/
├── cmd/worker/main.go            进程入口：加载 config → 起 web + worker → 等信号
├── internal/
│   ├── config/                   JSON 持久化 + 默认值 + XDG 路径
│   ├── broker/                   m3u8-preview-go HTTP client（v3/v4 broker 协议）
│   ├── worker/                   poll loop / heartbeat / runner / state / lifecycle / asr_mutex / errors
│   ├── audio/                    FLAC 下载 + SHA-256 校验 + ffmpeg 解码
│   ├── asr/                      whisper-cli 子进程 + SRT 解析 + VTT 转换
│   ├── translate/                批量翻译 + content_template + langmap
│   │   └── providers/            10 种 translator 实现（含 V4 / ACS3 V3 签名）
│   ├── models/                   whisper catalog / downloader / installer
│   ├── web/                      chi router + REST + WebSocket + go:embed SPA + Bearer auth
│   └── logger/                   stderr + 环形 buffer + 订阅广播
├── web/                          Next.js 14 前端
│   ├── pages/                    index (worker) / models / providers / settings
│   ├── components/ui/            shadcn/ui
│   ├── lib/api.ts                fetch wrapper（替代 Electron 的 window.ipc）
│   └── lib/ws.ts                 WebSocket hook + 自动重连
├── scripts/{build,dev}.sh        一键构建 / 一键开发
├── Dockerfile                    多阶段构建：node → go → debian:bookworm-slim
├── docker-compose.yml            含 GPU + 卷挂载示例
├── go.mod / go.sum
└── README.md
```

---

## License

复用原 SmartSub / m3u8-preview-subtitle-worker 项目协议。
