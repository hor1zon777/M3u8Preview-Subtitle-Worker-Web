import type { AppProps } from 'next/app';
import Head from 'next/head';
import Link from 'next/link';
import { useRouter } from 'next/router';
import { Toaster } from 'sonner';
import { ThemeProvider } from 'next-themes';
import { Server, Cpu, Languages, Settings, Activity } from 'lucide-react';
import '../styles/globals.css';

function NavItem({ href, label, icon }: { href: string; label: string; icon: React.ReactNode }) {
  const router = useRouter();
  const active = router.pathname === href;
  return (
    <Link
      href={href}
      className={`flex items-center gap-2 px-3 py-2 rounded-md text-sm transition-colors ${
        active
          ? 'bg-primary text-primary-foreground'
          : 'text-muted-foreground hover:bg-muted hover:text-foreground'
      }`}
    >
      {icon}
      <span>{label}</span>
    </Link>
  );
}

export default function App({ Component, pageProps }: AppProps) {
  return (
    <ThemeProvider attribute="class" defaultTheme="system" enableSystem>
      <Head>
        <title>m3u8 Subtitle Worker</title>
        <meta name="viewport" content="width=device-width, initial-scale=1" />
      </Head>
      <div className="min-h-screen flex flex-col">
        <header className="border-b border-border bg-card">
          <div className="container max-w-6xl mx-auto px-4 h-14 flex items-center justify-between">
            <Link href="/" className="flex items-center gap-2 font-semibold">
              <Activity className="size-5 text-primary" />
              <span>m3u8 Subtitle Worker</span>
            </Link>
            <nav className="flex items-center gap-1">
              <NavItem href="/" label="Worker" icon={<Server className="size-4" />} />
              <NavItem href="/models" label="模型" icon={<Cpu className="size-4" />} />
              <NavItem href="/providers" label="翻译服务" icon={<Languages className="size-4" />} />
              <NavItem href="/settings" label="设置" icon={<Settings className="size-4" />} />
            </nav>
          </div>
        </header>
        <main className="flex-1">
          <Component {...pageProps} />
        </main>
        <Toaster richColors position="top-right" />
      </div>
    </ThemeProvider>
  );
}
