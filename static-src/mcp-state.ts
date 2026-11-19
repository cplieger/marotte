// MCP state: wire types, secret sentinel, in-memory state, server fetch.

import { apiGetTyped, CancellableSlot } from "./api-client.js";
import { registerCleanup } from "./actions/index.js";
import { configStanding } from "./config-standing.js";
import type { ConfigStanding } from "./wire/types.gen.js";
import {
  SignalMap,
  computed,
  createCollection,
  signal,
  type ReadonlySignal,
  type Signal,
  type Collection,
} from "@cplieger/reactive";
import { createMirror, type MirrorChange } from "./mirror.js";
import { versionMap } from "./subject-versions.js";
import { decodeMCPStatusResponse } from "./wire/decoders.gen.js";
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
} from "./wire/validators.gen.js";

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
  const oauthMode = optStr(o, "oauth_mode", p);
  if (oauthMode === "browser" || oauthMode === "enterprise" || oauthMode === "auto") {
    out.oauth_mode = oauthMode;
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
  const versionNegotiation = optStr(o, "version_negotiation", p);
  if (versionNegotiation !== undefined) {
    out.version_negotiation = versionNegotiation as VersionNegotiation;
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
  if (Array.isArray(o["oauth_scopes"])) {
    out.oauth_scopes = (o["oauth_scopes"] as unknown[]).map((x) => String(x));
  }
  return out;
};

const decodeMCPServersResponseLocal: Decoder<{ servers: Server[]; unreadable: number }> = (v) => {
  const o = asObject(v, "$.mcp_servers");
  return {
    servers: decodeArray(o["servers"], decodeServer, "$.mcp_servers.servers"),
    unreadable: optNum(o, "unreadable", "$.mcp_servers") ?? 0,
  };
};

// SSE is the legacy HTTP+SSE remote transport, a first-class stored value sharing the url/headers form with http.
export type { Transport } from "./wire/types.gen.js";
import type {
  MCPIdentityProviderStatus,
  MCPOrigin,
  MCPPrewarmState,
  MCPPromptInfo,
  MCPResourceInfo,
  MCPResourceTemplateInfo,
  MCPServerStatus,
  SubjectStamp,
  Transport,
} from "./wire/types.gen.js";

/** KAS's per-server `versionNegotiation` enum. */
type VersionNegotiation = "auto" | "legacy";

/** KAS's per-server `oauth.mode`: its own browser sign-in, the organization sign-in, or that with the browser as
 *  fallback. Absent is KAS's default, browser. */
export type OAuthMode = "browser" | "enterprise" | "auto";

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
  /** KAS's per-server `versionNegotiation`, from a paste or the raw edit; absent means kiro-cli's `auto`. No form control edits it. */
  version_negotiation?: VersionNegotiation;
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
  /** KAS `oauthScopes`, from a paste or the raw edit; no form control edits it. */
  oauth_scopes?: string[];
  /** KAS `oauth.mode`, a remote server's Sign-in method. */
  oauth_mode?: OAuthMode;
  created_at: number;
  updated_at: number;
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

export type RuntimeState =
  "connected" | "needs_auth" | "needs_org_sign_in" | "signing_in" | "idle" | "failed" | "disabled";

/**
 * `origin` picks the row a live status belongs to, never editability. `shadows`: marotte's config holds the name, so
 * marotte's row is not the one KAS runs.
 */
interface Provenance {
  origin: MCPOrigin;
  originRoot?: string;
  originPower?: string;
  shadows?: boolean;
  /** The config keys kiro-cli reported it does not use for this server; absent when none. */
  ignoredFields?: readonly string[];
}

export type RuntimeStatus = Provenance &
  (
    | { name: string; state: "connected" }
    /** `error` is why kiro-cli refused the last Sign in, absent when none was refused. */
    | { name: string; state: "needs_auth"; error?: string }
    /** Waits on the organization sign-in, whose card's Sign in is the remedy. */
    | { name: string; state: "needs_org_sign_in" }
    /** `oauth_url` is the consent page of the sign-in the user started. `relayed` rides the status wire, so a reload
     *  or second device does not offer the paste box for a spent code. */
    | { name: string; state: "signing_in"; oauth_url: string; relayed: boolean }
    | { name: string; state: "idle" }
    | { name: string; state: "failed"; error: string }
    | { name: string; state: "disabled" }
  );

export const SECRET_MASK = "***";

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

function adaptProvenance(w: MCPServerStatus): Provenance {
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
  if (w.ignored_fields !== undefined && w.ignored_fields.length > 0) {
    p.ignoredFields = w.ignored_fields;
  }
  return p;
}

/** Exported for testing: adapt a wire status to the domain type. */
export function adaptStatus(w: MCPServerStatus): RuntimeStatus {
  const prov = adaptProvenance(w);
  switch (w.state) {
    case "needs_auth":
      return w.error === undefined || w.error === ""
        ? { name: w.name, ...prov, state: "needs_auth" }
        : { name: w.name, ...prov, state: "needs_auth", error: w.error };
    case "needs_org_sign_in":
      return { name: w.name, ...prov, state: "needs_org_sign_in" };
    case "signing_in":
      return {
        name: w.name,
        ...prov,
        state: "signing_in",
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

/** The `mcp_status` version and epoch the organization sign-in was last read at. */
interface OrgHeld {
  readonly version: bigint;
  readonly epoch: string | null;
}

let orgHeld: OrgHeld | null = null;
// A fetched read's organization sign-in, taken once the mirror applied the rows read with it.
let orgFetched: { status: MCPIdentityProviderStatus | undefined; stamp: SubjectStamp } | null =
  null;

/** The organization sign-in's runtime state, absent while kiro-cli reported none and no sign-in stands. */
export const orgSignInStatus: Signal<MCPIdentityProviderStatus | undefined> = signal(undefined);

/** Take `status` when its stamp is newer than what is held, or the server process changed. Answers whether it was
 *  taken. */
function takeOrg(
  status: MCPIdentityProviderStatus | undefined,
  stamp: SubjectStamp | undefined,
  atOrAbove: boolean,
): boolean {
  if (stamp?.kind !== "mcp_status" || !/^\d{1,20}$/u.test(stamp.version)) {
    return false;
  }
  const version = BigInt(stamp.version);
  const epoch = versionMap().epoch();
  if (
    orgHeld !== null &&
    orgHeld.epoch === epoch &&
    (atOrAbove ? version < orgHeld.version : version <= orgHeld.version)
  ) {
    return false;
  }
  orgHeld = { version, epoch };
  orgSignInStatus.value = status;
  return true;
}

/** The registry's status rows, ordered by the `mcp_status` version: its `mcp_status` frames and
 *  `GET /api/mcp/status` are the only writers. The organization sign-in rides the same version. */
const mcpStatus = createMirror<string, MCPServerStatus>({
  kind: "mcp_status",
  async fetch(signal) {
    const d = await apiGetTyped("/api/mcp/status", decodeMCPStatusResponse, signal);
    if (d?.subject === undefined) {
      return null;
    }
    orgFetched = { status: d.identity_provider, stamp: d.subject };
    return { rows: new Map(d.servers.map((s) => [s.name, s])), stamp: d.subject };
  },
  onFetched() {
    if (orgFetched !== null) {
      takeOrg(orgFetched.status, orgFetched.stamp, true);
      orgFetched = null;
    }
  },
});

/** The saved organization identity provider (GET /api/mcp/identity-provider), null while none is. */
export interface IdentityProviderConfig {
  issuer: string;
  client_id: string;
  name?: string;
  authorization_endpoint?: string;
  token_endpoint?: string;
}

/** Whether the provider read has answered, and what it held. */
export interface IdentityProviderRead {
  readonly loaded: boolean;
  readonly provider: IdentityProviderConfig | null;
  /** The record holds a provider marotte could not read, which a save replaces. */
  readonly unreadable: boolean;
}

export const identityProvider: Signal<IdentityProviderRead> = signal({
  loaded: false,
  provider: null,
  unreadable: false,
});

/** Narrow GET /api/mcp/identity-provider's body. Exported for its test. */
export function decodeIdentityProviderBody(v: unknown): Omit<IdentityProviderRead, "loaded"> {
  const o = asObject(v, "$.idp");
  const unreadable = optBool(o, "unreadable", "$.idp") ?? false;
  const raw = o["identity_provider"];
  if (raw === null || raw === undefined) {
    return { provider: null, unreadable };
  }
  const p = asObject(raw, "$.idp.identity_provider");
  const at = "$.idp.identity_provider";
  const provider: IdentityProviderConfig = {
    issuer: reqStr(p, "issuer", at),
    client_id: reqStr(p, "client_id", at),
  };
  for (const key of ["name", "authorization_endpoint", "token_endpoint"] as const) {
    const val = optStr(p, key, at);
    if (val !== undefined && val !== "") {
      provider[key] = val;
    }
  }
  return { provider, unreadable };
}

/** The name a sign-in prompt gives the organization: the saved name, else kiro-cli's display name, else the issuer.
 *  Reactive inside an effect. */
export function orgProviderName(): string {
  const cfg = identityProvider.value.provider;
  const reported = orgSignInStatus.value?.reported;
  for (const n of [cfg?.name, reported?.display_name, cfg?.issuer, reported?.issuer]) {
    if (n !== undefined && n.trim() !== "") {
      return n.trim();
    }
  }
  return ORG_NAME_FALLBACK;
}

/** What orgProviderName answers while neither marotte nor kiro-cli knows a name. */
export const ORG_NAME_FALLBACK = "your organization";

function idleStatus(name: string): RuntimeStatus {
  return { name, origin: "user", state: "idle" };
}

/** kiro-cli's standing for a server it loaded but another layer replaced or switched off, undefined while the server
 *  is in use. Distinct from marotte's own Enabled toggle. Reactive inside an effect. */
export function standingFor(name: string): ConfigStanding | undefined {
  return configStanding.value.servers.get(name);
}

const statusSignals = new Map<string, ReadonlySignal<RuntimeStatus>>();

/** Reactive per-server runtime status, idle while the server has no row. */
export function statusSignalFor(name: string): ReadonlySignal<RuntimeStatus> {
  let sig = statusSignals.get(name);
  if (sig === undefined) {
    const row = mcpStatus.watch(name);
    sig = computed(() => {
      const r = row.value;
      return r === undefined ? idleStatus(name) : adaptStatus(r);
    });
    statusSignals.set(name, sig);
  }
  return sig;
}

const EMPTY_DISCOVERY: ServerDiscovery = {
  tools: [],
  prompts: [],
  resources: [],
  resource_templates: [],
};
const discoverySignals = new Map<string, ReadonlySignal<ServerDiscovery>>();

function discoveryOf(row: MCPServerStatus | undefined): ServerDiscovery {
  const tools = row?.tools ?? [];
  const prompts = row?.prompts ?? [];
  const resources = row?.resources ?? [];
  const templates = row?.resource_templates ?? [];
  if (
    tools.length === 0 &&
    prompts.length === 0 &&
    resources.length === 0 &&
    templates.length === 0
  ) {
    // The shared frozen empty value, so idle servers do not churn their readers.
    return EMPTY_DISCOVERY;
  }
  return { tools, prompts, resources, resource_templates: templates };
}

/** Reactive per-server discovery (empty while the server advertises nothing), from its status row. */
export function discoverySignalFor(name: string): ReadonlySignal<ServerDiscovery> {
  let sig = discoverySignals.get(name);
  if (sig === undefined) {
    const row = mcpStatus.watch(name);
    sig = computed(() => discoveryOf(row.value));
    discoverySignals.set(name, sig);
  }
  return sig;
}

export type PrewarmState = "none" | "installing" | "failed";

const prewarmMap = new SignalMap<PrewarmState>();

/** Reactive per-server prewarm-state signal (created lazily as "none"). */
export function prewarmSignalFor(id: string): ReadonlySignal<PrewarmState> {
  return prewarmMap.ensure(id, "none");
}

/** Set a server's prewarm state ("done" clears the badge -> "none"). */
export function setPrewarm(id: string, state: MCPPrewarmState): void {
  prewarmMap.ensure(id, "none").value = state === "done" ? "none" : state;
}

/** The configured MCP servers: field changes fire `signalFor(id)`, add/remove/reorder fire `ids`. */
export const servers: Collection<Server> = createCollection<Server>((s) => s.id);

function sameNames(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((n, i) => n === b[i]);
}

/** The status rows a foreign server needs its own row for, sorted. */
function foreignNames(
  rows: readonly (readonly [string, MCPServerStatus])[],
  configured: readonly Server[],
): string[] {
  const names = new Set(configured.map((s) => s.name));
  const out: string[] = [];
  for (const [name, row] of rows) {
    // A foreign server gets its own row unless marotte's config has the name, except when it SHADOWS that name: then
    // marotte's row is not in use and the running server needs its own.
    if (adaptOrigin(row.origin) !== "user" && (!names.has(name) || row.shadows === true)) {
      out.push(name);
    }
  }
  return out.sort();
}

/**
 * Names the status rows hold that the config list does not (a Power's server, or an unreadable config), sorted, and
 * recomputed when either input moves. Such a server has no persisted id, so its rows are read-only.
 */
export const unconfiguredNames: ReadonlySignal<readonly string[]> = computed(
  () => foreignNames(mcpStatus.entries(), servers.items()),
  // An equal list must not rebuild every read-only row.
  { equals: sameNames },
);

/** How many entries of marotte's record could not be read at boot, from GET /api/mcp. */
export const unreadableEntries: Signal<number> = signal(0);

/** Snapshot of the configured servers (non-reactive). */
export function configuredServers(): Server[] {
  return servers.items();
}

class MCPStateController {
  private readonly serversSlot = new CancellableSlot();
  private serversFetchPending = false;

  abort(): void {
    this.serversSlot.abort();
    this.serversFetchPending = false;
  }

  /** Apply one `mcp_status` frame (an absent row: the server left the inventory). Answers the
   *  change, or `undefined` when the frame is not newer than the row held. */
  applyStatusFrame(
    name: string,
    row: MCPServerStatus | undefined,
    stamp: SubjectStamp | undefined,
  ): MirrorChange<string, MCPServerStatus> | undefined {
    return mcpStatus.applyFrame(name, row, stamp);
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
    unreadableEntries.value = d?.unreadable ?? 0;
  }

  /** Read the status inventory again (`Mirror.refetch`). */
  refetchStatus(signal?: AbortSignal): Promise<void> {
    return mcpStatus.refetch(signal);
  }

  /** Apply one `mcp_identity_provider` frame; a frame not newer than the held state is refused. */
  applyOrgFrame(status: MCPIdentityProviderStatus, stamp: SubjectStamp | undefined): void {
    if (!takeOrg(status, stamp, false) && stamp?.kind !== "mcp_status") {
      void mcpStatus.refetch();
    }
  }

  /** Read the saved identity provider again. */
  async refetchIdentityProvider(): Promise<void> {
    const d = await apiGetTyped("/api/mcp/identity-provider", decodeIdentityProviderBody);
    if (d !== null) {
      identityProvider.value = { loaded: true, ...d };
    }
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
