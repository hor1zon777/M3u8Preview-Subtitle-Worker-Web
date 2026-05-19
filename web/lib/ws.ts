// lib/ws.ts — WebSocket 客户端 + 自动重连。
//
// 替代原 Electron window.ipc.on('worker-status', ...)。
import { useEffect, useState, useRef } from 'react';
import type { WorkerRuntimeStatus } from './api';

export interface WSMessage {
  type: 'workerStatus' | 'modelProgress' | 'log';
  payload: any;
}

export interface ModelProgressEvent {
  model: string;
  percent: number;
  error?: string;
}

export interface LogEntry {
  timestamp: number;
  message: string;
  type: 'debug' | 'info' | 'warning' | 'error';
}

type Listeners = {
  onStatus?: (s: WorkerRuntimeStatus) => void;
  onModelProgress?: (e: ModelProgressEvent) => void;
  onLog?: (l: LogEntry) => void;
};

/**
 * useWS 订阅 /api/ws/status。Connection 在组件 mount 时建立，unmount 时关闭。
 * 自动重连：指数退避 1s → 30s 封顶。
 */
export function useWS(listeners: Listeners) {
  const listenersRef = useRef(listeners);
  listenersRef.current = listeners;
  const [connected, setConnected] = useState(false);
  useEffect(() => {
    let ws: WebSocket | null = null;
    let timer: ReturnType<typeof setTimeout> | null = null;
    let stopped = false;
    let backoff = 1000;

    const connect = () => {
      if (stopped) return;
      const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const tok = localStorage.getItem('mws_token');
      const qs = tok ? '?token=' + encodeURIComponent(tok) : '';
      const url = `${proto}//${window.location.host}/api/ws/status${qs}`;
      try {
        ws = new WebSocket(url);
      } catch (e) {
        scheduleReconnect();
        return;
      }
      ws.onopen = () => {
        backoff = 1000;
        setConnected(true);
      };
      ws.onclose = () => {
        setConnected(false);
        scheduleReconnect();
      };
      ws.onerror = () => {
        // 让 onclose 处理重连
      };
      ws.onmessage = (ev) => {
        try {
          const msg: WSMessage = JSON.parse(ev.data);
          const ls = listenersRef.current;
          if (msg.type === 'workerStatus') ls.onStatus?.(msg.payload);
          else if (msg.type === 'modelProgress') ls.onModelProgress?.(msg.payload);
          else if (msg.type === 'log') ls.onLog?.(msg.payload);
        } catch {}
      };
    };
    const scheduleReconnect = () => {
      if (stopped) return;
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => {
        backoff = Math.min(30_000, Math.floor(backoff * 1.7));
        connect();
      }, backoff);
    };
    connect();
    return () => {
      stopped = true;
      if (timer) clearTimeout(timer);
      if (ws) ws.close();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  return { connected };
}
