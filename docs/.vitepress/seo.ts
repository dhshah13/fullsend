import type { HeadConfig } from "vitepress";

// SEO metadata helpers for the VitePress docs site.
//
// The docs are served under a base path — https://fullsend.sh/docs/ — so every
// absolute URL we emit (canonical, og:url, sitemap entries) must include that
// `/docs/` segment. VitePress 1.6.x builds sitemap entries as base-less relative
// paths and resolves them against `sitemap.hostname`, so the hostname itself must
// carry the base and a trailing slash. The same rule applies to URLs we resolve
// here, which is why DOCS_URL_BASE ends in a slash.

/** Public origin of the site. */
export const SITE_ORIGIN = "https://fullsend.sh";

/** Base path the docs are served under (matches `base` in config.ts). */
export const DOCS_BASE = "/docs/";

/**
 * Absolute base for canonical/OG URLs and the sitemap `hostname`.
 * The trailing slash is required for correct `new URL(relative, base)` resolution.
 */
export const DOCS_URL_BASE = `${SITE_ORIGIN}${DOCS_BASE}`;

/**
 * Default social-share image (absolute URL). This is the square brand logo; a
 * dedicated 1200×630 asset would render better on cards and is a good follow-up.
 */
export const OG_IMAGE = `${DOCS_URL_BASE}img/logo.png`;

/**
 * Convert a VitePress page path (the post-rewrite source path, e.g. "index.md",
 * "agents/index.md", "guides/user/jira-integration.md") into the site-relative
 * output path VitePress actually serves, mirroring its sitemap/link logic:
 *   - `index.md` / `README.md` → the containing directory ("" for the root)
 *   - other `*.md` → `*.html` (or the bare path when `cleanUrls` is enabled)
 *
 * The result is a base-less relative path, suitable for resolving against
 * {@link DOCS_URL_BASE}.
 */
export function pageOutputPath(page: string, cleanUrls = false): string {
  const asDir = page.replace(/(^|\/)(index|README)\.md$/, "$1");
  if (asDir !== page) return asDir;
  return page.replace(/\.md$/, cleanUrls ? "" : ".html");
}

/** Absolute canonical URL for a page, including the `/docs/` base. */
export function canonicalUrl(page: string, cleanUrls = false): string {
  return new URL(pageOutputPath(page, cleanUrls), DOCS_URL_BASE).href;
}

export interface PageSeoInput {
  /** Post-rewrite source path of the page (VitePress `TransformContext.page`). */
  page: string;
  /** Resolved page title (VitePress `TransformContext.title`). */
  title: string;
  /** Resolved page description (VitePress `TransformContext.description`). */
  description: string;
  cleanUrls?: boolean;
}

/**
 * Per-page SEO head tags: a canonical link plus page-specific Open Graph tags.
 * Twitter cards fall back to the `og:*` values, so no `twitter:title` /
 * `twitter:description` duplication is needed.
 */
export function pageSeoHead({
  page,
  title,
  description,
  cleanUrls = false,
}: PageSeoInput): HeadConfig[] {
  const url = canonicalUrl(page, cleanUrls);
  return [
    ["link", { rel: "canonical", href: url }],
    ["meta", { property: "og:url", content: url }],
    ["meta", { property: "og:title", content: title }],
    ["meta", { property: "og:description", content: description }],
  ];
}

/**
 * Whether a page should advertise a self-canonical / OG URL to crawlers.
 * The 404 page is served for unknown paths and must not declare itself
 * canonical, or crawlers would treat the not-found page as indexable.
 */
export function isIndexablePage(page: string): boolean {
  return page !== "404.md";
}

/** Site-wide SEO head tags that are identical on every page. */
export const globalSeoHead: HeadConfig[] = [
  ["meta", { property: "og:type", content: "website" }],
  ["meta", { property: "og:site_name", content: "Fullsend" }],
  ["meta", { property: "og:image", content: OG_IMAGE }],
  ["meta", { name: "twitter:card", content: "summary_large_image" }],
  ["meta", { name: "twitter:image", content: OG_IMAGE }],
];
