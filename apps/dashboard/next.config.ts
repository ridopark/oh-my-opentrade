import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  output: "standalone",
  async rewrites() {
    return [
      {
        source: "/api/performance/:path*",
        destination: "http://localhost:8080/performance/:path*",
      },
      {
        source: "/api/backtest/:path*",
        destination: "http://localhost:8080/backtest/:path*",
      },
      {
        source: "/api/:path*",
        destination: "http://localhost:8080/api/:path*",
      },
    ];
  },
};

export default nextConfig;
