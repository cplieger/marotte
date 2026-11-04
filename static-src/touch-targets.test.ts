// The coarse tier's touch contract: a control paints at the mouse's height and its target is the
// 44px hit floor (Apple HIG's hit region) around it, a row is its own 44px target, and no target
// covers another. Only a hit test reads the target, since boxes and declarations are identical
// whichever element wins a point. The fixture is the production `#app` markup and modals, opened by
// the production openers. What scripts add comes from its exported builder where one builds without
// app state, and is planted by hand in that module's shape where none does.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { page, server } from "vitest/browser";
import { _resetForTest as resetAsk } from "@cplieger/ui-primitives/ask";
import { _resetForTest as resetScrollLock } from "@cplieger/ui-primitives/modal";

import indexHtml from "../static/index.html?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type { Pele } from "./mermaid-svg.js";

const FLOOR = 44;
type Size = 1180 | 820 | 390;
const VIEWPORTS: Record<Size, readonly [number, number]> = {
  1180: [1180, 820],
  820: [820, 1180],
  390: [390, 844],
};
const TWO: readonly Size[] = [1180, 390];
const ALL: readonly Size[] = [1180, 820, 390];

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
const { confirm } = await import("./confirm.js");
const { entryList, entryRow } = await import("./entry-row.js");
const { buildSteerNote } = await import("./fundamentals/steer-note.js");
const { buildRunCard } = await import("./fundamentals/run-card.js");
const { buildSubagentCard } = await import("./fundamentals/subagent-block.js");
const { buildAssistantBubble } = await import("./fundamentals/text-bubble.js");
const { buildToolCard } = await import("./tool-card.js");
const { buildToolGroupShell, groupBody, refreshGroupHeader } = await import("./tool-group.js");
const { iconEl } = await import("./icon-el.js");
const { ICON_CHEVRON_DOWN, ICON_CHEVRON_UP, ICON_SEND } = await import("./icons.js");
const { openModal } = await import("./modals.js");
const { createSearchShell, searchIconButton } = await import("./search-shell.js");
const { createSearchPopup } = await import("./search-popup.js");
const { info, _resetForTest: resetToasts } = await import("./toast.js");
const { buildPermissionCard } = await import("./permission.js");
const { buildUserInputCard } = await import("./user-input.js");
const { buildElicitationCard } = await import("./elicitation.js");
const { buildEffortSlider } = await import("./effort-slider.js");
const { buildAttachmentPill } = await import("./attachment-pill.js");
const { buildChip } = await import("./chip.js");
const { mountNewDocButton } = await import("./docs-new.js");
const { mountSteerStack } = await import("./pending-steers.js");
const { makeExpandable } = await import("./pill-expand.js");
const { buildExecPage } = await import("./exec-view/page.js");
const { buildStepEndView } = await import("./run-controls-view.js");
const { renderRegistryResult } = await import("./mcp-panels-search.js");
const { renderKeyPairList } = await import("./mcp-pairs.js");
const { buildReasoning } = await import("./fundamentals/reasoning.js");
const { syncCodeReferences } = await import("./code-refs.js");
const { buildTurnFooter } = await import("./fundamentals/turn-footer.js");
const { buildTurnHeader } = await import("./fundamentals/turn-header.js");
const { wireRowToggle } = await import("./disclosure-row.js");
const { renderRecentCommits } = await import("./git-changes-commit.js");
const { renderRepoRow } = await import("./forge-auth-repos.js");
const { buildRunDefaultsPicker } = await import("./run-defaults-picker.js");
const { showContextMenu } = await import("./context-menu.js");
const { buildSchedulePicker, defaultSpec } = await import("./schedule-picker.js");
const { mountRow } = await import("./mcp-ui.js");
const { openMermaidOverlay } = await import("./mermaid-overlay.js");
const { renderDiffPane } = await import("./diff-pane.js");
const { renderConnectionTarget } = await import("./forge-auth-connection.js");
const { setWorkspaceRoot, _resetForTest: resetWorkspaceRoot } = await import("./workspace.js");
document.body.replaceChildren();

let style: HTMLStyleElement;
let entry: { readonly width: number; readonly height: number };
const teardown: (() => void)[] = [];

beforeAll(() => {
  entry = { width: window.innerWidth, height: window.innerHeight };
  style = mountAppCSS();
  document.body.style.margin = "0";
});

afterAll(async () => {
  style.remove();
  await page.viewport(entry.width, entry.height);
});

function clearSurface(): void {
  for (const t of teardown.splice(0)) {
    t();
  }
  resetAsk();
  resetToasts();
  resetScrollLock();
  document.body.replaceChildren();
}

afterEach(() => {
  clearSurface();
  delete document.documentElement.dataset["pointer"];
});

function q<T extends HTMLElement>(sel: string): T {
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
    close.textContent = "×";
    row.append(text("span", "tab-name", `Conversation ${String(i)}`), close);
    list.appendChild(row);
  }
}

function turn(...body: HTMLElement[]): HTMLElement {
  const t = make("div", "turn");
  const header = buildTurnHeader({
    n: 1,
    outcome: "completed",
    ts: 0,
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

/** A finished turn: its fold, a tool group with two file edits, its footer's actions, in the
 *  shapes `turns.ts` and `fundamentals/turn-footer.ts` build. */
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
  const t = turn(buildReasoning("Read the two files first.", false, false).root, shell);
  refreshGroupHeader(shell);

  const footer = make("div", "turn-footer");
  const actions = make("span", "turn-actions-buttons");
  const more = make("details", "turn-actions-more");
  const summary = make("summary", "turn-action-btn turn-action-more", {
    "aria-label": "More turn actions",
  });
  summary.textContent = "…";
  const actionGroup = make("span", "turn-actions-group");
  for (const label of ["Copy as text", "Copy as markdown", "Copy link to this turn"]) {
    actionGroup.appendChild(iconButton("turn-action-btn", label));
  }
  more.append(summary, actionGroup);
  actions.appendChild(more);
  const rewind = iconButton("turn-rewind", "Rewind to this point");
  rewind.appendChild(text("span", "turn-rewind-label", "Rewind"));
  footer.append(button("turn-ledger-summary", "Completed"), actions, rewind);
  t.appendChild(footer);
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

/** A run card whose loop holds three steps, beside a top-level one. */
function plantLoop(): void {
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
              children: [step("code"), step("review"), step("fix")],
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

function plantToolRows(): void {
  const host = q("#tools-list");
  host.replaceChildren();
  for (const name of ["ripgrep", "typescript-language-server"]) {
    const toggle = make("label", "toggle toggle-inline tool-toggle");
    toggle.append(
      make("input", "", { type: "checkbox", "aria-label": `Disable ${name}` }),
      make("span", "toggle-slider"),
    );
    const wrap = box("tool-name-wrap", [
      text("span", "list-row-name", name),
      box("tool-hit-chips", [text("span", "tool-source-chip", "LSP")]),
    ]);
    const row = box("list-row", [
      make("span", "tool-state-dot tool-state-ok"),
      wrap,
      text("span", "list-row-meta", "15.2.0"),
      box("list-row-actions", [
        toggle,
        iconButton("list-row-btn list-row-pin", `Pin ${name} version`),
        iconButton("list-row-btn", `Remove ${name}`),
      ]),
    ]);
    host.appendChild(row);
  }
}

/** Two writable policy rules and a chip list, as `permissions-ui.ts` and `chip.ts` render them. */
function plantPermissions(): void {
  const host = q("#native-policy-list");
  for (const cap of ["fs_write", "execute"]) {
    const effect = make("select", "native-rule-effect", { "aria-label": "Effect" });
    effect.appendChild(text("option", "", "ask"));
    host.appendChild(
      box("native-rule native-rule-ask", [
        effect,
        text("span", "native-rule-cap mono", cap),
        text("span", "native-rule-src", "workspace"),
        iconButton("icon-btn native-rule-remove", "Remove rule"),
      ]),
    );
  }
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
    row.appendChild(text("span", "fb-meta", "1.2 KB"));
    host.appendChild(row);
  }
}

function plantRunBar(): void {
  const fold = q("#run-bar-fold");
  fold.hidden = false;
  const toggle = button("run-bar-open run-bar-toggle", "3 workflows running");
  toggle.setAttribute("aria-expanded", "true");
  fold.appendChild(toggle);
  const bar = q("#run-bar");
  bar.classList.remove("hidden");
  for (const name of ["deadset-ci", "land", "app-review"]) {
    const open = button("run-bar-open", name);
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
      queued: [],
    }),
    sendNow: () => Promise.resolve(),
    edit: () => Promise.resolve(),
    editByClear: () => Promise.resolve(),
    remove: () => Promise.resolve(),
    discard: () => Promise.resolve(),
    waitingFor: "the agent",
  });
}

function plantModelList(): void {
  openCard("#switch-model-btn", "#model-switch-list", fillModelList);
}

function fillModelList(list: HTMLElement): void {
  const scroll = make("div", "pill-model-scroll", { role: "listbox", "aria-label": "Models" });
  for (const [i, name] of ["Claude Opus", "Claude Sonnet", "Auto"].entries()) {
    const opt = make("div", i === 0 ? "pill-model-item active" : "pill-model-item", {
      role: "option",
      "aria-selected": i === 0 ? "true" : "false",
    });
    opt.append(text("span", "", name), text("span", "pill-model-meta", "1x"));
    scroll.appendChild(opt);
  }
  const slider = buildEffortSlider({ onPick: () => undefined });
  list.append(scroll, slider.el);
  slider.setLevels([
    { id: "low", name: "Low" },
    { id: "high", name: "High" },
  ]);
}

function plantRoleList(): void {
  openCard("#role-pill", "#role-list", fillRoleList);
}

function fillRoleList(list: HTMLElement): void {
  for (const name of ["Default", "Spec", "Reviewer"]) {
    const opt = make("button", "pill-role-item", { type: "button", role: "option" });
    opt.append(make("span", "pill-role-item-icon"), text("span", "pill-role-name", name));
    list.appendChild(opt);
  }
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

interface Surface {
  readonly name: string;
  readonly sizes: readonly Size[];
  readonly setup: (size: Size) => void;
  /** Controls inside a layout: painted at the mouse's size, the 44px target invisible around them.
   *  Each selector must match at least one rendered element, and every match is checked. */
  readonly controls: readonly string[];
  /** Elements that are their own target: rows, fields and the header icons, whose box grows to
   *  the floor. */
  readonly rows?: readonly string[];
  /** Controls with no target of their own (a slider's thumb rides its track): painted size only. */
  readonly painted?: readonly string[];
  /** The mouse tier draws a different control here, so no fine height is there to match. */
  readonly ownFineForm?: true;
}

const HEADER_ICONS = [
  "#find-btn",
  "#files-btn",
  "#git-btn",
  "#shell-btn",
  "#docs-btn",
  "#settings-btn",
];

const SURFACES: readonly Surface[] = [
  {
    name: "the sidebar",
    sizes: ALL,
    setup: (size) => {
      plantTabs();
      q("#pointer-mode-btn").classList.remove("hidden");
      if (size === 390) {
        q("#sidebar").classList.add("open");
      }
    },
    controls: [
      "#new-chat",
      "#history-btn",
      "#theme-btn",
      "#pointer-mode-btn",
      "#account-btn",
      "#logout-btn",
      "#tab-list .tab-close",
    ],
    rows: ["#tab-list .tab"],
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
    controls: [],
    rows: HEADER_ICONS,
  },
  {
    name: "the phone header's menu",
    sizes: [390],
    setup: () => undefined,
    controls: [],
    rows: ["#menu-toggle"],
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
    setup: plantModelList,
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
    setup: plantRunBar,
    controls: [],
    rows: ["#run-bar-fold .run-bar-toggle", "#run-bar .run-bar-open"],
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
    rows: [".tool-group-header", ".tool-header", "summary.reasoning-summary", ".tool-diff-stats"],
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
    controls: ["a.run-open", "button.steer-note-run"],
    rows: [".run-head", ".run-step-head"],
  },
  {
    name: "a run card's loop",
    sizes: TWO,
    setup: plantLoop,
    controls: [],
    rows: [".run-head", ".run-step-head"],
  },
  {
    name: "a reply",
    sizes: TWO,
    setup: plantReply,
    controls: [".code-act-btn"],
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
    name: "the web preview's bar",
    sizes: TWO,
    setup: () => {
      show("web-view");
      q("#web-path").textContent = "/preview/index.html";
    },
    controls: ["#web-reload-btn", "#web-edit-btn"],
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
    name: "Settings, Tools",
    sizes: TWO,
    setup: () => {
      show("settings-view");
      showPanel("data-settings-panel", "tools");
      plantToolRows();
    },
    controls: [
      "#tool-update-btn",
      "#tool-add-btn",
      "#tool-catalog-refresh-btn",
      "#tools-list label.toggle",
      "#tools-list .list-row-btn",
      "#tool-open-manifest",
    ],
    rows: ["#tools-list .list-row"],
  },
  {
    name: "Settings, Permissions",
    sizes: TWO,
    setup: () => {
      show("settings-view");
      showPanel("data-settings-panel", "permissions");
      q("#native-policy-explain-box").setAttribute("open", "");
      plantPermissions();
    },
    controls: [
      "#native-rule-add",
      "#native-explain-run",
      ".native-rule-remove",
      "#agent-dirs-add",
      "#agent-dirs-chips .chip-remove",
    ],
    rows: ['.rule-form[data-rule-form="policy"] .rf-select', "#native-rule-match", ".native-rule"],
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
      void confirm("Delete this chat?", "Delete", "destructive");
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

/** Targets the layout cannot give 44px on one axis, each with the reason it is accepted. */
const NARROW: readonly { sel: string; sizes: readonly Size[]; axis: "inline" | "block" }[] = [
  // Nine segments share the 390px bar: each paints 36 wide and its target meets its neighbour's at
  // the gap midpoint, 40px wide.
  { sel: "#docs-tab-bar .seg", sizes: [390], axis: "inline" },
  // A × beside a filename or the next chip's ×: 24px inline, WCAG 2.5.8's minimum, since a wider
  // reach would cross the neighbour's target.
  { sel: ".attachment-close", sizes: ALL, axis: "inline" },
  { sel: "#agent-dirs-chips .chip-remove", sizes: ALL, axis: "inline" },
];

/** Shaped expanders, whose target is not centred on the box: the box grown by the `::after`'s
 *  used insets, which the press cases then hit-test like any other target. */
const SHAPED = new Set([
  ".attachment-close",
  "#agent-dirs-chips .chip-remove",
  "#fb-list .fb-git-clickable",
  "#run-bar-fold .run-bar-toggle",
  "a.git-pr-row-title",
  ".ev-box-toggle",
]);

function shaped(el: HTMLElement, b: Box): Box {
  const cs = getComputedStyle(el, "::after");
  const out = (v: string): number => Math.max(0, -(parseFloat(v) || 0));
  return {
    left: Math.max(0, b.left - out(cs.left)),
    right: Math.min(window.innerWidth, b.right + out(cs.right)),
    top: Math.max(0, b.top - out(cs.top)),
    bottom: Math.min(window.innerHeight, b.bottom + out(cs.bottom)),
  };
}

interface Box {
  readonly left: number;
  readonly right: number;
  readonly top: number;
  readonly bottom: number;
}

interface Probe {
  readonly el: HTMLElement;
  readonly sel: string;
  readonly label: string;
  readonly box: Box;
  /** The target the contract promises: the box where it clears the floor, else the floor centred
   *  on it, cut at the screen's edge and at the midpoint of the gap to a neighbour it would cross. */
  t: Box;
}

function boxOf(el: HTMLElement): Box {
  const r = el.getBoundingClientRect();
  return { left: r.left, right: r.right, top: r.top, bottom: r.bottom };
}

function floored(b: Box): Box {
  const w = Math.max(b.right - b.left, FLOOR) / 2;
  const h = Math.max(b.bottom - b.top, FLOOR) / 2;
  const cx = (b.left + b.right) / 2;
  const cy = (b.top + b.bottom) / 2;
  return {
    left: Math.max(0, cx - w),
    right: Math.min(window.innerWidth, cx + w),
    top: Math.max(0, cy - h),
    bottom: Math.min(window.innerHeight, cy + h),
  };
}

const span = (a0: number, a1: number, b0: number, b1: number): number =>
  Math.min(a1, b1) - Math.max(a0, b0);

/** Where two facing targets meet across a gap: its midpoint, unless one side reaches less than
 *  half of it (a box that already clears the floor reaches nothing), which leaves the rest to the
 *  other. */
function cut(nearEdge: number, nearReach: number, farEdge: number, farReach: number): number {
  const half = (farEdge - nearEdge) / 2;
  if (nearReach < half) {
    return nearEdge + nearReach;
  }
  if (farReach < half) {
    return farEdge - farReach;
  }
  return nearEdge + half;
}

/** Two neighbours whose floors would cross meet without overlapping. */
function meet(a: Probe, b: Probe): void {
  const across = span(a.t.top, a.t.bottom, b.t.top, b.t.bottom) > 0.5;
  const along = span(a.t.left, a.t.right, b.t.left, b.t.right) > 0.5;
  if (!(across && along)) {
    return;
  }
  const [l, r] = a.box.left < b.box.left ? [a, b] : [b, a];
  const [u, d] = a.box.top < b.box.top ? [a, b] : [b, a];
  if (span(a.box.top, a.box.bottom, b.box.top, b.box.bottom) > 0) {
    const mid = cut(l.box.right, l.t.right - l.box.right, r.box.left, r.box.left - r.t.left);
    l.t = { ...l.t, right: Math.min(l.t.right, mid) };
    r.t = { ...r.t, left: Math.max(r.t.left, mid) };
  } else {
    const mid = cut(u.box.bottom, u.t.bottom - u.box.bottom, d.box.top, d.box.top - d.t.top);
    u.t = { ...u.t, bottom: Math.min(u.t.bottom, mid) };
    d.t = { ...d.t, top: Math.max(d.t.top, mid) };
  }
}

const nested = (a: Probe, b: Probe): boolean => a.el.contains(b.el) || b.el.contains(a.el);

function probesFor(s: Surface, which: "all" | "controls" = "all"): Probe[] {
  const sels =
    which === "all" ? [...s.controls, ...(s.rows ?? [])] : [...s.controls, ...(s.painted ?? [])];
  const probes = sels.flatMap((sel) => {
    const found = [...document.querySelectorAll<HTMLElement>(sel)].filter(
      (e) => e.checkVisibility() && e.getBoundingClientRect().width > 0,
    );
    expect(found.length, `${s.name}: ${sel} renders`).toBeGreaterThan(0);
    return found.map((el, i) => {
      const box = boxOf(el);
      const t = SHAPED.has(sel) ? shaped(el, box) : floored(box);
      return { el, sel, label: `${sel}[${String(i)}]`, box, t };
    });
  });
  for (const [i, a] of probes.entries()) {
    for (const b of probes.slice(i + 1)) {
      if (!nested(a, b)) {
        meet(a, b);
      }
    }
  }
  return probes;
}

function reaches(p: Probe, x: number, y: number): string | null {
  const hit = document.elementFromPoint(x, y);
  if (hit !== null && p.el.contains(hit)) {
    return null;
  }
  const owner = hit?.closest("[id], [class]");
  const name =
    owner === null || owner === undefined
      ? "nothing"
      : owner.id !== ""
        ? `#${owner.id}`
        : (owner.getAttribute("class") ?? "");
  const b = boxOf(p.el);
  return `${p.label} [${b.left.toFixed(1)}..${b.right.toFixed(1)} x ${b.top.toFixed(1)}..${b.bottom.toFixed(1)}] at ${x.toFixed(1)},${y.toFixed(1)} reached ${name}`;
}

/** Longer than any entry transition and shorter than a toast's lifetime countdown. */
const ENTRY_MS = 1000;

/** A few frames, then every entry animation (a dialog's, a toast's) to its end, so a box is
 *  measured where it comes to rest. */
async function frames(): Promise<void> {
  for (let i = 0; i < 3; i++) {
    await new Promise((r) => requestAnimationFrame(r));
  }
  const entries = document.getAnimations().filter((a) => {
    const end = a.effect?.getComputedTiming().endTime;
    return typeof end === "number" && end <= ENTRY_MS;
  });
  await Promise.all(entries.map((a) => a.finished.catch(() => undefined)));
}

async function mountSurface(s: Surface, size: Size, tier: "fine" | "coarse"): Promise<void> {
  clearSurface();
  mountApp();
  document.documentElement.dataset["pointer"] = tier;
  s.setup(size);
  await frames();
}

/** Mount the surface, then for each control bring it on screen and measure everything afresh. */
async function eachControl(
  s: Surface,
  size: Size,
  check: (p: Probe, all: Probe[]) => void,
): Promise<void> {
  await mountSurface(s, size, "coarse");
  const count = probesFor(s).length;
  for (let i = 0; i < count; i++) {
    probesFor(s)[i]?.el.scrollIntoView({ block: "center", inline: "nearest" });
    await frames();
    const all = probesFor(s);
    check(all[i] as Probe, all);
  }
}

interface Findings {
  /** Presses 1px inside a target that reached something else. */
  readonly misses: string[];
  /** Targets under 44px on an axis the layout should give it. */
  readonly short: string[];
  /** Presses on either side of two meeting targets that reached the wrong one. */
  readonly faults: string[];
}

/** One mounting of a surface answers all three per-control cases, each asserting its own list. */
const findings = new Map<string, Promise<Findings>>();

function measured(s: Surface, size: Size): Promise<Findings> {
  const key = `${s.name}@${String(size)}`;
  let f = findings.get(key);
  if (f === undefined) {
    f = measure(s, size);
    findings.set(key, f);
  }
  return f;
}

async function measure(s: Surface, size: Size): Promise<Findings> {
  const out: Findings = { misses: [], short: [], faults: [] };
  const accepted = (p: Probe, axis: "inline" | "block"): boolean =>
    NARROW.some((n) => n.sel === p.sel && n.sizes.includes(size) && n.axis === axis);
  await eachControl(s, size, (a, all) => {
    const { left, right, top, bottom } = a.t;
    const cx = (left + right) / 2;
    const cy = (top + bottom) / 2;
    for (const [x, y] of [
      [left + 1, cy],
      [right - 1, cy],
      [cx, top + 1],
      [cx, bottom - 1],
    ] as const) {
      const miss = reaches(a, x, y);
      if (miss !== null) {
        out.misses.push(miss);
      }
    }
    const w = right - left;
    const h = bottom - top;
    if (w < FLOOR - 0.5 && !(left <= 0 || right >= window.innerWidth) && !accepted(a, "inline")) {
      out.short.push(`${a.label}: ${w.toFixed(1)}px wide`);
    }
    if (h < FLOOR - 0.5 && !(top <= 0 || bottom >= window.innerHeight) && !accepted(a, "block")) {
      out.short.push(`${a.label}: ${h.toFixed(1)}px tall`);
    }
    for (const b of all) {
      if (b === a || nested(a, b)) {
        continue;
      }
      const across = span(a.t.top, a.t.bottom, b.t.top, b.t.bottom);
      const along = span(a.t.left, a.t.right, b.t.left, b.t.right);
      if (across > 2 && Math.abs(a.t.right - b.t.left) < 2) {
        const y = (Math.max(a.t.top, b.t.top) + Math.min(a.t.bottom, b.t.bottom)) / 2;
        for (const miss of [reaches(a, a.t.right - 1, y), reaches(b, b.t.left + 1, y)]) {
          if (miss !== null) {
            out.faults.push(miss);
          }
        }
      }
      if (along > 2 && Math.abs(a.t.bottom - b.t.top) < 2) {
        const x = (Math.max(a.t.left, b.t.left) + Math.min(a.t.right, b.t.right)) / 2;
        for (const miss of [reaches(a, x, a.t.bottom - 1), reaches(b, x, b.t.top + 1)]) {
          if (miss !== null) {
            out.faults.push(miss);
          }
        }
      }
    }
  });
  return out;
}

describe.each(ALL)("the coarse tier at %ipx", (size) => {
  beforeAll(async () => {
    await page.viewport(...VIEWPORTS[size]);
  });

  const cases = SURFACES.filter((s) => s.sizes.includes(size));

  it.each(cases.map((s) => [s.name, s] as const))(
    "%s: a press 1px inside each control's 44px target reaches it",
    async (_name, s) => {
      const { misses } = await measured(s, size);
      expect(misses, `\n${misses.join("\n")}\n`).toEqual([]);
    },
  );

  it.each(cases.map((s) => [s.name, s] as const))(
    "%s: each target clears 44px on both axes once neighbours meet",
    async (_name, s) => {
      const { short } = await measured(s, size);
      expect(short, `\n${short.join("\n")}\n`).toEqual([]);
    },
  );

  it.each(cases.map((s) => [s.name, s] as const))(
    "%s: a press in a neighbour's target reaches the neighbour",
    async (_name, s) => {
      const { faults } = await measured(s, size);
      expect(faults, `\n${faults.join("\n")}\n`).toEqual([]);
    },
  );

  const painted = cases.filter(
    (s) => s.controls.length + (s.painted?.length ?? 0) > 0 && s.ownFineForm !== true,
  );
  it.each(painted.map((s) => [s.name, s] as const))(
    "%s: each control paints at its mouse-tier height",
    async (_name, s) => {
      const heights = async (tier: "fine" | "coarse"): Promise<Map<string, number>> => {
        await mountSurface(s, size, tier);
        return new Map(
          probesFor(s, "controls").map((p) => [p.label, p.box.bottom - p.box.top] as const),
        );
      };
      const fine = await heights("fine");
      const coarse = await heights("coarse");
      const drift = [...coarse]
        .filter(
          ([label, h]) => Math.abs(h - (fine.get(label) ?? Number.NaN)) > 0.5 || !fine.has(label),
        )
        .map(
          ([label, h]) =>
            `${label}: ${h.toFixed(1)}px coarse, ${String(fine.get(label)?.toFixed(1))}px fine`,
        );
      expect(drift, `\n${drift.join("\n")}\n`).toEqual([]);
    },
  );
});

describe("a run card's step pitch on the coarse tier", () => {
  // A step is its 44px head plus a 1px border on each side, and the gap between steps is the 2px
  // the 44px head leaves for, inside a loop as at the top level.
  it.each(TWO)("is 48px at the top level and inside a loop at %ipx", async (size) => {
    await page.viewport(...VIEWPORTS[size]);
    const loop = SURFACES.find((s) => s.name === "a run card's loop")!;
    await mountSurface(loop, size, "coarse");
    const pitches = (sel: string): number[] => {
      const tops = [...document.querySelectorAll<HTMLElement>(sel)].map(
        (e) => e.getBoundingClientRect().top,
      );
      return tops.slice(1).map((t, i) => t - (tops[i] ?? 0));
    };
    const top = pitches(".run-steps > .run-step");
    const inLoop = pitches(".run-group-body > .run-step");
    expect(top.length, "two top-level steps").toBeGreaterThan(0);
    expect(inLoop.length, "three steps in the loop").toBeGreaterThan(1);
    expect([...top, ...inLoop]).toEqual([...top, ...inLoop].map(() => 48));
  });
});

describe("a transcript's task list on the coarse tier", () => {
  // A task mark sits on its text line (WCAG 2.5.8's inline case), so its own box is its target
  // and no part of it reaches the next line's mark.
  it.each(TWO)("a press anywhere in a mark reaches that mark at %ipx", async (size) => {
    await page.viewport(...VIEWPORTS[size]);
    await mountSurface(
      {
        name: "a task list",
        sizes: TWO,
        setup: () => {
          turn(buildAssistantBubble("- [ ] one\n- [ ] two\n", false).root);
        },
        controls: [],
      },
      size,
      "coarse",
    );
    const marks = [
      ...document.querySelectorAll<HTMLInputElement>('.message input[type="checkbox"]'),
    ];
    expect(marks.length, "two task marks").toBe(2);
    const misses: string[] = [];
    for (const [i, mark] of marks.entries()) {
      const b = boxOf(mark);
      const cx = (b.left + b.right) / 2;
      for (let y = b.top + 1; y < b.bottom; y += 1) {
        if (document.elementFromPoint(cx, y) !== mark) {
          misses.push(`mark ${String(i)} at y=${y.toFixed(1)}`);
        }
      }
    }
    expect(misses, `\n${misses.join("\n")}\n`).toEqual([]);
  });
});

describe("the chat header's icons", () => {
  it.each(ALL)(
    "keep a 44px box on the coarse tier at %ipx, their mouse box on a fine one",
    async (size) => {
      await page.viewport(...VIEWPORTS[size]);
      const header = SURFACES.find((s) => s.name === "the chat header")!;
      for (const [tier, want] of [
        ["fine", 36],
        ["coarse", FLOOR],
      ] as const) {
        await mountSurface(header, size, tier);
        const sizes = probesFor(header).map(
          (p) =>
            `${p.label} ${String(p.box.bottom - p.box.top)}x${String(p.box.right - p.box.left)}`,
        );
        expect(sizes, tier).toEqual(
          probesFor(header).map((p) => `${p.label} ${String(want)}x${String(want)}`),
        );
      }
    },
  );
});

/** Everything a press can land on: the ARIA widget roles a pointer operates, focusable elements,
 *  a label that carries its checkbox or radio, and a row `wireRowToggle` makes a target. */
const INTERACTIVE = [
  "button",
  "a[href]",
  'input:not([type="hidden"])',
  "select",
  "textarea",
  "summary",
  '[role="button"]',
  '[role="checkbox"]',
  '[role="radio"]',
  '[role="tab"]',
  '[role="menuitem"]',
  '[role="option"]',
  '[role="switch"]',
  '[role="slider"]',
  '[tabindex]:not([tabindex="-1"])',
  'label:has(input[type="checkbox"], input[type="radio"])',
  "[data-row-toggle]",
].join(", ");

interface Hit {
  readonly el: Element;
  readonly label: string;
  readonly layer: Element;
  readonly box: Box;
  /** The box joined with any pseudo-element that reaches outside it, cut by every clip. */
  readonly target: Box;
  /** The box is on screen and inside every clip, and no scroller's fold cuts the target, so a
   *  short target is the layout's. */
  readonly whole: boolean;
}

const EMPTY: Box = { left: 0, right: 0, top: 0, bottom: 0 };
const area = (b: Box): boolean => b.right - b.left > 0.5 && b.bottom - b.top > 0.5;
const join = (a: Box, b: Box): Box => ({
  left: Math.min(a.left, b.left),
  right: Math.max(a.right, b.right),
  top: Math.min(a.top, b.top),
  bottom: Math.max(a.bottom, b.bottom),
});
const crop = (a: Box, b: Box): Box => {
  const c = {
    left: Math.max(a.left, b.left),
    right: Math.min(a.right, b.right),
    top: Math.max(a.top, b.top),
    bottom: Math.min(a.bottom, b.bottom),
  };
  return area(c) ? c : EMPTY;
};
const px = (v: string): number => parseFloat(v) || 0;

function paddingBox(e: Element, cs: CSSStyleDeclaration): Box {
  const r = e.getBoundingClientRect();
  return {
    left: r.left + px(cs.borderLeftWidth),
    right: r.right - px(cs.borderRightWidth),
    top: r.top + px(cs.borderTopWidth),
    bottom: r.bottom - px(cs.borderBottomWidth),
  };
}

/** Cut `b` to what `e` lets through: its padding box on each axis it clips. */
function clipBy(e: Element, b: Box): Box {
  const cs = getComputedStyle(e);
  const paint = /paint|strict|content/.test(cs.contain) || cs.contentVisibility === "auto";
  const x = paint || cs.overflowX !== "visible";
  const y = paint || cs.overflowY !== "visible";
  if (!x && !y) {
    return b;
  }
  const p = paddingBox(e, cs);
  return crop(b, {
    left: x ? p.left : -Infinity,
    right: x ? p.right : Infinity,
    top: y ? p.top : -Infinity,
    bottom: y ? p.bottom : Infinity,
  });
}

/** The element an absolute box inside `e` is positioned against: `e` or its nearest positioned
 *  ancestor. */
function containingBlock(e: Element): Element {
  for (let a: Element | null = e; a !== null; a = a.parentElement) {
    const cs = getComputedStyle(a);
    if (
      cs.position !== "static" ||
      cs.transform !== "none" ||
      /paint|layout|strict|content/.test(cs.contain)
    ) {
      return a;
    }
  }
  return document.documentElement;
}

/** Where a pseudo-element's box reaches: a stretched link's reaches its containing block. */
function pseudoBox(e: Element, which: "::before" | "::after"): Box | null {
  const cs = getComputedStyle(e, which);
  if (
    cs.content === "none" ||
    cs.content === "normal" ||
    cs.display === "none" ||
    cs.pointerEvents === "none" ||
    cs.position !== "absolute"
  ) {
    return null;
  }
  const cb = containingBlock(e);
  const p = paddingBox(cb, getComputedStyle(cb));
  return {
    left: p.left + px(cs.left),
    right: p.right - px(cs.right),
    top: p.top + px(cs.top),
    bottom: p.bottom - px(cs.bottom),
  };
}

/** The surface a press on `e` is decided in: a modal or popup over the page, or the page. */
function layerOf(e: Element): Element {
  for (let a: Element | null = e; a !== null; a = a.parentElement) {
    const cs = getComputedStyle(a);
    const raised = cs.position === "absolute" && (parseInt(cs.zIndex, 10) || 0) > 0;
    if (a.matches("dialog[open], [popover]") || cs.position === "fixed" || raised) {
      return a;
    }
  }
  return document.body;
}

function describeEl(e: Element): string {
  const id = e.id === "" ? "" : `#${e.id}`;
  const cls = [...e.classList]
    .slice(0, 3)
    .map((c) => `.${c}`)
    .join("");
  const name = (e.getAttribute("aria-label") ?? e.textContent ?? "").trim().slice(0, 28);
  const type = e instanceof HTMLInputElement ? `[${e.type}]` : "";
  return `${e.tagName.toLowerCase()}${type}${id}${cls}${name === "" ? "" : ` "${name}"`}`;
}

/** Whether a press inside `b` can land on `e` at all, rather than on a surface over it. */
function onTop(e: Element, b: Box): boolean {
  const w = b.right - b.left;
  const h = b.bottom - b.top;
  return [
    [0.5, 0.5],
    [0.25, 0.25],
    [0.75, 0.25],
    [0.25, 0.75],
    [0.75, 0.75],
  ].some(([fx = 0, fy = 0]) => {
    const hit = document.elementFromPoint(b.left + w * fx, b.top + h * fy);
    return (
      hit !== null && (e.contains(hit) || (e instanceof HTMLLabelElement && e.control === hit))
    );
  });
}

function walk(): Hit[] {
  const modal = document.querySelector("dialog:modal");
  const screen: Box = { left: 0, right: window.innerWidth, top: 0, bottom: window.innerHeight };
  const seen = new Map<string, number>();
  const hits: Hit[] = [];
  for (const el of document.querySelectorAll(INTERACTIVE)) {
    if (
      !el.checkVisibility({ visibilityProperty: true }) ||
      getComputedStyle(el).pointerEvents === "none" ||
      el.closest("[inert]") !== null ||
      (modal !== null && !modal.contains(el))
    ) {
      continue;
    }
    const box = boxOf(el as HTMLElement);
    if (!area(box)) {
      continue;
    }
    let target = box;
    for (const which of ["::before", "::after"] as const) {
      const pb = pseudoBox(el, which);
      if (pb !== null) {
        target = join(target, clipBy(el, pb));
      }
    }
    let visible = crop(box, screen);
    let scrolled = false;
    for (let a = el.parentElement; a !== null && area(target); a = a.parentElement) {
      const cut = clipBy(a, target);
      const scrolls = a.scrollHeight > a.clientHeight + 1 || a.scrollWidth > a.clientWidth + 1;
      scrolled ||= scrolls && (cut.top > target.top || cut.bottom < target.bottom);
      target = cut;
      visible = clipBy(a, visible);
    }
    target = crop(target, screen);
    if (!area(visible) || !onTop(el, visible)) {
      continue;
    }
    const base = describeEl(el);
    const n = seen.get(base) ?? 0;
    seen.set(base, n + 1);
    const whole =
      !scrolled &&
      Math.abs(visible.right - visible.left - (box.right - box.left)) < 0.5 &&
      Math.abs(visible.bottom - visible.top - (box.bottom - box.top)) < 0.5;
    hits.push({ el, label: `${base}[${String(n)}]`, layer: layerOf(el), box, target, whole });
  }
  return hits;
}

const fmt = (b: Box): string =>
  `${b.left.toFixed(1)}..${b.right.toFixed(1)} x ${b.top.toFixed(1)}..${b.bottom.toFixed(1)}`;

/** Every pair of targets in one layer that share more than device-pixel snapping explains. */
function overlaps(hits: readonly Hit[]): string[] {
  const out: string[] = [];
  for (const [i, a] of hits.entries()) {
    for (const b of hits.slice(i + 1)) {
      if (a.layer !== b.layer || a.el.contains(b.el) || b.el.contains(a.el)) {
        continue;
      }
      const o = crop(a.target, b.target);
      const w = o.right - o.left;
      const h = o.bottom - o.top;
      if (w > 1 && h > 1) {
        out.push(
          `${a.label} [${fmt(a.target)}] and ${b.label} [${fmt(b.target)}] share ${w.toFixed(1)}x${h.toFixed(1)}`,
        );
      }
    }
  }
  return out;
}

/** Why a walked element may have a target under 44px on an axis, or null where it may not. */
function shortAllowed(h: Hit, size: Size, axis: "inline" | "block"): string | null {
  const e = h.el;
  const t = h.target;
  const edge =
    axis === "inline"
      ? t.left <= 0.5 || t.right >= window.innerWidth - 0.5
      : t.top <= 0.5 || t.bottom >= window.innerHeight - 0.5;
  if (!h.whole || edge) {
    return "scrolled or at the screen edge";
  }
  if (e.matches('input[type="checkbox"], input[type="radio"]') && e.closest("label") !== null) {
    return "rides its label";
  }
  if (e.matches("textarea")) {
    return "a text field keeps its typing height";
  }
  if (e.matches(".effort-knob")) {
    return "rides its track";
  }
  return NARROW.some((n) => e.matches(n.sel) && n.sizes.includes(size) && n.axis === axis)
    ? "NARROW"
    : null;
}

interface WalkRecord {
  readonly surface: string;
  readonly size: Size;
  readonly label: string;
  readonly coarse: readonly [number, number];
  readonly target: readonly [number, number];
  /** The coarse box's and the target's top-left corners, for reading a failure. */
  readonly at: readonly number[];
}

const dims = (b: Box): readonly [number, number] => [
  Math.round((b.right - b.left) * 10) / 10,
  Math.round((b.bottom - b.top) * 10) / 10,
];

describe("every interactive element on the coarse tier", () => {
  // Every walked target and its size, written out so a layout change can be read as a diff.
  const records: WalkRecord[] = [];

  afterAll(async () => {
    await server.commands.writeFile(
      "./.vitest/touch-targets-walk.json",
      JSON.stringify(records, null, 1),
    );
  });

  describe.each(ALL)("at %ipx", (size) => {
    beforeAll(async () => {
      await page.viewport(...VIEWPORTS[size]);
    });

    it.each(SURFACES.map((s) => [s.name, s] as const))(
      "%s: no target overlaps another, and each clears 44px",
      async (_name, s) => {
        await mountSurface(s, size, "coarse");
        const hits = walk();
        for (const h of hits) {
          records.push({
            surface: s.name,
            size,
            label: h.label,
            coarse: dims(h.box),
            target: dims(h.target),
            at: [h.box.left, h.box.top, h.target.left, h.target.top].map(
              (v) => Math.round(v * 10) / 10,
            ),
          });
        }
        expect(hits.length, `${s.name} has interactive elements`).toBeGreaterThan(0);
        const found = overlaps(hits);
        for (const h of hits) {
          const w = h.target.right - h.target.left;
          const tall = h.target.bottom - h.target.top;
          if (w < FLOOR - 0.5 && shortAllowed(h, size, "inline") === null) {
            found.push(`${h.label} [${fmt(h.target)}]: ${w.toFixed(1)}px wide`);
          }
          if (tall < FLOOR - 0.5 && shortAllowed(h, size, "block") === null) {
            found.push(`${h.label} [${fmt(h.target)}]: ${tall.toFixed(1)}px tall`);
          }
        }
        expect(found, `\n${found.join("\n")}\n`).toEqual([]);
      },
    );
  });
});
