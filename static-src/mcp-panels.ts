// ---------------------------------------------------------------------------
// MCP panels: add/edit modal forms (registry search, npm, remote, raw JSON),
// submit helpers, and key/value pair editors.
// ---------------------------------------------------------------------------

import { $, byId } from "./dom.js";
import { el } from "@cplieger/reactive";
import { closeModal, RollingOutput } from "./modals.js";
import {
  type Server,
  type Transport,
  SECRET_MASK,
  mcpState,
  autoApproveHonoured,
  discoverySignalFor,
} from "./mcp-state.js";
import { openSetting } from "./settings-highlight.js";
import {
  type EditablePair,
  renderKeyPairList,
  appendKeyPair,
  collectKeyPairs,
} from "./mcp-pairs.js";
import { buildChip } from "./chip.js";
import {
  type ValidationField,
  importServers,
  saveServer,
  searchRegistry,
  validationFieldsOf,
} from "./actions/mcp.js";
import { getToolsStatus } from "./actions/tools.js";
import { installToolAndWait } from "./tools.js";
import { bindLoadingState } from "./actions/index.js";
import { initSegmentedBar } from "./segmented-bar.js";
import {
  type InstallField,
  initSearchPanel,
  setSwitchMode,
  cleanupSearch,
} from "./mcp-panels-search.js";

// --- Add / edit modal ---

export type AddMode = "search" | "remote" | "npm" | "raw";

interface EditingContext {
  id: string;
}

class EditSession {
  editing: EditingContext = { id: "" };
  disabledToolsList: string[] = [];
  autoApproveList: string[] = [];

  reset(): void {
    this.editing = { id: "" };
    this.disabledToolsList = [];
    this.autoApproveList = [];
  }

  startEdit(id: string): void {
    this.reset();
    this.editing = { id };
  }

  startAdd(): void {
    this.reset();
  }
}

const session = new EditSession();

/** Cancel any in-flight work and tear down search subscription when
 *  the modal is dismissed (close button, Escape, or overlay click). */
export function cleanupModal(): void {
  cleanupSearch();
  searchRegistry.cancel();
}

export function setEditing(ctx: EditingContext): void {
  if (ctx.id === "") {
    session.startAdd();
  } else {
    session.startEdit(ctx.id);
  }
}

interface InitArgs {
  mode: AddMode;
  server: Server | null;
}

const MODE_TABS: readonly { readonly id: AddMode; readonly label: string }[] = [
  { id: "search", label: "Search registry" },
  { id: "remote", label: "Remote URL" },
  { id: "npm", label: "npm package" },
  { id: "raw", label: "Paste JSON" },
];

/** The bar's active-segment projection, wired once per bar element: the
 *  controller adds listeners, and the modal opens many times. */
const paintTabsFor = new WeakMap<HTMLElement, (mode: AddMode) => void>();
let paintTabs: ((mode: AddMode) => void) | undefined;

export function initModal(args: InitArgs): void {
  const title = byId<HTMLSpanElement>("mcp-modal-title");
  title.textContent = session.editing.id === "" ? "Connect integration" : "Edit integration";

  const tabs = byId<HTMLElement>("mcp-modal-tabs");
  tabs.classList.toggle("hidden", session.editing.id !== "");
  paintTabs = paintTabsFor.get(tabs);
  if (paintTabs === undefined) {
    paintTabs = initSegmentedBar(tabs, {
      attr: "data-mcp-mode",
      idPrefix: "mcp",
      tabs: MODE_TABS,
      onSelect: (mode) => {
        setMode(mode, null);
      },
    });
    paintTabsFor.set(tabs, paintTabs);
  }

  initToolListSection(SECTION_DISABLED, args.server);
  initToolListSection(SECTION_AUTO_APPROVE, args.server);
  setMode(args.mode, args.server);
}

// Each mode's initialiser. There is no per-mode `transport` any more: the paste
// panel used to declare "stdio", which is what stopped a pasted remote server
// from going through it at all, and once that was gone the field had exactly one
// reader left. The npm form states its own transport at its save site.
const PANEL_MODES: Readonly<Record<AddMode, (existing: Server | null) => void>> = {
  search: () => {
    initSearchPanel();
  },
  remote: (s) => {
    initRemotePanel(s);
  },
  npm: (s) => {
    initNpmPanel(s);
  },
  raw: (s) => {
    initRawPanel(s);
  },
};

// A `data-mcp-mode` attribute marks TWO different things — one panel and one tab
// button per mode — so a selector over it has to say which. A bare
// `[data-mcp-mode]` here hid the tab BUTTONS as well as the panels.
const PANEL_SELECTOR = ".mcp-mode-panel[data-mcp-mode]";

function setMode(mode: AddMode, existing: Server | null): void {
  // HTMLElement, not HTMLDivElement: the remote panel is a <form> (its password
  // field has to sit in one), and this loop only touches classList and dataset.
  for (const panel of document.querySelectorAll<HTMLElement>(PANEL_SELECTOR)) {
    const panelMode = panel.dataset["mcpMode"] ?? "";
    panel.classList.toggle("hidden", panelMode !== mode);
    // The panel half of the pairing the controller's `aria-controls` writes.
    panel.setAttribute("role", "tabpanel");
    panel.id = `mcp-panel-${panelMode}`;
    panel.setAttribute("aria-labelledby", `mcp-tab-${panelMode}`);
  }
  paintTabs?.(mode);

  PANEL_MODES[mode](existing);
}

// --- Validation failures ---
//
// The server accumulates across independent checks, so one response can name
// three bad fields. Printing three sentences above one box would leave the user
// hunting for which inputs they were about, so the field attribution is spent on
// MARKING the inputs and the messages sit under them as a list.

/** Wire field name -> the form input that holds it. The wire names are the ones
 *  the server already put in its messages (`oauth_client_secret`, `headers`), so
 *  this is a lookup rather than a translation. A field with no input here (a
 *  `transport` refusal on the raw-paste panel, say) still gets its message
 *  printed; only the mark is skipped. */
const FIELD_INPUT_IDS: Readonly<Record<string, readonly string[]>> = {
  name: ["mcp-remote-name", "mcp-npm-name"],
  url: ["mcp-remote-url"],
  command: ["mcp-npm-pkg"],
  args: ["mcp-npm-pkg"],
  transport: ["mcp-remote-transport"],
  headers: ["mcp-remote-headers"],
  env: ["mcp-npm-env"],
  oauth_client_id: ["mcp-remote-oauth-client-id"],
  oauth_client_secret: ["mcp-remote-oauth-client-secret"],
};

const CLS_FIELD_INVALID = "field-invalid";

/** Drop every mark a previous submit left. Runs at the top of each submit, so a
 *  field the user has since fixed stops claiming to be wrong. */
function clearFieldMarks(): void {
  for (const node of document.querySelectorAll<HTMLElement>("." + CLS_FIELD_INVALID)) {
    node.classList.remove(CLS_FIELD_INVALID);
    node.removeAttribute("aria-invalid");
  }
}

/** Mark the inputs the server named, and return the message lines to print. */
function markInvalidFields(fields: readonly ValidationField[]): string[] {
  const lines: string[] = [];
  for (const f of fields) {
    lines.push(f.message);
    for (const id of FIELD_INPUT_IDS[f.field] ?? []) {
      const node = document.getElementById(id);
      if (node === null) {
        continue;
      }
      node.classList.add(CLS_FIELD_INVALID);
      node.setAttribute("aria-invalid", "true");
    }
  }
  return lines;
}

/** Render a dispatch failure into an inline error element.
 *
 *  A validation failure lists every field the server named; anything else (a
 *  parse error, a name conflict, a network death) keeps the single-message shape
 *  it always had, which is why the field list is absent rather than empty on
 *  those paths. */
function showSubmitError(
  errEl: HTMLElement,
  err: { message: string; cause?: unknown } | undefined,
  fallback: string,
): void {
  const fields = validationFieldsOf(err);
  errEl.replaceChildren();
  if (fields.length > 1) {
    const lines = markInvalidFields(fields);
    errEl.appendChild(el("span", {}, `${String(lines.length)} problems to fix:`));
    const list = el("ul", { className: "mcp-error-list" });
    for (const line of lines) {
      list.appendChild(el("li", {}, line));
    }
    errEl.appendChild(list);
  } else {
    if (fields.length === 1) {
      markInvalidFields(fields);
    }
    errEl.textContent = err !== undefined ? err.message : fallback;
  }
  errEl.classList.remove("hidden");
}

// --- Submit helpers ---

async function submitServer(
  body: Partial<Server>,
  errEl: HTMLElement,
  saveBtn: HTMLButtonElement | null,
): Promise<boolean> {
  errEl.classList.add("hidden");
  errEl.replaceChildren();
  clearFieldMarks();

  // Both lists go out on every edit, empty included: the store reads an omitted
  // list as unchanged, so withholding one is how a save silently re-grants what
  // the user just removed.
  if (session.editing.id !== "") {
    body.disabled_tools = session.disabledToolsList;
    body.auto_approve = session.autoApproveList;
  }

  const unbind =
    saveBtn != null
      ? bindLoadingState("mcp.save_server", saveBtn, { pendingClass: "btn-loading" })
      : undefined;

  // The typed outcome carries THIS dispatch's terminal state, so a
  // concurrent save for another server can't cross-contaminate the inline
  // error (the previous subscribeToActions + name-filter capture could).
  const o = await saveServer.dispatch(
    { id: session.editing.id, body },
    {
      onSettled: () => {
        unbind?.();
      },
    },
  ).outcome;

  if (o.status !== "success") {
    showSubmitError(errEl, o.status === "error" ? o.error : undefined, "Save failed.");
    return false;
  }
  closeModal($.mcpModal);
  mcpState.refetchServers();
  // A save can change what KAS runs (a new server, a credential the connect
  // attempt needed), so the row's dot and meta line are stale until the status
  // is re-read. Both fetches coalesce per microtask.
  mcpState.refetchStatus();
  return true;
}

// --- Panel: registry search (delegated to mcp-panels-search.ts) ---

// Wire the switch-mode callback so search results can switch panels.
setSwitchMode((kind, slug, identifier, fields) => {
  if (kind === "npm") {
    setMode("npm", null);
    fillNpmForm(slug, identifier, fields);
  } else {
    // Any non-npm registry hit lands on the remote panel. `kind` is the
    // normalised marotte transport ("http" or "sse", mapped from the
    // registry's remote type by supportedRemoteTypes server-side), so we
    // preselect it in the panel's transport selector. That holds only because
    // npm is the one package registry the server surfaces
    // (supportedPackageRegistries in registry_proxy.go); a second one would
    // arrive here as a package's registry_type and need its own arm.
    setMode("remote", null);
    fillRemoteForm(slug, identifier, fields, kind);
  }
});

// --- Panel: npm (stdio via npx) ---

function initNpmPanel(existing: Server | null): void {
  const name = byId<HTMLInputElement>("mcp-npm-name");
  const pkg = byId<HTMLInputElement>("mcp-npm-pkg");
  const prewarm = byId<HTMLInputElement>("mcp-npm-prewarm");
  const envList = byId<HTMLDivElement>("mcp-npm-env");
  const errEl = byId<HTMLParagraphElement>("mcp-npm-error");
  errEl.classList.add("hidden");
  errEl.textContent = "";

  showOtherCommandsNote();

  // npx-based MCP servers need the Node runtime, which is opt-in. Probe
  // and, if missing, show an inline install affordance gating the form.
  void gateNpmPanelOnNode();

  if (existing !== null) {
    name.value = existing.name;
    pkg.value = extractNpxPackage(existing);
    prewarm.checked = existing.prewarm === true;
    renderKeyPairList(envList, existing.env ?? [], "env");
  } else {
    name.value = "";
    pkg.value = "";
    prewarm.checked = true;
    renderKeyPairList(envList, [], "env");
  }

  byId<HTMLButtonElement>("mcp-npm-add-env").onclick = (): void => {
    appendKeyPair(envList, { name: "", value: "" }, "env");
  };

  byId<HTMLButtonElement>("mcp-npm-save").onclick = (): void => {
    const args = ["-y", pkg.value.trim()].filter((a) => a !== "");
    void submitServer(
      {
        transport: "stdio",
        name: name.value.trim(),
        command: NPX_COMMAND,
        args,
        env: collectKeyPairs(envList),
        prewarm: prewarm.checked,
        enabled: existing?.enabled ?? true,
      },
      errEl,
      byId<HTMLButtonElement>("mcp-npm-save"),
    );
  };
}

const CLS_NPM_ALT = "mcp-npm-alt";
const NPM_PANEL_SELECTOR = '.mcp-mode-panel[data-mcp-mode="npm"]';

/** Name the tab that takes every other stdio command. Paste JSON is the only
 *  surface that can express a `uvx`, `docker` or bare-binary server, and its own
 *  hint is on a tab a reader has to already be on to read it. */
function showOtherCommandsNote(): void {
  const panel = document.querySelector<HTMLDivElement>(NPM_PANEL_SELECTOR);
  if (panel?.querySelector("." + CLS_NPM_ALT) !== null) {
    return;
  }
  const note = el(
    "p",
    { className: `mcp-mode-hint ${CLS_NPM_ALT}` },
    "Paste JSON takes a server that runs any other command: ",
    el("code", {}, "uvx"),
    ", ",
    el("code", {}, "docker"),
    ", or a binary of your own.",
  );
  panel.querySelector(".mcp-mode-hint")?.after(note);
}

// Probe Node availability and, when missing, render an inline banner
// inside the npm panel that installs the Node runtime on click. The
// package fields stay usable (the user can fill them in while Node
// installs), but the banner makes the dependency explicit and the
// install one-click. After a successful enable the banner removes
// itself. Mirrors the Sources sub-tab's auto-install-on-intent flow.
async function gateNpmPanelOnNode(): Promise<void> {
  const panel = document.querySelector<HTMLDivElement>(NPM_PANEL_SELECTOR);
  if (panel === null) {
    return;
  }
  const existingBanner = panel.querySelector(".mcp-node-banner");
  if (existingBanner !== null) {
    existingBanner.remove();
  }

  const status = await getToolsStatus.dispatch();
  if (status !== null && status["npx"] === true) {
    return; // Node already present, nothing to do.
  }

  const banner = el("div", { className: "mcp-node-banner inline-install-banner" });
  const msg = el(
    "p",
    { className: "section-hint" },
    "npx-based MCP servers need the Node.js runtime (~100 MB). It is not installed yet.",
  );
  const btn = el(
    "button",
    { type: "button", className: "btn-small btn-primary" },
    "Install Node.js runtime",
  ) as HTMLButtonElement;
  const out = el("div", {
    className: "rolling-output hidden",
    role: "log",
    "aria-live": "polite",
    "aria-label": "Node install progress",
  }) as HTMLDivElement;

  // Disable the button while the install job runs (auto re-enabled on
  // settle); replaces the manual btn.disabled toggles.
  bindLoadingState("tools.ensure", btn);
  btn.addEventListener("click", () => {
    void (async () => {
      const roll = new RollingOutput(out, "git-output-modal");
      out.classList.remove("hidden");
      roll.append("Installing Node.js runtime…");
      const res = await installToolAndWait("node", (line) => {
        roll.append(line);
      });
      if (!res.ok) {
        roll.append(`Install failed${res.error !== undefined ? `: ${res.error}` : ""}`);
        return;
      }
      // Re-probe; if npx is now present, drop the banner.
      const after = await getToolsStatus.dispatch();
      if (after !== null && after["npx"] === true) {
        banner.remove();
      }
    })();
  });

  banner.append(msg, btn, out);
  panel.prepend(banner);
}

function fillNpmForm(name: string, pkg: string, fields: InstallField[]): void {
  byId<HTMLInputElement>("mcp-npm-name").value = name;
  byId<HTMLInputElement>("mcp-npm-pkg").value = pkg;
  byId<HTMLInputElement>("mcp-npm-prewarm").checked = true;
  renderKeyPairList(byId<HTMLDivElement>("mcp-npm-env"), declaredRows(fields), "env");
}

/** Carry the publisher's declared fields onto the form rows.
 *
 *  The names were already prefilled; the description, the required marker and
 *  the secret hint were thrown away here, which is why a server could install
 *  cleanly and then fail with nothing on screen saying it wanted a token. */
function declaredRows(fields: InstallField[]): EditablePair[] {
  return fields.map((f) => ({
    name: f.name,
    value: "",
    declared: {
      description: f.description,
      required: f.required,
      secret: f.secret,
    },
  }));
}

const NPX_COMMAND = "npx";

/** `prewarm.NpmPkgSpecRe`, transcribed. A leading `-` fails the first class,
 *  which is what refuses a flag. */
const NPM_PKG_SPEC =
  /^(?:@[a-z0-9][a-z0-9._-]*\/)?[a-z0-9][a-z0-9._-]*(?:@[A-Za-z0-9^~><=.+_-][A-Za-z0-9^~><=.+_-]*)?$/;

/** The npm package a stdio server runs through `npx`, or "" when it runs
 *  something else. `internal/mcp/prewarm`'s ExtractNpxPackage transcribed, so
 *  the two halves of the app answer this question the same way: the command
 *  must BE npx, a flag past the package is a refusal, and the spec must match
 *  NpmPkgSpecRe. Prewarm's own enabled/prewarm gates are policy, not shape,
 *  and are not part of it. */
export function extractNpxPackage(s: Server): string {
  if (s.transport !== "stdio" || (s.command ?? "").trim() !== NPX_COMMAND) {
    return "";
  }
  for (const arg of s.args ?? []) {
    const a = arg.trim();
    if (a === "" || a === "-y" || a === "--yes") {
      continue;
    }
    return NPM_PKG_SPEC.test(a) ? a : "";
  }
  return "";
}

/** The panel that can EDIT this server without rewriting it. Routing on the
 *  record's SHAPE rather than on its transport is what keeps a `uvx`, `docker`
 *  or bare-binary command out of the npm form, which can only express
 *  `npx -y <pkg>` and saves that whatever it was handed. */
export function editModeFor(s: Server): AddMode {
  if (s.transport !== "stdio") {
    return "remote";
  }
  return npmFormFits(s) ? "npm" : "raw";
}

/** Whether the npm form's save reproduces this record's argv. Narrower than
 *  extractNpxPackage, which answers what a server INSTALLS and so stops at the
 *  package: an argument past it (`npx -y mcp-remote <url>`) survives that
 *  predicate and would be dropped by a save from this form. */
function npmFormFits(s: Server): boolean {
  const pkg = extractNpxPackage(s);
  if (pkg === "") {
    return false;
  }
  const args = (s.args ?? []).map((a) => a.trim()).filter((a) => a !== "");
  return args.length === 2 && args[0] === "-y" && args[1] === pkg;
}

// --- Panel: remote (Streamable HTTP or legacy SSE; the transport is
// chosen via the panel's transport selector and emitted as the ACP
// `type` discriminator — kiro-cli v3/KAS accepts both http and sse) ---

/** Normalise a remote-panel transport-select value to a valid remote
 *  Transport. Only "http" and "sse" are remote transports; anything else
 *  (including undefined) falls back to the recommended "http". */
function remoteTransport(v: string | undefined): Transport {
  return v === "sse" ? "sse" : "http";
}

function initRemotePanel(existing: Server | null): void {
  const name = byId<HTMLInputElement>("mcp-remote-name");
  const url = byId<HTMLInputElement>("mcp-remote-url");
  const transportSel = byId<HTMLSelectElement>("mcp-remote-transport");
  const oauthClientID = byId<HTMLInputElement>("mcp-remote-oauth-client-id");
  const oauthClientSecret = byId<HTMLInputElement>("mcp-remote-oauth-client-secret");
  const headers = byId<HTMLDivElement>("mcp-remote-headers");
  const errEl = byId<HTMLParagraphElement>("mcp-remote-error");
  errEl.classList.add("hidden");
  errEl.textContent = "";

  if (existing !== null) {
    name.value = existing.name;
    url.value = existing.url ?? "";
    oauthClientID.value = existing.oauth_client_id ?? "";
    oauthClientSecret.value = existing.oauth_client_secret ?? "";
    renderKeyPairList(headers, existing.headers ?? [], "header");
  } else {
    name.value = "";
    url.value = "";
    oauthClientID.value = "";
    oauthClientSecret.value = "";
    renderKeyPairList(headers, [], "header");
  }
  // Preselect the stored transport (http/sse); default to http for a new
  // server. A stdio server never reaches this panel (openEditModal routes
  // it to the npm panel), so existing.transport is always http or sse here.
  transportSel.value = remoteTransport(existing?.transport);

  byId<HTMLButtonElement>("mcp-remote-add-header").onclick = (): void => {
    appendKeyPair(headers, { name: "", value: "" }, "header");
  };

  // The SUBMIT event, not the button's click. The remote panel is a real <form>
  // (a password field outside one is what Chromium's "[DOM] Password field is not
  // contained in a form" warns about), so Save is `type="submit"` and Enter in any
  // field reaches the same path — which is what a form is for. `preventDefault` is
  // required: the default action navigates.
  remotePanel().onsubmit = (ev: SubmitEvent): void => {
    ev.preventDefault();
    const body: Partial<Server> = {
      transport: remoteTransport(transportSel.value),
      name: name.value.trim(),
      url: url.value.trim(),
      headers: collectKeyPairs(headers),
      enabled: existing?.enabled ?? true,
    };
    const oauthID = oauthClientID.value.trim();
    if (oauthID !== "") {
      body.oauth_client_id = oauthID;
    }
    // Secret round-trips as "***" when already stored; sending it back
    // unchanged preserves it server-side (mergeSecret), any other value
    // replaces it, empty leaves it untouched on create / clears intent.
    const oauthSecret = oauthClientSecret.value.trim();
    if (oauthSecret !== "") {
      body.oauth_client_secret = oauthSecret;
    }
    void submitServer(body, errEl, byId<HTMLButtonElement>("mcp-remote-save"));
  };
}

/** The remote panel's form element. Resolved by the same `[data-mcp-mode]`
 *  attribute the panel loop uses, so there is one way to name a panel. */
function remotePanel(): HTMLFormElement {
  const form = document.querySelector<HTMLFormElement>(
    'form.mcp-mode-panel[data-mcp-mode="remote"]',
  );
  if (form === null) {
    throw new Error("mcp: remote panel form missing");
  }
  return form;
}

function fillRemoteForm(
  name: string,
  url: string,
  fields: InstallField[],
  transportHint?: string,
): void {
  byId<HTMLInputElement>("mcp-remote-name").value = name;
  byId<HTMLInputElement>("mcp-remote-url").value = url;
  byId<HTMLSelectElement>("mcp-remote-transport").value = remoteTransport(transportHint);
  renderKeyPairList(byId<HTMLDivElement>("mcp-remote-headers"), declaredRows(fields), "header");
}

// --- Panel: paste a block ---
//
// Every MCP server's README hands out a JSON block, and this is where it goes.
// The panel does NOT translate it: the server owns that (internal/mcp/paste.go),
// because the translation and the naming of an unknown key are one job and it
// belongs at the decode boundary. So this parses only far enough to catch
// invalid JSON without a round trip, then posts the object unchanged.
//
// A block may name SEVERAL servers, and pasting installs all of them — the block
// is one artifact the user copied out of one README, so asking them to pick one
// adds a step that gains nothing. The server is all-or-nothing on failure, so a
// bad entry means nothing lands and the message names it; because a re-paste of
// an already-configured server is a no-op, fixing the block and pasting again
// re-lands the entries that were fine at no cost.

function initRawPanel(existing: Server | null): void {
  const editing = session.editing.id !== "" && existing !== null;
  const textarea = byId<HTMLTextAreaElement>("mcp-raw-input");
  const err = byId<HTMLParagraphElement>("mcp-raw-error");
  err.classList.add("hidden");
  err.textContent = "";
  textarea.value = editing ? storedRecordJSON(existing) : RAW_TEMPLATE;
  showRawEditNote(editing);

  const saveBtn = byId<HTMLButtonElement>("mcp-raw-save");
  setSaveLabel(saveBtn, editing ? "Save" : "Connect");
  saveBtn.onclick = (): void => {
    err.classList.add("hidden");
    err.textContent = "";
    let parsed: unknown;
    try {
      parsed = JSON.parse(textarea.value);
    } catch (e: unknown) {
      showPasteError(err, "Invalid JSON: " + (e instanceof Error ? e.message : String(e)));
      return;
    }
    if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
      showPasteError(err, "Paste a JSON object: either an mcpServers block or one server.");
      return;
    }
    if (editing) {
      // The server owns validation, so the box's own fields go on the wire as
      // they are: a second translator here would be paste.go's rules written
      // twice. `enabled` comes from the record because the row's switch owns
      // it, and the PUT would otherwise decode an absent field as off.
      void submitServer(
        { ...(parsed as Partial<Server>), enabled: existing.enabled },
        err,
        saveBtn,
      );
      return;
    }
    void submitPaste(parsed as Record<string, unknown>, err, saveBtn);
  };
}

/** Fields the modal's other controls own: the store's own three, the row's
 *  enable switch, and the two chip sections' tool lists. Everything else the
 *  record carries reaches the box, so a field added to the wire needs no edit
 *  here. */
const RAW_EDIT_OMIT = new Set([
  "id",
  "created_at",
  "updated_at",
  "enabled",
  "disabled_tools",
  "auto_approve",
]);

function storedRecordJSON(s: Server): string {
  const rec = s as unknown as Record<string, unknown>;
  const out: Record<string, unknown> = {};
  for (const key of Object.keys(rec)) {
    if (!RAW_EDIT_OMIT.has(key)) {
      out[key] = rec[key];
    }
  }
  return JSON.stringify(out, null, 2) + "\n";
}

const CLS_RAW_EDIT = "mcp-raw-edit-note";

/** Say what the box holds while editing: this server's stored record, not a
 *  README's `mcpServers` block, and saving replaces it. */
function showRawEditNote(editing: boolean): void {
  const panel = document.querySelector<HTMLDivElement>('.mcp-mode-panel[data-mcp-mode="raw"]');
  if (panel === null) {
    return;
  }
  panel.querySelector("." + CLS_RAW_EDIT)?.remove();
  if (!editing) {
    return;
  }
  const note = el(
    "p",
    { className: `mcp-mode-hint ${CLS_RAW_EDIT}` },
    "This is the server's stored configuration. Saving replaces it. Secrets read ",
    el("code", {}, SECRET_MASK),
    "; leave them as they are to keep the stored values.",
  );
  panel.querySelector(".mcp-mode-hint")?.after(note);
}

/** Retitle a save button, whose label is the one text node beside its icon. */
function setSaveLabel(btn: HTMLButtonElement, label: string): void {
  for (const node of btn.childNodes) {
    if (node.nodeType === Node.TEXT_NODE && (node.textContent ?? "").trim() !== "") {
      node.textContent = label;
      return;
    }
  }
}

function showPasteError(err: HTMLParagraphElement, msg: string): void {
  err.replaceChildren();
  err.textContent = msg;
  err.classList.remove("hidden");
}

async function submitPaste(
  block: Record<string, unknown>,
  err: HTMLParagraphElement,
  saveBtn: HTMLButtonElement,
): Promise<void> {
  clearFieldMarks();
  const unbind = bindLoadingState("mcp.import_servers", saveBtn, { pendingClass: "btn-loading" });
  const o = await importServers.dispatch(block, {
    onSettled: () => {
      unbind();
    },
  }).outcome;
  if (o.status !== "success") {
    // A pasted block is the case D80 exists for: several fields wrong at once,
    // none of them typed here. The textarea holds every one of them, so there is
    // no input to mark — the list under the box IS the answer.
    showSubmitError(err, o.status === "error" ? o.error : undefined, "Connect failed.");
    return;
  }
  closeModal($.mcpModal);
  mcpState.refetchServers();
  mcpState.refetchStatus();
}

const RAW_TEMPLATE = `{
  "mcpServers": {
    "my-server": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem"],
      "env": {
        "MY_TOKEN": "..."
      }
    }
  }
}
`;

// --- Tool-name chip lists ---

/** One chip editor over a server's tool names. The deny list and the
 *  run-without-asking list are the same control over the same vocabulary —
 *  chips, a typed adder, and the runtime discovery suggestions — so they differ
 *  only in the field they edit and in what removing a chip restores. */
interface ToolListSection {
  readonly sectionID: string;
  readonly chipsID: string;
  readonly inputID: string;
  readonly addID: string;
  /** A chip's remove tooltip, said as what removal does. */
  readonly removeTitle: string;
  /** The suspension surface, present on the ONE list a security profile can
   *  withhold the effect of. Absent means the list is always in force, which is
   *  the deny list: blocking a tool is never widened by a profile, so there is
   *  nothing for a rung to suspend. */
  readonly suspension?: {
    readonly noticeID: string;
    readonly linkID: string;
    /** Whether this list's EFFECT is in force right now. Supplied by the section
     *  rather than read inside the renderer, so the config says where its posture
     *  comes from instead of the renderer hardcoding one list's source. */
    honoured(): boolean;
  };
  /** The list as the record holds it. */
  stored(server: Server): string[] | undefined;
  /** The list as the modal is editing it. */
  read(): string[];
  write(names: string[]): void;
}

const SECTION_DISABLED: ToolListSection = {
  sectionID: "mcp-disabled-tools",
  chipsID: "mcp-disabled-chips",
  inputID: "mcp-disabled-input",
  addID: "mcp-disabled-add",
  removeTitle: "Unblock",
  stored: (s) => s.disabled_tools,
  read: () => session.disabledToolsList,
  write: (names) => {
    session.disabledToolsList = names;
  },
};

const SECTION_AUTO_APPROVE: ToolListSection = {
  sectionID: "mcp-auto-approve",
  chipsID: "mcp-auto-approve-chips",
  inputID: "mcp-auto-approve-input",
  addID: "mcp-auto-approve-add",
  removeTitle: "Ask again",
  suspension: {
    noticeID: "mcp-auto-approve-suspended",
    linkID: "mcp-auto-approve-profile-link",
    honoured: () => autoApproveHonoured.peek(),
  },
  stored: (s) => s.auto_approve,
  read: () => session.autoApproveList,
  write: (names) => {
    session.autoApproveList = names;
  },
};

/** Mark a tool list as recorded-but-not-in-force when the security profile in
 *  force does not honour it.
 *
 *  The names stay VISIBLE and stay EDITABLE, and both halves are the point: the
 *  record is the user's intent and it applies again on a profile that honours it,
 *  so only the EFFECT is suspended. Dropping the chips, or disabling the adder,
 *  would reproduce the defect this whole mechanism exists to remove — a grant
 *  nobody could see — one state over.
 *
 *  The posture is read UNTRACKED, and the pre-fetch window that would make that
 *  wrong is unreachable here: this section renders only when EDITING an existing
 *  server, and a server row exists only because the same `GET /api/mcp` response
 *  that carries the posture has already landed. A profile cannot change while
 *  this modal is open either — its picker is a panel behind it — so there is no
 *  live change for an effect to follow and none is registered.
 *
 *  The link NAVIGATES and nothing else, which is the constraint
 *  `permission.ts`'s buildPolicyPointer established: the profile is Settings-only,
 *  so a surface that would benefit from a looser one must never itself be a path
 *  that loosens it. There is deliberately no control here that changes a profile.
 *
 *  It reuses that pointer's two classes rather than restating their look: one
 *  idiom — a quiet line pointing at the profile picker — should read one way
 *  wherever it appears, and a second copy of the rules is what drifts. The names
 *  are the dock's (`approval-*`) because that is where the idiom started;
 *  renaming them app-wide is out of this change's scope. */
function applySuspension(cfg: ToolListSection, section: HTMLDivElement): void {
  const sus = cfg.suspension;
  if (sus === undefined) {
    return;
  }
  const honoured = sus.honoured();
  byId<HTMLDivElement>(sus.noticeID).classList.toggle("hidden", honoured);
  section.toggleAttribute("data-suspended", !honoured);
  // ASSIGNED, not added: this runs on every modal open, and addEventListener
  // would stack one listener per open.
  byId<HTMLButtonElement>(sus.linkID).onclick = (): void => {
    openSetting("permissions", "security-profile-list");
  };
}

function initToolListSection(cfg: ToolListSection, server: Server | null): void {
  const section = byId<HTMLDivElement>(cfg.sectionID);
  const chips = byId<HTMLDivElement>(cfg.chipsID);
  const input = byId<HTMLInputElement>(cfg.inputID);
  const addBtn = byId<HTMLButtonElement>(cfg.addID);

  if (server === null) {
    section.classList.add("hidden");
    cfg.write([]);
    return;
  }

  section.classList.remove("hidden");
  applySuspension(cfg, section);
  cfg.write([...(cfg.stored(server) ?? [])]);
  // The tool names come from the RUNTIME status (what the connected server
  // advertises), not from the config record — they are a discovery result, and
  // the config file is KAS's now. Empty until a chat has connected the server,
  // which is the honest state: nothing has told us its tools yet.
  const knownTools = discoverySignalFor(server.name).peek().tools;
  renderToolChips(cfg, chips, section, knownTools);

  const add = (): void => {
    const name = input.value.trim();
    if (name === "" || cfg.read().includes(name)) {
      return;
    }
    cfg.write([...cfg.read(), name]);
    input.value = "";
    renderToolChips(cfg, chips, section, knownTools);
    renderToolSuggestions(cfg, section, knownTools, chips);
  };

  addBtn.onclick = add;
  input.onkeydown = (e: KeyboardEvent): void => {
    if (e.key === "Enter") {
      e.preventDefault();
      add();
    }
  };

  // Render known tools as clickable suggestions below the input.
  renderToolSuggestions(cfg, section, knownTools, chips);
}

function renderToolSuggestions(
  cfg: ToolListSection,
  section: HTMLDivElement,
  knownTools: string[],
  chips: HTMLDivElement,
): void {
  section.querySelector(".mcp-tool-suggestions")?.remove();
  const available = knownTools.filter((t) => !cfg.read().includes(t));
  if (available.length === 0) {
    return;
  }

  const label = el("span", { className: "mcp-tool-suggestions-label" }, "Available:");
  const suggestionsEl = el("div", { className: "mcp-tool-suggestions" }, label);

  for (const name of available) {
    const pill = el("button", { type: "button", className: "action-pill mono" }, name);
    pill.addEventListener("click", () => {
      if (!cfg.read().includes(name)) {
        cfg.write([...cfg.read(), name]);
        renderToolChips(cfg, chips, section, knownTools);
        renderToolSuggestions(cfg, section, knownTools, chips);
      }
    });
    suggestionsEl.appendChild(pill);
  }
  section.appendChild(suggestionsEl);
}

function renderToolChips(
  cfg: ToolListSection,
  container: HTMLDivElement,
  section: HTMLDivElement,
  knownTools: string[],
): void {
  container.replaceChildren();
  for (const name of cfg.read()) {
    container.appendChild(
      buildChip({
        label: name,
        code: true,
        chipClass: "chip mono",
        removeTitle: cfg.removeTitle,
        onRemove: () => {
          cfg.write(cfg.read().filter((n) => n !== name));
          renderToolChips(cfg, container, section, knownTools);
          renderToolSuggestions(cfg, section, knownTools, container);
        },
      }),
    );
  }
}
