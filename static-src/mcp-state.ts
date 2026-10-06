// MCP state: wire types, secret sentinel, in-memory state, server fetch.

import { apiGetTyped, CancellableSlot } from "./api-client.js";
import { registerCleanup } from "./actions/index.js";
import {
  SignalMap,
  createCollection,
  signal,
  type ReadonlySignal,
  type Signal,
  type Collection,
} from "@cplieger/reactive";
import {
  asObject,
  decodeArray,
  optBool,
  optNum,
  optStr,
  reqBool,
  reqNum,
  reqStr,
  type Decoder,
} from "./validators.js";

const decodePromptArg: Decoder<MCPPromptArg> = (v) => {
  const o = asObject(v, "$.mcp.prompt.arg");
  const out: MCPPromptArg = { name: reqStr(o, "name", "$.mcp.prompt.arg") };
  const d = optStr(o, "description", "$.mcp.prompt.arg");
  if (d !== undefined) {
    out.description = d;
  }
  const r = optBool(o, "required", "$.mcp.prompt.arg");
  if (r !== undefined) {
    out.required = r;
  }
  return out;
};

const decodePromptInfo: Decoder<MCPPromptInfo> = (v) => {
  const o = asObject(v, "$.mcp.prompt");
  const out: MCPPromptInfo = {
    name: reqStr(o, "name", "$.mcp.prompt"),
    prompt_name: reqStr(o, "prompt_name", "$.mcp.prompt"),
  };
  const d = optStr(o, "description", "$.mcp.prompt");
  if (d !== undefined) {
    out.description = d;
  }
  if (Array.isArray(o["arguments"])) {
    out.arguments = decodeArray(o["arguments"], decodePromptArg, "$.mcp.prompt.arguments");
  }
  return out;
};

export const decodeResourceInfo: Decoder<MCPResourceInfo> = (v) => {
  const o = asObject(v, "$.mcp.resource");
  const out: MCPResourceInfo = {
    name: reqStr(o, "name", "$.mcp.resource"),
    uri: reqStr(o, "uri", "$.mcp.resource"),
  };
  const d = optStr(o, "description", "$.mcp.resource");
  if (d !== undefined) {
    out.description = d;
  }
  const m = optStr(o, "mime_type", "$.mcp.resource");
  if (m !== undefined) {
    out.mime_type = m;
  }
  return out;
};

export const decodeResourceTemplateInfo: Decoder<MCPResourceTemplateInfo> = (v) => {
  const o = asObject(v, "$.mcp.resource_template");
  const out: MCPResourceTemplateInfo = {
    name: reqStr(o, "name", "$.mcp.resource_template"),
    uri_template: reqStr(o, "uri_template", "$.mcp.resource_template"),
  };
  const d = optStr(o, "description", "$.mcp.resource_template");
  if (d !== undefined) {
    out.description = d;
  }
  const m = optStr(o, "mime_type", "$.mcp.resource_template");
  if (m !== undefined) {
    out.mime_type = m;
  }
  return out;
};

const decodeWireRuntimeStatus: Decoder<WireRuntimeStatus> = (v) => {
  const s = asObject(v, "$.mcp_status.server");
  const out: WireRuntimeStatus = {
    name: reqStr(s, "name", "$.mcp_status.server"),
    state: reqStr(s, "state", "$.mcp_status.server"),
  };
  // Optional on decode though always sent: requiring it would reject the whole status response if one server omitted it.
  const origin = optStr(s, "origin", "$.mcp_status.server");
  if (origin !== undefined) {
    out.origin = origin;
  }
  const root = optStr(s, "origin_root", "$.mcp_status.server");
  if (root !== undefined) {
    out.origin_root = root;
  }
  const power = optStr(s, "origin_power", "$.mcp_status.server");
  if (power !== undefined) {
    out.origin_power = power;
  }
  // Strict `=== true`: omitted in the common case, and absent means it shadows nothing.
  if (s["shadows"] === true) {
    out.shadows = true;
  }
  const oauthUrl = optStr(s, "oauth_url", "$.mcp_status.server");
  if (oauthUrl !== undefined) {
    out.oauth_url = oauthUrl;
  }
  const err = optStr(s, "error", "$.mcp_status.server");
  if (err !== undefined) {
    out.error = err;
  }
  // Strict `=== true`: omitted in the common case, and absent means "not yet relayed", which keeps the paste box offered.
  if (s["relayed"] === true) {
    out.relayed = true;
  }
  if (Array.isArray(s["tools"])) {
    out.tools = (s["tools"] as unknown[]).map((x) => String(x));
  }
  if (Array.isArray(s["prompts"])) {
    out.prompts = decodeArray(s["prompts"], decodePromptInfo, "$.mcp_status.server.prompts");
  }
  if (Array.isArray(s["resources"])) {
    out.resources = decodeArray(
      s["resources"],
      decodeResourceInfo,
      "$.mcp_status.server.resources",
    );
  }
  if (Array.isArray(s["resource_templates"])) {
    out.resource_templates = decodeArray(
      s["resource_templates"],
      decodeResourceTemplateInfo,
      "$.mcp_status.server.resource_templates",
    );
  }
  return out;
};

const decodeMCPStatusResponseLocal: Decoder<{ servers: WireRuntimeStatus[] }> = (v) => {
  const o = asObject(v, "$.mcp_status");
  return {
    servers: decodeArray(o["servers"], decodeWireRuntimeStatus, "$.mcp_status.servers"),
  };
};

const decodeKeyPair: Decoder<KeyPair> = (v) => {
  const o = asObject(v, "$.kp");
  return { name: reqStr(o, "name", "$.kp"), value: reqStr(o, "value", "$.kp") };
};

const decodeServer: Decoder<Server> = (v) => {
  const o = asObject(v, "$.server");
  const p = "$.server";
  const out: Server = {
    id: reqStr(o, "id", p),
    name: reqStr(o, "name", p),
    transport: reqStr(o, "transport", p) as Transport,
    enabled: reqBool(o, "enabled", p),
    created_at: reqNum(o, "created_at", p),
    updated_at: reqNum(o, "updated_at", p),
  };
  const command = optStr(o, "command", p);
  if (command !== undefined) {
    out.command = command;
  }
  const url = optStr(o, "url", p);
  if (url !== undefined) {
    out.url = url;
  }
  const oauthClientId = optStr(o, "oauth_client_id", p);
  if (oauthClientId !== undefined) {
    out.oauth_client_id = oauthClientId;
  }
  const oauthMetadataURL = optStr(o, "oauth_client_metadata_url", p);
  if (oauthMetadataURL !== undefined) {
    out.oauth_client_metadata_url = oauthMetadataURL;
  }
  const oauthRedirectURI = optStr(o, "oauth_redirect_uri", p);
  if (oauthRedirectURI !== undefined) {
    out.oauth_redirect_uri = oauthRedirectURI;
  }
  const prewarm = optBool(o, "prewarm", p);
  if (prewarm !== undefined) {
    out.prewarm = prewarm;
  }
  const waitForReady = optBool(o, "wait_for_ready", p);
  if (waitForReady !== undefined) {
    out.wait_for_ready = waitForReady;
  }
  const timeoutMS = optNum(o, "timeout_ms", p);
  if (timeoutMS !== undefined) {
    out.timeout_ms = timeoutMS;
  }
  if (Array.isArray(o["args"])) {
    out.args = (o["args"] as unknown[]).map((x) => String(x));
  }
  if (Array.isArray(o["env"])) {
    out.env = decodeArray(o["env"], decodeKeyPair, `${p}.env`);
  }
  if (Array.isArray(o["headers"])) {
    out.headers = decodeArray(o["headers"], decodeKeyPair, `${p}.headers`);
  }
  if (Array.isArray(o["disabled_tools"])) {
    out.disabled_tools = (o["disabled_tools"] as unknown[]).map((x) => String(x));
  }
  return out;
};

const decodeMCPServersResponseLocal: Decoder<{ servers: Server[] }> = (v) => {
  const o = asObject(v, "$.mcp_servers");
  return { servers: decodeArray(o["servers"], decodeServer, "$.mcp_servers.servers") };
};

// SSE is the legacy HTTP+SSE remote transport, a first-class stored value sharing the url/headers form with http.
export type { Transport } from "./wire/types.gen.js";
import type { MCPOrigin, Transport } from "./wire/types.gen.js";

export interface KeyPair {
  name: string;
  value: string;
}

/** Persisted server record. `value` fields come back as "***" when a
 *  secret is stored; send "***" unchanged to preserve it, any other
 *  string to replace it. */
export interface Server {
  id: string;
  name: string;
  transport: Transport;
  enabled: boolean;
  prewarm?: boolean;
  /** KAS's per-server `waitForReady`, from a pasted config; keeps one server waited on when the Settings switch is off. */
  wait_for_ready?: boolean;
  /** KAS's per-server connect timeout in milliseconds; absent means KAS's
   *  own default. Imported from a pasted config; no form control edits it. */
  timeout_ms?: number;
  command?: string;
  args?: string[];
  env?: KeyPair[];
  url?: string;
  headers?: KeyPair[];
  disabled_tools?: string[];
  /**
   * Pre-registered OAuth client id for servers without Dynamic Client Registration, rendered as `oauth.clientId`.
   * Empty falls back to DCR.
   */
  oauth_client_id?: string;
  /** KAS `oauth.clientMetadataUrl`: an https client-ID metadata document. */
  oauth_client_metadata_url?: string;
  /** KAS `oauth.redirectUri`: the pinned loopback callback ("localhost:7778"). */
  oauth_redirect_uri?: string;
  created_at: number;
  updated_at: number;
}

/** One argument of an MCP prompt (from _kiro/mcp/status discovery). */
export interface MCPPromptArg {
  name: string;
  description?: string;
  required?: boolean;
}

/** A prompt a connected MCP server advertises. `prompt_name` is the machine
 *  id passed to the fetch endpoint; `name` is the display title. */
export interface MCPPromptInfo {
  name: string;
  prompt_name: string;
  description?: string;
  arguments?: MCPPromptArg[];
}

/** A resource a connected MCP server advertises. `uri` is the fetch key. */
export interface MCPResourceInfo {
  name: string;
  uri: string;
  description?: string;
  mime_type?: string;
}

/** A parameterised resource a server advertises; `uri_template` is RFC 6570. */
export interface MCPResourceTemplateInfo {
  name: string;
  uri_template: string;
  description?: string;
  mime_type?: string;
}

/**
 * What a connected server exposes (tools, prompts, resources, resource templates), keyed by server name and arriving
 * together on /api/mcp/status. Tools are a discovery result, not persisted config.
 */
export interface ServerDiscovery {
  tools: string[];
  prompts: MCPPromptInfo[];
  resources: MCPResourceInfo[];
  resource_templates: MCPResourceTemplateInfo[];
}

export type RuntimeState = "connected" | "needs_auth" | "idle" | "failed" | "disabled";

/**
 * `origin` picks the row a live status belongs to, never editability. `shadows`: marotte's config holds the name, so
 * marotte's row is not the one KAS runs.
 */
interface Provenance {
  origin: MCPOrigin;
  originRoot?: string;
  originPower?: string;
  shadows?: boolean;
}

export type RuntimeStatus = Provenance &
  (
    | { name: string; state: "connected" }
    /** `relayed` rides the status wire, so a reload or second device does not offer the paste box for a spent code. */
    | { name: string; state: "needs_auth"; oauth_url: string; relayed: boolean }
    | { name: string; state: "idle" }
    | { name: string; state: "failed"; error: string }
    | { name: string; state: "disabled" }
  );

/** Distributive: a plain `Omit` collapses the union to its common members. */
type WithoutOrigin<T> = T extends unknown ? Omit<T, keyof Provenance> : never;

export const SECRET_MASK = "***";

interface WireRuntimeStatus {
  name: string;
  state: string;
  origin?: string;
  origin_root?: string;
  origin_power?: string;
  shadows?: boolean;
  oauth_url?: string;
  relayed?: boolean;
  error?: string;
  tools?: string[];
  prompts?: MCPPromptInfo[];
  resources?: MCPResourceInfo[];
  resource_templates?: MCPResourceTemplateInfo[];
}

const FOREIGN_ORIGINS: ReadonlySet<string> = new Set<Exclude<MCPOrigin, "user">>([
  "workspace",
  "power",
  "bundled",
  "unknown",
]);

/** Defaults to `user`; the server never sends `user` for a name marotte lacks, so the default grants a row nothing. */
function adaptOrigin(raw: string | undefined): MCPOrigin {
  return raw !== undefined && FOREIGN_ORIGINS.has(raw) ? (raw as MCPOrigin) : "user";
}

function adaptProvenance(w: WireRuntimeStatus): Provenance {
  const p: Provenance = { origin: adaptOrigin(w.origin) };
  if (w.origin_root !== undefined && w.origin_root !== "") {
    p.originRoot = w.origin_root;
  }
  if (w.origin_power !== undefined && w.origin_power !== "") {
    p.originPower = w.origin_power;
  }
  if (w.shadows === true) {
    p.shadows = true;
  }
  return p;
}

/** Exported for testing: adapt a wire status to the domain type. */
export function adaptStatus(w: WireRuntimeStatus): RuntimeStatus {
  const prov = adaptProvenance(w);
  switch (w.state) {
    case "needs_auth":
      return {
        name: w.name,
        ...prov,
        state: "needs_auth",
        oauth_url: w.oauth_url ?? "",
        relayed: w.relayed ?? false,
      };
    case "failed":
      return { name: w.name, ...prov, state: "failed", error: w.error ?? "" };
    case "connected":
      return { name: w.name, ...prov, state: "connected" };
    case "disabled":
      return { name: w.name, ...prov, state: "disabled" };
    default:
      return { name: w.name, ...prov, state: "idle" };
  }
}

const statusMap = new SignalMap<RuntimeStatus>();

function idleStatus(name: string): RuntimeStatus {
  return { name, origin: "user", state: "idle" };
}

/** Reactive per-server runtime-status signal, created lazily as "idle". */
export function statusSignalFor(name: string): ReadonlySignal<RuntimeStatus> {
  return statusMap.ensure(name, idleStatus(name));
}

const EMPTY_DISCOVERY: ServerDiscovery = {
  tools: [],
  prompts: [],
  resources: [],
  resource_templates: [],
};
const discoveryMap = new SignalMap<ServerDiscovery>();

/** Reactive per-server discovery signal (empty default), from /api/mcp/status. */
export function discoverySignalFor(name: string): ReadonlySignal<ServerDiscovery> {
  return discoveryMap.ensure(name, EMPTY_DISCOVERY);
}

export type PrewarmState = "none" | "installing" | "failed";

const prewarmMap = new SignalMap<PrewarmState>();

/** Reactive per-server prewarm-state signal (created lazily as "none"). */
export function prewarmSignalFor(id: string): ReadonlySignal<PrewarmState> {
  return prewarmMap.ensure(id, "none");
}

/** Set a server's prewarm state ("done" clears the badge -> "none"). */
export function setPrewarm(id: string, state: "installing" | "done" | "failed"): void {
  prewarmMap.ensure(id, "none").value = state === "done" ? "none" : state;
}

/** The configured MCP servers: field changes fire `signalFor(id)`, add/remove/reorder fire `ids`. */
export const servers: Collection<Server> = createCollection<Server>((s) => s.id);

/**
 * Names /api/mcp/status reported that the config list does not hold (a Power's server, or an unreadable config),
 * sorted. Such a server has no persisted id, so its rows are read-only.
 */
export const unconfiguredNames: Signal<string[]> = signal<string[]>([]);

/** Snapshot of the configured servers (non-reactive). */
export function configuredServers(): Server[] {
  return servers.items();
}

function sameNames(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((n, i) => n === b[i]);
}

class MCPStateController {
  private readonly serversSlot = new CancellableSlot();
  private readonly statusSlot = new CancellableSlot();
  private serversFetchPending = false;
  private statusFetchPending = false;

  abort(): void {
    this.serversSlot.abort();
    this.statusSlot.abort();
    this.serversFetchPending = false;
    this.statusFetchPending = false;
  }

  setStatus(name: string, rs: RuntimeStatus): void {
    statusMap.ensure(name, rs).value = rs;
  }

  /**
   * SSE frames carry no provenance, so the known one is kept; defaulting to `user` would move a foreign server's status
   * onto marotte's row.
   */
  setStatusFromEvent(name: string, next: WithoutOrigin<RuntimeStatus>): void {
    const prev = statusMap.ensure(name, idleStatus(name)).peek();
    const prov: Provenance = { origin: prev.origin };
    if (prev.originRoot !== undefined) {
      prov.originRoot = prev.originRoot;
    }
    if (prev.originPower !== undefined) {
      prov.originPower = prev.originPower;
    }
    if (prev.shadows !== undefined) {
      prov.shadows = prev.shadows;
    }
    this.setStatus(name, { ...next, ...prov });
  }

  deleteStatus(name: string): void {
    // Reset to idle rather than clear, which would orphan the row's subscription.
    statusMap.ensure(name, idleStatus(name)).value = idleStatus(name);
  }

  refetchServers(): void {
    if (this.serversFetchPending) {
      return;
    }
    this.serversFetchPending = true;
    queueMicrotask(() => {
      this.serversFetchPending = false;
      void this.doRefetchServers();
    });
  }

  private async doRefetchServers(): Promise<void> {
    // Named `abort` to avoid shadowing reactive's `signal`.
    const abort = this.serversSlot.start();
    const d = await apiGetTyped("/api/mcp", decodeMCPServersResponseLocal, abort);
    if (abort.aborted) {
      return;
    }
    servers.setAll(d?.servers ?? []);
  }

  refetchStatus(): void {
    if (this.statusFetchPending) {
      return;
    }
    this.statusFetchPending = true;
    queueMicrotask(() => {
      this.statusFetchPending = false;
      void this.doRefetchStatus();
    });
  }

  private async doRefetchStatus(): Promise<void> {
    const abort = this.statusSlot.start();
    const d = await apiGetTyped("/api/mcp/status", decodeMCPStatusResponseLocal, abort);
    if (abort.aborted) {
      return;
    }
    const configured = new Set(servers.items().map((s) => s.name));
    const seen = new Set<string>();
    const foreign: string[] = [];
    for (const s of d?.servers ?? []) {
      seen.add(s.name);
      const st = adaptStatus(s);
      this.setStatus(s.name, st);
      this.setDiscovery(
        s.name,
        s.tools ?? [],
        s.prompts ?? [],
        s.resources ?? [],
        s.resource_templates ?? [],
      );
      // A foreign server gets its own row unless marotte's config has the name, except when it SHADOWS that name: then
      // marotte's row is not in use and the running server needs its own.
      if (st.origin !== "user" && (!configured.has(s.name) || st.shadows === true)) {
        foreign.push(s.name);
      }
    }
    foreign.sort();
    // Replace only on a real change: an equal new array would rebuild every read-only row.
    if (!sameNames(unconfiguredNames.peek(), foreign)) {
      unconfiguredNames.value = foreign;
    }
    // Servers with no reported status revert to idle.
    for (const s of servers.items()) {
      if (!seen.has(s.name)) {
        this.deleteStatus(s.name);
        this.setDiscovery(s.name, [], [], []);
      }
    }
  }

  setDiscovery(
    name: string,
    tools: string[],
    prompts: MCPPromptInfo[],
    resources: MCPResourceInfo[],
    templates: MCPResourceTemplateInfo[] = [],
  ): void {
    if (
      tools.length === 0 &&
      prompts.length === 0 &&
      resources.length === 0 &&
      templates.length === 0
    ) {
      // The shared frozen empty value, so idle servers do not churn the signal.
      discoveryMap.ensure(name, EMPTY_DISCOVERY).value = EMPTY_DISCOVERY;
      return;
    }
    discoveryMap.ensure(name, EMPTY_DISCOVERY).value = {
      tools,
      prompts,
      resources,
      resource_templates: templates,
    };
  }
}

const instance = new MCPStateController();
export const mcpState = instance;
registerCleanup(() => {
  instance.abort();
});

/** Patch a configured entry in-place. Returns the previous entry for rollback. */
export function updateConfiguredEntry(id: string, patch: Partial<Server>): Server | undefined {
  const prev = servers.get(id);
  if (prev === undefined) {
    return undefined;
  }
  const snapshot = { ...prev };
  servers.update(id, (cur) => ({ ...cur, ...patch }));
  return snapshot;
}

/** @internal Remove a configured entry by id. Returns [entry, index] for rollback. */
export function removeConfiguredEntry(id: string): [Server, number] | undefined {
  const idx = servers.ids.peek().indexOf(id);
  if (idx === -1) {
    return undefined;
  }
  const entry = servers.get(id);
  if (entry === undefined) {
    return undefined;
  }
  servers.remove(id);
  return [entry, idx];
}

/** Re-insert a previously removed entry at its original position when available. */
export function insertConfiguredEntry(entry: Server, atIndex?: number): void {
  if (servers.has(entry.id)) {
    return;
  }
  const arr = servers.items();
  let pos: number;
  if (atIndex !== undefined && atIndex >= 0 && atIndex <= arr.length) {
    pos = atIndex;
  } else {
    // No positional hint: id ordering.
    pos = arr.findIndex((s) => s.id > entry.id);
    if (pos === -1) {
      pos = arr.length;
    }
  }
  arr.splice(pos, 0, entry);
  servers.setAll(arr);
}
