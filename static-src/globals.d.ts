// Build-time constants esbuild's Define injects into the page bundle (cmd/bundle).

/**
 * The content-hashed SSE worker path (`/chunks/sse-worker-<hash>.js`), from the worker build that runs first. Empty
 * means no worker shipped, and the page runs the per-tab stream.
 */
declare const __SSE_WORKER_URL__: string;
