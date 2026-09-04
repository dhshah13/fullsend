/// <reference types="@cloudflare/workers-types" />

/**
 * Site Worker: serves the static documentation site via the `[assets]` binding.
 *
 * This Worker previously hosted an OAuth BFF for the admin SPA under `web/admin/`.
 * That SPA was removed, so the Worker is now a plain passthrough to `ASSETS`.
 * The public mint is a separate Worker (`internal/dispatch/cf/workersrc/`) and is
 * unaffected by anything here.
 */
export interface Env {
  ASSETS?: Fetcher;
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    if (env.ASSETS != null) {
      return env.ASSETS.fetch(request);
    }
    return new Response("Worker misconfigured: ASSETS binding missing", {
      status: 503,
    });
  },
};
