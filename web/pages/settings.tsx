// pages/settings.tsx — 系统设置。
//
// 复刻原 settings.tsx 必要字段：
//   - 主题（next-themes 处理）
//   - 模型路径 / whisper-cli / ffmpeg 路径
//   - VAD 配置（threshold / minSpeech / minSilence / speechPad / samplesOverlap）
//   - CUDA 开关
//   - Bearer Token UI（可选）

import React, { useEffect, useState } from 'react';
import { Card, CardContent, CardHeader, CardTitle } from '../components/ui/card';
import { Button } from '../components/ui/button';
import { Input } from '../components/ui/input';
import { Label } from '../components/ui/label';
import { Switch } from '../components/ui/switch';
import { toast } from 'sonner';
import { Settings as SettingsIcon, KeyRound, Bug } from 'lucide-react';
import { useTheme } from 'next-themes';
import { api, SystemSettings, setAuthToken } from '../lib/api';

const DEFAULT: SystemSettings = {
  language: 'zh',
  useCuda: true,
  modelsPath: '',
  whisperCliPath: 'whisper-cli',
  ffmpegPath: 'ffmpeg',
  assetsPath: '',
  useVAD: true,
  vadThreshold: 0.5,
  vadMinSpeechDuration: 250,
  vadMinSilenceDuration: 100,
  vadMaxSpeechDuration: 0,
  vadSpeechPad: 30,
  vadSamplesOverlap: 0.1,
  debug: false,
  webToken: '',
};

export default function SettingsPage() {
  const { theme, setTheme } = useTheme();
  const [s, setS] = useState<SystemSettings>(DEFAULT);

  useEffect(() => {
    api.getSettings()
      .then((v) => setS({ ...DEFAULT, ...v }))
      .catch((e) => toast.error('加载失败：' + e.message));
  }, []);

  const update = <K extends keyof SystemSettings>(key: K, v: SystemSettings[K]) =>
    setS((p) => ({ ...p, [key]: v }));

  const save = async () => {
    try {
      const next = await api.putSettings(s);
      setS({ ...DEFAULT, ...next });
      // 如果用户改了 webToken，把新 token 同步到 localStorage，避免下次刷新
      // 之后 401。空 token = 关闭认证，本地 token 一并清掉。
      const newToken = (next.webToken || '').trim();
      setAuthToken(newToken || undefined);
      toast.success('已保存');
    } catch (e: any) {
      toast.error('保存失败：' + e.message);
    }
  };

  return (
    <div className="container mx-auto p-6 max-w-3xl space-y-6">
      <div className="flex items-center gap-3">
        <SettingsIcon className="size-7 text-primary" />
        <div>
          <h1 className="text-2xl font-semibold">系统设置</h1>
          <p className="text-sm text-muted-foreground">whisper-cli / ffmpeg / VAD / GPU / 主题等。</p>
        </div>
      </div>

      {/* 主题 */}
      <Card>
        <CardHeader>
          <CardTitle className="text-sm">外观</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="flex items-center gap-2">
            <Label className="w-24">主题</Label>
            <select
              value={theme || 'system'}
              onChange={(e) => setTheme(e.target.value)}
              className="bg-background border border-border rounded px-2 py-1 text-sm"
            >
              <option value="system">跟随系统</option>
              <option value="light">明亮</option>
              <option value="dark">暗黑</option>
            </select>
          </div>
        </CardContent>
      </Card>

      {/* Web UI 访问 Token */}
      <Card>
        <CardHeader>
          <CardTitle className="text-sm flex items-center gap-2">
            <KeyRound className="size-4" /> Web UI 访问 Token
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <p className="text-xs text-muted-foreground">
            设置后，所有访问 Web UI 都需要先在登录页输入此 Token。留空 = 不强制认证（任何人能访问，建议仅在本机环境使用）。
            保存后立即生效，所有打开此 UI 的浏览器都会在下次请求时被要求重新登录。
          </p>
          <div className="flex gap-2">
            <Input
              type="password"
              value={s.webToken}
              onChange={(e) => update('webToken', e.target.value)}
              placeholder="留空 = 不强制认证"
            />
          </div>
        </CardContent>
      </Card>

      {/* 路径配置 */}
      <Card>
        <CardHeader>
          <CardTitle className="text-sm">可执行文件 / 模型路径</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <div>
            <Label className="text-xs">whisper-cli 路径</Label>
            <Input
              value={s.whisperCliPath}
              onChange={(e) => update('whisperCliPath', e.target.value)}
              placeholder="whisper-cli（在 $PATH 中）或 /opt/whisper.cpp/build/bin/whisper-cli"
            />
          </div>
          <div>
            <Label className="text-xs">ffmpeg 路径</Label>
            <Input
              value={s.ffmpegPath}
              onChange={(e) => update('ffmpegPath', e.target.value)}
              placeholder="ffmpeg（在 $PATH 中）"
            />
          </div>
          <div>
            <Label className="text-xs">模型存储路径</Label>
            <Input
              value={s.modelsPath}
              onChange={(e) => update('modelsPath', e.target.value)}
            />
          </div>
          <div>
            <Label className="text-xs">资源路径（含 VAD silero 模型）</Label>
            <Input
              value={s.assetsPath}
              onChange={(e) => update('assetsPath', e.target.value)}
              placeholder="包含 ggml-silero-v6.2.0.bin 的目录"
            />
          </div>
        </CardContent>
      </Card>

      {/* GPU / CUDA */}
      <Card>
        <CardHeader>
          <CardTitle className="text-sm">GPU 加速</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="flex items-center gap-3">
            <Switch checked={s.useCuda} onCheckedChange={(v) => update('useCuda', v)} id="useCuda" />
            <Label htmlFor="useCuda" className="text-sm">
              使用 CUDA（whisper-cli 默认开启；关闭后传 -ng 切回 CPU）
            </Label>
          </div>
        </CardContent>
      </Card>

      {/* VAD */}
      <Card>
        <CardHeader>
          <CardTitle className="text-sm">VAD 配置（silero-v6.2.0）</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="flex items-center gap-3">
            <Switch checked={s.useVAD} onCheckedChange={(v) => update('useVAD', v)} id="useVAD" />
            <Label htmlFor="useVAD" className="text-sm">启用 VAD（过滤静音段，节省 ASR 时间）</Label>
          </div>
          {s.useVAD && (
            <div className="grid grid-cols-2 gap-3">
              <NumField label="threshold (0-1)" value={s.vadThreshold}
                onChange={(v) => update('vadThreshold', v)} step={0.1} min={0} max={1} />
              <NumField label="min speech ms" value={s.vadMinSpeechDuration}
                onChange={(v) => update('vadMinSpeechDuration', v)} step={1} min={0} />
              <NumField label="min silence ms" value={s.vadMinSilenceDuration}
                onChange={(v) => update('vadMinSilenceDuration', v)} step={1} min={0} />
              <NumField label="max speech ms（0 = 无上限）" value={s.vadMaxSpeechDuration}
                onChange={(v) => update('vadMaxSpeechDuration', v)} step={1} min={0} />
              <NumField label="speech pad ms" value={s.vadSpeechPad}
                onChange={(v) => update('vadSpeechPad', v)} step={1} min={0} />
              <NumField label="samples overlap (0-1)" value={s.vadSamplesOverlap}
                onChange={(v) => update('vadSamplesOverlap', v)} step={0.05} min={0} max={1} />
            </div>
          )}
        </CardContent>
      </Card>

      {/* 调试 */}
      <Card>
        <CardHeader>
          <CardTitle className="text-sm flex items-center gap-2">
            <Bug className="size-4" /> 调试模式
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="flex items-center gap-3">
            <Switch checked={s.debug} onCheckedChange={(v) => update('debug', v)} id="debug" />
            <Label htmlFor="debug" className="text-sm">
              输出每一步骤的详细日志（poll / claim / 下载 / ffmpeg / whisper / 翻译 / 上传 / 心跳）
            </Label>
          </div>
          <p className="text-xs text-muted-foreground">
            开启后会增加日志量，建议仅在排查问题时启用。生效是立即的，无需重启 worker。
            日志输出到 stderr 与 WebSocket 实时推送通道，前端 LogEntry.type 会包含 <code>debug</code>。
          </p>
        </CardContent>
      </Card>

      <div className="flex justify-end">
        <Button onClick={save}>保存设置</Button>
      </div>
    </div>
  );
}

function NumField({
  label,
  value,
  onChange,
  step,
  min,
  max,
}: {
  label: string;
  value: number;
  onChange: (v: number) => void;
  step?: number;
  min?: number;
  max?: number;
}) {
  return (
    <div>
      <Label className="text-xs">{label}</Label>
      <Input
        type="number"
        step={step}
        min={min}
        max={max}
        value={value}
        onChange={(e) => onChange(Number(e.target.value))}
      />
    </div>
  );
}
