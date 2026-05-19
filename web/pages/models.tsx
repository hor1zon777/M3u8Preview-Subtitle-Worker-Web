// pages/models.tsx — 模型管理。
//
// 复刻原 modelsControl.tsx 主要交互：
//   - 列出 catalog 分类 + 每分类模型卡片
//   - 已安装显示 ✓；可删除
//   - 未安装显示下载按钮 + 进度（来自 WS modelProgress）
//   - 顶部选择下载源（huggingface / hf-mirror）

import React, { useEffect, useState } from 'react';
import { Card, CardContent, CardHeader, CardTitle } from '../components/ui/card';
import { Button } from '../components/ui/button';
import { Badge } from '../components/ui/badge';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../components/ui/select';
import { toast } from 'sonner';
import { Download, Trash2, Cpu, CheckCircle2 } from 'lucide-react';
import { api, ModelCategory, ModelEntry } from '../lib/api';
import { useWS } from '../lib/ws';

export default function ModelsPage() {
  const [catalog, setCatalog] = useState<ModelCategory[]>([]);
  const [installed, setInstalled] = useState<string[]>([]);
  const [modelsPath, setModelsPath] = useState<string>('');
  const [source, setSource] = useState<'hf-mirror' | 'huggingface' | 'hf-cdn'>('hf-mirror');
  const [progress, setProgress] = useState<Record<string, number>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [engine, setEngine] = useState<string>('unknown');

  useWS({
    onModelProgress: (e) => {
      setProgress((p) => ({ ...p, [e.model]: e.percent }));
      if (e.error) {
        setErrors((p) => ({ ...p, [e.model]: e.error || '' }));
        toast.error(`下载 ${e.model} 失败: ${e.error}`);
      } else if (e.percent >= 100) {
        toast.success(`模型 ${e.model} 下载完成`);
        void refresh();
      }
    },
  });

  const refresh = async () => {
    try {
      const [r, sys] = await Promise.all([api.listModels(), api.getSystemInfo()]);
      setCatalog(Array.isArray(r.catalog) ? r.catalog : []);
      setInstalled(Array.isArray(r.installed) ? r.installed : []);
      setModelsPath(r.modelsPath);
      setEngine(sys?.whisperEngine || 'unknown');
      // 下载完成后清除进度，让按钮切回"已安装 ✓"
      setProgress({});
      setErrors({});
    } catch (e: any) {
      toast.error('加载模型列表失败：' + e.message);
    }
  };

  useEffect(() => { void refresh(); }, []);

  const download = async (m: ModelEntry) => {
    try {
      setProgress((p) => ({ ...p, [m.name]: 0 }));
      setErrors((p) => ({ ...p, [m.name]: '' }));
      await api.downloadModel(m.name, source);
      toast.info(`已开始下载 ${m.name}`);
    } catch (e: any) {
      toast.error('下载失败：' + e.message);
    }
  };

  const del = async (name: string) => {
    if (!confirm(`确认删除模型 ${name}？`)) return;
    try {
      await api.deleteModel(name);
      toast.success(`已删除 ${name}`);
      void refresh();
    } catch (e: any) {
      toast.error('删除失败：' + e.message);
    }
  };

  return (
    <div className="container mx-auto p-6 max-w-5xl space-y-6">
      <div className="flex items-center justify-between gap-4">
        <div className="flex items-center gap-3">
          <Cpu className="size-7 text-primary" />
          <div>
            <h1 className="text-2xl font-semibold">Whisper 模型管理</h1>
            <p className="text-sm text-muted-foreground">
              引擎：<Badge variant={engine === 'faster-whisper' ? 'default' : 'secondary'} className="text-xs">
                {engine === 'faster-whisper' ? 'faster-whisper' : engine === 'whisper-cli' ? 'whisper.cpp' : engine}
              </Badge>
              <span className="mx-1">·</span>
              存储：<code className="text-xs">
                {engine === 'faster-whisper' ? '~/.cache/huggingface/hub/' : (modelsPath || '未配置')}
              </code>
            </p>
          </div>
        </div>
        <div className="flex items-center gap-2">
            <span className="text-xs text-muted-foreground">下载源</span>
          <Select value={source} onValueChange={(v) => setSource(v as any)}>
            <SelectTrigger className="w-52">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="hf-mirror">hf-mirror.com（国内）</SelectItem>
              <SelectItem value="hf-cdn">hf-cdn.sufy.com（国内 CDN）</SelectItem>
              <SelectItem value="huggingface">huggingface.co</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>

      {catalog.map((cat) => (
        <Card key={cat.name}>
          <CardHeader>
            <CardTitle className="capitalize flex items-center gap-2">
              <span>{cat.name}</span>
              <Badge variant="outline" className="text-xs">{cat.description}</Badge>
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-2">
            {cat.models.map((m) => {
              const isInstalled = installed.includes(m.name);
              const pct = progress[m.name];
              const dlError = errors[m.name];
              const downloading = pct !== undefined && pct < 100;
              return (
                <div
                  key={m.name}
                  className="flex items-center gap-3 p-3 rounded border border-border bg-muted/20"
                >
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2 flex-wrap">
                      <span className="font-mono text-sm">{m.name}</span>
                      <Badge variant="secondary" className="text-xs">{m.size}</Badge>
                      {m.quantized && <Badge variant="outline" className="text-xs">quantized</Badge>}
                      {m.englishOnly && <Badge variant="outline" className="text-xs">English only</Badge>}
                    </div>
                    <div className="text-xs text-muted-foreground mt-1">
                      speed={'★'.repeat(m.speed)} · quality={'★'.repeat(m.quality)} · 推荐 RAM ≥ {m.minRamGb}GB
                    </div>
                    {downloading && (
                      <div className="mt-2 flex items-center gap-2">
                        <div className="flex-1 h-1.5 bg-muted rounded-full overflow-hidden">
                          <div className="h-full bg-primary" style={{ width: `${pct}%` }} />
                        </div>
                        <span className="text-xs font-mono w-10 text-right">{pct}%</span>
                      </div>
                    )}
                    {dlError && (
                      <div className="text-xs text-red-500 mt-1">{dlError}</div>
                    )}
                  </div>
                  <div className="shrink-0">
                    {isInstalled ? (
                      <div className="flex items-center gap-2">
                        <CheckCircle2 className="size-4 text-green-500" />
                        <Button variant="destructive" size="sm" onClick={() => del(m.name)}>
                          <Trash2 className="size-3 mr-1" /> 删除
                        </Button>
                      </div>
                    ) : (
                      <Button size="sm" onClick={() => download(m)} disabled={downloading}>
                        <Download className="size-3 mr-1" />
                        {downloading ? '下载中…' : '下载'}
                      </Button>
                    )}
                  </div>
                </div>
              );
            })}
          </CardContent>
        </Card>
      ))}
    </div>
  );
}
