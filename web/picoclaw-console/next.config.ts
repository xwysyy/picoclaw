import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  output: "export",
  basePath: "/console",
  trailingSlash: true,
  images: {
    unoptimized: true,
  },
};

export default nextConfig;
