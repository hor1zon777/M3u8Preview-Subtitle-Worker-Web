// pages/providers.tsx — 翻译服务管理。
//
// 复刻原 translateControl.tsx 左右两栏布局：
//   - 左栏：provider 列表 + 添加按钮
//   - 右栏：当前选中 provider 的字段编辑表单 + 测试按钮

import React, { useEffect, useMemo, useState } from 'react';
import { Card, CardContent, CardHeader, CardTitle } from '../components/ui/card';
import { Button } from '../components/ui/button';
import { Input } from '../components/ui/input';
import { Label } from '../components/ui/label';
import { Switch } from '../components/ui/switch';
import { Textarea } from '../components/ui/textarea';
import { Badge } from '../components/ui/badge';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '../components/ui/select';
import { toast } from 'sonner';
import { Languages, Plus, Trash2, FlaskConical } from 'lucide-react';
import { api, Provider } from '../lib/api';

// 内置 provider 类型；用于「新增」时选择 type。
const PROVIDER_TYPES = [
  { type: 'openai', name: 'OpenAI 兼容', isAi: true, fields: ['apiUrl', 'apiKey', 'modelName'] },
  { type: 'deepseek', name: 'DeepSeek', isAi: true, fields: ['apiUrl', 'apiKey', 'modelName'] },
  { type: 'qwen', name: 'Qwen / 通义千问', isAi: true, fields: ['apiUrl', 'apiKey', 'modelName'] },
  { type: 'siliconflow', name: 'SiliconFlow', isAi: true, fields: ['apiUrl', 'apiKey', 'modelName'] },
  { type: 'Gemini', name: 'Gemini (OpenAI 协议)', isAi: true, fields: ['apiUrl', 'apiKey', 'modelName'] },
  { type: 'azureopenai', name: 'Azure OpenAI', isAi: true, fields: ['apiUrl', 'apiKey', 'modelName'] },
  { type: 'ollama', name: 'Ollama 本地 LLM', isAi: true, fields: ['apiUrl', 'modelName'] },
  { type: 'doubao', name: '豆包翻译', isAi: false, fields: ['apiUrl', 'apiKey', 'modelName'] },
  { type: 'volc', name: '火山引擎翻译 API', isAi: false, fields: ['apiKey', 'apiSecret'] },
  { type: 'baidu', name: '百度通用翻译', isAi: false, fields: ['apiKey', 'apiSecret'] },
  { type: 'aliyun', name: '阿里云翻译', isAi: false, fields: ['apiKey', 'apiSecret', 'endpoint'] },
  { type: 'google', name: 'Google Translate', isAi: false, fields: ['apiKey', 'apiUrl'] },
  { type: 'azure', name: 'Azure Cognitive Translator', isAi: false, fields: ['apiKey', 'apiSecret', 'apiUrl'] },
  { type: 'deeplx', name: 'DeepLX 自部署', isAi: false, fields: ['apiUrl'] },
];

const FIELD_LABELS: Record<string, string> = {
  apiUrl: 'API URL',
  apiKey: 'API Key',
  apiSecret: 'API Secret',
  modelName: '模型名',
  endpoint: 'Endpoint',
};

export default function ProvidersPage() {
  const [providers, setProviders] = useState<Provider[]>([]);
  const [selected, setSelected] = useState<string>('');
  const [showAddDialog, setShowAddDialog] = useState(false);
  const [newName, setNewName] = useState('');
  const [newType, setNewType] = useState<string>('openai');
  const [testingId, setTestingId] = useState<string>('');
  const [testResult, setTestResult] = useState<string>('');

  const refresh = async () => {
    try {
      const list = await api.getProviders();
      const safe = Array.isArray(list) ? list : [];
      setProviders(safe);
      if (safe.length > 0 && !selected) setSelected(safe[0].id);
    } catch (e: any) {
      toast.error('加载失败：' + e.message);
    }
  };

  useEffect(() => { void refresh(); }, []);

  const current = useMemo(
    () => providers.find((p) => p.id === selected) || null,
    [providers, selected],
  );
  const currentType = useMemo(
    () => PROVIDER_TYPES.find((t) => t.type === (current?.type || '')),
    [current],
  );

  const save = async () => {
    try {
      const next = await api.putProviders(providers);
      setProviders(next);
      toast.success('已保存');
    } catch (e: any) {
      toast.error('保存失败：' + e.message);
    }
  };

  const add = async () => {
    if (!newName.trim()) {
      toast.error('请输入名称');
      return;
    }
    const t = PROVIDER_TYPES.find((p) => p.type === newType)!;
    const id = `${newType}_${Date.now()}`;
    const p: Provider = {
      id,
      name: newName.trim(),
      type: newType,
      isAi: t.isAi,
      batchSize: t.isAi ? 10 : 1,
      structuredOutput: t.isAi ? 'json_schema' : undefined,
    };
    const next = [...providers, p];
    setProviders(next);
    setSelected(id);
    setShowAddDialog(false);
    setNewName('');
    try {
      await api.putProviders(next);
      toast.success('已新增 ' + p.name);
    } catch (e: any) {
      toast.error('保存失败：' + e.message);
    }
  };

  const del = async (id: string) => {
    if (!confirm('确认删除该 provider？')) return;
    const next = providers.filter((p) => p.id !== id);
    setProviders(next);
    if (selected === id) setSelected(next[0]?.id || '');
    try {
      await api.putProviders(next);
      toast.success('已删除');
    } catch (e: any) {
      toast.error('保存失败：' + e.message);
    }
  };

  const update = (patch: Partial<Provider>) => {
    if (!current) return;
    const next = providers.map((p) => (p.id === current.id ? { ...p, ...patch } : p));
    setProviders(next);
  };

  const test = async () => {
    if (!current) return;
    setTestingId(current.id);
    setTestResult('');
    try {
      const r = await api.testProvider(current.id, { sourceLanguage: 'en', targetLanguage: 'zh' });
      setTestResult(`OK: ${r.translation}`);
      toast.success('测试通过');
    } catch (e: any) {
      setTestResult('失败：' + e.message);
      toast.error('测试失败：' + e.message);
    }
    setTestingId('');
  };

  return (
    <div className="container mx-auto p-6 max-w-6xl">
      <div className="flex items-center justify-between mb-6">
        <div className="flex items-center gap-3">
          <Languages className="size-7 text-primary" />
          <div>
            <h1 className="text-2xl font-semibold">翻译服务管理</h1>
            <p className="text-sm text-muted-foreground">配置 OpenAI / 火山 / 百度 / 阿里云 / Google 等翻译 provider。</p>
          </div>
        </div>
        <Button onClick={() => setShowAddDialog(true)} className="gap-2">
          <Plus className="size-4" /> 新增 Provider
        </Button>
      </div>

      <div className="grid grid-cols-12 gap-4">
        {/* 左：列表 */}
        <Card className="col-span-4">
          <CardHeader>
            <CardTitle className="text-sm">Provider 列表</CardTitle>
          </CardHeader>
          <CardContent className="space-y-1">
            {providers.length === 0 ? (
              <div className="text-sm text-muted-foreground py-4 text-center">还没有 provider，点上方 +</div>
            ) : (
              providers.map((p) => (
                <div
                  key={p.id}
                  className={`group flex items-center gap-2 p-2 rounded cursor-pointer ${
                    selected === p.id ? 'bg-muted' : 'hover:bg-muted/60'
                  }`}
                  onClick={() => setSelected(p.id)}
                >
                  <div className="flex-1 min-w-0">
                    <div className="text-sm font-medium truncate">{p.name}</div>
                    <div className="text-xs text-muted-foreground">
                      {p.type}
                      {p.isAi && ' · AI'}
                    </div>
                  </div>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="opacity-0 group-hover:opacity-100"
                    onClick={(e) => {
                      e.stopPropagation();
                      void del(p.id);
                    }}
                  >
                    <Trash2 className="size-3.5 text-red-500" />
                  </Button>
                </div>
              ))
            )}
          </CardContent>
        </Card>

        {/* 右：编辑 */}
        <Card className="col-span-8">
          <CardHeader className="flex flex-row items-center justify-between">
            <CardTitle className="text-sm">
              {current ? current.name : '选择一个 provider'}
              {current && (
                <Badge variant="secondary" className="ml-2 text-xs">
                  {current.type}
                </Badge>
              )}
            </CardTitle>
            {current && (
              <Button
                onClick={test}
                disabled={testingId === current.id}
                variant="outline"
                size="sm"
                className="gap-1"
              >
                <FlaskConical className="size-3.5" />
                {testingId === current.id ? '测试中...' : '测试翻译'}
              </Button>
            )}
          </CardHeader>
          <CardContent className="space-y-4">
            {!current ? (
              <div className="text-sm text-muted-foreground py-8 text-center">从左侧选一个 provider</div>
            ) : (
              <>
                <div className="grid grid-cols-2 gap-4">
                  <div>
                    <Label className="text-xs">名称</Label>
                    <Input value={current.name} onChange={(e) => update({ name: e.target.value })} />
                  </div>
                  <div>
                    <Label className="text-xs">类型（不可改）</Label>
                    <Input value={current.type} disabled />
                  </div>
                  {(currentType?.fields || []).map((f) => (
                    <div key={f} className={f === 'apiUrl' ? 'col-span-2' : ''}>
                      <Label className="text-xs">{FIELD_LABELS[f] || f}</Label>
                      <Input
                        type={f === 'apiKey' || f === 'apiSecret' ? 'password' : 'text'}
                        value={(current as any)[f] || ''}
                        onChange={(e) => update({ [f]: e.target.value } as any)}
                      />
                    </div>
                  ))}
                  {current.isAi && (
                    <>
                      <div>
                        <Label className="text-xs">Batch Size</Label>
                        <Input
                          type="number"
                          min={1}
                          max={100}
                          value={current.batchSize || 10}
                          onChange={(e) => update({ batchSize: Number(e.target.value) || 10 })}
                        />
                      </div>
                      <div>
                        <Label className="text-xs">并发</Label>
                        <Input
                          type="number"
                          min={1}
                          max={32}
                          value={current.concurrency || 1}
                          onChange={(e) => update({ concurrency: Number(e.target.value) || 1 })}
                        />
                      </div>
                      <div>
                        <Label className="text-xs">请求间隔（秒）</Label>
                        <Input
                          type="number"
                          min={0}
                          step={0.1}
                          value={current.requestInterval || 0}
                          onChange={(e) =>
                            update({ requestInterval: Number(e.target.value) || 0 })
                          }
                        />
                      </div>
                      <div>
                        <Label className="text-xs">Structured Output</Label>
                        <Select
                          value={current.structuredOutput || 'json_schema'}
                          onValueChange={(v) => update({ structuredOutput: v })}
                        >
                          <SelectTrigger>
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            <SelectItem value="json_schema">json_schema</SelectItem>
                            <SelectItem value="json_object">json_object</SelectItem>
                            <SelectItem value="disabled">disabled</SelectItem>
                          </SelectContent>
                        </Select>
                      </div>
                    </>
                  )}
                </div>
                {current.isAi && (
                  <div>
                    <Label className="text-xs">System Prompt（留空使用默认）</Label>
                    <Textarea
                      rows={4}
                      value={current.systemPrompt || ''}
                      onChange={(e) => update({ systemPrompt: e.target.value })}
                    />
                  </div>
                )}
                {testResult && (
                  <div className={`text-xs ${testResult.startsWith('OK') ? 'text-green-600' : 'text-red-500'}`}>
                    {testResult}
                  </div>
                )}
                <div className="flex justify-end">
                  <Button onClick={save}>保存</Button>
                </div>
              </>
            )}
          </CardContent>
        </Card>
      </div>

      {/* 新增对话框（简易内嵌，不引 Dialog 组件） */}
      {showAddDialog && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
          <Card className="w-96">
            <CardHeader>
              <CardTitle>新增 Provider</CardTitle>
            </CardHeader>
            <CardContent className="space-y-3">
              <div>
                <Label>名称</Label>
                <Input
                  autoFocus
                  value={newName}
                  onChange={(e) => setNewName(e.target.value)}
                  placeholder="例如 我的 OpenAI"
                />
              </div>
              <div>
                <Label>类型</Label>
                <Select value={newType} onValueChange={setNewType}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent className="max-h-72">
                    {PROVIDER_TYPES.map((t) => (
                      <SelectItem key={t.type} value={t.type}>
                        {t.name}（{t.type}）{t.isAi && ' · AI'}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="flex justify-end gap-2 pt-2">
                <Button variant="outline" onClick={() => setShowAddDialog(false)}>取消</Button>
                <Button onClick={add}>添加</Button>
              </div>
            </CardContent>
          </Card>
        </div>
      )}
    </div>
  );
}
