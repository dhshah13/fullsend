import path from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";

const repoRoot = path.dirname(fileURLToPath(import.meta.url));

/**
 * Vitest-only config for browser-side unit tests.
 *
 * The admin SPA under `web/admin/` was removed, so there is no Vite build here
 * any more — the documentation site is built by VitePress (`npm run docs:build`)
 * and the Worker is configured in `cloudflare_site/wrangler.toml`.
 */
export default defineConfig({
  root: repoRoot,
  test: {
    environment: "jsdom",
    include: ["docs/.vitepress/**/*.test.ts"],
    passWithNoTests: true,
  },
});
