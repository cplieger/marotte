// One bindList over `servers` (keyed by id), so a row element persists across changes and keeps toggle focus. Each
// row's one effect reads its server, status and prewarm signals and patches the row surgically.

import { el, bindList, effect, signal } from "@cplieger/reactive";

import { $, setControlBusy } from "./dom.js";
import { reconcile } from "./reconcile.js";
import { sigChanged, wireSignature } from "./paint-sig.js";
import { isSafeURL } from "./url-safety.js";
import { onSSE } from "./bus.js";
import { GOVERNANCE_UNAVAILABLE, governanceReasonKind, onGovernanceChange } from "./governance.js";
import type { GovernanceMCPRegistry, GovernanceStatePayload } from "./types.js";
import { onModalClose, openModal } from "./modals.js";
import { confirm as confirmDialog } from "./confirm.js";
import { showToast } from "./toast.js";
import { ICON_EDIT_UI, ICON_TRASH_UI, ICON_PLUS_UI, ICON_REFRESH, ICON_SPINNER } from "./icons.js";
import {
  type Server,
  type KeyPair,
  type RuntimeStatus,
  type RuntimeState,
  type PrewarmState,
  type ServerDiscovery,
  type MCPPromptInfo,
  type MCPPromptArg,
  type MCPResourceInfo,
  servers,
  unconfiguredNames,
  mcpState,
  statusSignalFor,
  prewarmSignalFor,
  discoverySignalFor,
  setPrewarm,
  configuredServers,
} from "./mcp-state.js";
import { setEditing, initModal, cleanupModal } from "./mcp-panels.js";
import type { MCPOrigin } from "./wire/types.gen.js";
import { editModeFor, extractNpxPackage } from "./mcp-panels.js";
import {
  toggleServer,
  deleteServer,
  openEdit,
  saveServer,
  reconnectServer,
  relayOAuthCallback,
  getPromptContent,
  getResourceContent,
} from "./actions/mcp.js";
import { promptResultToText, resourceResultToText } from "./mcp-content.js";
import { bindLoadingState, isPending, registerCleanup } from "./actions/index.js";

let sectionBody: HTMLDivElement | null = null;
let foreignBody: HTMLDivElement | null = null;
let emptyMsg: HTMLParagraphElement | null = null;
let listBound = false;
let foreignBound = false;
// Held so the governance effect can disable it and toggle the notice.
let addBtn: HTMLButtonElement | null = null;
let govDisabledMsg: HTMLParagraphElement | null = null;

// Per-row cleanups (the row effect and its loading bindings), disposed when the row leaves.
const rowCleanups = new Map<string, (() => void)[]>();
registerCleanup(() => {
  for (const cs of rowCleanups.values()) {
    for (const fn of cs) {
      fn();
    }
  }
  rowCleanups.clear();
});

function buildSectionScaffold(): void {
  const section = document.getElementById("mcp-section");
  if (section === null) {
    return;
  }

  const title = el("h2", { className: "section-title" }, "MCP integrations");

  const hint = el(
    "p",
    { className: "section-hint" },
    "Connect your agent to external systems: GitHub, Linear, Postgres, Sentry, or anything else that speaks the Model Context Protocol. Changes apply immediately, including to chats already running. Disabled servers are kept on disk but do not consume context tokens or spawn subprocesses. For servers that need a local package, such as a pip or npm package or a binary, install it first with Add tool in the Tools section above, then fill in credentials here.",
  );

  const btn = el("button", {
    type: "button",
    className: "action-pill",
    "data-tooltip": "Connect integration",
    "aria-label": "Connect integration",
  }) as HTMLButtonElement;
  btn.innerHTML = ICON_PLUS_UI;
  btn.addEventListener("click", () => {
    openAddModal();
  });
  addBtn = btn;

  const actions = el("div", { className: "action-bar action-bar-inline" }, btn);
  const actionRow = el("div", { className: "section-actions-row" }, actions);

  govDisabledMsg = el("p", {
    className: "mcp-gov-disabled",
    hidden: true,
  }) as HTMLParagraphElement;
  const registryPanel = el("div", { className: "mcp-registry-panel hidden" }) as HTMLDivElement;
  bindRegistryPanel(registryPanel);

  emptyMsg = el("p", { className: "mcp-empty" }, EMPTY_DEFAULT) as HTMLParagraphElement;
  sectionBody = el("div", { className: "mcp-server-list" }) as HTMLDivElement;
  // A separate container: these servers have no persisted id, and every mutation path on the configured collection
  // addresses a record that does not exist for them.
  foreignBody = el("div", { className: "mcp-server-list mcp-foreign-list" }) as HTMLDivElement;

  section.replaceChildren(
    title,
    hint,
    actionRow,
    govDisabledMsg,
    registryPanel,
    emptyMsg,
    sectionBody,
    foreignBody,
  );
  bindServerList();
  bindForeignList();
}

// Holds the section's elements, keeping applyMcpGovernance pure and testable.
function applyGovernance(g: GovernanceStatePayload): void {
  applyMcpRowPolicy(g);
  if (addBtn !== null && govDisabledMsg !== null && emptyMsg !== null) {
    applyMcpGovernance(g, { add: addBtn, notice: govDisabledMsg, empty: emptyMsg });
  }
}

/** What the organization decides about the configured rows and the registry panel. */
interface RowPolicy {
  /** Why MCP is blocked, while it is; the rows are read-only. */
  readonly lockReason: string | undefined;
  /** Configured servers the organization's registry does not allow. */
  readonly filtered: ReadonlySet<string>;
  /** The registry MCP is restricted to, while it is. */
  readonly registry: GovernanceMCPRegistry | undefined;
}

const rowPolicy = signal<RowPolicy>({
  lockReason: undefined,
  filtered: new Set(),
  registry: undefined,
});

const NOT_IN_REGISTRY = "Not allowed by your organization's MCP registry";
const MCP_ORG_OFF = "MCP is disabled by your organization";
const MCP_UNAVAILABLE = "MCP is unavailable";

/** Publish the row policy every configured row reads. Exported for testing. */
export function applyMcpRowPolicy(g: GovernanceStatePayload): void {
  rowPolicy.value = {
    lockReason: mcpBlockReason(g),
    filtered: new Set(g.mcp_registry?.filtered ?? []),
    registry: g.mcp_registry,
  };
}

/**
 * The registry panel's one writer, repainting on a policy change and on a configured-list change, so neither drops the
 * other's block. Exported for testing.
 */
export function bindRegistryPanel(host: HTMLElement): () => void {
  return effect(() => {
    const policy = rowPolicy.value;
    const configured = servers.ids.value.length === 0 ? [] : configuredServers();
    renderRegistryPanel(host, policy.registry, configured, addRegistryServer, policy.lockReason);
  });
}

/** The account profile turning MCP off outranks an administrator's deny, as in the notice. */
function mcpBlockReason(g: GovernanceStatePayload): string | undefined {
  if (g.known && !g.features.mcp_enabled) {
    return governanceReasonKind(g.disabled_reason) === "unavailable"
      ? MCP_UNAVAILABLE
      : MCP_ORG_OFF;
  }
  return g.locks?.["mcp"]?.reason;
}

/** A `{"type":"registry"}` entry; KAS resolves the rest. */
async function addRegistryServer(name: string): Promise<void> {
  const o = await saveServer.dispatch({
    id: "",
    body: { name, transport: "registry", enabled: true },
  }).outcome;
  if (o.status !== "success") {
    return;
  }
  mcpState.refetchServers();
  mcpState.refetchStatus();
}

/**
 * Render the organization's registry: allowed catalog servers with Add, plus dropped servers with no row of their own
 * and entries the catalog lacks. Hidden when not restricted; Add disabled while `lockReason` is set. Exported for
 * testing.
 */
export function renderRegistryPanel(
  host: HTMLElement,
  reg: GovernanceMCPRegistry | undefined,
  configured: readonly Server[],
  onAdd: (name: string) => Promise<void>,
  lockReason?: string,
): void {
  host.classList.toggle("hidden", reg === undefined);
  if (reg === undefined) {
    host.replaceChildren();
    return;
  }
  const have = new Set(configured.map((s) => s.name));
  const children: HTMLElement[] = [
    el(
      "p",
      { className: "mcp-gov-disabled" },
      "Your organization limits MCP to its registry: only the servers below can run, and any other server you configure stays off.",
    ),
  ];
  const list = el("ul", { className: "mcp-registry-list" });
  for (const srv of reg.servers) {
    const added = have.has(srv.name);
    const locked = lockReason !== undefined;
    const btn = el(
      "button",
      { type: "button", className: "btn-small", disabled: added || locked },
      added ? "Added" : "Add",
    ) as HTMLButtonElement;
    btn.setAttribute("aria-label", added ? `${srv.name} is added` : `Add ${srv.name}`);
    if (lockReason !== undefined && !added) {
      btn.dataset["tooltip"] = lockReason;
    }
    btn.addEventListener("click", () => {
      btn.disabled = true;
      void onAdd(srv.name).finally(() => {
        btn.disabled = have.has(srv.name) || locked;
      });
    });
    const text = el("div", { className: "mcp-registry-text" }, el("strong", null, srv.name));
    if (srv.description !== undefined && srv.description !== "") {
      text.appendChild(el("span", { className: "section-hint" }, srv.description));
    }
    list.appendChild(el("li", { className: "mcp-registry-row" }, text, btn));
  }
  if (reg.servers.length === 0) {
    children.push(el("p", { className: "section-hint" }, "Its catalog lists no servers."));
  } else {
    children.push(list);
  }
  const rowless = reg.filtered.filter((name) => !have.has(name));
  if (rowless.length > 0) {
    children.push(
      el("p", { className: "section-hint" }, `${NOT_IN_REGISTRY}: ${rowless.join(", ")}.`),
    );
  }
  if (reg.unresolved.length > 0) {
    children.push(
      el(
        "p",
        { className: "section-hint" },
        `Not in your organization's catalog: ${reg.unresolved.join(", ")}.`,
      ),
    );
  }
  host.replaceChildren(...children);
}

/**
 * The three elements the MCP policy decides: the add affordance, the notice saying why it is off, and the empty state
 * naming that affordance. Named, since a positional swap would be silent.
 */
export interface McpGovernanceEls {
  add: HTMLButtonElement;
  notice: HTMLElement;
  empty: HTMLElement;
}

/**
 * Apply the MCP governance policy. Known and off: the add affordance is disabled, the notice says why, and the empty
 * state stops naming it. Unknown leaves it enabled. Registry mode hides it (KAS would discard what it adds). Exported
 * for testing.
 */
export function applyMcpGovernance(g: GovernanceStatePayload, els: McpGovernanceEls): void {
  // An administrator's deny on mcp blocks MCP on any account.
  const adminLock = g.locks?.["mcp"];
  const orgOff = g.known && !g.features.mcp_enabled;
  const disabled = orgOff || adminLock !== undefined;
  const unavailable = orgOff && governanceReasonKind(g.disabled_reason) === "unavailable";
  const registry = g.mcp_registry !== undefined;
  els.add.classList.toggle("hidden", registry);
  els.add.disabled = disabled;
  els.add.setAttribute("data-tooltip", mcpBlockReason(g) ?? "Connect integration");
  els.notice.hidden = !disabled;
  if (disabled) {
    els.notice.textContent =
      !orgOff && adminLock !== undefined
        ? `${adminLock.reason}.`
        : unavailable
          ? `MCP integrations are unavailable. ${GOVERNANCE_UNAVAILABLE}`
          : "MCP integrations are disabled by your organization.";
  }
  els.empty.textContent = disabled ? EMPTY_GOV_OFF : registry ? EMPTY_REGISTRY : EMPTY_DEFAULT;
}

const EMPTY_DEFAULT =
  "No integrations connected yet. Click + to search the official MCP registry or paste a config.";
const EMPTY_GOV_OFF = "No integrations connected, and none can be added while MCP is disabled.";
const EMPTY_REGISTRY =
  "No integrations connected yet. Add one from your organization's registry above.";

function bindServerList(): void {
  if (listBound || sectionBody === null) {
    return;
  }
  listBound = true;
  const empty = emptyMsg;
  bindList(sectionBody, servers, {
    mount: (s, id) => mountRow(s, id),
    onRemove: (_el, id) => {
      cleanupRow(id);
    },
  });
  // Empty-state toggle, a sibling of the bindList container.
  effect(() => {
    if (empty !== null) {
      empty.hidden = servers.ids.value.length > 0;
    }
  });
}

function cleanupRow(id: string): void {
  const cs = rowCleanups.get(id);
  if (cs !== undefined) {
    for (const fn of cs) {
      fn();
    }
    rowCleanups.delete(id);
  }
}

/** Read-only rows for servers this page does not configure, mirroring the configured list's shape. */
function bindForeignList(): void {
  if (foreignBound || foreignBody === null) {
    return;
  }
  foreignBound = true;
  const host = foreignBody;
  registerCleanup(
    effect(() => {
      const names = unconfiguredNames.value;
      // Keyed by name: a row holds a `<details>` the reader can open, and this effect re-runs on any status move.
      // `onRemove` disposes a departing row's effect.
      reconcile(host, names, {
        key: (name: string) => name,
        mount: (name: string) => mountForeignRow(name),
        onRemove: (_el: HTMLElement, name: string) => {
          foreignRowCleanups.get(name)?.();
          foreignRowCleanups.delete(name);
        },
      });
      host.hidden = names.length === 0;
    }),
  );
}

/** Its own map, keyed by name where `rowCleanups` is keyed by id. */
const foreignRowCleanups = new Map<string, () => void>();
registerCleanup(() => {
  for (const stop of foreignRowCleanups.values()) {
    stop();
  }
  foreignRowCleanups.clear();
});

/**
 * One read-only row for a server KAS runs that marotte's store does not hold: dot, name, provenance chip, discovery,
 * and Reconnect and sign-in while live. No toggle, edit or delete. Exported for testing.
 */
export function mountForeignRow(name: string): HTMLElement {
  const dot = el("span", { className: "mcp-dot", role: "img" });
  const nameEl = el("span", { className: "mcp-row-name" }, name);
  const originChip = buildOriginChip();
  const metaText = el("span", { className: "mcp-row-meta-text" });
  const discoveryBox = el("div", { className: "mcp-discovery" }) as HTMLDivElement;
  const body = el(
    "div",
    { className: "mcp-row-body" },
    el("div", { className: "mcp-row-name-line" }, dot, nameEl),
    el("div", { className: "mcp-row-meta" }, originChip, metaText),
    discoveryBox,
  );
  const reconnectBtn = renderReconnectBtn(name, () => name);
  const actions = el("div", { className: "mcp-row-actions" }, reconnectBtn);
  const row = el(
    "div",
    { className: "mcp-row mcp-row-readonly", "data-server-name": name },
    body,
    actions,
  ) as HTMLDivElement;

  const syncOAuth = oauthControls(body, () => name);

  // A nested effect is not disposed by its enclosing effect, so the list holds its stop function and `onRemove` calls it.
  foreignRowCleanups.get(name)?.();
  foreignRowCleanups.set(
    name,
    effect(() => {
      const st = statusSignalFor(name).value;
      applyStatusDotForeign(dot, name, st);
      applyOriginChip(originChip, st);
      metaText.textContent = renderForeignMeta(st);
      const live = carriesLiveReport(st, false);
      reconnectBtn.hidden = !live;
      syncOAuth(st, live);
      renderDiscovery(discoveryBox, name, discoverySignalFor(name).value, true);
    }),
  );
  return row;
}

/** No config record here, so the reported state is the only input. */
function applyStatusDotForeign(dot: HTMLSpanElement, name: string, st: RuntimeStatus): void {
  dot.className = "mcp-dot";
  const meta = STATUS_META[st.state] ?? STATUS_META.idle; // eslint-disable-line @typescript-eslint/no-unnecessary-condition
  dot.classList.add(meta.css);
  if (isFailedWithError(st)) {
    dot.dataset["tooltip"] = `Failed to initialise: ${st.error}`;
    dot.setAttribute("aria-label", `${name} failed. ${st.error}`);
    return;
  }
  dot.dataset["tooltip"] = meta.title;
  dot.setAttribute("aria-label", `${name}: ${meta.title.toLowerCase()}`);
}

/** Meta text for a read-only row. Exported for testing. */
export function renderForeignMeta(st: RuntimeStatus): string {
  if (isFailedWithError(st)) {
    return `Failed to start. ${st.error}`;
  }
  switch (st.state) {
    case "connected":
      return "Connected. Its tools are available to the agent";
    case "needs_auth":
      return "Waiting for sign-in";
    case "disabled":
      return "Disabled. The agent is not using it";
    default:
      return "Not connected";
  }
}

/**
 * One row for a server marotte's store holds. Edit, delete and the toggle unless an administrator denies MCP;
 * Reconnect and sign-in follow the live report. Exported for testing.
 */
export function mountRow(s: Server, id: string): HTMLElement {
  const cleanups: (() => void)[] = [];

  // Built once; the effect syncs only `.checked`, so focus survives.
  const input = el("input", { type: "checkbox" }) as HTMLInputElement;
  input.addEventListener("change", () => {
    input.setAttribute("aria-label", `${input.checked ? "Disable" : "Enable"} ${s.name}`);
    void toggleServer.dispatch(
      { id, enabled: input.checked },
      {
        silent: true,
        onSuccess: () => {
          mcpState.refetchServers();
          // A toggle changes what KAS runs, so status is re-read; both fetches coalesce per microtask.
          mcpState.refetchStatus();
        },
      },
    );
  });
  // A settled toggle returns to the lock's answer, not to enabled.
  cleanups.push(
    bindLoadingState("mcp.toggle_server", input, {
      disabledFn: () => rowPolicy.peek().lockReason !== undefined,
    }),
  );
  const toggle = el(
    "label",
    { className: "toggle mcp-toggle" },
    input,
    el("span", { className: "toggle-slider" }),
  );

  const dot = el("span", { className: "mcp-dot", role: "img" });
  const nameEl = el("span", { className: "mcp-row-name" });
  const transportBadge = el("span", { className: "mcp-transport" });
  const nameLine = el("div", { className: "mcp-row-name-line" }, dot, nameEl, transportBadge);
  const metaText = el("span", { className: "mcp-row-meta-text" });
  const meta = el("div", { className: "mcp-row-meta" }, metaText);
  const body = el("div", { className: "mcp-row-body" }, nameLine, meta);

  const reconnectBtn = renderReconnectBtn(s.name, () => (servers.signalFor(id)?.value ?? s).name);
  const editBtn = renderEditBtn(s, cleanups);
  const deleteBtn = renderDeleteBtn(s, cleanups);
  const actions = el("div", { className: "mcp-row-actions" }, reconnectBtn, editBtn, deleteBtn);

  // Its own effect, re-rendering only when this server's discovery changes.
  const discoveryBox = el("div", { className: "mcp-discovery" }) as HTMLDivElement;
  body.appendChild(discoveryBox);
  cleanups.push(
    effect(() => {
      const cur = servers.signalFor(id)?.value ?? s;
      const disc = discoverySignalFor(cur.name).value;
      // A shadowed name's discovery belongs to the running server's own read-only row.
      const live = carriesLiveReport(statusSignalFor(cur.name).value, true);
      renderDiscovery(discoveryBox, cur.name, disc, live);
    }),
  );

  const row = el(
    "div",
    { className: "mcp-row", "data-server-id": id },
    toggle,
    body,
    actions,
  ) as HTMLDivElement;

  // Patch surgically, never replaceChildren, so focus and identity are kept.
  const syncOAuth = oauthControls(body, () => (servers.signalFor(id)?.value ?? s).name);
  let prewarmBadge: HTMLSpanElement | null = null;
  cleanups.push(
    effect(() => {
      const cur = servers.signalFor(id)?.value ?? s;
      const st = statusSignalFor(cur.name).value;
      const pw = prewarmSignalFor(id).value;
      const policy = rowPolicy.value;
      const notInRegistry = policy.filtered.has(cur.name);
      const lockReason = policy.lockReason;

      input.checked = cur.enabled;
      input.setAttribute("aria-label", `${cur.enabled ? "Disable" : "Enable"} ${cur.name}`);
      if (!isPending("mcp.toggle_server")) {
        input.disabled = lockReason !== undefined;
      }
      if (lockReason === undefined) {
        toggle.removeAttribute("data-tooltip");
      } else {
        toggle.dataset["tooltip"] = lockReason;
      }
      editBtn.classList.toggle("hidden", cur.transport === "registry" || lockReason !== undefined);
      deleteBtn.classList.toggle("hidden", lockReason !== undefined);
      row.classList.toggle("mcp-row-readonly", lockReason !== undefined);
      nameEl.textContent = cur.name;
      transportBadge.className = `mcp-transport mcp-transport-${cur.transport}`;
      transportBadge.textContent = cur.transport;
      applyStatusDot(dot, cur, st, notInRegistry);
      metaText.textContent = renderMeta(cur, st, notInRegistry);

      const live = carriesLiveReport(st, true);
      reconnectBtn.hidden = !live;
      syncOAuth(st, live);

      prewarmBadge = applyPrewarm(prewarmBadge, nameEl, pw);
    }),
  );

  rowCleanups.set(id, cleanups);
  return row;
}

/**
 * Whether a row shows a server KAS is running, which Reconnect, sign-in and discovery act on. A config row carries one
 * only for a `user` status. Exported for testing.
 */
export function carriesLiveReport(st: RuntimeStatus, ownRow: boolean): boolean {
  if (ownRow && st.origin !== "user") {
    return false;
  }
  return st.state !== "idle" && st.state !== "disabled";
}

/** Shared by both row kinds: a server awaiting sign-in may be on either. */
function oauthControls(
  body: HTMLElement,
  nameOf: () => string,
): (st: RuntimeStatus, live: boolean) => void {
  let pill: HTMLAnchorElement | null = null;
  let pillUrl: string | null = null;
  let relay: HTMLDetailsElement | null = null;
  return (st, live) => {
    if (!live || st.state !== "needs_auth") {
      pill?.remove();
      pill = null;
      pillUrl = null;
      relay?.remove();
      relay = null;
      return;
    }
    if (pill === null || pillUrl !== st.oauth_url) {
      pill?.remove();
      pill = renderOAuthPill(st.oauth_url);
      pillUrl = st.oauth_url;
      body.appendChild(pill);
    }
    // A relayed code is spent, so a second paste can only fail.
    if (st.relayed) {
      relay?.remove();
      relay = null;
    } else if (relay === null) {
      relay = renderOAuthRelay(nameOf());
      body.appendChild(relay);
    }
  };
}

const STATUS_META: Readonly<Record<RuntimeState, { css: string; title: string }>> = {
  connected: { css: "connected", title: "Connected" },
  needs_auth: { css: "oauth", title: "Needs authentication" },
  idle: { css: "idle", title: "Not connected because no chat is running" },
  failed: { css: "failed", title: "Failed to initialise" },
  disabled: { css: "disabled", title: "Disabled" },
};

/** The provenance a row's chip and copy read off a status. */
type RowProvenance = Pick<RuntimeStatus, "origin" | "originRoot" | "originPower">;

/** `user` has no entry: the config list already owns that row, and a chip on every row means nothing. */
const ORIGIN_META: Readonly<
  Record<Exclude<MCPOrigin, "user">, (p: RowProvenance) => { label: string; title: string }>
> = {
  workspace: (p) => ({
    label: "from the workspace config",
    title: `Defined in ${p.originRoot ?? "a workspace"}/.kiro/settings/mcp.json. Edit it there; this page cannot edit or remove it.`,
  }),
  power: (p) => ({
    label: p.originPower === undefined ? "from a Power" : `from the ${p.originPower} Power`,
    title:
      "An installed Power contributed this server. Manage it where the Power is installed. This page cannot edit or remove it.",
  }),
  bundled: () => ({
    label: "bundled with Kiro",
    title: "Kiro ships this server, so this page cannot edit or remove it.",
  }),
  unknown: () => ({
    label: "not managed here",
    title:
      "The agent reported this server, but it is not in this page's configuration. It comes from a config marotte does not manage, so it cannot be edited or removed here.",
  }),
};

function buildOriginChip(): HTMLSpanElement {
  return el("span", { className: "mcp-origin", hidden: true });
}

/** Show or hide the provenance chip for a status's origin. Exported for testing. */
export function applyOriginChip(chip: HTMLSpanElement, p: RowProvenance): void {
  if (p.origin === "user") {
    chip.hidden = true;
    chip.textContent = "";
    chip.removeAttribute("data-tooltip");
    return;
  }
  const meta = ORIGIN_META[p.origin](p);
  chip.hidden = false;
  chip.textContent = meta.label;
  chip.dataset["tooltip"] = meta.title;
}

/** Who defines the server running under a name marotte holds. */
function shadowSource(p: RowProvenance): string {
  switch (p.origin) {
    case "workspace":
      return "the workspace config";
    case "power":
      return p.originPower === undefined ? "a Power" : `the ${p.originPower} Power`;
    case "bundled":
      return "Kiro's bundled config";
    default:
      return "another config";
  }
}

function isFailedWithError(
  st: RuntimeStatus,
): st is RuntimeStatus & { state: "failed"; error: string } {
  const FAILED: RuntimeState = "failed";
  return st.state === FAILED && st.error !== "";
}

function applyStatusDot(
  dot: HTMLSpanElement,
  s: Server,
  st: RuntimeStatus,
  notInRegistry: boolean,
): void {
  dot.className = "mcp-dot";
  if (st.origin !== "user") {
    dot.classList.add(STATUS_META.idle.css);
    dot.dataset["tooltip"] = "Not in use";
    dot.setAttribute("aria-label", `${s.name}: not in use`);
    return;
  }
  if (notInRegistry) {
    dot.classList.add("disabled");
    dot.dataset["tooltip"] = NOT_IN_REGISTRY;
    dot.setAttribute("aria-label", `${s.name}: not allowed by the MCP registry`);
    return;
  }
  if (!s.enabled) {
    dot.classList.add("disabled");
    dot.dataset["tooltip"] = "Disabled";
    dot.setAttribute("aria-label", `${s.name}: disabled`);
    return;
  }
  const meta = STATUS_META[st.state] ?? STATUS_META.idle; // eslint-disable-line @typescript-eslint/no-unnecessary-condition
  dot.classList.add(meta.css);
  if (isFailedWithError(st)) {
    dot.dataset["tooltip"] = `Failed to initialise: ${st.error}`;
    dot.setAttribute("aria-label", `${s.name} failed. ${st.error}`);
  } else {
    dot.dataset["tooltip"] = meta.title;
    dot.setAttribute("aria-label", `${s.name}: ${meta.title.toLowerCase()}`);
  }
}

/**
 * `collectKeyPairs` skips a row with an empty trimmed name and the editor seeds one blank row, so a bare count would
 * report a credential nobody entered.
 */
function namedPairs(pairs: readonly KeyPair[] | undefined): number {
  return (pairs ?? []).filter((p) => p.name.trim() !== "").length;
}

/**
 * What credentials this server carries. Read off the masked record (pair names and slice length survive, oauth
 * members are unmasked), reporting what the record holds whatever the transport uses.
 */
export function credentialSummary(s: Server): string {
  const parts: string[] = [];
  const env = namedPairs(s.env);
  if (env > 0) {
    parts.push(env === 1 ? "1 environment variable" : `${env} environment variables`);
  }
  const headers = namedPairs(s.headers);
  if (headers > 0) {
    parts.push(headers === 1 ? "1 header" : `${headers} headers`);
  }
  if ((s.oauth_client_id ?? "") !== "" || (s.oauth_client_metadata_url ?? "") !== "") {
    parts.push("OAuth client configured");
  }
  return parts.length === 0 ? "no credentials" : parts.join(", ");
}

/** `connected` and `idle` get none: the dot says so, and a phrase would push the rest out of the track. */
function statePhrase(st: RuntimeStatus): string {
  if (st.state === "failed") {
    return st.error === "" ? "Failed to start" : `Failed to start. ${st.error}`;
  }
  if (st.state === "needs_auth") {
    return "Waiting for sign-in";
  }
  return "";
}

/**
 * The row's meta line: state phrase, credentials, then the source, because the line ellipsises and the source is the
 * long segment. Exported for its order test.
 */
export function renderMeta(s: Server, st: RuntimeStatus, notInRegistry = false): string {
  if (st.origin !== "user") {
    return `Not in use: ${shadowSource(st)} defines a server with this name`;
  }
  if (notInRegistry) {
    return NOT_IN_REGISTRY;
  }
  if (!s.enabled) {
    return "Disabled";
  }
  const source = s.transport === "stdio" ? (s.command ?? "") : (s.url ?? "");
  return [statePhrase(st), credentialSummary(s), source].filter((p) => p !== "").join(" · ");
}

/** Returns the badge, or null when cleared, so the caller tracks it. */
function applyPrewarm(
  badge: HTMLSpanElement | null,
  nameEl: HTMLElement,
  pw: PrewarmState,
): HTMLSpanElement | null {
  if (pw === "none") {
    badge?.remove();
    return null;
  }
  let b = badge;
  if (b === null) {
    b = el("span", { className: "prewarm-badge" });
    nameEl.after(b);
  }
  b.textContent = pw === "installing" ? "Installing…" : "Install failed";
  b.classList.toggle("prewarm-failed", pw !== "installing");
  return b;
}

function renderOAuthPill(url: string): HTMLAnchorElement {
  const safe = isSafeURL(url);
  return el(
    "a",
    {
      className: "mcp-oauth-pill",
      href: safe ? url : "#",
      target: "_blank",
      rel: "noopener noreferrer",
      title: safe
        ? "Open the server's authorisation page in a new tab."
        : "The server sent an unsafe URL that is not http or https.",
    },
    safe ? "Finish sign-in" : "Invalid OAuth URL",
  ) as HTMLAnchorElement;
}

/**
 * The rescue box for a sign-in whose redirect landed on the wrong machine: KAS listens on the container's localhost,
 * so a remote browser's callback dies. Inline, beside the field the refusal names; collapsed by default.
 */
export function renderOAuthRelay(serverName: string): HTMLDetailsElement {
  const input = el("input", {
    type: "url",
    className: "mcp-relay-input",
    // No `required` or `pattern`: the server checks against the authorization URL it stored.
    placeholder: "http://localhost:1234/oauth/callback?code=…",
    "aria-label": "The address the sign-in page could not reach",
    autocomplete: "off",
    spellcheck: "false",
  }) as HTMLInputElement;

  const note = el("p", { className: "mcp-relay-note" }) as HTMLParagraphElement;
  const setNote = (text: string, kind: "err" | "ok" | "") => {
    note.textContent = text;
    note.classList.toggle("mcp-relay-err", kind === "err");
    note.classList.toggle("mcp-relay-ok", kind === "ok");
  };

  const submit = el(
    "button",
    { type: "button", className: "btn-small" },
    "Finish",
  ) as HTMLButtonElement;
  submit.addEventListener("click", () => {
    const pasted = input.value.trim();
    if (pasted === "") {
      setNote("Paste the address from the page that failed to load.", "err");
      return;
    }
    setControlBusy(submit, true);
    setNote("Delivering…", "");
    void relayOAuthCallback
      .dispatch(
        { server: serverName, redirect_url: pasted },
        {
          onSuccess: () => {
            // Delivered, not connected: the token exchange is KAS's to finish, and only `_kiro/mcp/status` reports it.
            setNote("Delivered. Waiting for the server to finish signing in…", "ok");
            input.value = "";
            mcpState.refetchStatus();
          },
          // Only the server can name which part was wrong; the client never sees the stored authorization URL.
          onError: (err) => {
            setNote(err.message, "err");
          },
        },
      )
      .finally(() => {
        setControlBusy(submit, false);
      });
  });

  const box = el(
    "details",
    { className: "mcp-relay" },
    el("summary", {}, "The sign-in page did not load?"),
    el(
      "p",
      { className: "mcp-relay-help" },
      "The sign-in redirects to an address only this container can reach. Copy the " +
        "whole address from the page that failed to load and paste it here.",
    ),
    el("div", { className: "mcp-relay-row" }, input, submit),
    note,
  ) as HTMLDetailsElement;
  return box;
}

function renderEditBtn(s: Server, cleanups: (() => void)[]): HTMLButtonElement {
  const btn = el("button", {
    type: "button",
    className: "icon-btn",
    "data-tooltip": "Edit",
    "aria-label": `Edit ${s.name}`,
  }) as HTMLButtonElement;
  btn.innerHTML = ICON_EDIT_UI;
  // Visibility is the row effect's: a registry entry has no form, and a locked row has no edit.
  btn.addEventListener("click", () => {
    void openEditModal(s.id);
  });
  cleanups.push(bindLoadingState("mcp.open_edit", btn));
  return btn;
}

function renderDeleteBtn(s: Server, cleanups: (() => void)[]): HTMLButtonElement {
  const btn = el("button", {
    type: "button",
    className: "icon-btn danger",
    "data-tooltip": "Remove",
    "aria-label": `Remove ${s.name}`,
  }) as HTMLButtonElement;
  btn.innerHTML = ICON_TRASH_UI;
  cleanups.push(bindLoadingState("mcp.delete_server", btn));
  btn.addEventListener("click", () => {
    void (async () => {
      const ok = await confirmDialog(
        `Remove "${s.name}"? The agent loses access to this integration immediately.`,
        "Remove",
        "destructive",
      );
      if (!ok) {
        return;
      }
      void deleteServer.dispatch(
        { id: s.id },
        {
          onSuccess: () => {
            mcpState.refetchServers();
            mcpState.refetchStatus();
          },
        },
      );
    })();
  });
  return btn;
}

function renderReconnectBtn(label: string, serverName: () => string): HTMLButtonElement {
  const btn = el("button", {
    type: "button",
    className: "icon-btn",
    "data-tooltip": "Reconnect",
    "aria-label": `Reconnect ${label}`,
  }) as HTMLButtonElement;
  btn.innerHTML = ICON_REFRESH;
  btn.addEventListener("click", () => {
    if (btn.disabled) {
      return;
    }
    setControlBusy(btn, true);
    btn.innerHTML = ICON_SPINNER;
    void reconnectServer
      .dispatch(
        { server: serverName() },
        {
          onSuccess: (res) => {
            // The reconnect re-emits _kiro/mcp/status on every bridge; refetch for the dot and discovery.
            mcpState.refetchStatus();
            if (res.reconnected === 0) {
              showToast("No active chat to reconnect through. Open a chat first.", "info");
            }
          },
        },
      )
      .finally(() => {
        setControlBusy(btn, false);
        btn.innerHTML = ICON_REFRESH;
      });
  });
  return btn;
}

function orFallback(primary: string, fallback: string): string {
  return primary !== "" ? primary : fallback;
}

/**
 * (Re)render the per-server prompts/resources disclosure; hidden when disabled or empty. Guarded: the box is a
 * `<details>` whose open state nothing else records, and both callers run on state that moves without it.
 */
function renderDiscovery(
  box: HTMLDivElement,
  serverName: string,
  disc: ServerDiscovery,
  enabled: boolean,
): void {
  if (!sigChanged(box, [serverName, enabled ? "1" : "", wireSignature(disc)])) {
    return;
  }
  box.replaceChildren();
  const count = disc.prompts.length + disc.resources.length;
  if (!enabled || count === 0) {
    box.hidden = true;
    return;
  }
  box.hidden = false;

  const details = el("details", { className: "mcp-discovery-details" });
  details.appendChild(
    el("summary", { className: "mcp-discovery-summary" }, `Prompts & resources (${String(count)})`),
  );
  if (disc.prompts.length > 0) {
    details.appendChild(el("div", { className: "mcp-disc-group" }, "Prompts"));
    for (const p of disc.prompts) {
      details.appendChild(buildPromptItem(serverName, p));
    }
  }
  if (disc.resources.length > 0) {
    details.appendChild(el("div", { className: "mcp-disc-group" }, "Resources"));
    for (const res of disc.resources) {
      details.appendChild(buildResourceItem(serverName, res));
    }
  }
  box.appendChild(details);
}

function discItemLabel(name: string, description: string | undefined): HTMLElement {
  const label = el(
    "div",
    { className: "mcp-disc-item-label" },
    el("span", { className: "mcp-disc-item-name" }, name),
  );
  if (description !== undefined && description !== "") {
    label.appendChild(el("span", { className: "mcp-disc-item-desc" }, description));
  }
  return label;
}

function buildResourceItem(serverName: string, res: MCPResourceInfo): HTMLElement {
  const label = discItemLabel(orFallback(res.name, res.uri), res.description);
  const btn = el(
    "button",
    { type: "button", className: "mcp-disc-insert", "data-tooltip": "Insert into prompt" },
    "Insert",
  ) as HTMLButtonElement;
  btn.addEventListener("click", () => {
    void insertResource(serverName, res, btn);
  });
  return el("div", { className: "mcp-disc-item" }, label, btn);
}

function buildPromptItem(serverName: string, p: MCPPromptInfo): HTMLElement {
  const displayName = orFallback(p.name, p.prompt_name);
  const args = p.arguments ?? [];
  const item = el("div", { className: "mcp-disc-item" }, discItemLabel(displayName, p.description));

  if (args.length === 0) {
    const btn = el(
      "button",
      { type: "button", className: "mcp-disc-insert", "data-tooltip": "Insert into prompt" },
      "Insert",
    ) as HTMLButtonElement;
    btn.addEventListener("click", () => {
      void insertPrompt(serverName, p, {}, btn);
    });
    item.appendChild(btn);
    return item;
  }

  // Prompt with arguments: a toggle reveals an inline form; submit inserts.
  const form = buildArgForm(serverName, p, args);
  form.hidden = true;
  const toggleBtn = el(
    "button",
    { type: "button", className: "mcp-disc-insert", "data-tooltip": "Fill in arguments" },
    "Fill in…",
  ) as HTMLButtonElement;
  toggleBtn.addEventListener("click", () => {
    form.hidden = !form.hidden;
  });
  item.appendChild(toggleBtn);
  const wrap = el("div", { className: "mcp-disc-prompt-wrap" }, item, form);
  return wrap;
}

function buildArgForm(serverName: string, p: MCPPromptInfo, args: MCPPromptArg[]): HTMLFormElement {
  const form = el("form", { className: "mcp-disc-arg-form" }) as HTMLFormElement;
  const inputs = new Map<string, HTMLInputElement>();
  for (const a of args) {
    const input = el("input", {
      type: "text",
      className: "mcp-disc-arg-input",
      placeholder: orFallback(a.description ?? "", a.name),
    }) as HTMLInputElement;
    if (a.required === true) {
      input.required = true;
    }
    inputs.set(a.name, input);
    form.appendChild(
      el(
        "label",
        { className: "mcp-disc-arg-row" },
        el(
          "span",
          { className: "mcp-disc-arg-name" },
          a.required === true ? `${a.name} *` : a.name,
        ),
        input,
      ),
    );
  }
  const submit = el(
    "button",
    { type: "submit", className: "mcp-disc-insert" },
    "Insert",
  ) as HTMLButtonElement;
  form.appendChild(submit);
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    const values: Record<string, string> = {};
    for (const [name, input] of inputs) {
      if (input.value !== "") {
        values[name] = input.value;
      }
    }
    void insertPrompt(serverName, p, values, submit);
  });
  return form;
}

async function insertPrompt(
  serverName: string,
  p: MCPPromptInfo,
  args: Record<string, string>,
  btn: HTMLButtonElement,
): Promise<void> {
  setControlBusy(btn, true);
  try {
    const res = await getPromptContent.dispatch({
      server: serverName,
      prompt: p.prompt_name,
      arguments: args,
    });
    if (res === null) {
      return;
    }
    const text = promptResultToText(res);
    if (text === "") {
      showToast("Prompt returned no text.", "info");
      return;
    }
    insertIntoPrompt(text);
    showToast(`Inserted "${orFallback(p.name, p.prompt_name)}" into the prompt.`, "success");
  } finally {
    setControlBusy(btn, false);
  }
}

async function insertResource(
  serverName: string,
  res: MCPResourceInfo,
  btn: HTMLButtonElement,
): Promise<void> {
  setControlBusy(btn, true);
  try {
    const result = await getResourceContent.dispatch({ server: serverName, uri: res.uri });
    if (result === null) {
      return;
    }
    const text = resourceResultToText(result);
    if (text === "") {
      showToast("Resource has no text content to insert.", "info");
      return;
    }
    const heading = orFallback(res.name, res.uri);
    insertIntoPrompt(`# ${heading}\n\n${text}`);
    showToast(`Inserted "${heading}" into the prompt.`, "success");
  } finally {
    setControlBusy(btn, false);
  }
}

/** Keeps any draft and notifies prompt-input so it resizes and re-enables send. */
function insertIntoPrompt(text: string): void {
  const input = $.promptInput;
  input.value = input.value === "" ? text : `${input.value}\n\n${text}`;
  input.focus();
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

function openAddModal(): void {
  setEditing({ id: "" });
  initModal({ mode: "search", server: null });
  openModal($.mcpModal);
}

async function openEditModal(id: string): Promise<void> {
  const s = await openEdit.dispatch(id);
  if (s === null) {
    return;
  }
  setEditing({ id });
  initModal({ mode: editModeFor(s), server: s });
  openModal($.mcpModal);
}

export function initMCP(): void {
  buildSectionScaffold();

  // The cleanup must run on every close path, so it hangs off the controller's onClose.
  onModalClose($.mcpModal, cleanupModal);

  // Status and config flow through the per-server signals; rows re-render reactively.
  onSSE("mcp_config_changed", () => {
    mcpState.refetchServers();
    mcpState.refetchStatus();
  });
  onSSE("mcp_connected", (_chat, p) => {
    mcpState.setStatusFromEvent(p.server, { name: p.server, state: "connected" });
    // Prompts and resources ride /api/mcp/status, and so does a foreign server's origin.
    mcpState.refetchStatus();
  });
  onSSE("mcp_oauth_needed", (_chat, p) => {
    mcpState.setStatusFromEvent(p.server, {
      name: p.server,
      state: "needs_auth",
      oauth_url: p.url,
      // A fresh attempt clears the relay latch: a stale `true` hides the paste box for an undelivered code.
      relayed: false,
    });
  });
  onSSE("mcp_failed", (_chat, p) => {
    // Read the previous state first: the toast fires on the transition into `failed`.
    announceMCPFailure(p.server, p.error, statusSignalFor(p.server).peek().state);
    mcpState.setStatusFromEvent(p.server, { name: p.server, state: "failed", error: p.error });
  });
  onSSE("mcp_disconnected", (_chat, p) => {
    mcpState.deleteStatus(p.server);
  });
  onSSE("mcp_prewarm", (_chat, p) => {
    updatePrewarmStatus(p.package, p.state as "installing" | "done" | "failed");
  });

  // Fires immediately if governance is known, then on every change.
  onGovernanceChange(applyGovernance);

  mcpState.refetchServers();
  mcpState.refetchStatus();
}

/** Map an npx prewarm event (keyed by package) to its server's prewarm signal. */
function updatePrewarmStatus(pkg: string, state: "installing" | "done" | "failed"): void {
  for (const server of configuredServers()) {
    const serverPkg = extractNpxPackage(server);
    if (serverPkg !== pkg && server.name !== pkg) {
      continue;
    }
    setPrewarm(server.id, state);
    return;
  }
}

// A toast when a server turns out to be broken, with no proactive probing: the failure is the one kiro-cli reported.

/**
 * Dedupe on the state transition: every bridge emits its own status on connect, so a reconnect storm is a broadcast
 * storm. Leaving `failed` re-arms it.
 */
export function announceMCPFailure(server: string, reason: string, prevState: RuntimeState): void {
  if (prevState === "failed") {
    return;
  }
  showToast(mcpFailureText(server, reason), "error");
}

/** kiro-cli's own text separates "command not found" from a timeout. It can be empty, so the fallback names the server. */
export function mcpFailureText(server: string, reason: string): string {
  const trimmed = reason.trim();
  if (trimmed === "") {
    return `Integration "${server}" failed to start.`;
  }
  return `Integration "${server}" failed to start: ${trimmed}`;
}
