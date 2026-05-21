// pages/index.tsx — Worker dashboard。
//
// 复刻原 renderer/pages/[locale]/worker.tsx 的所有卡片：
//   - 运行状态卡（注册 / Polling / WorkerID / 完成 / 失败 / 运行时长 / 多维并发槽位 / lastError）
//   - 当前任务卡（每个 in-flight job 显示 stage / progress bar / id / ago / attempt）
//   - 历史任务卡（completed / failed / lost）
//   - 服务端配置卡（baseUrl / token / workerName / pollInterval / heartbeatInterval / errorBackoff / verifyTls）
//   - ASR & 翻译配置卡（whisperModel / providerId / sourceLang / targetLang / maxContext / prompt）
//   - 操作按钮（保存 / 测试连接 / 启动 / 停止）

import React, { useEffect, useState } from 'react';
import { Card, CardContent, CardHeader, CardTitle } from '../components/ui/card';
import { Button } from '../components/ui/button';
import { Input } from '../components/ui/input';
import { Label } from '../components/ui/label';
import { Switch } from '../components/ui/switch';
import { Badge } from '../components/ui/badge';
import { Textarea } from '../components/ui/textarea';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '../components/ui/select';
import { toast } from 'sonner';
import {
  Server,
  Play,
  Pause,
  PlugZap,
  Activity,
  CheckCircle2,
  XCircle,
  Clock,
  Languages,
  History,
} from 'lucide-react';
import { api, WorkerSettings, WorkerRuntimeStatus, Provider, SystemInfo } from '../lib/api';
import { useWS } from '../lib/ws';
import { supportedLanguage } from '../lib/utils';

const DEFAULT: WorkerSettings = {
  baseUrl: '',
  token: '',
  pollIntervalSec: 5,
  heartbeatIntervalSec: 30,
  errorBackoffSec: 5,
  verifyTls: true,
  workerName: '',
  workerId: '',
  enabled: false,
  whisperModel: 'large-v3',
  sourceLanguage: 'auto',
  targetLanguage: '',
  translateProviderId: '',
  whisperPrompt: '',
  whisperMaxContext: -1,
  localMaxConcurrentTasks: 0,
};

const NO_TRANSLATE = '__none__';
const NO_TARGET = '__no_target__';

function formatUptime(sec: number): string {
  if (sec < 60) return `${sec}s`;
  const m = Math.floor(sec / 60);
  if (m < 60) return `${m}m ${sec % 60}s`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m`;
}

function fmtAgo(ts: number): string {
  const sec = Math.max(0, Math.floor((Date.now() - ts) / 1000));
  return formatUptime(sec) + ' ago';
}

export default function WorkerPage() {
  const [settings, setSettings] = useState<WorkerSettings>(DEFAULT);
  const [status, setStatus] = useState<WorkerRuntimeStatus | null>(null);
  const [providers, setProviders] = useState<Provider[]>([]);
  const [system, setSystem] = useState<SystemInfo | null>(null);
  const [pingResult, setPingResult] = useState<string | null>(null);
  const [busy, setBusy] = useState<'idle' | 'starting' | 'stopping' | 'pinging'>('idle');

  useWS({
    onStatus: (s) => setStatus(s),
  });

  const refreshAll = async () => {
    try {
      const [s, st, pr, sys] = await Promise.all([
        api.getWorkerSettings(),
        api.getWorkerStatus(),
        api.getProviders(),
        api.getSystemInfo(),
      ]);
      setSettings({ ...DEFAULT, ...s });
      setStatus(st);
      setProviders(Array.isArray(pr) ? pr : []);
      setSystem(sys ? {
        ...sys,
        modelsInstalled: sys.modelsInstalled || [],
        downloadingModels: sys.downloadingModels || [],
      } : null);
    } catch (e: any) {
      toast.error('加载失败：' + e.message);
    }
  };

  useEffect(() => {
    void refreshAll();
    // 定时拉一次 system info（worker status 由 WS 推；providers/system 偶发变化）
    const t = setInterval(() => {
      api.getSystemInfo().then(setSystem).catch(() => {});
    }, 5000);
    return () => clearInterval(t);
  }, []);

  const update = <K extends keyof WorkerSettings>(key: K, val: WorkerSettings[K]) =>
    setSettings((prev) => ({ ...prev, [key]: val }));

  const save = async () => {
    try {
      const next = await api.putWorkerSettings(settings);
      setSettings({ ...DEFAULT, ...next });
      toast.success('设置已保存');
    } catch (e: any) {
      toast.error('保存失败：' + e.message);
    }
  };

  const testConn = async () => {
    setBusy('pinging');
    setPingResult(null);
    try {
      await api.putWorkerSettings(settings);
      const r = await api.workerTest();
      setPingResult(r.ok ? `OK: ${r.message}` : `失败: ${r.message}`);
    } catch (e: any) {
      setPingResult('失败：' + e.message);
    }
    setBusy('idle');
  };

  const start = async () => {
    setBusy('starting');
    try {
      await api.putWorkerSettings({ ...settings, enabled: true });
      await api.workerStart();
      setSettings((p) => ({ ...p, enabled: true }));
      toast.success('Worker 已启动');
    } catch (e: any) {
      toast.error('启动失败：' + e.message);
      await api.putWorkerSettings({ ...settings, enabled: false }).catch(() => {});
      setSettings((p) => ({ ...p, enabled: false }));
    }
    setBusy('idle');
  };

  const stop = async () => {
    setBusy('stopping');
    try {
      await api.workerStop();
      setSettings((p) => ({ ...p, enabled: false }));
      toast.success('Worker 已停止');
    } catch (e: any) {
      toast.error('停止失败：' + e.message);
    }
    setBusy('idle');
  };

  const isRegistered = status?.registered === true;
  const isPolling = status?.pollingActive === true;
  const installedModels = system?.modelsInstalled || [];

  return (
    <div className="container mx-auto p-6 max-w-5xl space-y-6">
      <div className="flex items-center gap-3 mb-2">
        <Server className="size-7 text-primary" />
        <div>
          <h1 className="text-2xl font-semibold">远程 Worker</h1>
          <p className="text-sm text-muted-foreground">
            接入 m3u8-preview-go v3/v4 broker 协议；ASR 与翻译 Provider 在本页独立配置。
          </p>
        </div>
      </div>

      {/* 运行状态卡 */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Activity className="size-5" /> 运行状态
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="grid grid-cols-3 gap-3 text-sm">
            <StatusBadge label="注册" ok={isRegistered} valueOk="已注册" valueFail="未注册" />
            <StatusBadge label="Polling" ok={isPolling} valueOk="运行中" valueFail="已停止" />
            <div className="flex flex-col gap-1">
              <span className="text-xs text-muted-foreground">Worker ID</span>
              <code className="text-xs font-mono break-all">
                {status?.workerId || settings.workerId || '(未生成)'}
              </code>
            </div>
          </div>
          <div className="grid grid-cols-3 gap-3 text-sm pt-2 border-t border-border">
            <Stat label="累计完成" value={status?.stats.completed ?? 0} />
            <Stat label="累计失败" value={status?.stats.failed ?? 0} />
            <Stat label="运行时长" value={formatUptime(status?.uptimeSec ?? 0)} />
          </div>
          {status?.stats.lastError && (
            <div className="text-xs text-red-500 break-words pt-1">
              最后错误：{status.stats.lastError}
            </div>
          )}

          {/* 并发槽位 */}
          <div className="pt-3 border-t border-border space-y-2">
            <div className="flex items-center justify-between gap-3">
              <span className="text-xs text-muted-foreground">并发配置</span>
              <Badge variant="secondary" className="font-mono text-xs">
                maxConcurrent = {status?.maxConcurrentTasks ?? '?'}
              </Badge>
            </div>
            <div className="flex items-center gap-2">
              <Label htmlFor="localMaxConcurrent" className="text-xs text-muted-foreground shrink-0">
                本地并发上限 override
              </Label>
              <Input
                id="localMaxConcurrent"
                type="number"
                min={0}
                max={32}
                value={settings.localMaxConcurrentTasks}
                onChange={(e) =>
                  update('localMaxConcurrentTasks', Number(e.target.value) || 0)
                }
                onBlur={async () => {
                  try {
                    await api.putWorkerSettings({
                      ...settings,
                      localMaxConcurrentTasks: settings.localMaxConcurrentTasks,
                    });
                    toast.success(
                      `本地并发上限已生效: ${
                        settings.localMaxConcurrentTasks > 0
                          ? settings.localMaxConcurrentTasks
                          : '跟随服务端'
                      }`,
                    );
                  } catch (e: any) {
                    toast.error('保存失败: ' + e.message);
                  }
                }}
                className="h-7 w-20 text-xs"
              />
              <span className="text-xs text-muted-foreground">0 = 跟随服务端</span>
            </div>
            {status?.slots ? (
              <div className="grid grid-cols-3 gap-2 text-xs">
                <SlotIndicator
                  label="IO 槽"
                  inflight={status.slots.ioInflight}
                  max={status.slots.ioMax}
                  hint="拉 FLAC + ffmpeg 解码 + 上传 VTT"
                />
                <SlotIndicator
                  label="ASR 槽"
                  inflight={status.slots.asrInflight}
                  max={status.slots.asrMax}
                  queue={status.slots.asrQueueDepth}
                  hint="whisper.cpp（全局 mutex 强制串行）"
                />
                <SlotIndicator
                  label="翻译槽"
                  inflight={status.slots.translateInflight}
                  max={status.slots.translateMax}
                  hint="LLM provider 翻译"
                />
              </div>
            ) : (
              <div className="text-xs text-muted-foreground italic">
                槽位信息未上报（worker 未运行）
              </div>
            )}
          </div>
        </CardContent>
      </Card>

      {/* 当前任务卡 */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Clock className="size-5" />
            当前任务（{status?.currentJobs.length ?? 0}）
          </CardTitle>
        </CardHeader>
        <CardContent>
          {!status?.currentJobs.length ? (
            <div className="text-sm text-muted-foreground py-6 text-center">
              空闲中（poll 循环每 {settings.pollIntervalSec}s 向服务端查询一次）
            </div>
          ) : (
            <div className="space-y-2">
              {status.currentJobs.map((j) => (
                <div
                  key={j.jobId}
                  className="flex items-center gap-3 p-3 rounded-md border border-border bg-muted/30"
                >
                  <Badge variant="outline" className="shrink-0">{j.stage}</Badge>
                  <div className="flex-1 min-w-0">
                    <div className="text-sm font-medium truncate">{j.displayName || j.jobId}</div>
                    <div className="text-xs text-muted-foreground font-mono truncate">
                      {j.jobId} · {fmtAgo(j.startedAt)}
                      {j.attempt && j.maxAttempts ? ` · attempt ${j.attempt}/${j.maxAttempts}` : ''}
                    </div>
                  </div>
                  <div className="text-sm text-right shrink-0">
                    <div className="font-medium">{j.progress}%</div>
                    <div className="w-24 h-1.5 bg-muted rounded-full overflow-hidden mt-1">
                      <div
                        className="h-full bg-primary transition-all"
                        style={{ width: `${j.progress}%` }}
                      />
                    </div>
                  </div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      {/* 历史任务卡 */}
      {!!status?.historyJobs.length && (
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <History className="size-5" />
              历史任务（{status.historyJobs.length}）
            </CardTitle>
          </CardHeader>
          <CardContent>
            <div className="space-y-1 max-h-80 overflow-auto">
              {status.historyJobs.map((h) => (
                <div
                  key={h.jobId + '_' + h.endedAt}
                  className="flex items-center gap-3 px-2 py-1.5 rounded text-xs hover:bg-muted/40"
                >
                  <Badge
                    variant={
                      h.finalStage === 'completed'
                        ? 'default'
                        : h.finalStage === 'lost'
                          ? 'outline'
                          : 'destructive'
                    }
                    className="shrink-0"
                  >
                    {h.finalStage}
                  </Badge>
                  <span className="flex-1 truncate font-mono">{h.displayName || h.jobId}</span>
                  <span className="text-muted-foreground shrink-0">{fmtAgo(h.endedAt)}</span>
                </div>
              ))}
            </div>
          </CardContent>
        </Card>
      )}

      {/* 服务端配置卡 */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <PlugZap className="size-5" />
            服务端配置
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <Field label="服务端 URL" required>
              <Input
                value={settings.baseUrl}
                onChange={(e) => update('baseUrl', e.target.value)}
                placeholder="https://m3u8.example.com"
              />
            </Field>
            <Field label="Worker Token" required>
              <Input
                type="password"
                value={settings.token}
                onChange={(e) => update('token', e.target.value)}
                placeholder="mwt_..."
              />
            </Field>
            <Field label="Worker 名称">
              <Input
                value={settings.workerName}
                onChange={(e) => update('workerName', e.target.value)}
                placeholder="自动用主机名"
              />
            </Field>
            <Field label="Poll 间隔（秒）">
              <Input
                type="number"
                min={1}
                max={120}
                value={settings.pollIntervalSec}
                onChange={(e) => update('pollIntervalSec', Math.max(1, Number(e.target.value) || 5))}
              />
            </Field>
            <Field label="心跳间隔（秒）">
              <Input
                type="number"
                min={5}
                max={300}
                value={settings.heartbeatIntervalSec}
                onChange={(e) =>
                  update('heartbeatIntervalSec', Math.max(5, Number(e.target.value) || 30))
                }
              />
            </Field>
            <Field label="错误退避起点（秒）">
              <Input
                type="number"
                min={1}
                max={60}
                value={settings.errorBackoffSec}
                onChange={(e) => update('errorBackoffSec', Math.max(1, Number(e.target.value) || 5))}
              />
            </Field>
          </div>
          <div className="flex items-center gap-3">
            <Switch
              checked={settings.verifyTls}
              onCheckedChange={(v) => update('verifyTls', v)}
              id="verifyTls"
            />
            <Label htmlFor="verifyTls" className="text-sm">
              校验 TLS 证书（dev 环境自签证书可关闭）
            </Label>
          </div>
        </CardContent>
      </Card>

      {/* ASR & 翻译配置卡 */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Languages className="size-5" />
            ASR & 翻译配置
            {system?.whisperEngine && system.whisperEngine !== 'unknown' && (
              <Badge variant={system.whisperEngine === 'faster-whisper' ? 'default' : 'secondary'} className="ml-2 text-xs">
                {system.whisperEngine === 'faster-whisper' ? 'faster-whisper' : 'whisper.cpp'}
              </Badge>
            )}
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <Field label="Whisper 模型">
              <Select
                value={settings.whisperModel || ''}
                onValueChange={(v) => update('whisperModel', v)}
              >
                <SelectTrigger>
                  <SelectValue placeholder="选择已下载的模型" />
                </SelectTrigger>
                <SelectContent>
                  {installedModels.length === 0 ? (
                    <SelectItem value="__no_model__" disabled>
                      无已下载模型，请到「模型」页面下载
                    </SelectItem>
                  ) : (
                    installedModels.map((m) => (
                      <SelectItem key={m} value={m}>
                        {m}
                      </SelectItem>
                    ))
                  )}
                </SelectContent>
              </Select>
            </Field>
            <Field label="翻译 Provider">
              <Select
                value={settings.translateProviderId || NO_TRANSLATE}
                onValueChange={(v) =>
                  update('translateProviderId', v === NO_TRANSLATE ? '' : v)
                }
              >
                <SelectTrigger>
                  <SelectValue placeholder="选择翻译服务" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={NO_TRANSLATE}>不翻译（仅生成单语 SRT）</SelectItem>
                  {providers.map((p) => (
                    <SelectItem key={p.id} value={p.id}>
                      {p.name}
                      {p.isAi ? ' · AI' : ''} ({p.type})
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            <Field label="默认源语言">
              <Select
                value={settings.sourceLanguage || 'auto'}
                onValueChange={(v) => update('sourceLanguage', v)}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="auto">自动检测（auto）</SelectItem>
                  {supportedLanguage.map((l) => (
                    <SelectItem key={l.value} value={l.value}>
                      {l.name}（{l.value}）
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            <Field label="默认目标语言">
              <Select
                value={settings.targetLanguage || NO_TARGET}
                onValueChange={(v) => update('targetLanguage', v === NO_TARGET ? '' : v)}
              >
                <SelectTrigger>
                  <SelectValue placeholder="不指定（跟随任务）" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={NO_TARGET}>不指定（跟随任务，否则跳过翻译）</SelectItem>
                  {supportedLanguage.map((l) => (
                    <SelectItem key={l.value} value={l.value}>
                      {l.name}（{l.value}）
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            <Field label="Whisper Max Context">
              <Input
                type="number"
                min={-1}
                max={32768}
                value={settings.whisperMaxContext}
                onChange={(e) => {
                  // 空串保持 -1，避免被 Number("") === 0 吞掉用户的"清空"动作
                  const raw = e.target.value;
                  if (raw === '' || raw === '-') {
                    update('whisperMaxContext', -1);
                    return;
                  }
                  const n = Number(raw);
                  update(
                    'whisperMaxContext',
                    Number.isFinite(n) ? Math.floor(n) : -1,
                  );
                }}
              />
              <p className="text-xs text-muted-foreground mt-1">-1 表示用 whisper 默认值。</p>
            </Field>
          </div>
          <Field label="Whisper Prompt（可空）">
            <Textarea
              value={settings.whisperPrompt}
              onChange={(e) => update('whisperPrompt', e.target.value)}
              placeholder="可填入专有名词 / 人名 / 术语等，帮助 whisper 提升识别准确率"
              rows={2}
            />
          </Field>
        </CardContent>
      </Card>

      {/* 操作按钮 */}
      <div className="flex flex-wrap items-center gap-3">
        <Button onClick={save} variant="secondary">保存设置</Button>
        <Button
          onClick={testConn}
          variant="outline"
          disabled={busy !== 'idle' || !settings.baseUrl || !settings.token}
        >
          {busy === 'pinging' ? '测试中...' : '测试连接'}
        </Button>
        {!settings.enabled ? (
          <Button
            onClick={start}
            disabled={busy !== 'idle' || !settings.baseUrl || !settings.token}
            className="gap-2"
          >
            <Play className="size-4" />
            {busy === 'starting' ? '启动中...' : '启动 Worker'}
          </Button>
        ) : (
          <Button onClick={stop} variant="destructive" disabled={busy !== 'idle'} className="gap-2">
            <Pause className="size-4" />
            {busy === 'stopping' ? '停止中...' : '停止 Worker'}
          </Button>
        )}
        {pingResult && (
          <span className={`text-sm ${pingResult.startsWith('OK') ? 'text-green-600' : 'text-red-500'}`}>
            {pingResult}
          </span>
        )}
      </div>
    </div>
  );
}

function Field({ label, required, children }: { label: string; required?: boolean; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-1.5">
      <Label className="text-sm">
        {label}
        {required && <span className="text-red-500 ml-0.5">*</span>}
      </Label>
      {children}
    </div>
  );
}

function StatusBadge({
  label,
  ok,
  valueOk,
  valueFail,
}: {
  label: string;
  ok: boolean;
  valueOk: string;
  valueFail: string;
}) {
  return (
    <div className="flex flex-col gap-1">
      <span className="text-xs text-muted-foreground">{label}</span>
      <div className="flex items-center gap-1.5">
        {ok ? (
          <CheckCircle2 className="size-4 text-green-500" />
        ) : (
          <XCircle className="size-4 text-muted-foreground" />
        )}
        <span className="text-sm font-medium">{ok ? valueOk : valueFail}</span>
      </div>
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string | number }) {
  return (
    <div className="flex flex-col gap-1">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="text-base font-semibold">{value}</span>
    </div>
  );
}

function SlotIndicator({
  label,
  inflight,
  max,
  queue,
  hint,
}: {
  label: string;
  inflight: number;
  max: number;
  queue?: number;
  hint?: string;
}) {
  const ratio = max > 0 ? Math.min(1, inflight / max) : 0;
  const saturated = inflight >= max;
  return (
    <div
      className="flex flex-col gap-1 p-2 rounded-md border border-border bg-muted/20"
      title={hint}
    >
      <div className="flex items-center justify-between">
        <span className="text-xs text-muted-foreground">{label}</span>
        <span
          className={`font-mono text-xs ${
            saturated ? 'text-amber-600 dark:text-amber-400 font-semibold' : ''
          }`}
        >
          {inflight}/{max}
          {typeof queue === 'number' ? ` (q=${queue})` : ''}
        </span>
      </div>
      <div className="w-full h-1 bg-muted rounded-full overflow-hidden">
        <div
          className={`h-full transition-all ${saturated ? 'bg-amber-500' : 'bg-primary'}`}
          style={{ width: `${Math.round(ratio * 100)}%` }}
        />
      </div>
    </div>
  );
}
