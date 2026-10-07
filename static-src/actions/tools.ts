// Tools engine actions (Settings -> Tools). Each mutation enqueues a job and answers 202;
// progress streams over tool_job_changed / tool_job_output, so no long requests or retry hazards.

import {
  apiAction,
  defineAction,
  ActionError,
  classifyFetchError,
  hasErrorString,
  withTimeout,
  retryNetwork,
  RETRY_STANDARD,
  IDEMPOTENCY_HEADER,
} from "./index.js";
import type { ActionContext } from "./index.js";
import type {
  CatalogInfo,
  Inventory,
  JobResponse,
  JobsResponse,
  SearchResponse,
} from "../types.js";

import { MCP_API } from "./mcp.js";
import { RATE_LIMITED } from "../tool-rate-limit.js";

/** POST /api/tools fields. Only the name is required; the server fills the rest from the catalog. */
export interface CreateToolRequest {
  name: string;
  source?: string;
  version?: string;
  pin?: boolean;
  /** Add as a disabled template: recorded, not installed, no job. */
  disabled?: boolean;
  requires?: string[];
  description?: string;
  origin?: string;
  install?: string;
  uninstall?: string;
  probe?: string;
}

/** The code an /api/tools answer carries while the tools engine is down; a retry cannot change it. */
export const TOOLS_UNAVAILABLE = "tools_unavailable";

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args
export const loadTools = apiAction<void, Inventory>({
  name: "tools.load",
  retryable: (err) => err.code !== TOOLS_UNAVAILABLE && retryNetwork(err),
  retry: RETRY_STANDARD,
  dedupe: true,
  request: () => ({ method: "GET", path: "/api/tools" }),
  error: false,
});

/** The caller reports a failure itself: a GitHub rate limit gets its own notice.
 *  That 503 keeps its body as the error's cause, because the body says whether
 *  the refused request carried a token. */
export const createTool = apiAction<CreateToolRequest, JobResponse>({
  name: "tools.create",
  scope: "tools",
  idempotencyKey: true,
  request: (body) => ({ method: "POST", path: "/api/tools", body }),
  decodeError: (info) =>
    info.status === 503 && info.code === RATE_LIMITED
      ? {
          kind: "error",
          error: new ActionError(info.message, {
            status: info.status,
            code: info.code,
            cause: info.body,
          }),
        }
      : undefined,
  error: false,
});

export const installTool = apiAction<{ name: string }, JobResponse>({
  name: "tools.install",
  scope: "tools",
  idempotencyKey: true,
  request: ({ name }) => ({
    method: "POST",
    path: `/api/tools/${encodeURIComponent(name)}/install`,
    body: {},
  }),
  error: "Could not start install",
});

export const updateTools = apiAction<{ names?: string[] } | undefined, JobResponse>({
  name: "tools.update",
  scope: "tools",
  idempotencyKey: true,
  request: (body) => ({ method: "POST", path: "/api/tools/update", body: body ?? {} }),
  error: "Could not start update",
});

/** PATCH result: 202 + job (null when no work was needed); a 409 has_dependents envelope resolves
 *  as a success payload for the force-confirm flow. */
export interface PatchToolResult {
  job?: { id: string } | null;
  code?: string;
  dependents?: string[];
  error?: string;
}

/** PATCH fields. `disabled` false→true uninstalls (may 409 with dependents unless force),
 *  true→false installs; a version change reinstalls. */
export const patchTool = apiAction<
  {
    name: string;
    version?: string;
    pin?: boolean;
    disabled?: boolean;
    force?: boolean;
    description?: string;
    install?: string;
    uninstall?: string;
  },
  PatchToolResult
>({
  name: "tools.patch",
  scope: "tools",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: ({ name, ...body }) => ({
    method: "PATCH",
    path: `/api/tools/${encodeURIComponent(name)}`,
    body,
  }),
  decode: (data) => data ?? {},
  decodeError: (info) =>
    info.status === 409 ? { kind: "success", value: info.body ?? {} } : undefined,
  error: "Could not update tool",
});

// decodeError resolves the 409 has_dependents envelope as a success payload for the cascade
// confirm; every other failure keeps the default mapping.
export interface DeleteToolResult {
  job?: { id: string };
  code?: string;
  dependents?: string[];
  error?: string;
}

const DELETE_TIMEOUT_MS = 30_000;

export const deleteTool = apiAction<{ name: string; force?: boolean }, DeleteToolResult>({
  name: "tools.delete",
  scope: "tools",
  idempotencyKey: true,
  request: ({ name, force }) => ({
    method: "DELETE",
    path: `/api/tools/${encodeURIComponent(name)}${force === true ? "?force=1" : ""}`,
  }),
  // Callers never see undefined on an empty-body 2xx.
  decode: (data) => data ?? {},
  decodeError: (info) =>
    info.status === 409 ? { kind: "success", value: info.body ?? {} } : undefined,
  error: false, // 409 cascade is a normal flow, handled by the caller
});

// Install-by-name for a feature banner: create from the catalog, else (re)install. error: false:
// banners render their own progress and errors.
async function runEnsure(
  args: { name: string },
  signal: AbortSignal,
  ctx?: ActionContext,
): Promise<JobResponse> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (ctx?.idempotencyKey !== undefined) {
    headers[IDEMPOTENCY_HEADER] = ctx.idempotencyKey;
  }
  const send = async (method: string, path: string, body: unknown): Promise<Response> => {
    try {
      return await fetch(path, {
        method,
        headers,
        body: JSON.stringify(body),
        signal: withTimeout(signal, DELETE_TIMEOUT_MS),
      });
    } catch (e) {
      throw classifyFetchError(e, signal);
    }
  };
  // An already-manifested name 400s -> plain install; a disabled template 409s -> enable (PATCH).
  let res = await send("POST", "/api/tools", { name: args.name });
  if (res.status === 400) {
    res = await send("POST", `/api/tools/${encodeURIComponent(args.name)}/install`, {});
  }
  if (res.status === 409) {
    res = await send("PATCH", `/api/tools/${encodeURIComponent(args.name)}`, { disabled: false });
  }
  let parsed: unknown;
  try {
    parsed = await res.json();
  } catch {
    parsed = undefined;
  }
  if (!res.ok) {
    const msg = hasErrorString(parsed) ? parsed.error : `HTTP ${String(res.status)}`;
    throw new ActionError(msg, { status: res.status });
  }
  return parsed ?? {};
}

export const ensureTool = defineAction<{ name: string }, JobResponse>({
  name: "tools.ensure",
  scope: "tools",
  idempotencyKey: true,
  run: runEnsure,
  error: false,
});

/** Whether the engine read the host's package index: three-valued because "no Debian hit" and
 *  "the index could not be consulted" are opposite answers `apt_available` merges. */
type AptState = "unavailable" | "indexing" | "available";

function isAptState(v: unknown): v is AptState {
  return v === "unavailable" || v === "indexing" || v === "available";
}

/** The search reply plus two fields only newer engines state. Absent means UNSTATED. */
export interface ToolSearchResponse extends SearchResponse {
  apt_state?: AptState;
  /** How many rows the query matched over the blocks the reply holds, so it is
   *  a denominator for `results.length` whenever it is stated. */
  matched?: number;
}

/** Reads both additive fields off the raw body and VALIDATES them: the wire type's bare-string
 *  `apt_state` would let an invented value reach the renderer's exhaustive switch with no arm. */
function decodeSearch(data: unknown): ToolSearchResponse {
  const o = data as Record<string, unknown>;
  const out: ToolSearchResponse = { ...(data as Omit<SearchResponse, "apt_state">) };
  // Dropped first, or a REFUSED value would survive the spread.
  delete out.apt_state;
  delete out.matched;
  const state = o["apt_state"];
  if (isAptState(state)) {
    out.apt_state = state;
  }
  const matched = o["matched"];
  if (typeof matched === "number" && Number.isInteger(matched) && matched >= 0) {
    out.matched = matched;
  }
  return out;
}

export const searchTools = apiAction<{ q: string }, ToolSearchResponse>({
  name: "tools.search",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  dedupe: true,
  request: ({ q }) => ({
    method: "GET",
    path: `/api/tools/search?q=${encodeURIComponent(q)}`,
  }),
  decode: decodeSearch,
  error: false,
});

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args
export const getToolsJobs = apiAction<void, JobsResponse>({
  name: "tools.jobs",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  dedupe: true,
  request: () => ({ method: "GET", path: "/api/tools/jobs" }),
  error: false,
});

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args
export const getCatalogInfo = apiAction<void, CatalogInfo>({
  name: "tools.catalog_info",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  dedupe: true,
  request: () => ({ method: "GET", path: "/api/tools/catalog" }),
  error: false,
});

/** Enqueue a catalog refresh (fetch, verify, swap); progress streams over the job SSE. */
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args
export const refreshCatalog = apiAction<void, JobResponse>({
  name: "tools.refresh_catalog",
  scope: "tools",
  request: () => ({ method: "POST", path: "/api/tools/catalog/refresh" }),
  error: "Could not refresh the tool catalog",
});

export const cancelToolJob = apiAction<{ id: string }>({
  name: "tools.cancel_job",
  scope: "tools",
  idempotencyKey: true,
  request: ({ id }) => ({
    method: "POST",
    path: `/api/tools/jobs/${encodeURIComponent(id)}/cancel`,
    body: {},
  }),
  error: "Could not cancel job",
});

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args/result
export const runDiagnostics = apiAction<void, { report?: string; error?: string }>({
  name: "tools.run_diagnostics",
  dedupe: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: () => ({ method: "POST", path: "/api/diagnostics", body: {} }),
  error: false,
});

export const seedMcp = apiAction<{ name: string; install?: string }>({
  name: "tools.seed_mcp",
  scope: "tools",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: ({ name, install }) => ({
    method: "POST",
    path: MCP_API,
    body: {
      name,
      transport: "stdio",
      enabled: false,
      prewarm: false,
      command: name,
      args: [],
      env: [],
      ...(install !== undefined ? { install } : {}),
    },
  }),
  error: "Could not create MCP entry",
});

// Which well-known binaries exist on PATH, for the inline "Setting up <feature>..." banners.
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type
export const getToolsStatus = apiAction<void, Record<string, boolean>>({
  name: "tools.status",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  dedupe: true,
  request: () => ({ method: "GET", path: "/api/tools/status" }),
  error: false,
});
