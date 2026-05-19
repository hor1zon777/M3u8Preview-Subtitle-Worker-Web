// components/LoginGate.tsx — 全局登录守卫。
//
// 流程：
//   1. mount 时调 /api/auth/status
//   2. tokenRequired=false → 直接渲染子节点
//   3. tokenRequired=true 且当前 authenticated → 渲染子节点
//   4. tokenRequired=true 且未认证 → 渲染登录表单
//
// 监听 window 'mws:unauthorized' 事件（api.ts 在 401 时触发）→ 重新拉起登录页。

import React, { useEffect, useState } from 'react';
import { Card, CardContent, CardHeader, CardTitle } from './ui/card';
import { Input } from './ui/input';
import { Button } from './ui/button';
import { Label } from './ui/label';
import { toast } from 'sonner';
import { KeyRound, Loader2 } from 'lucide-react';
import { api, setAuthToken } from '../lib/api';

type GateState = 'loading' | 'open' | 'locked';

export function LoginGate({ children }: { children: React.ReactNode }) {
  const [state, setState] = useState<GateState>('loading');
  const [token, setToken] = useState('');
  const [submitting, setSubmitting] = useState(false);

  const refresh = async () => {
    setState('loading');
    try {
      const st = await api.getAuthStatus();
      if (!st.tokenRequired || st.authenticated) {
        setState('open');
      } else {
        setState('locked');
      }
    } catch (e: any) {
      // 极端：后端挂了；保持 locked 避免误放行
      setState('locked');
    }
  };

  useEffect(() => {
    void refresh();
    const onUnauthorized = () => setState('locked');
    if (typeof window !== 'undefined') {
      window.addEventListener('mws:unauthorized', onUnauthorized);
    }
    return () => {
      if (typeof window !== 'undefined') {
        window.removeEventListener('mws:unauthorized', onUnauthorized);
      }
    };
  }, []);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const t = token.trim();
    if (!t) {
      toast.error('请输入 Token');
      return;
    }
    setSubmitting(true);
    try {
      // 临时把输入的 token 写进 storage，下面的 login 调用会带上 Bearer
      setAuthToken(t);
      const r = await api.login(t);
      if (r.authenticated) {
        toast.success('已登录');
        setState('open');
      } else {
        setAuthToken(undefined);
        toast.error('Token 不正确');
      }
    } catch (e: any) {
      setAuthToken(undefined);
      toast.error(e.message || '登录失败');
    } finally {
      setSubmitting(false);
    }
  };

  if (state === 'loading') {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <Loader2 className="size-6 animate-spin text-muted-foreground" />
      </div>
    );
  }

  if (state === 'locked') {
    return (
      <div className="min-h-screen flex items-center justify-center bg-background px-4">
        <Card className="w-full max-w-md">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <KeyRound className="size-5 text-primary" />
              m3u8 Subtitle Worker 登录
            </CardTitle>
          </CardHeader>
          <CardContent>
            <form onSubmit={submit} className="space-y-4">
              <div>
                <Label className="text-xs">访问 Token</Label>
                <Input
                  type="password"
                  value={token}
                  onChange={(e) => setToken(e.target.value)}
                  placeholder="请输入设置页配置的 Token"
                  autoFocus
                />
              </div>
              <Button type="submit" className="w-full" disabled={submitting}>
                {submitting ? <Loader2 className="size-4 animate-spin mr-2" /> : null}
                登录
              </Button>
              <p className="text-xs text-muted-foreground">
                Token 在系统设置页配置。首次部署时 Token 为空，可以直接进入并到设置页设置一个。
              </p>
            </form>
          </CardContent>
        </Card>
      </div>
    );
  }

  return <>{children}</>;
}
