// Package broker — HTTP client，对接 m3u8-preview-go /api/v1/worker/* 端点。
//
// 与原 TS WorkerApiClient 行为一致：
//   - timeout 60s 默认；claim 用 waitSec+5s；audioFetch 6 分钟；complete 2 分钟
//   - 410 Gone → ErrJobLost；503 audio_fetch → 上层 retry
//   - Bearer token 头 + 可选 InsecureSkipVerify
package broker

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

// Client 与服务端的 HTTP 会话。线程安全：底层 http.Client 自带连接池。
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New 构造 Client。baseURL 末尾的 / 会被剥掉；token 为 mwt_xxx。
func New(baseURL, token string, verifyTLS bool) (*Client, error) {
	if baseURL == "" || token == "" {
		return nil, fmt.Errorf("baseUrl 或 token 未配置")
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: !verifyTLS},
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http: &http.Client{
			Timeout:   60 * time.Second,
			Transport: tr,
		},
	}, nil
}

// Ping GET /healthz —— "测试连接"按钮用。
func (c *Client) Ping(ctx context.Context) error {
	req, err := c.newReq(ctx, http.MethodGet, "/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ping 失败: HTTP %d", resp.StatusCode)
	}
	return nil
}

// RegisterRequest — POST /api/v1/worker/register 请求体。
type RegisterRequest struct {
	WorkerID     string   `json:"workerId"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	GPU          string   `json:"gpu"`
	Capabilities []string `json:"capabilities"`
}

// Register 注册 worker，返回服务端响应。
func (c *Client) Register(ctx context.Context, req RegisterRequest) (*RegisterResponse, error) {
	r, err := c.newJSONReq(ctx, http.MethodPost, "/api/v1/worker/register", req)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusGone {
		return nil, ErrJobLost
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("register HTTP %d: %s", resp.StatusCode, truncate(string(body), 500))
	}
	var env APIEnvelope[RegisterResponse]
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("register decode: %w (body=%s)", err, truncate(string(body), 200))
	}
	if !env.Success {
		return nil, fmt.Errorf("register server error: %s", firstNonEmpty(env.Message, env.Code, "unknown"))
	}
	return &env.Data, nil
}

// Claim long-poll：返回 nil 表示无任务（HTTP 204）。waitSec=25 推荐；0 = 短轮询。
// 客户端 timeout = waitSec+5s，waitSec=0 时回退 60s。
func (c *Client) Claim(ctx context.Context, workerID string, waitSec int) (*ClaimedJob, error) {
	timeout := 60 * time.Second
	if waitSec > 0 {
		timeout = time.Duration(waitSec+5) * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body := map[string]any{"workerId": workerID, "waitSec": waitSec}
	r, err := c.newJSONReq(reqCtx, http.MethodPost, "/api/v1/worker/claim", body)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil // 无任务
	}
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusGone {
		return nil, ErrJobLost
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("claim HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 500))
	}
	var env APIEnvelope[ClaimedJob]
	if err := json.Unmarshal(respBody, &env); err != nil {
		return nil, fmt.Errorf("claim decode: %w", err)
	}
	if !env.Success {
		return nil, fmt.Errorf("claim server error: %s", firstNonEmpty(env.Message, env.Code, "unknown"))
	}
	return &env.Data, nil
}

// Deregister v4 优雅下线。服务端会把本 worker 持有的 RUNNING 任务按
// worker_shutdown（neutral）回滚。失败可忽略——stale recovery 兜底。
func (c *Client) Deregister(ctx context.Context, workerID string) error {
	body := map[string]any{"workerId": workerID}
	r, err := c.newJSONReq(ctx, http.MethodPost, "/api/v1/worker/deregister", body)
	if err != nil {
		return err
	}
	c2, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(c2)
	resp, err := c.http.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return expectSuccess(resp, "deregister")
}

// Heartbeat 上报当前任务的 stage / progress。410 Gone → ErrJobLost。
func (c *Client) Heartbeat(ctx context.Context, jobID, workerID, stage string, progress int) error {
	body := map[string]any{"workerId": workerID, "stage": stage, "progress": progress}
	path := "/api/v1/worker/jobs/" + url.PathEscape(jobID) + "/heartbeat"
	r, err := c.newJSONReq(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusGone {
		return ErrJobLost
	}
	return expectSuccess(resp, "heartbeat")
}

// Fail 上报任务失败 + errorKind（v4 重试策略分流）。
func (c *Client) Fail(ctx context.Context, jobID, workerID, errMsg string, kind ErrorKind) error {
	if len(errMsg) > 2000 {
		errMsg = errMsg[:2000]
	}
	body := map[string]any{"workerId": workerID, "errorMsg": errMsg, "errorKind": string(kind)}
	path := "/api/v1/worker/jobs/" + url.PathEscape(jobID) + "/fail"
	r, err := c.newJSONReq(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return expectSuccess(resp, "fail")
}

// Complete multipart 上传 VTT；410 Gone → ErrJobLost。
//
// vttData 是已生成的 VTT 字节。meta 包含 size/sha256/segmentCount 等给服务端记录。
func (c *Client) Complete(ctx context.Context, jobID string, meta CompleteMeta, vttData []byte) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	// meta 字段（JSON 字符串）
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal meta: %w", err)
	}
	if err := mw.WriteField("meta", string(metaJSON)); err != nil {
		return fmt.Errorf("write meta field: %w", err)
	}

	// vtt 字段（文件）
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="vtt"; filename="subtitle.vtt"`)
	h.Set("Content-Type", "text/vtt")
	part, err := mw.CreatePart(h)
	if err != nil {
		return fmt.Errorf("create vtt part: %w", err)
	}
	if _, err := part.Write(vttData); err != nil {
		return fmt.Errorf("write vtt: %w", err)
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("close multipart: %w", err)
	}

	path := "/api/v1/worker/jobs/" + url.PathEscape(jobID) + "/complete"
	reqCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.baseURL+path, &buf)
	if err != nil {
		return err
	}
	c.applyHeaders(req)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusGone {
		return ErrJobLost
	}
	return expectSuccess(resp, "complete")
}

// AudioFetchResult 流式 FLAC 响应。调用方负责 Close。
type AudioFetchResult struct {
	Body          io.ReadCloser
	ContentLength int64
}

// AudioFetch 从 broker 拉 FLAC（流式响应）。
// 调用方负责把 Body 写到磁盘 + 算 SHA-256。
//
// timeout 给 6 分钟兜底（broker holdTimeout 5min + 上传时间裕量）。
func (c *Client) AudioFetch(ctx context.Context, audioURL, workerID string) (*AudioFetchResult, error) {
	resolved := c.resolveURL(audioURL)
	resolved = appendQuery(resolved, "workerId", workerID)

	reqCtx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, resolved, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	c.applyHeaders(req)

	// 用一个独立的 client 关掉 60s 默认 timeout（流式响应靠 context 控制时长）
	streamClient := &http.Client{Transport: c.http.Transport}
	resp, err := streamClient.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.StatusCode == http.StatusGone {
		resp.Body.Close()
		cancel()
		return nil, ErrJobLost
	}
	if resp.StatusCode == http.StatusServiceUnavailable {
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("audio worker 暂时不在线（broker 5min 内未收到上传），请稍后再试")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		preview := readPreview(resp.Body, 4096)
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("audio_fetch HTTP %d: %s", resp.StatusCode, preview)
	}
	if resp.ContentLength == 0 {
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("audio_fetch 200 但 Content-Length=0：audio worker 未在 5min 内将 FLAC 推流到服务端")
	}

	// 把 cancel 绑到 body 关闭——让调用方 close 时也 cancel context
	return &AudioFetchResult{
		Body:          &cancelOnClose{rc: resp.Body, cancel: cancel},
		ContentLength: resp.ContentLength,
	}, nil
}

// --- helpers ---

func (c *Client) newReq(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	c.applyHeaders(req)
	return req, nil
}

func (c *Client) newJSONReq(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := c.newReq(ctx, method, path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (c *Client) applyHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", "M3u8PreviewSubtitleWorker/0.1.0")
}

// resolveURL 把相对/绝对 URL 还原成完整 URL（服务端 PublicBaseURL 为空时 audioArtifactUrl 是相对的）。
func (c *Client) resolveURL(raw string) string {
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	if strings.HasPrefix(raw, "/") {
		return c.baseURL + raw
	}
	return c.baseURL + "/" + raw
}

func appendQuery(rawURL, key, value string) string {
	sep := "?"
	if strings.Contains(rawURL, "?") {
		sep = "&"
	}
	return rawURL + sep + key + "=" + url.QueryEscape(value)
}

func expectSuccess(resp *http.Response, label string) error {
	if resp.StatusCode == http.StatusGone {
		return ErrJobLost
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s HTTP %d: %s", label, resp.StatusCode, truncate(string(body), 500))
	}
	// 服务端通常返回 envelope；有些 endpoint 仅返回 OK 文本——两者都允许。
	if len(body) == 0 {
		return nil
	}
	var env APIEnvelope[json.RawMessage]
	if err := json.Unmarshal(body, &env); err != nil {
		// 非 JSON 但 2xx：也算成功
		return nil
	}
	// 有 success 字段且为 false → 失败
	if strings.Contains(string(body), `"success"`) && !env.Success {
		return fmt.Errorf("%s server error: %s", label, firstNonEmpty(env.Message, env.Code, "unknown"))
	}
	return nil
}

func readPreview(r io.Reader, max int) string {
	buf := make([]byte, max)
	n, _ := io.ReadFull(r, buf)
	if n < 0 {
		n = 0
	}
	return string(buf[:n])
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// cancelOnClose 包一层 ReadCloser：调用方 Close 时同时 cancel context。
// 防止 audio worker 流式响应卡住 6 分钟超时不被释放。
type cancelOnClose struct {
	rc     io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Read(p []byte) (int, error) { return c.rc.Read(p) }
func (c *cancelOnClose) Close() error {
	c.cancel()
	return c.rc.Close()
}
