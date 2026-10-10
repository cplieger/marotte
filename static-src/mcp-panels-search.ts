// MCP registry search panel.

import { byId } from "./dom.js";
import { searchRegistry, registryFailureOf } from "./actions/mcp.js";
import type {
  RegistryEntry,
  RegistrySearchFailure,
  RegistrySearchResult,
} from "./wire/types.gen.js";
import {
  subscribeToActions,
  bindLoadingState,
  debouncedDispatch,
  registerCleanup,
} from "./actions/index.js";
import type { DebouncedDispatch } from "./actions/index.js";
import { reconcile } from "./reconcile.js";
import { chevronEl } from "./chevron.js";
import { emptyNote, type Nouns } from "./textsearch/copy.js";
import { configuredServers } from "./mcp-state.js";
import type { KeyPair, Server } from "./mcp-state.js";
import { el } from "@cplieger/reactive";

/**
 * The no-rows answers, from a 502 body, the reply's `filtered` count and the query length. `tooShort` renders through
 * the shared `emptyNote`; the rest keep local sentences. An in-flight search is not an answer.
 */
type RegistryEmptyState =
  | { kind: "none" }
  | { kind: "withheld"; matched: number }
  | { kind: "failed"; retryAfterS?: number }
  | { kind: "tooShort"; min: number };

/** The rows are servers and it scans nothing of its own, so one noun serves both keys. */
const NOUNS: Nouns = {
  match: { one: "server", many: "servers" },
  scanned: { one: "server", many: "servers" },
};

/** Switches the modal to another panel mode. */
type SwitchModeFn = (
  kind: string,
  slug: string,
  identifier: string,
  fields: InstallField[],
) => void;

/**
 * One field a registry entry declares (env var or header), with the publisher's description and required / secret
 * markers.
 */
export interface InstallField {
  name: string;
  description?: string | undefined;
  required?: boolean | undefined;
  secret?: boolean | undefined;
}

/**
 * The upstream refuses connections after a burst (measured: ~16 requests in seconds, then a minute of `Could not
 * connect`), and each typed prefix is a distinct query no cache collapses.
 */
const DEBOUNCE_MS = 400;

/** A single letter matches most of the index, for a slow round trip that answers nothing useful. */
const MIN_QUERY_LEN = 2;

let debouncedSearch: DebouncedDispatch<{ q: string }> | null = null;
let searchUnsub: (() => void) | null = null;
let retryBtnUnbind: (() => void) | null = null;
/** Re-enables a Retry button the registry asked to hold for an interval. */
let retryTimer: ReturnType<typeof setTimeout> | null = null;
let searchBtnUnbind: (() => void) | null = null;

/** Dispatches are not scoped, so a slow answer for an abandoned prefix must not render. */
let wantedQuery = "";

registerCleanup(() => {
  debouncedSearch?.cancel();
  searchUnsub?.();
  clearRetry();
  searchBtnUnbind?.();
});

/** Every render replaces the button, so its binding and hold timer go together. */
function clearRetry(): void {
  retryBtnUnbind?.();
  retryBtnUnbind = null;
  if (retryTimer !== null) {
    clearTimeout(retryTimer);
    retryTimer = null;
  }
}

/** Wire to call when the user clicks an install button in search results. */
let switchMode: SwitchModeFn | null = null;

export function setSwitchMode(fn: SwitchModeFn): void {
  switchMode = fn;
}

/** Cancel in-flight search work and tear down subscriptions. */
export function cleanupSearch(): void {
  debouncedSearch?.cancel();
  clearRetry();
  searchBtnUnbind?.();
  searchBtnUnbind = null;
  searchUnsub?.();
  searchUnsub = null;
  wantedQuery = "";
}

export function initSearchPanel(): void {
  const input = byId<HTMLInputElement>("mcp-search-input");
  const results = byId<HTMLDivElement>("mcp-search-results");
  const btn = byId<HTMLButtonElement>("mcp-search-btn");
  input.value = "";
  wantedQuery = "";
  results.replaceChildren();
  input.focus();

  searchUnsub?.();
  debouncedSearch = debouncedDispatch(searchRegistry, { wait: DEBOUNCE_MS });

  // Re-initialised on every open, so the previous binding goes. `pendingClass` covers the typed path and the click.
  searchBtnUnbind?.();
  searchBtnUnbind = bindLoadingState("mcp.search_registry", btn, {
    pendingClass: "is-searching",
  });

  searchUnsub = subscribeToActions((inst) => {
    if (inst.name !== "mcp.search_registry") {
      return;
    }
    const q = (inst.args as { q: string }).q;
    if (q !== wantedQuery) {
      return; // An abandoned prefix answering late.
    }
    if (inst.status === "pending") {
      renderSearching(results);
    } else if (inst.status === "success") {
      const d = inst.result as RegistrySearchResult | undefined;
      renderSearchResults(results, d, q);
    } else if (inst.status === "error") {
      renderSearchError(results, q, registryFailureOf(inst.error));
    }
  });

  /**
   * A query under the floor renders the hint, since an empty box reads as no matches. `immediate` is Enter / the
   * button, skipping the quiet window.
   */
  const ask = (immediate: boolean): void => {
    const q = input.value.trim();
    wantedQuery = q;
    if (q.length < MIN_QUERY_LEN) {
      debouncedSearch?.cancel();
      renderEmpty(results, { kind: "tooShort", min: MIN_QUERY_LEN }, q);
      return;
    }
    if (immediate) {
      void debouncedSearch?.flush({ q });
      return;
    }
    debouncedSearch?.({ q });
  };

  input.oninput = (): void => {
    ask(false);
  };

  input.onkeydown = (e: KeyboardEvent): void => {
    if (e.key === "Enter") {
      e.preventDefault();
      ask(true);
    }
  };

  btn.onclick = (): void => {
    ask(true);
  };
}

/** The registry can take ten seconds; an empty box reads as "does nothing". */
function renderSearching(results: HTMLDivElement): void {
  clearRetry();
  results.replaceChildren(el("p", { className: "mcp-empty" }, "Searching the registry…"));
}

const ROW_SPEC = {
  key: (e: RegistryEntry) => e.name,
  mount: (e: RegistryEntry) => renderRegistryResult(e),
};

function renderSearchResults(
  results: HTMLDivElement,
  d: RegistrySearchResult | undefined,
  q: string,
): void {
  clearRetry();
  for (const child of [...results.children]) {
    if ((child as HTMLElement).getAttribute("data-reconcile-key") === null) {
      child.remove();
    }
  }
  if (d == null) {
    renderEmpty(results, { kind: "failed" }, q);
    return;
  }
  if (d.servers.length === 0) {
    renderEmpty(
      results,
      d.filtered > 0 ? { kind: "withheld", matched: d.filtered } : { kind: "none" },
      q,
    );
    return;
  }
  reconcile(results, d.servers, ROW_SPEC);
  const note = resultNote(d);
  if (note !== null) {
    results.appendChild(el("p", { className: "mcp-empty" }, note));
  }
}

/** Both facts say "the list you see is not the list that matched". Null when it is the whole answer. */
function resultNote(d: RegistrySearchResult): string | null {
  const parts: string[] = [];
  if (d.filtered > 0) {
    parts.push(`${d.filtered} more matched but cannot be installed here.`);
  }
  if (d.truncated) {
    parts.push("More matched than shown. Narrow the query to see the rest.");
  }
  return parts.length === 0 ? null : parts.join(" ");
}

/** `tooShort` is the shared sentence; `none` echoes the query. */
function registryEmptyNote(state: RegistryEmptyState, q: string): string {
  switch (state.kind) {
    case "none":
      return `No results for "${q}".`;
    case "withheld":
      return `${state.matched} matched, but none can be installed here.`;
    case "failed":
      return state.retryAfterS === undefined
        ? "Registry unreachable. Use the Remote URL or npm package forms instead."
        : `The registry asked for a pause. Retry in ${state.retryAfterS}s, or use the Remote URL or npm package forms instead.`;
    case "tooShort":
      return emptyNote(state, NOUNS);
  }
}

function renderEmpty(results: HTMLDivElement, state: RegistryEmptyState, q: string): void {
  clearRetry();
  reconcile(results, [] as RegistryEntry[], ROW_SPEC);
  results.replaceChildren(el("p", { className: "mcp-empty" }, registryEmptyNote(state, q)));
  if (state.kind === "failed") {
    results.appendChild(renderRetry(q, state.retryAfterS));
  }
}

/** A rate-limited registry that named an interval will refuse a click inside it, so Retry waits it out. */
function renderSearchError(
  results: HTMLDivElement,
  q: string,
  failure: RegistrySearchFailure | undefined,
): void {
  const retryAfterS = failure?.retry_after;
  renderEmpty(
    results,
    retryAfterS === undefined ? { kind: "failed" } : { kind: "failed", retryAfterS },
    q,
  );
}

function renderRetry(q: string, holdS: number | undefined): HTMLButtonElement {
  const retryBtn = el(
    "button",
    { type: "button", className: "btn-small" },
    "Retry",
  ) as HTMLButtonElement;
  if (holdS !== undefined) {
    retryBtn.disabled = true;
    retryTimer = setTimeout(() => {
      retryTimer = null;
      retryBtn.disabled = false;
    }, holdS * 1000);
  }
  // `disabledFn` is restored on pending-to-idle, or an abandoned dispatch settling re-enables the button inside the hold.
  retryBtnUnbind = bindLoadingState("mcp.search_registry", retryBtn, {
    disabledFn: () => retryTimer !== null,
  });
  retryBtn.addEventListener("click", () => {
    // A failed query is not cached server-side, so this re-fetches; the subscription renders only the wanted query.
    wantedQuery = q;
    void searchRegistry.dispatch({ q });
  });
  return retryBtn;
}

/**
 * One search result: a compact row that expands. Install buttons are siblings of the `<details>`, reachable unexpanded
 * and outside a `role="button"` (axe `nested-interactive`).
 */
export function renderRegistryResult(entry: RegistryEntry): HTMLDivElement {
  // The registry still lists deprecated entries, so without the badge a dead server reads as live.
  const status = entry.status ?? "";

  const summary = el(
    "summary",
    { className: "mcp-result-summary" },
    chevronEl(),
    el("span", { className: "mcp-result-name" }, entry.title ?? entry.name),
  );
  const version = (entry.version ?? "").trim();
  if (version !== "") {
    summary.appendChild(el("span", { className: "mcp-result-version" }, version));
  }
  if (status !== "") {
    summary.appendChild(el("span", { className: "mcp-result-status" }, status));
  }
  summary.appendChild(
    el("span", { className: "mcp-result-desc" }, entry.description ?? entry.name),
  );

  const body = el("div", { className: "mcp-result-body" });
  if (status !== "") {
    const why = (entry.status_message ?? "").trim();
    body.appendChild(
      el(
        "p",
        { className: "mcp-result-status-note" },
        why !== "" ? why : `The registry marks this entry ${status}.`,
      ),
    );
  }

  const actions = el("div", { className: "mcp-result-actions" });
  // One snapshot per row, so two install paths agree on what is in mcp.json.
  const configured = configuredServers();
  for (const option of installOptions(entry, configured)) {
    actions.appendChild(option.btn);
    body.appendChild(option.detail);
  }

  const row = el(
    "div",
    { className: "mcp-result" },
    el("details", { className: "mcp-result-disc" }, summary, body),
    actions,
  ) as HTMLDivElement;
  if (status !== "") {
    row.classList.add("mcp-result-deprecated");
  }
  return row;
}

interface InstallOption {
  /** Sits on the row, so it needs no expansion. */
  btn: HTMLButtonElement;
  /** The identifier and what installing it will ask for. Sits in the body. */
  detail: HTMLDivElement;
}

/** Every declared path, in registry order: a package runs under `npx`, a remote is a hosted URL. */
function installOptions(entry: RegistryEntry, configured: readonly Server[]): InstallOption[] {
  const out: InstallOption[] = [];
  for (const pkg of entry.packages ?? []) {
    out.push(
      renderInstallOption(
        entry,
        pkg.registry_type,
        pkg.identifier,
        pkg.env_vars ?? [],
        "env",
        configured,
      ),
    );
  }
  for (const rem of entry.remotes ?? []) {
    out.push(
      renderInstallOption(
        entry,
        rem.type,
        rem.url,
        (rem.headers ?? []).map((h) => ({
          name: h.name,
          description: h.description,
          required: h.required,
          secret: h.secret,
        })),
        "header",
        configured,
      ),
    );
  }
  return out;
}

/**
 * A remote's URL matches first and exactly: an endpoint names one server. The name fallback is what install would write
 * (`simplifyName`), case-insensitive since `mcp.json` is hand-editable.
 */
function matchConfigured(
  configured: readonly Server[],
  entry: RegistryEntry,
  identifier: string,
  fieldKind: "env" | "header",
): Server | null {
  if (fieldKind === "header") {
    const byURL = configured.find((s) => s.url === identifier);
    if (byURL !== undefined) {
      return byURL;
    }
  }
  const slug = simplifyName(entry.name).toLowerCase();
  return configured.find((s) => s.name.toLowerCase() === slug) ?? null;
}

/**
 * Names only: a saved secret returns as `SECRET_MASK`. Header names fold case (HTTP), env names do not; the pair is
 * chosen by `fieldKind`.
 */
function fieldIsSet(server: Server, fieldKind: "env" | "header", name: string): boolean {
  const pairs: readonly KeyPair[] = (fieldKind === "env" ? server.env : server.headers) ?? [];
  const want = fieldKind === "env" ? name : name.toLowerCase();
  return pairs.some((p) => {
    const have = p.name.trim();
    return (fieldKind === "env" ? have : have.toLowerCase()) === want;
  });
}

/** The preview is disclosure, not consent: it gates nothing. */
function renderInstallOption(
  entry: RegistryEntry,
  kind: string,
  identifier: string,
  fields: InstallField[],
  fieldKind: "env" | "header",
  configured: readonly Server[],
): InstallOption {
  const detail = el(
    "div",
    { className: "mcp-install-option" },
    el("code", { className: "mcp-install-id" }, `${kind}: ${identifier}`),
  ) as HTMLDivElement;
  const preview = renderRequirements(
    fields,
    fieldKind,
    matchConfigured(configured, entry, identifier, fieldKind),
  );
  if (preview !== null) {
    detail.appendChild(preview);
  }
  return { btn: renderInstallBtn(entry, kind, identifier, fields), detail };
}

/**
 * Null when nothing was declared. `configured` (the server this path would land on) changes the label and marks
 * satisfied rows only; the list stays whole.
 */
function renderRequirements(
  fields: InstallField[],
  fieldKind: "env" | "header",
  configured: Server | null,
): HTMLElement | null {
  if (fields.length === 0) {
    return null;
  }
  const noun = fieldKind === "env" ? "environment variables" : "headers";
  const requiredFields = fields.filter((f) => f.required === true);
  const label =
    configured !== null
      ? configuredLabel(requiredFields, fieldKind, noun, configured)
      : requiredFields.length > 0
        ? `Needs ${requiredFields.length} of ${fields.length} ${noun}`
        : `Optional ${noun} (${fields.length})`;

  const list = el("ul", { className: "mcp-requires-list" });
  for (const f of fields) {
    const item = el("li", {}, el("code", { className: "mcp-requires-name" }, f.name));
    if (f.required === true) {
      item.appendChild(
        el("span", { className: "mcp-pair-mark mcp-pair-mark-required" }, "Required"),
      );
    }
    if (f.secret === true) {
      item.appendChild(el("span", { className: "mcp-pair-mark" }, "Secret"));
    }
    if (configured !== null && fieldIsSet(configured, fieldKind, f.name)) {
      item.appendChild(el("span", { className: "mcp-pair-mark mcp-pair-mark-set" }, "Set"));
    }
    const desc = (f.description ?? "").trim();
    if (desc !== "") {
      item.appendChild(el("span", { className: "mcp-requires-desc" }, desc));
    }
    list.appendChild(item);
  }
  return el(
    "div",
    { className: "mcp-requires" },
    el("p", { className: "mcp-requires-label" }, label),
    list,
  );
}

/** Counts the required fields only; per-row `Set` marks report the rest. */
function configuredLabel(
  requiredFields: InstallField[],
  fieldKind: "env" | "header",
  noun: string,
  configured: Server,
): string {
  if (requiredFields.length === 0) {
    return "Already configured";
  }
  const missing = requiredFields.filter((f) => !fieldIsSet(configured, fieldKind, f.name)).length;
  return missing === 0
    ? `Already configured. All required ${noun} set`
    : `Already configured. ${missing} of ${requiredFields.length} required ${noun} still missing`;
}

function renderInstallBtn(
  entry: RegistryEntry,
  kind: string,
  identifier: string,
  fields: InstallField[],
): HTMLButtonElement {
  // The label names the transport only; the identifier is in the accessible name and tooltip.
  const btn = el(
    "button",
    {
      type: "button",
      className: "btn-small mcp-install-btn",
      "aria-label": `Use ${kind}: ${identifier}`,
      "data-tooltip": identifier,
    },
    `Use ${kind}`,
  ) as HTMLButtonElement;
  btn.addEventListener("click", () => {
    const slug = simplifyName(entry.name);
    switchMode?.(kind, slug, identifier, fields);
  });
  return btn;
}

export function simplifyName(full: string): string {
  const slash = full.lastIndexOf("/");
  const raw = slash >= 0 ? full.slice(slash + 1) : full;
  return (
    raw
      .replace(/[^A-Za-z0-9_-]/g, "-")
      .replace(/^-+|-+$/g, "")
      .slice(0, 48) || "server"
  );
}
