// The surfaces the touch contract mounts: the production `#app` markup and modals, opened by the
// production openers, with what scripts add built by its exported builder where one builds without app
// state and planted by hand in that module's shape where none does. `touch-targets.test.ts` walks them
// on the coarse tier and `button-size.test.ts` classifies every control on them.
import { expect } from "vitest";
import { http, HttpResponse } from "msw";
import { setupWorker } from "msw/browser";
import { _resetForTest as resetAsk } from "../legacy-ui/ask.js";
import { _resetForTest as resetScrollLock } from "../legacy-ui/modal.js";

import indexHtml from "../../static/index.html?raw";
import type { Pele } from "../mermaid-svg.js";
import type { Inventory, PolicyView, SearchResponse } from "../wire/types.gen.js";
import type { RunNode } from "../run-store.js";
import { settingsPayload } from "./settings.js";

export type Size = 1180 | 820 | 390;
export const VIEWPORTS: Record<Size, readonly [number, number]> = {
  1180: [1180, 820],
  820: [820, 1180],
  390: [390, 844],
};
export const TWO: readonly Size[] = [1180, 390];
export const ALL: readonly Size[] = [1180, 820, 390];

function mountApp(): void {
  resetAsk();
  document.documentElement.dataset["pointer"] = "coarse";
  const doc = new DOMParser().parseFromString(indexHtml, "text/html");
  const app = doc.getElementById("app");
  if (app === null) {
    throw new Error("index.html has no #app");
  }
  const modals = [...doc.querySelectorAll("body > [data-modal]")];
  document.body.replaceChildren(...[app, ...modals].map((n) => document.importNode(n, true)));
}

// Several builders' import graphs reach the shared DOM registry, which throws on a missing app
// element, so the app is mounted before they load.
mountApp();
const { confirm } = await import("../confirm.js");
const { entryList, entryRow } = await import("../entry-row.js");
const { buildSteerNote } = await import("../fundamentals/steer-note.js");
const { buildRunCard } = await import("../fundamentals/run-card.js");
const { buildSubagentCard } = await import("../fundamentals/subagent-block.js");
const { buildAssistantBubble } = await import("../fundamentals/text-bubble.js");
const { buildToolCard } = await import("../tool-card.js");
const { buildToolGroupShell, groupBody, refreshGroupHeader } = await import("../tool-group.js");
const { stampHookCard } = await import("../hook-fold.js");
const { iconEl } = await import("../icon-el.js");
const { ICON_CHEVRON_DOWN, ICON_CHEVRON_UP, ICON_CLOSE, ICON_RUN_WHEN_UI, ICON_SEND, modelIcon } =
  await import("../icons.js");
const { FallbackSlot } = await import("../model-fallback-row.js");
const { modeOption } = await import("../role-picker.js");
const { openModal } = await import("../modals.js");
const { createSearchShell, searchIconButton } = await import("../search-shell.js");
const { createSearchPopup } = await import("../search-popup.js");
const { info, _resetForTest: resetToasts } = await import("../toast.js");
const { buildPermissionCard } = await import("../permission.js");
const { buildUserInputCard } = await import("../user-input.js");
const { buildElicitationCard } = await import("../elicitation.js");
const { buildEffortSlider } = await import("../effort-slider.js");
const { buildAttachmentPill } = await import("../attachment-pill.js");
const { buildChip } = await import("../chip.js");
const { initNativePolicyUI, loadNativePolicy } = await import("../permissions-ui.js");
const { initTools, loadToolsList } = await import("../tools.js");
const { formatDate, formatSize } = await import("../files-shared.js");
const { initKnowledge, loadKnowledge } = await import("../knowledge.js");
const { mountNewDocButton } = await import("../docs-new.js");
const { mountSteerStack } = await import("../pending-steers.js");
const { makeExpandable } = await import("../pill-expand.js");
const { buildExecPage } = await import("../exec-view/page.js");
const { buildStepEndView } = await import("../run-controls-view.js");
const { renderRegistryResult } = await import("../mcp-panels-search.js");
const { renderKeyPairList } = await import("../mcp-pairs.js");
const { buildReasoning } = await import("../fundamentals/reasoning.js");
const { syncCodeReferences } = await import("../code-refs.js");
const { buildTurnFooter } = await import("../fundamentals/turn-footer.js");
const { buildTurnHeader } = await import("../fundamentals/turn-header.js");
const { wireRowToggle } = await import("../disclosure-row.js");
const { renderRecentCommits } = await import("../git-changes-commit.js");
const { renderRepoRow } = await import("../forge-auth-repos.js");
const { buildRunDefaultsPicker } = await import("../run-defaults-picker.js");
const { showContextMenu } = await import("../context-menu.js");
const { buildSchedulePicker, defaultSpec } = await import("../schedule-picker.js");
const { buildEventForm, buildRuleList } = await import("../run-when.js");
const { mountRow } = await import("../mcp-ui.js");
const { openMermaidOverlay } = await import("../mermaid-overlay.js");
const { renderDiffPane } = await import("../diff-pane.js");
const { renderConnectionTarget } = await import("../forge-auth-connection.js");
const { setWorkspaceRoot, _resetForTest: resetWorkspaceRoot } = await import("../workspace.js");
const { openBranchSwitcher } = await import("../git-branch-switcher.js");
const { initDiagnostics } = await import("../settings.js");
document.body.replaceChildren();

const teardown: (() => void)[] = [];

export function clearSurface(): void {
  for (const t of teardown.splice(0)) {
    t();
  }
  resetAsk();
  resetToasts();
  resetScrollLock();
  document.body.replaceChildren();
}

export function q<T extends HTMLElement>(sel: string): T {
  const found = document.querySelector<T>(sel);
  if (found === null) {
    throw new Error(`fixture: no ${sel}`);
  }
  return found;
}

function make(tag: string, className: string, attrs: Record<string, string> = {}): HTMLElement {
  const e = document.createElement(tag);
  e.className = className;
  for (const [k, v] of Object.entries(attrs)) {
    e.setAttribute(k, v);
  }
  return e;
}

function text(tag: string, className: string, words: string): HTMLElement {
  const e = make(tag, className);
  e.textContent = words;
  return e;
}

function glyph(): SVGSVGElement {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("class", "ic-ui");
  svg.setAttribute("viewBox", "0 0 24 24");
  return svg;
}

function iconButton(className: string, label: string): HTMLButtonElement {
  const b = make("button", className, { type: "button", "aria-label": label }) as HTMLButtonElement;
  b.appendChild(glyph());
  return b;
}

function button(className: string, label: string): HTMLButtonElement {
  const b = make("button", className, { type: "button" }) as HTMLButtonElement;
  b.textContent = label;
  return b;
}

function roleButton(className: string, label: string): HTMLElement {
  const b = make("div", className, { role: "button", tabindex: "0" });
  b.textContent = label;
  return b;
}

function box(className: string, children: readonly (Node | null)[]): HTMLElement {
  const div = make("div", className);
  div.append(...children.filter((c) => c !== null));
  return div;
}

function show(view: string): void {
  for (const v of document.querySelectorAll("[data-tab-view]")) {
    v.classList.toggle("hidden", v.id !== view);
  }
}

function showPanel(attr: string, name: string): void {
  for (const p of document.querySelectorAll<HTMLElement>(`[${attr}]`)) {
    p.classList.toggle("hidden", p.getAttribute(attr) !== name);
  }
}

/** Fill a composer popup, then open it through `pill-expand.ts`, which clamps it to the screen. */
function openCard(pill: string, sel: string, fill: (card: HTMLElement) => void): void {
  const card = q(sel);
  fill(card);
  const abort = new AbortController();
  teardown.push(() => {
    abort.abort();
  });
  makeExpandable(q(pill), card, { signal: abort.signal });
  q(pill).click();
}

/** Clear `hidden` from an element and every ancestor up to `top`. */
function reveal(sel: string, top: string): void {
  const stop = q(top);
  for (let e: HTMLElement | null = q(sel); e !== null; e = e.parentElement) {
    e.classList.remove("hidden");
    e.hidden = false;
    if (e === stop) {
      break;
    }
  }
}

function plantTabs(): void {
  const list = q("#tab-list");
  for (let i = 0; i < 4; i++) {
    const row = make("div", "tab", { role: "tab", tabindex: i === 0 ? "0" : "-1" });
    const close = make("span", "tab-close", { "aria-hidden": "true" });
    close.append(iconEl(ICON_CLOSE));
    row.append(text("span", "tab-name", `Conversation ${String(i)}`), close);
    list.appendChild(row);
  }
}

/** The History section unfolded with one closed chat above More, as `history-section.ts` draws it. */
export function plantHistorySection(): void {
  q("#history-section").hidden = false;
  const list = q("#history-list");
  const row = make("button", "history-row", {
    type: "button",
    "aria-label": "Reopen Yesterday's fix",
  });
  row.append(
    make("span", "tab-status-dot", { "aria-hidden": "true", "data-status": "closed" }),
    text("bdi", "history-row-name", "Yesterday's fix"),
    text("time", "history-row-time", "14:05"),
  );
  const remove = make("button", "history-row-delete", {
    type: "button",
    "aria-label": "Delete Yesterday's fix",
  });
  remove.appendChild(glyph());
  const li = make("li", "history-item");
  li.append(row, remove);
  list.prepend(li);
}

/** The Runs section unfolded with one working run, as `runs-section.ts` draws it. */
export function plantRunsSection(): void {
  q("#runs-section").hidden = false;
  const link = make("a", "runs-row", { href: "/run/wf_1", "data-tooltip": "deadset-ci" });
  link.append(
    make("span", "tab-status-dot", { "aria-hidden": "true", "data-status": "working" }),
    text("bdi", "runs-row-name", "deadset-ci"),
    text("span", "sr-only", ", working"),
  );
  const li = make("li", "runs-item");
  li.appendChild(link);
  q("#runs-list").replaceChildren(li);
}

export function turn(...body: HTMLElement[]): HTMLElement {
  const t = make("div", "turn");
  const header = buildTurnHeader({
    n: 1,
    outcome: "completed",
    // A real time: the header writes `.turn-ts` only for one.
    ts: Date.UTC(2026, 10, 17, 14, 32),
    request: "Fix the build",
    attachments: [],
  });
  // `messages.ts` wires the fold this way once the turn is in the transcript.
  const fold = header.querySelector<HTMLElement>(".turn-fold-toggle");
  if (fold === null) {
    throw new Error("fixture: the turn header has no fold toggle");
  }
  wireRowToggle(header, fold);
  t.append(header, box("turn-body", body));
  const host = q("#messages");
  let view = host.querySelector(":scope > .transcript-view");
  if (view === null) {
    view = make("div", "transcript-view is-active");
    host.appendChild(view);
  }
  view.appendChild(t);
  return t;
}

/** A finished turn: its fold, an open tool group with two file edits and a folded passing guard counted on its
 *  hooks badge, its footer's actions, in the shapes `turns.ts` and `fundamentals/turn-footer.ts` build. */
function plantTurn(): void {
  const shell = buildToolGroupShell();
  for (const path of ["static-src/app.ts", "README.md"]) {
    groupBody(shell).appendChild(
      buildToolCard({
        id: path,
        title: "Edit File",
        kind: "edit",
        status: "completed",
        live: false,
        input: { path, oldStr: "a", newStr: "b" },
        output: "Applied 1 edit.",
      }),
    );
  }
  const guard = buildToolCard({
    id: "hook-op-1",
    title: "Hook fired: Destructive command guard",
    kind: "hook",
    status: "completed",
    live: false,
    sourcePath: ".kiro/hooks/destructive-guard.json",
  });
  stampHookCard(guard, { status: "completed", hook_trigger: "PreToolUse" });
  groupBody(shell).appendChild(guard);
  const t = turn(buildReasoning("Read the two files first.", false, false).root, shell);
  refreshGroupHeader(shell);

  const footer = make("div", "turn-footer");
  const actions = make("span", "turn-actions-buttons");
  const more = make("details", "turn-actions-more");
  const summary = make("summary", "turn-action-btn turn-action-more", {
    "aria-label": "More turn actions",
  });
  summary.appendChild(glyph());
  const actionGroup = make("span", "turn-actions-group");
  for (const label of ["Copy as text", "Copy as markdown", "Copy link to this turn"]) {
    // The label span is what the menu rows show; inline it is hidden.
    const text = make("span", "turn-action-label");
    text.textContent = label;
    const b = iconButton("turn-action-btn", label);
    b.appendChild(text);
    actionGroup.appendChild(b);
  }
  more.append(summary, actionGroup);
  actions.append(more, iconButton("turn-rewind", "Rewind"));
  footer.append(button("turn-ledger-summary", "Completed"), actions);
  t.appendChild(footer);
}

/** A lone file edit with a folded passing guard, so the hooks badge sits on the edit card's own header beside its
 *  file chip. */
function plantLoneEdit(): void {
  const shell = buildToolGroupShell();
  groupBody(shell).appendChild(
    buildToolCard({
      id: "lone-edit",
      title: "Edit File",
      kind: "edit",
      status: "completed",
      live: false,
      input: { path: "static-src/a-long-module-name-for-the-chip.ts", oldStr: "a", newStr: "b" },
      output: "Applied 1 edit.",
    }),
  );
  const guard = buildToolCard({
    id: "hook-op-2",
    title: "Hook fired: Destructive command guard",
    kind: "hook",
    status: "completed",
    live: false,
    sourcePath: ".kiro/hooks/destructive-guard.json",
  });
  stampHookCard(guard, { status: "completed", hook_trigger: "PreToolUse" });
  groupBody(shell).appendChild(guard);
  turn(shell);
  refreshGroupHeader(shell);
}

/** Sixty lines with every tenth one edited: more hunks than the diff preview's rows show. */
const LONG_EDIT = ((): { oldStr: string; newStr: string } => {
  const lines = Array.from({ length: 60 }, (_, i) => `line ${String(i)}`);
  return {
    oldStr: lines.join("\n"),
    newStr: lines.map((l, i) => (i % 10 === 0 ? `${l} edited` : l)).join("\n"),
  };
})();

/** An edit whose diff preview omits hunks, so it shows the "more hunks" control. */
function plantLongEdit(): void {
  const shell = buildToolGroupShell();
  groupBody(shell).appendChild(
    buildToolCard({
      id: "long-edit",
      title: "Edit File",
      kind: "edit",
      status: "completed",
      live: false,
      input: { path: "README.md", ...LONG_EDIT },
      output: "Applied 1 edit.",
    }),
  );
  turn(shell);
  refreshGroupHeader(shell);
}

/** A running workflow's card with three steps, and a step's note naming the run. */
function plantRun(): void {
  const view = buildRunCard("wf", "deadset-ci", () => undefined);
  const note = buildSteerNote({
    text: "Build finished.",
    origin: "step",
    dropped: false,
    step: "build",
    run: "wf",
    runLabel: "deadset-ci",
    onOpenRun: () => undefined,
  });
  turn(view.root, note);
  view.render({
    workflowId: "wf",
    status: "running",
    root: {
      nodeId: "wf",
      type: "sequence",
      status: "running",
      children: [
        { nodeId: "build", type: "step", status: "completed" },
        { nodeId: "test", type: "step", status: "running" },
        { nodeId: "land", type: "step", status: "completed" },
      ],
    },
  });
}

const step = (nodeId: string): { nodeId: string; type: "step"; status: "completed" } => ({
  nodeId,
  type: "step",
  status: "completed",
});

/** An inner loop on its second pass, so the progress phrase shows its "› 2" part. */
const NESTED = {
  nodeId: "patch-loop",
  type: "repeat",
  status: "running",
  children: [
    {
      nodeId: "patch-loop#0",
      type: "sequence",
      status: "completed",
      iteration: 0,
      children: [step("patch")],
    },
    {
      nodeId: "patch-loop#1",
      type: "sequence",
      status: "running",
      iteration: 1,
      children: [{ nodeId: "patch", type: "step", status: "running" }],
    },
  ],
} satisfies RunNode;

/** A run card whose loop holds three steps, beside a top-level one; `nested` adds an inner loop. */
function plantLoop(nested = false): void {
  const view = buildRunCard("wf", "deadset-ci", () => undefined);
  turn(view.root);
  view.render({
    workflowId: "wf",
    status: "running",
    root: {
      nodeId: "wf",
      type: "sequence",
      status: "running",
      children: [
        step("plan"),
        step("prepare"),
        {
          nodeId: "loop",
          type: "repeat",
          status: "running",
          children: [
            {
              nodeId: "loop#0",
              type: "sequence",
              status: "running",
              iteration: 0,
              children: [step("code"), step("review"), step("fix"), ...(nested ? [NESTED] : [])],
            },
          ],
        },
      ],
    },
  });
}

/** A finished turn's footer with its details open: file rows, two disclosure rows and the
 *  steering one open on its document link. */
function plantTurnDetails(): void {
  setWorkspaceRoot("/workspace");
  teardown.push(resetWorkspaceRoot);
  const footer = buildTurnFooter({
    changedFiles: {
      "static-src/app.ts": { lines_added: 4, lines_removed: 1 },
      "README.md": { lines_added: 2, lines_removed: 0, is_new_file: true },
    },
    requestIds: ["req-1", "req-2"],
    steering: ["file:///workspace/.kiro/steering/a.md"],
  });
  turn().appendChild(footer);
  q(".turn-ledger-summary").click();
  for (const b of footer.querySelectorAll<HTMLButtonElement>("button.turn-info-dbtn")) {
    if (b.getAttribute("aria-expanded") !== "true") {
      b.click();
    }
  }
  q(".turn-info-link");
}

/** A turn's footer with its More menu open. */
function plantTurnMore(): void {
  plantTurn();
  // The menu paints only under the attribute the footer's fit writes, which this plant never runs.
  q(".turn-footer").setAttribute("data-actions-menu", "");
  q<HTMLDetailsElement>(".turn-actions-more").open = true;
}

/** A turn's licensed-code notice, opened on its one linked reference. */
function plantCodeRefs(): void {
  const t = turn(buildAssistantBubble("Done.", false).root);
  syncCodeReferences(t, [
    { license_name: "MIT", repository: "ex/lib", url: "https://example.invalid/ex/lib" },
  ]);
  q<HTMLDetailsElement>(".code-refs").open = true;
}

/** The tab context menu, through its production opener. */
function plantContextMenu(): void {
  const dismiss = showContextMenu(
    ["Rename", "Pin", "Close"].map((label) => ({ label, action: () => undefined })),
    { x: 40, y: 120 },
  );
  teardown.push(dismiss);
}

/** The Spec tab's task tree in `spec-view.ts`'s shape: a parent with two runnable leaves. */
function plantSpec(): void {
  show("spec-view");
  const node = (depth: number, label: string, parent: boolean): HTMLElement => {
    const row = make("div", "spec-node-row");
    row.style.setProperty("--depth", String(depth));
    row.appendChild(
      parent
        ? make("button", "spec-toggle", { type: "button", "aria-label": "Collapse" })
        : make("span", "spec-toggle-spacer"),
    );
    row.append(make("span", "spec-status"), text("span", "spec-task-text", label));
    if (!parent) {
      row.appendChild(button("btn-small spec-run", "Run"));
    }
    return box("spec-node", [row]);
  };
  const select = make("select", "spec-run-in-select", { "aria-label": "Chat to run tasks in" });
  select.appendChild(text("option", "", "Chat"));
  const runIn = box("spec-run-in-row", [select, button("btn-small spec-run-in", "Run in")]);
  const tree = box("spec-tree", [
    node(0, "Build the parser", true),
    node(1, "Write the lexer", false),
    node(1, "Write the grammar", false),
    node(0, "Ship the docs", true),
  ]);
  q("#spec-body").append(runIn, box("spec-tasks", [tree]));
}

/** The git page's Sources tab with an account's repository rows, through `renderRepoRow`. */
function plantRepoRows(): void {
  show("git-view");
  showPanel("data-git-panel", "sources");
  const list = make("ul", "forge-account-repos-list");
  for (const name of ["marotte", "subflux"]) {
    list.appendChild(
      renderRepoRow(
        {
          repo_id: name,
          owner: "cplieger",
          name,
          full_name: `cplieger/${name}`,
          url: `https://example.invalid/cplieger/${name}`,
          clone_url: `https://example.invalid/cplieger/${name}.git`,
        } as Parameters<typeof renderRepoRow>[0],
        {
          isCloned: () => false,
          addCloned: () => undefined,
          removeCloned: () => undefined,
          bumpState: () => undefined,
          start: () => undefined,
          running: () => undefined,
        },
      ),
    );
  }
  const block = box("forge-account-repos", [box("forge-account-repos-body", [list])]);
  q("#git-sources-mount").appendChild(block);
}

/** Two recipe rows in `recipes.ts`'s shape: Run when… (an icon and its count on a phone) and Run. */
function plantRecipeRows(): void {
  show("docs-view");
  showPanel("data-docs-panel", "workflows");
  const compact = window.matchMedia("(width <= 48rem)").matches;
  const list = entryList();
  for (const [key, count] of [
    ["goal", "3"],
    ["ralph", ""],
  ] as const) {
    const sched = make("button", `btn-small recipe-sched-btn on${compact ? " is-compact" : ""}`, {
      type: "button",
      "aria-label": `Run when… ${count} for ${key}`,
    });
    const icon = iconEl(ICON_RUN_WHEN_UI);
    icon.classList.add("recipe-sched-icon");
    sched.append(icon, text("span", "recipe-sched-label", "Run when…"));
    sched.append(text("span", "recipe-sched-count", count));
    list.appendChild(
      entryRow({
        key,
        title: key,
        badges: [text("span", "docs-badge", "bundled")],
        sub: { kind: "lines", lines: [{ text: `${key} desc` }, { text: "watching" }] },
        actions: [sched, button("btn-small recipe-run-btn", "Run")],
      }),
    );
  }
  q('[data-docs-panel="workflows"]').appendChild(list);
}

/** A recipe's run form in `recipes.ts`'s shape, with the run picker it carries. */
function plantRecipeForm(): void {
  show("docs-view");
  const form = make("form", "recipe-input-form");
  for (const key of ["repo", "branch"]) {
    const label = text("label", "recipe-input-label", `${key}: `);
    label.appendChild(make("input", "recipe-input", { type: "text", "aria-label": key }));
    form.appendChild(label);
  }
  const picker = buildRunDefaultsPicker({ models: [], initial: {} });
  form.append(picker.el, button("btn-small recipe-launch-btn", "Run"));
  q('[data-docs-panel="workflows"]').classList.remove("hidden");
  showPanel("data-docs-panel", "workflows");
  q('[data-docs-panel="workflows"]').appendChild(form);
}

/** A reply with a shell block, a delegate's card and a plan, through their builders. */
function plantReply(): void {
  const reply = buildAssistantBubble("Run this:\n\n```sh\nnpm test\n```\n", false);
  const sub = buildSubagentCard("reviewer", "completed", {
    open: { href: "/chat/c/sub/1", open: () => undefined },
  });
  turn(reply.root, sub.root);
}

/** A notice band over the chat with its jump and its dismiss, as `banner-stack.ts` builds one. */
function plantBanner(): void {
  const banner = make("div", "banner banner-warning");
  banner.append(
    text("span", "banner-glyph", "!"),
    text("span", "banner-msg", "kiro-cli is out of date."),
    button("btn-small banner-link", "Update"),
    iconButton("icon-btn banner-dismiss", "Dismiss"),
  );
  q("#banner-stack").appendChild(banner);
}

/** The decision dock holding one card. */
function dock(card: HTMLElement): void {
  const d = q("#decision-dock");
  d.classList.remove("hidden");
  d.appendChild(card);
}

const LONG_QUESTION = Array.from(
  { length: 40 },
  (_, i) => `Line ${String(i)} of a question long enough to clamp.`,
).join(" ");

function plantHistory(): void {
  const list = entryList();
  for (const key of ["a", "b"]) {
    list.appendChild(
      entryRow({
        key,
        title: `Chat ${key}`,
        sub: { kind: "line", text: "two days ago" },
        actions: [iconButton("icon-btn entry-delete", `Delete chat ${key}`)],
        open: { name: `Chat ${key}`, onOpen: () => undefined },
      }),
    );
  }
  q("#history-view .page-shell").appendChild(list);
}

function plantDocs(): void {
  const list = entryList();
  for (const key of ["a", "b"]) {
    list.appendChild(
      entryRow({
        key,
        title: `steering-${key}`,
        sub: { kind: "lines", lines: [{ text: `.kiro/steering/${key}.md`, mono: true }] },
        actions: [
          iconButton("icon-btn docs-edit", `Edit steering-${key}`),
          iconButton("icon-btn entry-delete", `Delete steering-${key}`),
        ],
        open: { name: `steering-${key}`, onOpen: () => undefined },
      }),
    );
  }
  q('[data-docs-panel="steering"]').appendChild(list);
  mountNewDocButton(
    q("#docs-new-slot"),
    () => undefined,
    () => undefined,
  )("steering");
}

/** One open repository on the Changes tab with two changed files, as `git-changes-tab.ts` builds it. */
function plantGitRepo(): void {
  const section = make("section", "git-repo-section", { "data-repo": "demo" });
  const header = make("button", "git-repo-section-header", {
    type: "button",
    "aria-expanded": "true",
  });
  header.textContent = "demo";
  const bar = box("git-repo-action-bar", [
    button("btn-small", "Push"),
    button("btn-small", "Stash"),
  ]);
  const list = make("ul", "git-file-list");
  for (const path of ["static-src/app.ts", "README.md"]) {
    const top = box("git-file-row-top", [
      text("span", "git-file-status git-st-m", "M"),
      button("git-file-path", path),
      make("span", "git-file-actions"),
    ]);
    top
      .querySelector(".git-file-actions")
      ?.append(button("btn-small", "Stage"), button("btn-small btn-danger", "Discard"));
    const row = make("li", "git-file-row");
    row.appendChild(top);
    list.appendChild(row);
  }
  const recent = renderRecentCommits(
    { repo: "demo" } as Parameters<typeof renderRecentCommits>[0],
    {
      commitMessages: new Map(),
      diffAbort: null,
      press: (btn) => btn,
    },
  );
  const body = box("git-repo-section-body", [
    box("git-repo-section-body-inner", [bar, list, recent]),
  ]);
  section.append(header, body);
  q("#git-changes-mount").appendChild(section);
}

/** The branch switcher opened from the repo head, its branches answered by a stubbed fetch. */
function plantBranchSwitcher(): void {
  show("git-view");
  plantGitRepo();
  const real = globalThis.fetch;
  globalThis.fetch = (): Promise<Response> =>
    Promise.resolve(
      Response.json({
        branches: [
          { name: "main", current: true },
          { name: "dev", current: false },
          { name: "origin/dev", current: false },
        ],
      }),
    );
  const anchor = q(".git-repo-section-header");
  openBranchSwitcher("demo", anchor);
  teardown.push(() => {
    globalThis.fetch = real;
    // A second open from the same anchor closes it.
    if (document.querySelector(".git-branch-popover") !== null) {
      openBranchSwitcher("demo", anchor);
    }
  });
}

/** Two pull requests with their row actions, in the shape `git-prs-tab.ts` renders. */
function plantPRs(): void {
  const list = make("ul", "git-pr-list");
  for (const n of [12, 13]) {
    const title = make("a", "git-pr-row-title", {
      href: `https://example.invalid/pr/${String(n)}`,
    });
    title.append(
      text("span", "git-pr-row-number", `#${String(n)}`),
      text("span", "git-pr-row-text", "fix: a title"),
    );
    const li = make("li", "git-pr-row");
    li.append(
      title,
      box("git-pr-row-sub", [text("span", "git-pr-row-sub-text", "cplieger wants to merge")]),
      box("git-pr-row-actions", [button("btn-small", "Merge"), button("btn-small", "Close")]),
    );
    list.appendChild(li);
  }
  q("#git-prs-mount").appendChild(list);
}

/** The inventory `tools.ts` renders from GET /api/tools: a tool with an LSP chip, one without a
 *  chip, and a system tool. */
const INVENTORY: Inventory = {
  tools: [
    {
      name: "gopls",
      source: "aqua:golang/tools/gopls",
      version: "0.24.0",
      installed: true,
      installing: false,
      lsp: true,
    },
    {
      name: "ripgrep",
      source: "aqua:BurntSushi/ripgrep",
      version: "15.2.0",
      installed: true,
      installing: false,
    },
  ],
  system: [{ name: "git", installed: true }],
};

let server: ReturnType<typeof setupWorker> | null = null;

/** Serves `handlers` alone over the page's MSW worker, started on first use. */
async function serve(...handlers: Parameters<ReturnType<typeof setupWorker>["resetHandlers"]>) {
  if (server === null) {
    server = setupWorker();
    await server.start({ quiet: true, onUnhandledFrame: "bypass" });
  }
  server.resetHandlers(...handlers);
}

/** Polls until `sel` matches `n` elements, the rows a production loader paints after its fetch. */
async function rendered(sel: string, n: number): Promise<void> {
  for (let i = 0; document.querySelectorAll(sel).length < n; i++) {
    if (i === 100) {
      throw new Error(`fixture: ${sel} never rendered ${String(n)} rows`);
    }
    await new Promise((r) => setTimeout(r, 10));
  }
}

/** The tool rows `tools.ts` renders from the inventory. */
async function plantToolRows(): Promise<void> {
  await serve(http.get("/api/tools", () => HttpResponse.json(INVENTORY)));
  initTools();
  loadToolsList();
  await rendered("#tools-list .list-row", 3);
}

/** GET /api/tools/search's answer: a catalog entry, a Debian package with its version, an LSP. */
const TOOL_HITS: SearchResponse = {
  results: [
    {
      name: "ripgrep",
      source: "aqua:BurntSushi/ripgrep",
      description: "Recursively search directories for a regex pattern",
    },
    {
      name: "ripgrep",
      source: "apt:ripgrep",
      apt: true,
      version: "14.1.1-1",
      description: "Recursively searches directories for a regex pattern",
    },
    {
      name: "pyright",
      source: "npm:pyright",
      lsp: true,
      description: "Static type checker for Python",
    },
  ],
  apt_available: true,
  truncated: false,
};

/** The Add-tool modal's search hits, opened by its button and rendered by `tools.ts`. */
export async function plantToolSearch(): Promise<void> {
  await serve(
    http.get("/api/tools", () => HttpResponse.json(INVENTORY)),
    http.get("/api/tools/search", () => HttpResponse.json(TOOL_HITS)),
  );
  initTools();
  q("#tool-add-btn").click();
  await rendered("#tool-search-results .tool-hit", TOOL_HITS.results.length);
}

/** The knowledge rows `knowledge.ts` renders from GET /api/knowledge. */
async function plantKnowledgeRows(): Promise<void> {
  await serve(
    http.get("/api/knowledge", () =>
      HttpResponse.json({
        contexts: [
          { name: "docs", id: "a", item_count: 42, path: "/workspace/docs" },
          { name: "rust-book", id: "b", item_count: 1 },
        ],
      }),
    ),
  );
  initKnowledge();
  loadKnowledge();
  await rendered("#knowledge-list .list-row", 2);
}

/** The rule table's answer: writable rules with and without globs, and a read-only one. */
const POLICY: PolicyView = {
  available: true,
  profile: "custom",
  run_profile: "custom",
  profile_saved: true,
  profiles: [
    { id: "guarded", presets: [] },
    { id: "custom", presets: [] },
  ],
  writable_scopes: ["user", "workspace"],
  capabilities: ["fs_read", "fs_write", "shell"],
  load_errors: [],
  rules: [
    {
      capability: "fs_read",
      effect: "allow",
      scope: "kiro",
      source: "kiro-scope",
      match: ["/workspace/**", "/data/**"],
      editable: false,
    },
    {
      capability: "fs_write",
      effect: "ask",
      scope: "workspace",
      source: "/workspace/.kiro/settings/permissions.yaml",
      match: ["src/**"],
      editable: true,
    },
    {
      capability: "shell",
      effect: "deny",
      scope: "user",
      source: "/config/home/.kiro/settings/permissions.yaml",
      editable: true,
    },
  ],
};

/** The policy rows `permissions-ui.ts` renders from GET /api/permissions, and a chip list. */
async function plantPermissions(): Promise<void> {
  await serve(http.get("/api/permissions", () => HttpResponse.json(POLICY)));
  initNativePolicyUI();
  loadNativePolicy();
  await rendered("#native-policy-list .native-rule", 3);
  const chips = q("#agent-dirs-chips");
  for (const label of ["/data", "/srv"]) {
    chips.appendChild(buildChip({ label, code: true, onRemove: () => undefined }));
  }
}

/** Two file rows, the first with a clickable git letter, in the shape `files.ts` renders. */
function plantFileRows(): void {
  const host = q("#fb-list");
  for (const [name, letter] of [
    ["app.ts", "M"],
    ["README.md", ""],
  ] as const) {
    const icon = make("span", "fb-icon");
    icon.appendChild(glyph());
    const row = make("div", "fb-row", { role: "listitem" });
    row.append(
      make("input", "fb-check", { type: "checkbox", "aria-label": `Select ${name}` }),
      icon,
      text("span", "fb-name fb-name-link", name),
    );
    if (letter !== "") {
      const badge = text("span", "fb-git-letter git-st-m fb-git-clickable", letter);
      badge.setAttribute("role", "button");
      badge.setAttribute("aria-label", "Git status: modified");
      row.appendChild(badge);
    }
    const meta = [formatSize(1234), formatDate(Date.UTC(2026, 7, 14, 9, 30)), "-rw-r--r--"];
    row.appendChild(text("span", "fb-meta", meta.join("   ·   ")));
    host.appendChild(row);
  }
}

function plantRunBar(names: readonly string[] = ["deadset-ci", "land", "app-review"]): void {
  const fold = q("#run-bar-fold");
  fold.hidden = false;
  const toggle = roleButton("tracker-toggle", `${String(names.length)} workflows running`);
  toggle.setAttribute("aria-expanded", "true");
  fold.append(toggle, iconButton("icon-btn run-bar-all", "Pause all"));
  plantRunRows(names);
}

/** One run: no header, the batch button at the end of the run's own row. */
function plantSoloRun(): void {
  plantRunRows(["deadset-ci"]);
  q("#run-bar > li").appendChild(iconButton("icon-btn run-bar-all", "Pause"));
}

function plantRunRows(names: readonly string[]): void {
  const bar = q("#run-bar");
  bar.classList.remove("hidden");
  for (const name of names) {
    const open = roleButton("run-bar-open", name);
    open.prepend(make("span", "run-bar-glyph", { "aria-hidden": "true" }));
    const row = make("li", "run-bar-row");
    row.appendChild(open);
    bar.appendChild(row);
  }
}

function plantFindBar(): void {
  const nav = (label: string, icon: string): HTMLButtonElement =>
    searchIconButton("chat-find-btn", label, label, icon, () => undefined);
  const shell = createSearchShell<null>({
    id: "chat-find",
    regionClass: "chat-find search-pop uip-popup is-open",
    inputClass: "chat-find-input",
    buttonClass: "chat-find-btn",
    caseClass: "chat-find-case",
    label: "Find in conversation",
    placeholder: "Find in chat…",
    matchCase: true,
    closeButton: true,
    compose: ({ input, caseButton, closeButton }) => [
      box("chat-find-row", [
        input,
        box("chat-find-nav", [
          caseButton,
          nav("Previous match", ICON_CHEVRON_UP),
          nav("Next match", ICON_CHEVRON_DOWN),
          closeButton,
        ]),
      ]),
    ],
    query: () => null,
    render: () => undefined,
  });
  q("#messages-wrap-outer").appendChild(shell.region);
}

/** A page's find popup over the git page, through the production opener. */
function plantPageFind(): void {
  const find = createSearchPopup<null>({
    id: "tt-filter",
    kind: "filter",
    label: "Filter pull requests",
    placeholder: "Filter pull requests…",
    note: true,
    host: () => document.getElementById("git-view"),
    query: () => null,
    render: () => undefined,
  });
  expect(find.open()).toBe(true);
  teardown.push(() => {
    find.reset();
  });
}

/** The run tab's page with its dock open on its segments, a paused step that can be ended, a
 *  waiting question long enough to clamp, and the verbs `run-dock-layout.test.ts` seats. */
function plantRunPage(): void {
  runPage(["ok", "input"]);
  q(".ev-box-toggle").click();
}

/** The run tab's dock closed over three steps in flight, so it lists them as rows. */
function plantRunDock(): void {
  runPage(["running", "running", "input"]);
}

function runPage(states: readonly ("ok" | "running" | "input")[]): void {
  show("run-view");
  const verbs = box("run-controls", [
    iconButton("icon-btn run-icon-btn", "End this loop"),
    iconButton("icon-btn run-icon-btn is-danger", "Cancel"),
  ]);
  const ends = buildStepEndView("wf");
  const view = buildExecPage({
    emptyNote: () => "",
    headAction: (node) => ends.current(node),
    dock: q("#run-exec-dock"),
    controls: () => verbs,
    follow: "newest",
  });
  teardown.push(() => {
    view.dispose();
    ends.dispose();
  });
  q("#run-body").append(view.root);
  const steps = states.map((state, i) => ({
    path: `root/step-${String(i)}`,
    label: `step-${String(i)}`,
    kind: "step" as const,
    state,
    children: [],
    start: new Date(Date.parse("2026-10-07T10:00:00Z") + i * 60_000).toISOString(),
    startGen: i + 1,
    ...(state === "input" ? { endable: true as const } : {}),
  }));
  view.render({
    label: "nightly",
    state: "input",
    nodes: steps,
    questions: [
      {
        text: LONG_QUESTION,
        path: `root/step-${String(states.length - 1)}`,
        step: `step-${String(states.length - 1)}`,
      },
    ],
    live: false,
  });
}

/** The composer's steer dock with one unread steer, through the production stack. */
function plantSteers(): void {
  mountSteerStack(q<HTMLUListElement>("#steer-stack"), {
    read: () => ({
      id: "c",
      steers: [{ id: "steer-1", text: "also run the linter", origin: "user" }],
    }),
    sendNow: () => Promise.resolve(),
    edit: () => Promise.resolve(),
    editByClear: () => Promise.resolve(),
    remove: () => Promise.resolve(),
    discard: () => Promise.resolve(),
    waitingFor: "the agent",
  });
}

function plantModelList(fallback: boolean): void {
  openCard("#switch-model-btn", "#model-switch-list", (list) => {
    fillModelList(list, fallback);
  });
}

const MODELS = [
  { model_id: "claude-opus-5.5", model_name: "claude opus 5.5", rate_multiplier: 2.2 },
  { model_id: "claude-opus-4.8", model_name: "claude opus 4.8", rate_multiplier: 2.2 },
  { model_id: "auto", model_name: "Auto", rate_multiplier: 1 },
];

/** The model card as `model-switcher.ts` lays it out: the first panel holds the model rows (in its
 *  `buildModelOption` shape), the effort section and, with model fallback on, the Fallback row
 *  `FallbackSlot` builds. */
function fillModelList(list: HTMLElement, fallback: boolean): void {
  const main = make("div", "model-panel model-panel-main");
  const scroll = make("div", "pill-model-scroll", { role: "listbox", "aria-label": "Models" });
  for (const [i, m] of MODELS.entries()) {
    const opt = make("div", i === 0 ? "pill-model-item active" : "pill-model-item", {
      role: "option",
      "aria-selected": i === 0 ? "true" : "false",
    });
    const icon = make("span", "pill-model-item-icon", { "aria-hidden": "true" });
    icon.appendChild(iconEl(modelIcon(m.model_id, "ui")));
    opt.append(icon, text("span", "pill-model-name", m.model_name));
    if (m.model_id !== "auto") {
      opt.appendChild(text("span", "pill-model-meta", `${String(m.rate_multiplier)}x`));
    }
    scroll.appendChild(opt);
  }
  const slider = buildEffortSlider({ onPick: () => undefined });
  main.append(scroll, slider.el);
  list.appendChild(main);
  slider.setLevels([
    { id: "low", name: "Low" },
    { id: "high", name: "High" },
  ]);
  slider.setActive("high");
  new FallbackSlot(() => undefined).sync(
    { card: list, main },
    {
      model: "claude-opus-5.5",
      settings: settingsPayload({
        model_fallback_enabled: fallback,
        model_fallbacks: [
          {
            model: "claude-opus-5.5",
            configured: "",
            vendor_default: "claude-opus-4.8",
            read_only_reason: "",
          },
        ],
      }),
      catalog: MODELS,
      identity: {
        state: "signed_in",
        email: "someone@example.invalid",
        method: "",
        startUrl: "",
        region: "",
        apiKey: false,
      },
    },
  );
}

function plantRoleList(): void {
  openCard("#role-pill", "#role-list", fillRoleList);
}

/** The role card's rows from `role-picker.ts`: a bundled mode, and custom agents with their scope,
 *  one shadowing a global agent. */
function fillRoleList(list: HTMLElement): void {
  list.append(
    modeOption({ id: "kiro_default", name: "Default" }, "kiro_default"),
    modeOption({ id: "reviewer", name: "Reviewer", source: "workspace" }, "kiro_default"),
    modeOption(
      { id: "planner", name: "Planner", source: "workspace", shadows: "global" },
      "kiro_default",
    ),
  );
}

/** The chat options card's strip and action rows, as `chat-options.ts` builds them. */
function plantChatOptions(): void {
  openCard("#chat-options-btn", "#chat-options-card", fillChatOptions);
}

function fillChatOptions(card: HTMLElement): void {
  const strip = make("div", "chat-opt-strip");
  for (const cap of ["Link", "Markdown", "JSON", "Session"]) {
    const b = make("button", "chat-opt-strip-btn", { type: "button", "aria-label": cap });
    b.append(box("chat-opt-strip-icon", [glyph()]), text("span", "chat-opt-strip-caption", cap));
    strip.appendChild(b);
  }
  card.appendChild(strip);
  for (const name of ["Attach files", "Set a goal", "Compact"]) {
    const b = make("button", "chat-opt-btn", { type: "button" });
    b.append(
      box("chat-opt-icon", [glyph()]),
      box("chat-opt-text", [
        text("span", "chat-opt-name", name),
        text("span", "chat-opt-hint", "What it does, in one line."),
      ]),
    );
    card.appendChild(box("chat-opt-entry", [b]));
  }
  for (const [id, name] of [
    ["tt-opt-queue", "Queue while running"],
    ["tt-opt-supervised", "Supervised"],
  ] as const) {
    const row = make("label", "chat-opt-row", { for: id });
    row.append(
      make("input", "", { type: "checkbox", id }),
      box("chat-opt-text", [
        text("span", "chat-opt-name", name),
        text("span", "chat-opt-hint", "What it does, in one line."),
      ]),
    );
    card.appendChild(row);
  }
}

/** A turn's file decisions: three kept files, each a row with its Keep box and its diff. */
function plantFileDecisions(): void {
  dock(
    buildPermissionCard(
      "c",
      {
        request_id: 1,
        title: "Review this turn's changes",
        kind: "edit",
        options: [
          { option_id: "a", name: "Apply", kind: "allow_once" },
          { option_id: "r", name: "Reject", kind: "reject_once" },
        ],
        files: ["static-src/app.ts", "README.md", "go.mod"].map((path, i) => ({
          path,
          action_id: `act-${String(i)}`,
        })),
      } as Parameters<typeof buildPermissionCard>[1],
      () => undefined,
    ),
  );
}

/** A workflow's schedule form on its weekly frequency, in the body `recipes.ts` opens. */
function plantSchedule(): void {
  show("docs-view");
  showPanel("data-docs-panel", "workflows");
  const body = make("div", "sched-popup");
  body.appendChild(
    buildSchedulePicker({
      spec: { ...defaultSpec(), freq: "weekly", weekdays: [1, 3] },
      enabled: true,
      exists: true,
      autoApprove: false,
      picks: buildRunDefaultsPicker({ models: [], initial: {} }),
      onOpenPermissions: () => undefined,
      onSave: () => undefined,
      onRemove: () => undefined,
      onClose: () => undefined,
    }),
  );
  q('[data-docs-panel="workflows"]').appendChild(body);
}

/** A workflow's Run when… panel: its rule list, then the event form, in the body `recipes.ts`
 *  anchors. */
function plantEventRule(): void {
  show("docs-view");
  showPanel("data-docs-panel", "workflows");
  const body = make("div", "sched-popup");
  body.appendChild(
    buildRuleList({
      rules: [
        {
          id: "e1",
          source: "bundled://goal",
          enabled: true,
          when: {
            event: {
              connection: "github:github.com",
              repo: "cplieger/marotte",
              kind: "issue_labeled",
              filter: { label: "agent" },
            },
          },
          status: {},
          cursor: { baselined: true },
          held: 0,
        },
      ],
      autoApprove: false,
      declared: undefined,
      onToggle: () => undefined,
      onEdit: () => undefined,
      onDelete: () => undefined,
      onDismissGap: () => Promise.resolve(null),
      onAddClock: () => undefined,
      onAddEvent: () => undefined,
    }),
  );
  body.appendChild(
    buildEventForm({
      forges: [
        {
          id: "github:github.com",
          kind: "github",
          host: "github.com",
          username: "cplieger",
          account: "a1",
          status: "connected",
        },
      ],
      declared: { tag: "The release tag" },
      autoApprove: false,
      picks: buildRunDefaultsPicker({ models: [], initial: {} }),
      onSave: () => Promise.resolve(null),
      onClose: () => undefined,
      onOpenPermissions: () => undefined,
      onOpenSources: () => undefined,
    }),
  );
  q('[data-docs-panel="workflows"]').appendChild(body);
}

/** Settings' MCP server list with two configured servers, through `mountRow`. */
function plantMcpRows(): void {
  show("settings-view");
  const panel = q("#mcp-section").closest<HTMLElement>("[data-settings-panel]");
  showPanel("data-settings-panel", panel?.dataset["settingsPanel"] ?? "");
  const list = make("div", "mcp-server-list");
  for (const name of ["github", "linear"]) {
    list.appendChild(
      mountRow(
        {
          id: `id-${name}`,
          name,
          transport: "stdio",
          command: "run-it",
          enabled: true,
          created_at: 0,
          updated_at: 0,
        } as Parameters<typeof mountRow>[0],
        `id-${name}`,
      ),
    );
  }
  q("#mcp-section").appendChild(list);
}

/** A diagram's full-screen view, drawn by a pele stand-in that reports a finished drawing. */
function plantDiagram(): void {
  const pele = {
    mount: () => ({
      result: { svg: "<svg></svg>" },
      source: "graph TD",
      error: undefined,
      zoom: undefined,
      update: () => undefined,
      refresh: () => undefined,
      destroy: () => undefined,
    }),
  } as unknown as Pele;
  const back = button("btn-small", "Open diagram");
  document.body.appendChild(back);
  openMermaidOverlay(pele, "graph TD", { title: "Diagram", returnFocus: back });
}

/** The Memories tab's rows in `memories.ts`'s shape: a select box, Edit and Delete per row. */
function plantMemories(): void {
  show("docs-view");
  showPanel("data-docs-panel", "memories");
  const list = entryList();
  for (const key of ["a", "b", "c"]) {
    const select = make("input", "memories-select", {
      type: "checkbox",
      "aria-label": `Select memory ${key}`,
    });
    list.appendChild(
      entryRow({
        key,
        title: `Memory ${key}`,
        lead: box("memories-lead", [select]),
        sub: { kind: "clamp", text: "A remembered fact." },
        actions: [
          iconButton("icon-btn list-row-btn", `Edit memory ${key}`),
          iconButton("icon-btn list-row-btn entry-delete", `Delete memory ${key}`),
        ],
        open: { name: `Memory ${key}`, onOpen: () => undefined },
      }),
    );
  }
  q('[data-docs-panel="memories"]').appendChild(list);
}

/** The Hooks tab's rows in `docs.ts`'s shape: an enable switch and Delete per row. */
function plantHooks(): void {
  show("docs-view");
  showPanel("data-docs-panel", "hooks");
  const list = entryList();
  for (const key of ["a", "b"]) {
    const toggle = make("label", "toggle toggle-inline");
    toggle.append(
      make("input", "hook-toggle", { type: "checkbox", "aria-label": `Disable hook ${key}` }),
      make("span", "toggle-slider"),
    );
    list.appendChild(
      entryRow({
        key,
        title: `hook-${key}`,
        sub: { kind: "line", text: "Manual" },
        actions: [toggle, iconButton("icon-btn entry-delete", `Delete hook ${key}`)],
        open: { name: `hook-${key}`, onOpen: () => undefined },
      }),
    );
  }
  q('[data-docs-panel="hooks"]').appendChild(list);
}

/** The merge dialog with three methods, in `merge-dialog.ts`'s `methodRow` shape. */
function plantMergeDialog(): void {
  const methods = q("#pr-merge-methods");
  for (const [i, name] of [
    "Create a merge commit",
    "Squash and merge",
    "Rebase and merge",
  ].entries()) {
    const radio = make("input", "", { type: "radio", name: "pr-merge-method", value: name });
    const words = box("pr-merge-method-text", [
      text("span", "pr-merge-method-name", name),
      text("span", "pr-merge-method-desc", "What it does to the branch."),
    ]);
    const row = make("label", "pr-merge-method");
    row.append(radio, words);
    (radio as HTMLInputElement).checked = i === 0;
    methods.appendChild(row);
  }
  q<HTMLDialogElement>("#pr-merge-dialog").showModal();
}

function plantPRDialog(): void {
  q<HTMLDialogElement>("#pr-create-dialog").showModal();
}

/** A diff with its Ignore whitespace toggle, in the editor's diff view. */
function plantDiffPane(): void {
  show("editor-view");
  const lines = [
    { kind: "ctx", oldNo: 1, newNo: 1, text: "one" },
    { kind: "del", oldNo: 2, newNo: 0, text: "two" },
    { kind: "add", oldNo: 0, newNo: 2, text: "TWO" },
  ] as Parameters<typeof renderDiffPane>[0];
  q(".editor-page").appendChild(
    renderDiffPane(lines, { source: { oldText: "one\ntwo\n", newText: "one\nTWO\n" } }),
  );
}

/** The git page's add-a-server form with its connection options open. */
function plantForgeConnection(): void {
  show("git-view");
  showPanel("data-git-panel", "sources");
  const target = renderConnectionTarget(null);
  q("#git-sources-mount").appendChild(target.el);
  for (const d of target.el.querySelectorAll("details")) {
    d.open = true;
  }
}

export interface Surface {
  readonly name: string;
  readonly sizes: readonly Size[];
  readonly setup: (size: Size) => void | Promise<void>;
  /** Controls inside a layout: painted at the mouse's size, the 44px target invisible around them.
   *  Each selector must match at least one rendered element, and every match is checked. */
  readonly controls: readonly string[];
  /** Elements that are their own target: rows and fields, whose box grows to the floor. */
  readonly rows?: readonly string[];
  /** Controls with no target of their own (a slider's thumb rides its track): painted size only. */
  readonly painted?: readonly string[];
  /** The mouse tier draws a different control here, so no fine height is there to match. */
  readonly ownFineForm?: true;
  /** Listed controls that stand in a column of rows: each paints at its mouse height plus what
   *  `row` grows on coarse, so the column's rows change alike. */
  readonly rowStep?: { readonly row: string; readonly controls: readonly string[] };
}

const HEADER_ICONS = [
  "#find-btn",
  "#files-btn",
  "#git-btn",
  "#shell-btn",
  "#docs-btn",
  "#settings-btn",
];

export const SURFACES: readonly Surface[] = [
  {
    name: "the sidebar",
    sizes: ALL,
    setup: (size) => {
      plantTabs();
      plantHistorySection();
      plantRunsSection();
      q("#pointer-mode-btn").classList.remove("hidden");
      if (size === 390) {
        q("#sidebar").classList.add("open");
      }
    },
    controls: [
      "#new-chat",
      "#history-list .history-row",
      "#history-list .history-row-delete",
      "#history-more",
      "#runs-section .side-section-toggle",
      "#runs-list .runs-row",
      "#theme-btn",
      "#pointer-mode-btn",
      "#account-btn",
      "#logout-btn",
      "#tab-list .tab-close",
    ],
    rows: ["#tab-list .tab"],
    rowStep: {
      row: "#tab-list .tab",
      controls: [
        "#new-chat",
        "#history-list .history-row",
        "#history-more",
        "#runs-list .runs-row",
      ],
    },
  },
  {
    name: "the phone drawer's close",
    sizes: [390],
    setup: () => {
      q("#sidebar").classList.add("open");
    },
    controls: ["#sidebar-close"],
  },
  {
    name: "the chat header",
    sizes: ALL,
    setup: () => undefined,
    controls: HEADER_ICONS,
  },
  {
    name: "the phone header's menu",
    sizes: [390],
    setup: () => undefined,
    controls: ["#menu-toggle"],
  },
  {
    name: "the find bar",
    sizes: TWO,
    setup: plantFindBar,
    controls: ["#chat-find .chat-find-btn"],
    rows: ["#chat-find-input"],
  },
  {
    name: "a notice band",
    sizes: TWO,
    setup: plantBanner,
    controls: [".banner-link", ".banner-dismiss"],
  },
  {
    name: "the composer",
    sizes: ALL,
    setup: () => {
      q("#task-list-pill").classList.remove("hidden");
      q("#send-btn").replaceChildren(iconEl(ICON_SEND));
      const row = q("#attachment-row");
      row.classList.remove("hidden");
      row.appendChild(
        buildAttachmentPill(
          { path: "/workspace/app.ts", name: "app.ts" },
          { onRemove: () => undefined },
        ),
      );
    },
    controls: [
      "#context-indicator",
      "#switch-model-btn",
      "#role-pill",
      "#chat-options-btn",
      "#task-list-pill",
      "#send-btn",
      ".attachment-open",
      ".attachment-close",
    ],
  },
  {
    name: "the model popup",
    sizes: TWO,
    setup: () => {
      plantModelList(true);
    },
    controls: [],
    painted: [".effort-knob"],
    rows: [".pill-model-item", ".effort-track"],
  },
  {
    name: "the model popup without a fallback",
    sizes: TWO,
    setup: () => {
      plantModelList(false);
    },
    controls: [],
    painted: [".effort-knob"],
    rows: [".pill-model-item", ".effort-track"],
  },
  {
    name: "the role popup",
    sizes: TWO,
    setup: plantRoleList,
    controls: [],
    rows: [".pill-role-item"],
  },
  {
    name: "the chat options popup",
    sizes: TWO,
    setup: plantChatOptions,
    controls: [".chat-opt-strip-btn"],
    rows: [".chat-opt-btn", ".chat-opt-row"],
  },
  {
    name: "the steer dock",
    sizes: TWO,
    setup: plantSteers,
    controls: ["#steer-stack .steer-act"],
  },
  {
    name: "the run bar",
    sizes: TWO,
    setup: () => {
      plantRunBar();
    },
    controls: ["#run-bar-fold .run-bar-all"],
    rows: ["#run-bar-fold .tracker-toggle", "#run-bar .run-bar-open"],
  },
  {
    name: "the run bar at one run",
    sizes: TWO,
    setup: plantSoloRun,
    controls: ["#run-bar > li > .run-bar-all"],
    rows: ["#run-bar .run-bar-open"],
  },
  {
    name: "the run bar at two runs",
    sizes: TWO,
    setup: () => {
      plantRunBar(["deadset-ci", "land"]);
    },
    controls: ["#run-bar-fold .run-bar-all"],
    rows: ["#run-bar-fold .tracker-toggle", "#run-bar .run-bar-open"],
  },
  {
    name: "a turn",
    sizes: TWO,
    setup: plantTurn,
    controls: [
      ".turn-fold-toggle",
      ".tool-file-link",
      ".tool-disclosure",
      ".turn-ledger-summary",
      ".turn-footer .turn-action-btn",
      ".turn-rewind",
    ],
    rows: [
      ".tool-group-header",
      ".tool-header",
      "summary.reasoning-summary",
      ".tool-diff-stats",
      ".tool-group-hooks",
    ],
  },
  {
    name: "a lone edit with its hooks",
    sizes: TWO,
    setup: plantLoneEdit,
    controls: [".tool-file-link", ".tool-disclosure"],
    rows: [".tool-header", ".tool-group-hooks"],
  },
  {
    name: "a long edit's diff",
    sizes: TWO,
    setup: plantLongEdit,
    controls: [".tool-diff-more"],
    rows: [".tool-diff-stats"],
  },
  {
    name: "a turn's More menu",
    sizes: [390],
    setup: plantTurnMore,
    controls: [],
    rows: [".turn-actions-group > .turn-action-btn"],
  },
  {
    name: "a turn's details",
    sizes: TWO,
    setup: plantTurnDetails,
    controls: [".turn-ledger-summary"],
    rows: [".turn-info-dbtn", ".turn-file-row"],
  },
  {
    name: "a turn's code references",
    sizes: TWO,
    setup: plantCodeRefs,
    controls: [".code-refs-summary"],
    rows: [".code-refs-link"],
  },
  {
    name: "a run card",
    sizes: TWO,
    setup: plantRun,
    controls: ["a.run-open", ".steer-note-run[role=button]"],
    rows: [".run-head", ".run-step-head"],
  },
  {
    name: "a run card's loop",
    sizes: TWO,
    setup: () => {
      plantLoop();
    },
    controls: [],
    rows: [".run-head", ".run-step-head"],
  },
  {
    name: "a run card's nested loop",
    sizes: TWO,
    setup: () => {
      plantLoop(true);
    },
    controls: [],
    rows: [".run-head", ".run-step-head"],
  },
  {
    name: "a reply",
    sizes: TWO,
    setup: plantReply,
    controls: ['.cp-code [data-part="actions"] > .cp-button'],
    rows: [".subagent-header"],
  },
  {
    name: "a permission ask",
    sizes: TWO,
    setup: () => {
      dock(
        buildPermissionCard(
          "c",
          {
            request_id: 1,
            title: "grep -rn foo",
            kind: "execute",
            options: [
              { option_id: "a", name: "Allow", kind: "allow_once" },
              { option_id: "r", name: "Reject", kind: "reject_once" },
            ],
          } as Parameters<typeof buildPermissionCard>[1],
          () => undefined,
        ),
      );
    },
    controls: ["#decision-dock .approval-actions button", "#decision-dock .approval-policy-link"],
  },
  {
    name: "a question",
    sizes: TWO,
    setup: () => {
      dock(
        buildUserInputCard(
          {
            request_id: 1,
            question: LONG_QUESTION,
            options: [{ title: "Cancel" }, { title: "Keep Iterating" }],
          },
          () => undefined,
        ),
      );
    },
    controls: ["#decision-dock .dock-card .btn-small"],
    rows: ["#decision-dock .user-input-option", "#decision-dock .dock-ask-more"],
  },
  {
    name: "a question with a recommended option",
    sizes: TWO,
    setup: () => {
      dock(
        buildUserInputCard(
          {
            request_id: 1,
            question: "Ship it?",
            options: [{ title: "Ship", recommended: true }, { title: "Wait" }],
          },
          () => undefined,
        ),
      );
    },
    controls: ["#decision-dock .dock-card .btn-small"],
    rows: ["#decision-dock .user-input-option"],
  },
  {
    name: "a question's sub-options",
    sizes: TWO,
    setup: () => {
      dock(
        buildUserInputCard(
          {
            request_id: 1,
            question: "Which checks?",
            options: [{ title: "Run", sub_options: [{ title: "Lint" }, { title: "Test" }] }],
          } as Parameters<typeof buildUserInputCard>[0],
          () => undefined,
        ),
      );
      q<HTMLButtonElement>("#decision-dock .user-input-option").click();
    },
    controls: ["#decision-dock .dock-card .btn-small"],
    rows: ["#decision-dock .user-input-sub-option"],
  },
  {
    name: "a form request",
    sizes: TWO,
    setup: () => {
      dock(
        buildElicitationCard(
          {
            request_id: 1,
            mode: "form",
            message: "Fill this in",
            requested_schema: { properties: { name: { type: "string" } }, required: [] },
          } as Parameters<typeof buildElicitationCard>[0],
          () => undefined,
        ),
      );
    },
    controls: ["#decision-dock .elicitation-actions .btn-small"],
    rows: ["#decision-dock .elicitation-input"],
  },
  {
    name: "the run tab",
    sizes: TWO,
    setup: plantRunPage,
    controls: [".ev-dock-head .run-icon-btn", ".ev-dock-seg .seg", ".run-step-end .run-icon-btn"],
    rows: [".ev-dock-ask-more", ".ev-row-main", ".ev-box-toggle"],
  },
  {
    name: "the run tab's dock rows",
    sizes: TWO,
    setup: plantRunDock,
    controls: [],
    rows: [".ev-dock-row"],
  },
  {
    name: "the run tab's one-row dock",
    sizes: [390],
    setup: plantRunDock,
    controls: [],
    rows: [".ev-dock-row", ".ev-dock-more"],
  },
  {
    name: "the run tab's composer",
    sizes: TWO,
    setup: () => {
      show("run-view");
      q("#run-send-btn").replaceChildren(iconEl(ICON_SEND));
    },
    controls: ["#run-send-btn"],
    rows: ["#run-composer-input"],
  },
  {
    name: "the file browser's toolbar",
    sizes: [1180, 820],
    setup: () => {
      show("files-view");
    },
    controls: [
      "#fb-back",
      "#fb-forward",
      "#fb-add-to-chat",
      "#fb-upload",
      "#fb-download",
      "#fb-new-file",
      "#fb-new-folder",
      "#fb-rename",
      "#fb-delete",
    ],
    rows: ["#fb-path"],
  },
  {
    name: "the file browser's New menu",
    sizes: [390],
    setup: () => {
      show("files-view");
      q(".fb-new-menu").setAttribute("open", "");
      teardown.push(() => q(".fb-new-menu").removeAttribute("open"));
    },
    controls: [],
    rows: ["#fb-new-file", "#fb-new-folder"],
  },
  {
    name: "the file browser's rows",
    sizes: ALL,
    setup: () => {
      show("files-view");
      plantFileRows();
    },
    controls: ["#fb-list .fb-check", "#fb-list .fb-git-clickable"],
    rows: ["#fb-list .fb-row"],
  },
  {
    name: "the editor",
    sizes: TWO,
    setup: () => {
      show("editor-view");
      const overlay = q("#editor-conflict-overlay");
      overlay.classList.remove("hidden");
      const row = make("div", "conflict-hunk-row", { role: "group", "aria-label": "Conflict" });
      row.append(
        text("span", "conflict-hunk-title", "Line 4: ours vs theirs"),
        button("btn-small conflict-btn", "Keep ours"),
        button("btn-small conflict-btn", "Keep theirs"),
        button("btn-small conflict-btn", "Keep both"),
      );
      overlay.appendChild(row);
    },
    controls: ["#editor-diff-btn", "#editor-edit-btn", ".conflict-hunk-row .btn-small"],
  },
  {
    name: "the editor's go-to bar",
    sizes: TWO,
    setup: () => {
      show("editor-view");
      q("#editor-goto").classList.remove("hidden");
    },
    controls: [".editor-goto-submit", "#editor-goto-close"],
    rows: ["#editor-goto-input"],
  },
  {
    name: "the web preview's bar",
    sizes: TWO,
    setup: () => {
      show("web-view");
      q("#web-path").textContent = "/preview/index.html";
      q("#web-open-btn").setAttribute("href", "/preview/tab/tok/index.html");
    },
    controls: ["#web-reload-btn", "#web-open-btn", "#web-edit-btn"],
  },
  {
    name: "the web preview's scale",
    sizes: [1180],
    setup: () => {
      show("web-view");
      q("#web-viewport").append(
        ...["Fill", "Phone", "Tablet", "Desktop"].map((label) => {
          const seg = iconButton("seg", label);
          seg.setAttribute("role", "radio");
          return seg;
        }),
      );
    },
    controls: ["#web-scale-btn", "#web-reload-btn", "#web-viewport .seg"],
  },
  {
    name: "the git page",
    sizes: TWO,
    setup: () => {
      show("git-view");
      plantGitRepo();
    },
    controls: [
      "#git-tab-bar .seg",
      "#git-pull-all-btn",
      "#git-refresh-all-btn",
      "#git-changes-mount .btn-small",
    ],
    rows: ["#git-changes-mount .git-repo-section-header", "#git-changes-mount .git-file-path"],
  },
  {
    name: "the branch switcher",
    sizes: TWO,
    setup: plantBranchSwitcher,
    controls: [".git-branch-popover-go"],
    rows: [
      ".git-branch-popover-row",
      ".git-branch-popover-filter",
      ".git-branch-popover-create-input",
    ],
  },
  {
    name: "the git page's pull requests",
    sizes: TWO,
    setup: () => {
      show("git-view");
      showPanel("data-git-panel", "prs");
      plantPRs();
    },
    controls: ["#git-refresh-prs-btn", "a.git-pr-row-title", ".git-pr-row-actions .btn-small"],
  },
  {
    name: "a page's find popup",
    sizes: TWO,
    setup: () => {
      show("git-view");
      plantPageFind();
    },
    controls: ["#tt-filter .page-find-btn, #tt-filter button"],
    rows: ["#tt-filter-input"],
  },
  {
    name: "the git page's sources",
    sizes: TWO,
    setup: plantRepoRows,
    controls: [".forge-account-repo-row .btn-small"],
    rows: [".forge-account-repo-row"],
  },
  {
    name: "the workflow list",
    sizes: ALL,
    setup: plantRecipeRows,
    controls: [".recipe-sched-btn", ".recipe-run-btn"],
    rows: [],
  },
  {
    name: "a workflow's run form",
    sizes: TWO,
    setup: plantRecipeForm,
    controls: [".recipe-launch-btn"],
    rows: [".recipe-input", ".run-pick-select"],
  },
  {
    name: "the Spec tab",
    sizes: TWO,
    setup: plantSpec,
    controls: [".spec-toggle", ".spec-run", ".spec-run-in"],
    rows: [".spec-run-in-select"],
  },
  {
    name: "the tab menu",
    sizes: TWO,
    setup: plantContextMenu,
    controls: [],
    rows: [".tab-context-item"],
  },
  {
    name: "the account card",
    sizes: TWO,
    setup: (size) => {
      if (size === 390) {
        q("#sidebar").classList.add("open");
      }
      openCard("#account-btn", "#status-card", () => {
        q("#st-account").hidden = false;
      });
    },
    controls: [],
    rows: ["#st-account"],
  },
  {
    name: "the sign-in dialog",
    sizes: TWO,
    setup: () => {
      reveal("#modal-sso-form", "#login-modal");
      openModal(q<HTMLDivElement>("#login-modal"));
    },
    controls: [],
    rows: [".modal-input"],
  },
  {
    name: "Settings, General",
    sizes: TWO,
    setup: () => {
      show("settings-view");
    },
    controls: [
      "#settings-tab-bar .seg",
      '[data-settings-panel="general"] label.toggle',
      "#diagnostics-run",
      "#engine-restart",
      "#settings-open-config",
    ],
    rows: [
      '[data-settings-panel="general"] .rf-select',
      "#chat-retention-days",
      "#auto-compact-pct",
    ],
  },
  {
    name: "Settings, General, a report ready",
    sizes: TWO,
    setup: () => {
      show("settings-view");
      initDiagnostics();
      // The slot after a report, as `showCopy(true)` (settings.ts) leaves it.
      q("#diagnostics-run").classList.add("hidden");
      q(".diagnostics-copy").classList.remove("hidden");
    },
    controls: [".diagnostics-copy"],
    rows: [],
  },
  {
    name: "Settings, Tools",
    sizes: TWO,
    setup: () => {
      show("settings-view");
      showPanel("data-settings-panel", "tools");
      return plantToolRows();
    },
    controls: [
      "#tool-update-btn",
      "#tool-add-btn",
      "#tool-catalog-refresh-btn",
      "#tools-list label.toggle",
      "#tools-list .list-row-btn",
      "#tool-open-manifest",
    ],
    rows: ["#tools-list .list-row:not(.list-row-system)"],
  },
  {
    name: "Settings, Knowledge",
    sizes: TWO,
    setup: () => {
      show("settings-view");
      showPanel("data-settings-panel", "instructions");
      return plantKnowledgeRows();
    },
    controls: ["#knowledge-add-btn", "#knowledge-clear-btn", "#knowledge-list .list-row-btn"],
    rows: ["#knowledge-list .list-row"],
  },
  {
    name: "Settings, Permissions",
    sizes: TWO,
    setup: () => {
      show("settings-view");
      showPanel("data-settings-panel", "permissions");
      q("#native-policy-explain-box").setAttribute("open", "");
      return plantPermissions();
    },
    controls: [
      "#native-rule-add",
      "#native-explain-run",
      ".native-rule-remove",
      "#agent-dirs-add",
      "#agent-dirs-chips .chip-remove",
    ],
    rows: [".add-form .rf-select", "#native-rule-match", ".native-rule"],
  },
  {
    name: "the Docs page",
    sizes: TWO,
    setup: () => {
      show("docs-view");
      plantDocs();
    },
    controls: [
      "#docs-tab-bar .seg",
      "#docs-view .docs-edit",
      "#docs-view .entry-delete",
      ".docs-new-btn",
    ],
    rows: ["#docs-view .entry-open"],
  },
  {
    name: "the History page",
    sizes: TWO,
    setup: () => {
      show("history-view");
      plantHistory();
    },
    controls: ["#history-tab-bar .seg", "#history-view .entry-delete"],
    rows: ["#history-view .entry-open"],
  },
  {
    name: "the shell panel's header",
    sizes: TWO,
    setup: () => {
      q("#shell-panel").classList.remove("shell-closed");
      q("#shell-keys-btn").classList.remove("hidden");
    },
    controls: [
      "#shell-keys-btn",
      "#shell-restart-btn",
      "#shell-fullscreen-btn",
      "#shell-toggle-btn",
    ],
  },
  {
    name: "the shortcuts dialog",
    sizes: TWO,
    setup: () => {
      openModal(q<HTMLDivElement>("#shortcuts-modal"));
    },
    controls: ["#shortcuts-close"],
  },
  {
    name: "the MCP dialog",
    sizes: TWO,
    setup: () => {
      const modal = q<HTMLDivElement>("#mcp-modal");
      reveal("#mcp-search-btn", "#mcp-modal");
      q("#mcp-search-results").appendChild(
        renderRegistryResult({
          name: "ex/github",
          description: "GitHub",
          remotes: [{ type: "streamable-http", url: "https://example.invalid/mcp" }],
        } as Parameters<typeof renderRegistryResult>[0]),
      );
      openModal(modal);
    },
    controls: ["#mcp-modal-close", "#mcp-modal-tabs .seg", "#mcp-search-btn", ".mcp-install-btn"],
    rows: ["#mcp-search-input"],
  },
  {
    name: "the MCP dialog's variables",
    sizes: TWO,
    setup: () => {
      const modal = q<HTMLDivElement>("#mcp-modal");
      reveal("#mcp-npm-add-env", "#mcp-modal");
      renderKeyPairList(q<HTMLDivElement>("#mcp-npm-env"), [{ name: "TOKEN", value: "" }], "env");
      openModal(modal);
    },
    controls: ["#mcp-npm-add-env", "#mcp-npm-env button", "#mcp-npm-save"],
    rows: [
      "#mcp-npm-env .mcp-pair-name",
      "#mcp-npm-env .mcp-pair-value",
      "label.toggle-inline:has(#mcp-npm-prewarm)",
    ],
  },
  {
    name: "a turn's file decisions",
    sizes: ALL,
    setup: plantFileDecisions,
    controls: ["#decision-dock .dock-file-check", "#decision-dock .dock-file-row .turn-action-btn"],
    rows: ["#decision-dock .dock-file-row"],
  },
  {
    name: "a workflow's schedule",
    sizes: ALL,
    setup: plantSchedule,
    controls: [".sched-day", ".sched-actions .btn-small"],
    rows: [".sched-enabled", ".sched-freq"],
  },
  {
    name: "a workflow's event rule",
    sizes: ALL,
    setup: plantEventRule,
    controls: [
      ".rw-controls label.toggle",
      ".rw-controls .btn-small",
      ".rw-add .btn-small",
      ".sched-actions .btn-small",
    ],
    rows: [".rw-conn", ".rw-repo", ".rw-kind", ".rw-input"],
  },
  {
    name: "Settings, MCP servers",
    sizes: ALL,
    setup: plantMcpRows,
    controls: [".mcp-row label.toggle", ".mcp-row-actions .icon-btn"],
  },
  {
    name: "a diagram's full view",
    sizes: ALL,
    setup: plantDiagram,
    controls: [".mermaid-overlay-actions .icon-btn"],
  },
  {
    name: "the Memories tab",
    sizes: TWO,
    setup: plantMemories,
    controls: [".memories-select", '[data-docs-panel="memories"] .list-row-btn'],
    rows: ['[data-docs-panel="memories"] .entry-open'],
  },
  {
    name: "the Hooks tab",
    sizes: TWO,
    setup: plantHooks,
    controls: ['[data-docs-panel="hooks"] label.toggle', '[data-docs-panel="hooks"] .entry-delete'],
    rows: ['[data-docs-panel="hooks"] .entry-open'],
  },
  {
    name: "the merge dialog",
    sizes: TWO,
    setup: plantMergeDialog,
    controls: ["#pr-merge-dialog .icon-btn", "#pr-merge-dialog .pr-dialog-footer .btn-small"],
    rows: [".pr-merge-method"],
  },
  {
    name: "the pull request form",
    sizes: TWO,
    setup: plantPRDialog,
    controls: ["#pr-create-dialog .icon-btn", "#pr-create-dialog .pr-dialog-footer .btn-small"],
    rows: [".pr-dialog-check", "#pr-title"],
  },
  {
    name: "a diff's whitespace toggle",
    sizes: TWO,
    setup: plantDiffPane,
    controls: [],
    rows: [".diff-pane-ws-toggle"],
  },
  {
    name: "a server's connection options",
    sizes: TWO,
    setup: plantForgeConnection,
    controls: [],
    rows: [".forge-options-summary", ".forge-option .toggle-inline"],
  },
  {
    name: "the confirm dialog",
    sizes: TWO,
    setup: () => {
      void confirm({ title: "Delete this chat?", confirmLabel: "Delete", variant: "destructive" });
    },
    controls: [".uip-ask-cancel", ".uip-ask-ok"],
  },
  {
    name: "a toast",
    sizes: TWO,
    setup: () => {
      info("Copied the turn as Markdown");
    },
    controls: [],
    rows: [".uip-toast"],
  },
  {
    name: "the Latest button",
    sizes: TWO,
    setup: () => {
      q("#scroll-bottom").classList.remove("hidden");
    },
    controls: ["#scroll-bottom"],
    ownFineForm: true,
  },
];

/** Longer than any entry transition and shorter than a toast's lifetime countdown. */
const ENTRY_MS = 1000;

/** A few frames, then every entry animation (a dialog's, a toast's) to its end, so a box is
 *  measured where it comes to rest. */
export async function frames(): Promise<void> {
  for (let i = 0; i < 3; i++) {
    await new Promise((r) => requestAnimationFrame(r));
  }
  const entries = document.getAnimations().filter((a) => {
    const end = a.effect?.getComputedTiming().endTime;
    return typeof end === "number" && end <= ENTRY_MS;
  });
  await Promise.all(entries.map((a) => a.finished.catch(() => undefined)));
}

export async function mountSurface(s: Surface, size: Size, tier: "fine" | "coarse"): Promise<void> {
  clearSurface();
  mountApp();
  document.documentElement.dataset["pointer"] = tier;
  await s.setup(size);
  await frames();
}
