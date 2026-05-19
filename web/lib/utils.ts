import { type ClassValue, clsx } from 'clsx';
import { twMerge } from 'tailwind-merge';

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

// 与原 renderer/lib/utils.ts supportedLanguage 对齐（worker 设置页用）。
export const supportedLanguage = [
  { value: 'zh', name: '中文' },
  { value: 'en', name: 'English' },
  { value: 'ja', name: '日本語' },
  { value: 'ko', name: '한국어' },
  { value: 'fr', name: 'Français' },
  { value: 'es', name: 'Español' },
  { value: 'de', name: 'Deutsch' },
  { value: 'ru', name: 'Русский' },
  { value: 'it', name: 'Italiano' },
  { value: 'pt', name: 'Português' },
  { value: 'th', name: 'ไทย' },
  { value: 'vi', name: 'Tiếng Việt' },
  { value: 'ar', name: 'العربية' },
];
