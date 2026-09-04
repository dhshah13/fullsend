import path from "node:path";
import { fileURLToPath } from "node:url";
import { cloudflareTest } from "@cloudflare/vitest-pool-workers";
import { defineConfig } from "vitest/config";

const workerRoot = path.dirname(fileURLToPath(import.meta.url));

export default defineConfig({
  root: workerRoot,
  plugins: [
    cloudflareTest({
      // The Worker is a static-asset passthrough: the only binding it needs is
      // ASSETS, which comes from wrangler.toml. No vars or secrets.
      wrangler: {
        configPath: path.join(workerRoot, "..", "wrangler.toml"),
      },
    }),
  ],
  test: {
    include: ["src/**/*.test.ts"],
  },
});
