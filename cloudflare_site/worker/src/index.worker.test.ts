import { env } from "cloudflare:workers";
import { createExecutionContext, waitOnExecutionContext } from "cloudflare:test";
import { describe, expect, it } from "vitest";

import worker, { type Env } from "./index";

/** ASSETS stub that records what the Worker forwarded to it. */
function assetsStub(body = "asset") {
  const seen: string[] = [];
  const fetcher = {
    fetch: async (request: Request) => {
      seen.push(new URL(request.url).pathname);
      return new Response(body, { status: 200 });
    },
  } as unknown as Fetcher;
  return { fetcher, seen };
}

describe("site worker", () => {
  it("passes requests through to the ASSETS binding", async () => {
    const { fetcher, seen } = assetsStub("<!doctype html>");
    const ctx = createExecutionContext();

    const res = await worker.fetch(
      new Request("https://example.test/docs/"),
      { ASSETS: fetcher },
      ctx,
    );
    await waitOnExecutionContext(ctx);

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<!doctype html>");
    expect(seen).toEqual(["/docs/"]);
  });

  it("returns 503 when the ASSETS binding is missing", async () => {
    const ctx = createExecutionContext();

    const res = await worker.fetch(
      new Request("https://example.test/"),
      {} as Env,
      ctx,
    );
    await waitOnExecutionContext(ctx);

    expect(res.status).toBe(503);
    expect(await res.text()).toContain("ASSETS binding missing");
  });

  it("no longer handles the retired admin OAuth routes", async () => {
    // Before removal these three were intercepted by the Worker's OAuth BFF and
    // never reached ASSETS. Asserting the forwarded paths (not just the body)
    // is what proves no stray handler survives: a stub that always returns the
    // same body would pass a body-only assertion even if a handler still ran.
    const retired = [
      "/api/oauth/authorize",
      "/api/oauth/token",
      "/api/github/user",
    ];
    const { fetcher, seen } = assetsStub();

    for (const path of retired) {
      const ctx = createExecutionContext();
      const res = await worker.fetch(
        new Request(`https://example.test${path}`),
        { ASSETS: fetcher },
        ctx,
      );
      await waitOnExecutionContext(ctx);
      expect(res.status).toBe(200);
    }

    expect(seen).toEqual(retired);
  });
});

describe("wrangler bindings", () => {
  // Regression guard: the Worker only ever ran for /api/* while
  // `run_worker_first = ["/api/*"]` was set, so a missing ASSETS binding was
  // harmless. Without `binding = "ASSETS"` in wrangler.toml, every request that
  // *reaches* the Worker now 503s. That is not every request — exact asset
  // matches and unmatched navigations are served without invoking the Worker —
  // but it does include unmatched non-navigation requests (fetch/XHR, probes).
  it("provides env.ASSETS from wrangler.toml", () => {
    expect((env as Env).ASSETS).toBeDefined();
  });
});
