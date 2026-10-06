// Add/edit modal forms (registry search, npm, remote, raw JSON), submit helpers and pair editors.

import { $, byId } from "./dom.js";
import { el } from "@cplieger/reactive";
import { closeModal, RollingOutput } from "./modals.js";
import {
  type Server,
  type Transport,
  SECRET_MASK,
  mcpState,
  discoverySignalFor,
} from "./mcp-state.js";
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

export type AddMode = "search" | "remote" | "npm" | "raw";

interface EditingContext {
  id: string;
}

class EditSession {
  editing: EditingContext = { id: "" };
  disabledToolsList: string[] = [];

  reset(): void {
    this.editing = { id: "" };
    this.disabledToolsList = [];
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

/** Cancel in-flight work and tear down the search subscription when the modal is dismissed. */
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

/** Wired once per bar: the controller adds listeners, and the modal opens many times. */
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
  setMode(args.mode, args.server);
}

// Each mode's initialiser. The npm form states its own transport at its save site.
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

// `data-mcp-mode` marks a panel and a tab button per mode, so the selector must say which.
const PANEL_SELECTOR = ".mcp-mode-panel[data-mcp-mode]";

function setMode(mode: AddMode, existing: Server | null): void {
  // HTMLElement: the remote panel is a <form>, and this loop touches only classList and dataset.
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

// One response can name several bad fields, so the fields are marked and the messages listed under them.

/** Wire field name -> input id; the wire names are the server's own. A field with no input still prints its message. */
const FIELD_INPUT_IDS: Readonly<Record<string, readonly string[]>> = {
  name: ["mcp-remote-name", "mcp-npm-name"],
  url: ["mcp-remote-url"],
  command: ["mcp-npm-pkg"],
  args: ["mcp-npm-pkg"],
  transport: ["mcp-remote-transport"],
  headers: ["mcp-remote-headers"],
  env: ["mcp-npm-env"],
  oauth_client_id: ["mcp-remote-oauth-client-id"],
};

const CLS_FIELD_INVALID = "field-invalid";

/** Runs at the top of each submit, so a fixed field stops claiming to be wrong. */
function clearFieldMarks(): void {
  for (const node of document.querySelectorAll<HTMLElement>("." + CLS_FIELD_INVALID)) {
    node.classList.remove(CLS_FIELD_INVALID);
    node.removeAttribute("aria-invalid");
  }
}

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

/** A validation failure lists every named field; any other failure keeps one message, so the list is absent there. */
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

/**
 * Fields no form control edits, copied from the stored record: Update replaces the whole record, so an omitted field
 * is cleared. The raw panel sends its box instead.
 */
function formCarry(existing: Server | null): Partial<Server> {
  const out: Partial<Server> = {};
  if (existing?.wait_for_ready !== undefined) {
    out.wait_for_ready = existing.wait_for_ready;
  }
  if (existing?.timeout_ms !== undefined) {
    out.timeout_ms = existing.timeout_ms;
  }
  if (existing?.oauth_client_metadata_url !== undefined) {
    out.oauth_client_metadata_url = existing.oauth_client_metadata_url;
  }
  if (existing?.oauth_redirect_uri !== undefined) {
    out.oauth_redirect_uri = existing.oauth_redirect_uri;
  }
  return out;
}

async function submitServer(
  body: Partial<Server>,
  errEl: HTMLElement,
  saveBtn: HTMLButtonElement | null,
): Promise<boolean> {
  errEl.classList.add("hidden");
  errEl.replaceChildren();
  clearFieldMarks();

  // The list goes out on every edit, empty included: the store reads an omitted list as unchanged.
  if (session.editing.id !== "") {
    body.disabled_tools = session.disabledToolsList;
  }

  const unbind =
    saveBtn != null
      ? bindLoadingState("mcp.save_server", saveBtn, { pendingClass: "btn-loading" })
      : undefined;

  // The typed outcome is this dispatch's own, so a concurrent save cannot cross into the inline error.
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
  // A save can change what KAS runs, so status is re-read; both fetches coalesce per microtask.
  mcpState.refetchStatus();
  return true;
}

setSwitchMode((kind, slug, identifier, fields) => {
  if (kind === "npm") {
    setMode("npm", null);
    fillNpmForm(slug, identifier, fields);
  } else {
    // A non-npm registry hit lands on the remote panel with `kind` preselected. That holds only while npm is the one
    // package registry the server surfaces (registry_proxy.go supportedPackageRegistries).
    setMode("remote", null);
    fillRemoteForm(slug, identifier, fields, kind);
  }
});

function initNpmPanel(existing: Server | null): void {
  const name = byId<HTMLInputElement>("mcp-npm-name");
  const pkg = byId<HTMLInputElement>("mcp-npm-pkg");
  const prewarm = byId<HTMLInputElement>("mcp-npm-prewarm");
  const envList = byId<HTMLDivElement>("mcp-npm-env");
  const errEl = byId<HTMLParagraphElement>("mcp-npm-error");
  errEl.classList.add("hidden");
  errEl.textContent = "";

  showOtherCommandsNote();

  // npx servers need the opt-in Node runtime; probe it and gate the form.
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
        ...formCarry(existing),
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

/** Paste JSON is the only tab that can express a `uvx`, `docker` or bare-binary server. */
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

// The banner installs Node in one click; the fields stay usable meanwhile, and the banner removes itself on success.
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
    return; // Node already present.
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
      // Re-probe; drop the banner if npx is now present.
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

/** The publisher's description and required/secret markers ride onto the form rows. */
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

/** `prewarm.NpmPkgSpecRe`, transcribed. A leading `-` fails the first class, refusing a flag. */
const NPM_PKG_SPEC =
  /^(?:@[a-z0-9][a-z0-9._-]*\/)?[a-z0-9][a-z0-9._-]*(?:@[A-Za-z0-9^~><=.+_-][A-Za-z0-9^~><=.+_-]*)?$/;

/**
 * The npm package a stdio server runs through `npx`, or "". Transcribed from `internal/mcp/prewarm`'s
 * ExtractNpxPackage so both halves agree: the command must be npx, a flag past the package refuses, and the spec
 * must match NpmPkgSpecRe.
 */
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

/**
 * The panel that can edit this server without rewriting it, chosen by the record's shape: the npm form can only
 * express `npx -y <pkg>`.
 */
export function editModeFor(s: Server): AddMode {
  if (s.transport !== "stdio") {
    return "remote";
  }
  return npmFormFits(s) ? "npm" : "raw";
}

/** Narrower than extractNpxPackage: an argument past the package (`npx -y mcp-remote <url>`) would be dropped. */
function npmFormFits(s: Server): boolean {
  const pkg = extractNpxPackage(s);
  if (pkg === "") {
    return false;
  }
  const args = (s.args ?? []).map((a) => a.trim()).filter((a) => a !== "");
  return args.length === 2 && args[0] === "-y" && args[1] === pkg;
}

// Remote panel: Streamable HTTP or legacy SSE, emitted as the ACP `type` discriminator.

/** Anything but "http" or "sse" falls back to "http". */
function remoteTransport(v: string | undefined): Transport {
  return v === "sse" ? "sse" : "http";
}

function initRemotePanel(existing: Server | null): void {
  const name = byId<HTMLInputElement>("mcp-remote-name");
  const url = byId<HTMLInputElement>("mcp-remote-url");
  const transportSel = byId<HTMLSelectElement>("mcp-remote-transport");
  const oauthClientID = byId<HTMLInputElement>("mcp-remote-oauth-client-id");
  const headers = byId<HTMLDivElement>("mcp-remote-headers");
  const errEl = byId<HTMLParagraphElement>("mcp-remote-error");
  errEl.classList.add("hidden");
  errEl.textContent = "";

  if (existing !== null) {
    name.value = existing.name;
    url.value = existing.url ?? "";
    oauthClientID.value = existing.oauth_client_id ?? "";
    renderKeyPairList(headers, existing.headers ?? [], "header");
  } else {
    name.value = "";
    url.value = "";
    oauthClientID.value = "";
    renderKeyPairList(headers, [], "header");
  }
  // A stdio server never reaches this panel, so the stored transport is http or sse.
  transportSel.value = remoteTransport(existing?.transport);

  byId<HTMLButtonElement>("mcp-remote-add-header").onclick = (): void => {
    appendKeyPair(headers, { name: "", value: "" }, "header");
  };

  // The submit event: the remote panel is a real <form> (its password fields need one), so Enter submits too.
  // `preventDefault` is required, or the form navigates.
  remotePanel().onsubmit = (ev: SubmitEvent): void => {
    ev.preventDefault();
    const body: Partial<Server> = {
      ...formCarry(existing),
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
    void submitServer(body, errEl, byId<HTMLButtonElement>("mcp-remote-save"));
  };
}

/** Resolved by the same attribute the panel loop uses. */
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

// Paste a README's JSON block. The server translates it (internal/mcp/paste.go); this parses only enough to catch
// invalid JSON. A block naming several servers installs all of them, all-or-nothing; re-pasting a configured server
// is a no-op.

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
      // The box's fields go out as they are: the server validates. `enabled` comes from the record, or the PUT would read
      // an absent field as off.
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

/** The store's own fields, the enable switch, and the chip section's tool list; everything else reaches the box. */
const RAW_EDIT_OMIT = new Set(["id", "created_at", "updated_at", "enabled", "disabled_tools"]);

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

/** The box holds this server's stored record, and saving replaces it. */
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

/** The label is the one text node beside the icon. */
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
    // A pasted block can be wrong in several fields with no input to mark, so the list under the box is the answer.
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

/** The chip editor over a server's blocked tool names: chips, a typed adder,
 *  and the runtime discovery suggestions. */
interface ToolListSection {
  readonly sectionID: string;
  readonly chipsID: string;
  readonly inputID: string;
  readonly addID: string;
  /** A chip's remove tooltip, said as what removal does. */
  readonly removeTitle: string;
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
  cfg.write([...(cfg.stored(server) ?? [])]);
  // Tool names come from the runtime status (what the connected server advertises), not the config; empty until a
  // chat has connected it.
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
