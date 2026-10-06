import { ActionError, apiAction, retryNetwork, RETRY_STANDARD } from "./index.js";
import type { ApiErrorInfo, ApiErrorDecision } from "./index.js";

import {
  type Server,
  updateConfiguredEntry,
  removeConfiguredEntry,
  insertConfiguredEntry,
} from "../mcp-state.js";
import { decodeRegistrySearchFailure, decodeRegistrySearchResult } from "../wire/decoders.gen.js";
import type { RegistrySearchFailure, RegistrySearchResult } from "../wire/types.gen.js";

/** Base path for MCP API endpoints. */
export const MCP_API = "/api/mcp";

/** One validation failure attributed to its wire field (`internal/mcp.FieldError`). The server
 *  accumulates across checks, so one response carries every bad field. */
export interface ValidationField {
  field: string;
  message: string;
}

/** Narrow a `fields` array off a 400 body. Server-controlled input, so every
 *  entry is shape-checked rather than cast. */
function readValidationFields(body: unknown): ValidationField[] {
  if (typeof body !== "object" || body === null || !("fields" in body)) {
    return [];
  }
  const raw: unknown = body.fields;
  if (!Array.isArray(raw)) {
    return [];
  }
  const out: ValidationField[] = [];
  for (const entry of raw) {
    if (typeof entry !== "object" || entry === null) {
      continue;
    }
    const e = entry as { field?: unknown; message?: unknown };
    if (typeof e.field === "string" && typeof e.message === "string") {
      out.push({ field: e.field, message: e.message });
    }
  }
  return out;
}

/** Recovers a 400's per-field breakdown onto the error's `cause`, so the form can mark inputs;
 *  the dispatch still fails. */
function decodeValidationError<T>(info: ApiErrorInfo): ApiErrorDecision<T> | undefined {
  if (info.status !== 400) {
    return undefined;
  }
  const fields = readValidationFields(info.body);
  if (fields.length === 0) {
    return undefined;
  }
  return {
    kind: "error",
    error: new ActionError(info.message, { status: info.status, cause: fields }),
  };
}

/** Reads the field breakdown back off a failed dispatch's error. Empty for
 *  every other failure. */
export function validationFieldsOf(err: { cause?: unknown } | undefined): ValidationField[] {
  const raw = err?.cause;
  if (!Array.isArray(raw)) {
    return [];
  }
  return raw.filter((f): f is ValidationField => {
    if (typeof f !== "object" || f === null) {
      return false;
    }
    const c = f as { field?: unknown; message?: unknown };
    return typeof c.field === "string" && typeof c.message === "string";
  });
}

interface ToggleArgs {
  id: string;
  enabled: boolean;
}

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args/result
export const toggleServer = apiAction<ToggleArgs, void, Server>({
  name: "mcp.toggle_server",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  scope: (args) => "mcp:" + args.id,
  request: ({ id, enabled }) => ({
    method: "PATCH",
    path: `${MCP_API}/${encodeURIComponent(id)}`,
    body: { enabled },
  }),
  optimistic: ({ id, enabled }) => {
    return updateConfiguredEntry(id, { enabled });
  },
  rollback: (_args, op) => {
    if (op !== undefined) {
      updateConfiguredEntry(op.id, { enabled: op.enabled });
    }
  },
  error: "Could not toggle integration",
});

interface DeleteArgs {
  id: string;
}

// No auto-retry: a timed-out DELETE may have succeeded; a retry would 404 and roll back wrongly.
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args/result
export const deleteServer = apiAction<DeleteArgs, void, [Server, number]>({
  name: "mcp.delete_server",
  dedupe: (args) => `mcp.delete:${args.id}`,
  scope: (args) => "mcp:" + args.id,
  request: ({ id }) => ({
    method: "DELETE",
    path: `${MCP_API}/${encodeURIComponent(id)}`,
  }),
  optimistic: ({ id }) => {
    return removeConfiguredEntry(id);
  },
  rollback: (_args, op) => {
    if (op !== undefined) {
      const [entry, atIndex] = op;
      insertConfiguredEntry(entry, atIndex);
    }
  },
  error: "Could not remove integration",
});

export const openEdit = apiAction<string, Server>({
  name: "mcp.open_edit",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  dedupe: (id) => id,
  request: (id) => ({
    method: "GET",
    path: `${MCP_API}/${encodeURIComponent(id)}`,
  }),
  error: "Could not load integration details",
});

interface SaveArgs {
  /** Empty string for create, non-empty for update. */
  id: string;
  body: Partial<Server>;
}

export const saveServer = apiAction<SaveArgs, Server>({
  name: "mcp.save_server",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  scope: (args) => "mcp:" + args.id,
  request: ({ id, body }) => ({
    method: id === "" ? "POST" : "PUT",
    path: id === "" ? MCP_API : `${MCP_API}/${encodeURIComponent(id)}`,
    body,
  }),
  decodeError: decodeValidationError,
  error: false,
});

// The server owns the translation from the publisher's shape (internal/mcp/paste.go); this posts
// the parsed JSON unchanged.

/** What one entry of a pasted block did. No "updated": an entry naming a
 *  configured server either matches its spec or fails the paste. */
interface ImportResult {
  name: string;
  outcome: "created" | "unchanged";
}

/** Per-entry outcomes plus notes on keys marotte recognises but cannot
 *  store. */
export interface ImportServersResult {
  results: ImportResult[];
  notes?: string[];
}

export const importServers = apiAction<Record<string, unknown>, ImportServersResult>({
  name: "mcp.import_servers",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: (block) => ({
    method: "POST",
    path: `${MCP_API}/import`,
    body: block,
  }),
  decodeError: decodeValidationError,
  success: (_args, res) => summariseImport(res),
  // Rendered inline beside the textarea, not as a toast.
  error: false,
});

/** One sentence naming what landed. Exported for its test. */
export function summariseImport(res: ImportServersResult | null): string {
  const results = res?.results ?? [];
  const created = results.filter((r) => r.outcome === "created").length;
  const unchanged = results.length - created;
  const parts: string[] = [];
  if (created > 0) {
    parts.push(`Connected ${created} integration${created === 1 ? "" : "s"}`);
  }
  if (unchanged > 0) {
    parts.push(`${unchanged} already configured`);
  }
  if (parts.length === 0) {
    parts.push("Nothing to connect");
  }
  const notes = res?.notes ?? [];
  const onlyNote = notes.length === 1 ? notes[0] : undefined;
  if (onlyNote !== undefined) {
    parts.push(onlyNote);
  } else if (notes.length > 1) {
    parts.push(`${notes.length} keys marotte does not store were ignored`);
  }
  return parts.join(". ") + ".";
}

interface SearchRegistryArgs {
  q: string;
}

/** Narrow a 502 body through the generated decoder. An unclassified body (an off-shape 502, a
 *  proxy's error page) reads as no classification rather than failing the error branch. */
function readRegistryFailure(body: unknown): RegistrySearchFailure | undefined {
  try {
    return decodeRegistrySearchFailure(body);
  } catch {
    return undefined;
  }
}

/** Carries the 502's classification onto the error's `cause`, so the panel can say whether to
 *  wait and for how long. The dispatch still fails. */
function decodeRegistryFailure<T>(info: ApiErrorInfo): ApiErrorDecision<T> | undefined {
  const failure = readRegistryFailure(info.body);
  if (failure === undefined) {
    return undefined;
  }
  return {
    kind: "error",
    error: new ActionError(info.message, { status: info.status, cause: failure }),
  };
}

/** Reads the classification back off a failed dispatch's error. Undefined for
 *  a failure the server never classified (a network error, a timeout). */
export function registryFailureOf(
  err: { cause?: unknown } | undefined,
): RegistrySearchFailure | undefined {
  return readRegistryFailure(err?.cause);
}

// No retry, alone among the MCP actions: the registry refuses connections after a burst, so a
// retry waits out every upstream timeout in series. The panel's Retry button covers it.
export const searchRegistry = apiAction<SearchRegistryArgs, RegistrySearchResult>({
  name: "mcp.search_registry",
  dedupe: (args) => args.q,
  request: ({ q }) => ({
    method: "GET",
    path: `${MCP_API}/registry/search?q=${encodeURIComponent(q)}&limit=20`,
  }),
  decode: decodeRegistrySearchResult,
  decodeError: decodeRegistryFailure,
  error: false,
});

// Reconnect on every live chat bridge (server-side fan-out); the refreshed status arrives via SSE
// and a /api/mcp/status refetch, so no optimistic state.

/** Result of POST /api/mcp/reconnect: how many live bridges were targeted. */
export interface ReconnectResult {
  reconnected: number;
}

export const reconnectServer = apiAction<{ server: string }, ReconnectResult>({
  name: "mcp.reconnect_server",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  scope: (args) => "mcp-reconnect:" + args.server,
  request: ({ server }) => ({
    method: "POST",
    path: `${MCP_API}/reconnect`,
    body: { server },
  }),
  error: "Could not reconnect integration",
});

// The response is the raw MCP result; the UI inserts its text into the prompt bar.

/** One content block of an MCP message (text is the only kind we surface). */
export interface MCPContentBlock {
  type?: string;
  text?: string;
}

/** Raw MCP GetPromptResult: an ordered list of role-tagged messages. */
export interface MCPPromptResult {
  description?: string;
  messages?: { role?: string; content?: MCPContentBlock | MCPContentBlock[] }[];
}

/** Raw MCP ReadResourceResult: one or more resource contents. */
export interface MCPResourceResult {
  contents?: { uri?: string; mimeType?: string; text?: string; blob?: string }[];
}

interface GetPromptArgs {
  server: string;
  prompt: string;
  arguments?: Record<string, string>;
}

export const getPromptContent = apiAction<GetPromptArgs, MCPPromptResult>({
  name: "mcp.get_prompt",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: ({ server, prompt, arguments: args }) => ({
    method: "POST",
    path: `${MCP_API}/prompt`,
    body: { server, prompt, arguments: args ?? {} },
  }),
  error: "Could not load prompt",
});

export const getResourceContent = apiAction<{ server: string; uri: string }, MCPResourceResult>({
  name: "mcp.get_resource",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: ({ server, uri }) => ({
    method: "POST",
    path: `${MCP_API}/resource`,
    body: { server, uri },
  }),
  error: "Could not load resource",
});

// Rescues a sign-in whose redirect landed on the wrong machine: KAS binds its OAuth listener on
// the CONTAINER's localhost, so a remote browser is sent to its own. The user pastes the dead
// address and the server replays it inward (`internal/hub/mcp_oauth_relay.go`).

/** Result of POST /api/mcp/oauth-relay: the loopback listener's HTTP status. */
export interface OAuthRelayResult {
  status: number;
}

export const relayOAuthCallback = apiAction<
  { server: string; redirect_url: string },
  OAuthRelayResult
>({
  name: "mcp.relay_oauth_callback",
  // NO retry: an authorization code is single-use, and a replay would spend it twice.
  scope: (args) => "mcp-oauth-relay:" + args.server,
  request: ({ server, redirect_url }) => ({
    method: "POST",
    path: `${MCP_API}/oauth-relay`,
    body: { server, redirect_url },
  }),
  // Shown inline beside the pasted box: every rejection names the wrong part of the address.
  error: false,
});
