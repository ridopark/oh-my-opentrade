import type { NextConfig } from "next";

const backendUrl = process.env.BACKEND_URL || "http://omo-core:8080";

const nextConfig: NextConfig = {
  output: "standalone",
  // Reduce dev server memory footprint
  productionBrowserSourceMaps: false,
  experimental: {
    // Reduce webpack persistent cache memory usage
    webpackMemoryOptimizations: true,
  },
  async rewrites() {
    return [
      {
        source: "/api/performance/:path*",
        destination: `${backendUrl}/performance/:path*`,
      },
      {
        source: "/api/backtest/:path*",
        destination: `${backendUrl}/backtest/:path*`,
      },
      {
        source: "/api/strategies/config/:path*",
        destination: `${backendUrl}/strategies/config/:path*`,
      },
      {
        source: "/api/strategies/sweep/:path*",
        destination: `${backendUrl}/strategies/sweep/:path*`,
      },
      {
        source: "/api/:path*",
        destination: `${backendUrl}/api/:path*`,
      },
    ];
  },
};

export default nextConfig;
