import type { NextConfig } from "next";

/*
 * Next.js config for the Loamss console.
 *
 * v1 ships as a static export embedded into the runtime binary via
 * Go's embed.FS. `output: "export"` produces an `out/` directory of
 * HTML+JS+CSS that the runtime serves from /console/*.
 *
 * Image optimization is off because the static export has no
 * server-side runtime. The console doesn't need it — there are no
 * user-uploaded images, and any iconography we use is inline SVG.
 */
const nextConfig: NextConfig = {
  output: "export",
  images: {
    unoptimized: true,
  },
  trailingSlash: true,
  reactStrictMode: true,
};

export default nextConfig;
