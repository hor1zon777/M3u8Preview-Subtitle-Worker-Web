// lib/api.ts — 前端 → Go 后端 REST 调用封装。
//
// 行为对齐原 Electron window.ipc.invoke：成功返回 data，失败抛 Error。
// 服务端响应统一 envelope { ok: bool, data?, message? }。

const BASE = '';

export interface ApiEnvelope<T = any> {
  ok: boolean;
  data?: T;
  message?: string;
}

let authToken: string | undefined = undefined;

export function setAuthToken(t: string | undefined) {
  authToken = t;
  if (typeof window !== 'undefined') {
    if (t) localStorage.setItem('mws_token', t);
    else localStorage.removeItem('mws_token');
  }
}

function loadToken(): string | undefined {
  if (authToken) return authToken;
  if (typeof window !== 'undefined') {
    const t = localStorage.getItem('mws_token');
    if (t) authToken = t;
  }
  return authToken;
}

async function http<T>(method: string, path: string, body?: any): Promise<T> {
  const t = loadToken();
  const headers: Record<string, string> = {};
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  if (t) headers['Authorization'] = 'Bearer ' + t;
  const resp = await fetch(BASE + path, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (resp.status === 401) {
    // 触发全局登录 gate 重新拉起登录页
    setAuthToken(undefined);
    if (typeof window !== 'undefined') {
      window.dispatchEvent(new Event('mws:unauthorized'));
    }
    throw new Error('unauthorized: 请在登录页输入 Token');
  }
  const env: ApiEnvelope<T> = await resp.json();
  if (env.ok === false) {
    throw new Error(env.message || 'unknown error');
  }
  return env.data as T;
}

// --- 公开 API ---

export interface WorkerSettings {
  baseUrl: string;
  token: string;
  pollIntervalSec: number;
  heartbeatIntervalSec: number;
  errorBackoffSec: number;
  verifyTls: boolean;
  workerName: string;
  workerId: string;
  enabled: boolean;
  whisperModel: string;
  sourceLanguage: string;
  targetLanguage: string;
  translateProviderId: string;
  whisperPrompt: string;
  whisperMaxContext: number;
  localMaxConcurrentTasks: number;
}

export interface SystemSettings {
  language: string;
  useCuda: boolean;
  modelsPath: string;
  whisperCliPath: string;
  ffmpegPath: string;
  assetsPath: string;
  useVAD: boolean;
  vadThreshold: number;
  vadMinSpeechDuration: number;
  vadMinSilenceDuration: number;
  vadMaxSpeechDuration: number;
  vadSpeechPad: number;
  vadSamplesOverlap: number;
  debug: boolean;
  webToken: string;
}

export interface AuthStatus {
  tokenRequired: boolean;
  authenticated: boolean;
}

export interface Provider {
  id: string;
  name: string;
  type: string;
  isAi: boolean;
  apiUrl?: string;
  apiKey?: string;
  apiSecret?: string;
  modelName?: string;
  prompt?: string;
  systemPrompt?: string;
  useBatchTranslation?: boolean;
  batchSize?: number;
  concurrency?: number;
  requestInterval?: number;
  structuredOutput?: string;
  useJsonMode?: boolean;
  endpoint?: string;
  providerType?: string;
  customParameters?: Record<string, any>;
}

export interface CurrentJob {
  jobId: string;
  mediaId: string;
  mediaTitle?: string;
  displayName: string;
  stage: string;
  progress: number;
  startedAt: number;
  category?: string;
  attempt?: number;
  maxAttempts?: number;
}

export interface HistoryJob {
  jobId: string;
  displayName: string;
  mediaTitle?: string;
  finalStage: 'completed' | 'failed' | 'lost';
  errorMessage?: string;
  endedAt: number;
  errorKind?: string;
}

export interface SubtitleSlots {
  ioMax: number;
  asrMax: number;
  translateMax: number;
  ioInflight: number;
  asrInflight: number;
  asrQueueDepth: number;
  translateInflight: number;
}

export interface WorkerRuntimeStatus {
  registered: boolean;
  pollingActive: boolean;
  staleThresholdSec: number;
  maxConcurrentTasks: number;
  slots?: SubtitleSlots;
  workerId: string;
  uptimeSec: number;
  currentJobs: CurrentJob[];
  historyJobs: HistoryJob[];
  stats: { completed: number; failed: number; lastError?: string };
}

export interface SystemInfo {
  modelsInstalled: string[];
  downloadingModels: string[];
  modelsPath: string;
  totalMemoryGB?: number;
  whisperCliPath: string;
  whisperCliFound: boolean;
  whisperEngine: string; // "whisper-cli" | "faster-whisper" | "unknown"
  ffmpegPath: string;
  ffmpegFound: boolean;
}

export interface ModelEntry {
  name: string;
  size: string;
  speed: number;
  quality: number;
  minRamGb: number;
  quantized?: boolean;
  englishOnly?: boolean;
}

export interface ModelCategory {
  name: string;
  description: string;
  models: ModelEntry[];
}

export interface ModelsResponse {
  catalog: ModelCategory[];
  installed: string[];
  modelsPath: string;
}

export const api = {
  getWorkerSettings: () => http<WorkerSettings>('GET', '/api/worker/settings'),
  putWorkerSettings: (s: WorkerSettings) => http<WorkerSettings>('PUT', '/api/worker/settings', s),
  getWorkerStatus: () => http<WorkerRuntimeStatus>('GET', '/api/worker/status'),
  workerStart: () => http<{ started: boolean }>('POST', '/api/worker/start'),
  workerStop: () => http<{ stopped: boolean }>('POST', '/api/worker/stop'),
  workerTest: () => http<{ ok: boolean; message: string }>('POST', '/api/worker/test'),

  getSettings: () => http<SystemSettings>('GET', '/api/settings'),
  putSettings: (s: SystemSettings) => http<SystemSettings>('PUT', '/api/settings', s),

  getProviders: () => http<Provider[]>('GET', '/api/providers'),
  putProviders: (p: Provider[]) => http<Provider[]>('PUT', '/api/providers', p),
  testProvider: (id: string, body: { sourceLanguage: string; targetLanguage: string }) =>
    http<{ translation: string }>('POST', `/api/providers/${encodeURIComponent(id)}/test`, body),

  getSystemInfo: () => http<SystemInfo>('GET', '/api/system/info'),
  getLogs: () => http<{ timestamp: number; message: string; type: string }[]>('GET', '/api/logs'),

  listModels: () => http<ModelsResponse>('GET', '/api/models'),
  downloadModel: (name: string, source?: string) =>
    http<{ started: boolean }>('POST', `/api/models/${encodeURIComponent(name)}/download` + (source ? `?source=${source}` : '')),
  deleteModel: (name: string) => http<{ deleted: string }>('DELETE', `/api/models/${encodeURIComponent(name)}`),

  // 认证
  getAuthStatus: () => http<AuthStatus>('GET', '/api/auth/status'),
  login: (token: string) =>
    http<{ authenticated: boolean; tokenRequired: boolean }>('POST', '/api/auth/login', { token }),
};
