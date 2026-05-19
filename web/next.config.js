/** @type {import('next').NextConfig} */
module.exports = {
  // 静态导出（go:embed 使用）。需要时可在 dev 模式跳过：MWS_DEV=1 npm run dev
  output: process.env.MWS_DEV ? undefined : 'export',
  trailingSlash: true,
  images: { unoptimized: true },
  distDir: 'out',
  reactStrictMode: true,
  // 把 /api/* 反向代理到 Go 后端，让 next dev 模式（3000）能直连 :8089
  async rewrites() {
    if (process.env.MWS_DEV) {
      return [
        { source: '/api/:path*', destination: 'http://localhost:8089/api/:path*' },
      ];
    }
    return [];
  },
};
