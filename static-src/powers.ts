// The installed set is kiro-cli's own, so the list is refetched after a change rather than patched.

import { el } from "@cplieger/reactive";
import { join as joinKey } from "@cplieger/keyenc";
import { apiGetTyped, apiGetTypedOrError } from "./api-client.js";
import { asObject, decodeArray, optBool, optNum, optStr, reqBool, reqStr } from "./validators.js";
import { KEY_ATTR, reconcile } from "./reconcile.js";
import { entryList, entryRow } from "./entry-row.js";
import type { EntrySub } from "./entry-row.js";
import { iconEl } from "./icon-el.js";
import { ICON_TRASH } from "./icons.js";
import { confirm as confirmDialog } from "./confirm.js";
import { installPower, uninstallPower } from "./actions/powers.js";
import { onSSE } from "./bus.js";

export interface PowerRow {
  readonly name: string;
  readonly displayName: string;
  readonly description: string;
  readonly category: string;
  readonly publisherTier: string;
  readonly authType: string;
  readonly origin: string;
  readonly mcpServers: readonly string[];
  readonly issues: number;
  readonly installed: boolean;
  readonly inCatalog: boolean;
}

/** `partial`: KAS listed the Powers it could load and named others it could not. */
type HalfState = "ready" | "unavailable" | "partial";

/** An installed Power KAS could not load, with KAS's own reason. */
interface PowerLoadError {
  readonly name: string;
  readonly message: string;
}

interface PowerList {
  readonly catalog: HalfState;
  readonly installed: HalfState;
  /** The administrator's reason when installing is refused; empty otherwise. */
  readonly blocked: string;
  /** True until the administrator rules are read; the server refuses installs until then. */
  readonly policyPending: boolean;
  readonly powers: PowerRow[];
  readonly loadErrors: readonly PowerLoadError[];
}

/** How many Powers are listed and how many the filter shows, for the page's note. */
interface PowerCounts {
  total: number;
  shown: number;
}

type LoadState = "loading" | "ready" | "failed";

/** A row with no category still needs a section. */
const OTHER = "Other";
const INSTALLED = "Installed";

const AUTH_LABEL: Readonly<Record<string, string>> = {
  oauth: "OAuth",
  apiKey: "API key",
  awsCredentials: "AWS credentials",
  bearerToken: "Bearer token",
};

function strings(v: unknown): string[] {
  return Array.isArray(v)
    ? (v as unknown[]).filter((s): s is string => typeof s === "string" && s !== "")
    : [];
}

function half(v: string | undefined): HalfState {
  return v === "ready" || v === "partial" ? v : "unavailable";
}

function decodeLoadError(v: unknown): PowerLoadError {
  const o = asObject(v);
  return { name: optStr(o, "name") ?? "", message: optStr(o, "message") ?? "" };
}

function decodeRow(v: unknown): PowerRow {
  const o = asObject(v);
  return {
    name: reqStr(o, "name"),
    displayName: optStr(o, "display_name") ?? "",
    description: optStr(o, "description") ?? "",
    category: optStr(o, "category") ?? "",
    publisherTier: optStr(o, "publisher_tier") ?? "",
    authType: optStr(o, "auth_type") ?? "",
    origin: optStr(o, "origin") ?? "",
    mcpServers: strings(o["mcp_servers"]),
    issues: optNum(o, "issues") ?? 0,
    installed: reqBool(o, "installed"),
    inCatalog: optBool(o, "in_catalog") ?? false,
  };
}

function decodePowerList(v: unknown): PowerList {
  const o = asObject(v);
  return {
    catalog: half(optStr(o, "catalog")),
    installed: half(optStr(o, "installed")),
    blocked: optStr(o, "blocked") ?? "",
    policyPending: optBool(o, "policy_pending") ?? false,
    powers: decodeArray(o["powers"], decodeRow),
    loadErrors:
      o["load_errors"] === undefined ? [] : decodeArray(o["load_errors"], decodeLoadError),
  };
}

interface ServerList {
  readonly servers: string[];
  readonly known: boolean;
}

function decodeServers(v: unknown): ServerList {
  const o = asObject(v);
  return { servers: strings(o["servers"]), known: optBool(o, "known") ?? false };
}

let powers: PowerRow[] = [];
let catalogState: HalfState = "ready";
let installedState: HalfState = "ready";
let blockedReason = "";
let policyPending = false;
let loadErrors: readonly PowerLoadError[] = [];
let state: LoadState = "loading";
let filterText = "";
let container: HTMLElement | null = null;
const busy = new Set<string>();
let onCounts: ((c: PowerCounts) => void) | null = null;
/** Bumped per fetch, so a stale answer cannot overwrite a newer one. */
let generation = 0;
/** True from a fetch's start until the newest one answers. */
let requestPending = false;
let wired = false;

export function setPowerCountsListener(fn: (c: PowerCounts) => void): void {
  onCounts = fn;
}

function title(p: PowerRow): string {
  return p.displayName === "" ? p.name : p.displayName;
}

function haystack(p: PowerRow): string {
  return [p.name, p.displayName, p.description, p.category, ...p.mcpServers]
    .join("\n")
    .toLowerCase();
}

function visible(): PowerRow[] {
  return filterText === "" ? powers : powers.filter((p) => haystack(p).includes(filterText));
}

function isShowing(): boolean {
  return container?.isConnected === true && !container.classList.contains("hidden");
}

/** Render into the panel. A call whose filter is unchanged is a tab switch or a refresh and
 *  refetches; a filter change only repaints. */
export function renderPowersPanel(panel: HTMLElement, filter = ""): void {
  container = panel;
  if (!wired) {
    wired = true;
    // An install from a shell, another device or the agent lands here too.
    onSSE("powers_changed", () => {
      if (isShowing()) {
        void refresh();
      }
    });
  }
  const next = filter.trim().toLowerCase();
  const filterMoved = next !== filterText;
  filterText = next;
  paint();
  if (!filterMoved || (state === "loading" && !requestPending)) {
    void refresh();
  }
}

async function refresh(): Promise<void> {
  const gen = ++generation;
  requestPending = true;
  const res = await apiGetTypedOrError("/api/powers", decodePowerList);
  if (gen !== generation) {
    return;
  }
  requestPending = false;
  if (res.ok && res.data !== null) {
    powers = res.data.powers;
    catalogState = res.data.catalog;
    installedState = res.data.installed;
    blockedReason = res.data.blocked;
    policyPending = res.data.policyPending;
    loadErrors = res.data.loadErrors;
    state = "ready";
  } else {
    state = "failed";
  }
  paint();
}

interface Section {
  readonly label: string;
  readonly rows: PowerRow[];
}

/** Installed first, then the catalogue by category, alphabetically, with uncategorised rows
 *  last; rows keep the server's order inside a section. */
export function powerSections(rows: readonly PowerRow[]): Section[] {
  const installed = rows.filter((p) => p.installed);
  const by = new Map<string, PowerRow[]>();
  for (const p of rows) {
    if (p.installed) {
      continue;
    }
    const key = p.category === "" ? OTHER : p.category;
    const list = by.get(key) ?? [];
    list.push(p);
    by.set(key, list);
  }
  const labels = [...by.keys()].sort((a, b) =>
    a === OTHER ? 1 : b === OTHER ? -1 : a.localeCompare(b),
  );
  const out: Section[] = [];
  if (installed.length > 0) {
    out.push({ label: INSTALLED, rows: installed });
  }
  for (const label of labels) {
    out.push({ label, rows: by.get(label) ?? [] });
  }
  return out;
}

function paint(): void {
  if (container === null) {
    return;
  }
  const rows = visible();
  onCounts?.({ total: powers.length, shown: rows.length });
  const head = headNotes();
  if (state !== "ready") {
    container.replaceChildren(...head, statusBody());
    return;
  }
  if (rows.length === 0) {
    container.replaceChildren(
      ...head,
      el(
        "div",
        { className: "list-empty" },
        powers.length === 0 ? "No Powers to show." : "No Powers match the filter.",
      ),
    );
    return;
  }
  for (const child of [...container.children]) {
    if (child.getAttribute(KEY_ATTR) === null) {
      child.remove();
    }
  }
  reconcile(container, powerSections(rows), {
    key: (s: Section) => joinKey("section", s.label),
    mount: (s: Section) => {
      const node = el(
        "div",
        { className: "docs-section" },
        el("div", { className: "entry-section-label" }, s.label),
        entryList(),
      );
      fillSection(node, s);
      return node;
    },
    update: fillSection,
  });
  container.prepend(...head);
}

function statusBody(): HTMLElement {
  if (state === "loading") {
    return el("div", { className: "list-empty" }, "Loading Powers…");
  }
  const retry = el("button", { type: "button", className: "btn-small" }, "Retry");
  retry.addEventListener("click", () => {
    state = "loading";
    paint();
    void refresh();
  });
  return el("div", { className: "list-empty powers-failed" }, "Couldn't load Powers. ", retry);
}

/** What the list cannot show right now. Each half of it can fail alone. */
function headNotes(): HTMLElement[] {
  if (state !== "ready") {
    return [];
  }
  const notes: string[] = [];
  if (blockedReason !== "") {
    notes.push(`${blockedReason}. Installed Powers can still be removed.`);
  } else if (policyPending) {
    notes.push("Your organization's settings have not loaded yet, so Powers cannot be installed.");
  }
  if (catalogState === "unavailable") {
    notes.push("Couldn't load the Powers catalogue, so only installed Powers are listed.");
  }
  if (installedState === "unavailable") {
    notes.push("Couldn't read which Powers are installed, so every Power shows Install.");
  }
  if (installedState === "partial") {
    notes.push(loadErrorNote(loadErrors));
  }
  return notes.map((text) => el("p", { className: "powers-note" }, text));
}

function loadErrorNote(errors: readonly PowerLoadError[]): string {
  const what =
    errors.length === 1 ? "an installed Power" : `${String(errors.length)} installed Powers`;
  const reasons = errors
    .map((e) => (e.name === "" ? e.message : e.message === "" ? e.name : `${e.name}: ${e.message}`))
    .join("; ");
  return `Couldn't load ${what}. ${reasons}${/[.!?]$/.test(reasons) ? "" : "."}`;
}

function fillSection(node: HTMLElement, s: Section): void {
  const list = node.querySelector<HTMLElement>(".list-container");
  if (list === null) {
    return;
  }
  reconcile(list, s.rows, {
    key: rowKey,
    mount: buildRow,
  });
}

/** The key carries everything a row renders, so a changed row remounts. */
function rowKey(p: PowerRow): string {
  return joinKey(
    "power",
    p.name,
    title(p),
    p.description,
    p.authType,
    p.publisherTier,
    p.origin,
    p.mcpServers.join(","),
    String(p.issues),
    String(p.installed),
    String(p.inCatalog),
    String(busy.has(p.name)),
    String(blockedReason !== ""),
    String(policyPending),
  );
}

function badges(p: PowerRow): HTMLElement[] {
  const out: HTMLElement[] = [];
  if (p.issues > 0) {
    out.push(
      el(
        "span",
        { className: "docs-badge docs-badge-warn" },
        p.issues === 1 ? "1 issue" : `${String(p.issues)} issues`,
      ),
    );
  }
  if (p.installed && p.origin !== "" && p.origin !== "user") {
    out.push(el("span", { className: "docs-badge" }, p.origin));
  }
  if (Object.hasOwn(AUTH_LABEL, p.authType)) {
    out.push(el("span", { className: "docs-badge" }, AUTH_LABEL[p.authType] ?? p.authType));
  }
  if (p.publisherTier === "Community") {
    out.push(el("span", { className: "docs-badge" }, "Community"));
  }
  return out;
}

function sub(p: PowerRow): EntrySub {
  if (p.mcpServers.length === 0) {
    return { kind: "clamp", text: p.description };
  }
  return {
    kind: "lines",
    lines: [{ text: p.description }, { text: `MCP: ${p.mcpServers.join(", ")}`, mono: true }],
  };
}

function buildRow(p: PowerRow): HTMLElement {
  const actions: HTMLElement[] = [];
  const working = busy.has(p.name);
  if (p.installed) {
    actions.push(
      el("span", { className: "powers-installed" }, working ? "Removing…" : "Installed"),
    );
    const del = el(
      "button",
      {
        type: "button",
        className: "icon-btn list-row-btn entry-delete",
        "aria-label": `Uninstall ${title(p)}`,
        "data-tooltip": "Uninstall",
        disabled: working,
      },
      iconEl(ICON_TRASH),
    );
    del.addEventListener("click", () => {
      void uninstallOne(p);
    });
    actions.push(del);
  } else if (p.inCatalog && blockedReason === "") {
    const install = el(
      "button",
      {
        type: "button",
        className: "btn-small powers-install",
        "aria-label": `Install ${title(p)}`,
        disabled: working || policyPending,
      },
      working ? "Installing…" : "Install",
    );
    install.addEventListener("click", () => {
      void installOne(p);
    });
    actions.push(install);
  }
  return entryRow({
    key: p.name,
    title: title(p),
    badges: badges(p),
    sub: sub(p),
    actions,
    data: { "data-power": p.name },
  });
}

export function serverSentence(list: ServerList | null): string {
  if (list?.known !== true) {
    return "Its MCP servers could not be read ahead of time; any it declares start right away, in open chats too, and run unsandboxed, with your access.";
  }
  if (list.servers.length === 0) {
    return "It declares no MCP servers.";
  }
  const names = list.servers.join(", ");
  return list.servers.length === 1
    ? `It starts the MCP server ${names}, which runs unsandboxed, with your access.`
    : `It starts these MCP servers: ${names}. They run unsandboxed, with your access.`;
}

async function installOne(p: PowerRow): Promise<void> {
  const servers = await apiGetTyped(
    `/api/powers/${encodeURIComponent(p.name)}/servers`,
    decodeServers,
  );
  const ok = await confirmDialog(
    `Install the ${title(p)} Power? ${serverSentence(servers)}`,
    "Install",
    "normal",
  );
  if (!ok) {
    return;
  }
  await run(p.name, () => installPower.dispatch({ name: p.name }));
}

async function uninstallOne(p: PowerRow): Promise<void> {
  const ok = await confirmDialog(
    `Uninstall the ${title(p)} Power? Its MCP servers stop and its skills and steering are removed.`,
    "Uninstall",
    "destructive",
  );
  if (!ok) {
    return;
  }
  await run(p.name, () => uninstallPower.dispatch({ name: p.name }));
}

async function run(name: string, verb: () => Promise<unknown>): Promise<void> {
  busy.add(name);
  paint();
  try {
    await verb();
  } finally {
    busy.delete(name);
  }
  await refresh();
}

// deadset:ignore DS1004 -- test seam: resets the powers list, load states, filter and busy set
export function _resetPowersForTest(): void {
  powers = [];
  catalogState = "ready";
  installedState = "ready";
  blockedReason = "";
  policyPending = false;
  loadErrors = [];
  state = "loading";
  filterText = "";
  container = null;
  busy.clear();
  onCounts = null;
  generation++;
  requestPending = false;
}
