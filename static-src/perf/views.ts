// The measured scenarios and the stream drivers tests/perf/plan.json names. A view takes the seeded
// instance (seeded.json) and answers where it lives, its tab (or `opener`, the selector of the row
// that reopens a view with no tab), the tab an away window shows instead, the element whose DOM it
// counts, and `ready`, a page expression that is true once it painted. A driver takes the stream
// helpers view.mjs passes, the instance and the view, and resolves when its stream ended.

/** seeded.json, which tests/perf/instance/seed-fixture.sh writes. */
export interface SeededFacts {
  readonly run: string;
  readonly long_chat: string;
  readonly stream_chat: string;
  readonly run_parent: string;
  readonly work: string;
  readonly shell_lines: number;
  readonly tabs: Readonly<Record<string, string>>;
}

interface InstanceFacts extends SeededFacts {
  readonly small: readonly [string, string, string, ...string[]];
  readonly background: string;
}

export interface PerfView {
  readonly path: string;
  readonly tab: string | undefined;
  readonly away: string | null | undefined;
  readonly container: string;
  readonly ready: string;
  readonly chat?: string;
  readonly shell?: boolean;
  readonly opener?: string;
}

/** view.mjs's stream helpers; each resolves when its stream ended. */
interface StreamHelpers {
  trigger(op: { readonly op: string; readonly [field: string]: unknown }): Promise<unknown>;
  prompt(chat: string, shape: object): Promise<unknown>;
  forgeChurn(spec: object, holdMs: number): Promise<unknown>;
  shell(cmd: string, onTyped?: () => Promise<unknown>): Promise<unknown>;
  toggleShell(): Promise<unknown>;
}

/** A driver gets no view (null) on the server engine, which has no page. */
type Driver = (x: StreamHelpers, s: InstanceFacts, v: PerfView | null) => Promise<unknown>;

const TURN = { turns: 1, tools: 12, chunks: 10, text_chunks: 40, interval_ms: 50 };
const STEPS = { count: 22, lanes: 11, chunks: 10, tools: 4, interval_ms: 50 };
// run-drive's step stream, measured server-side with no stream listener attached.
const DRIVE = { count: 22, lanes: 11, chunks: 20, tools: 6, interval_ms: 100 };
const IN_PLACE_EDITS = 20;

const repoEdits = (s: InstanceFacts): string[] =>
  [0, 1, 2].flatMap((r) => [20, 21, 22, 23].map((i) => `${s.work}/repo-0${r}/internal/f0${i}.go`));
// Six existing files of the listed folder, each appended to twice by the turn's twelve edits.
const treeEdits = (s: InstanceFacts): string[] =>
  [0, 4, 8, 12, 16, 20].map((i) => `${s.work}/tree/big/entry-${String(i).padStart(5, "0")}.go`);
let docCalls = 0;
// Six new steering docs, a fresh name per call, alternating with six repository files; then one
// existing doc edited in place twenty times.
const docEdits = (s: InstanceFacts): string[] => {
  const call = docCalls++;
  const all = repoEdits(s);
  const repos = [0, 4, 8, 1, 5, 9].flatMap((i) => all[i] ?? []);
  const created = repos.flatMap((r, k) => [`${s.work}/.kiro/steering/live-${call}-${k}.md`, r]);
  const inPlace = Array.from(
    { length: IN_PLACE_EDITS },
    () => `${s.work}/.kiro/steering/doc-000.md`,
  );
  return [...created, ...inPlace];
};

export const DRIVERS: Readonly<Record<string, Driver>> = {
  "prompt-shown": (x, _s, v) => {
    if (v?.chat === undefined) {
      throw new Error("prompt-shown drives a chat view's own chat; this view shows none");
    }
    return x.prompt(v.chat, TURN);
  },
  "prompt-background": (x, s) => x.prompt(s.background, TURN),
  "edit-tree": (x, s) => x.prompt(s.background, { ...TURN, edit_paths: treeEdits(s) }),
  "edit-repos": (x, s) => x.prompt(s.background, { ...TURN, edit_paths: repoEdits(s) }),
  "edit-docs": (x, s) => {
    const edits = docEdits(s);
    return x.prompt(s.background, { ...TURN, tools: edits.length, edit_paths: edits });
  },
  "run-steps": (x, s) => x.trigger({ op: "steps", workflowId: s.run, ...STEPS }),
  "run-starts": (x) => x.trigger({ op: "starts", count: 5, interval_ms: 2000 }),
  "forge-churn": (x) => x.forgeChurn({ every_ms: 3000, count: 3 }, 130000),
  "shell-open": (x, s) => x.shell(`cd ${s.work} && ./emit.py ${s.shell_lines}`),
  // The command starts after the panel closed, so its output streams to a hidden terminal.
  "shell-closed": (x, s) => x.shell(`sleep 3 && ./emit.py ${s.shell_lines}`, () => x.toggleShell()),
  "run-drive": (x, s) => x.trigger({ op: "steps", workflowId: s.run, ...DRIVE }),
};

// CodeQL js/bad-code-sanitization takes a replace after JSON.stringify as its sanitizer: https://codeql.github.com/codeql-query-help/javascript/js-bad-code-sanitization/
const jsString = (s: string): string =>
  JSON.stringify(s).replace(
    /[<>/\u2028\u2029]/g,
    (c) => `\\u${c.charCodeAt(0).toString(16).padStart(4, "0")}`,
  );
const shown = (sel: string, min = 30): string =>
  `(() => { const c = document.querySelector(${jsString(sel)}); return !!c && c.getClientRects().length > 0 && !c.querySelector(".skeleton,.skeleton-rows,.turn-skel") && c.getElementsByTagName("*").length >= ${String(min)}; })()`;
const chatReady = (id: string): string =>
  `location.pathname === ${jsString(`/chat/${id}`)} && !!document.querySelector(".transcript-view.is-active .turn:not(.turn-skel)")`;
const at = (path: string): string => `location.pathname === ${jsString(path)}`;
// The run page's detail shows a step: its transcript, or a settled note. The step the page follows
// moves with the run, and a step that wrote nothing has no transcript host to wait for.
const runStepShown = `(() => { const d = document.querySelector("#run-body .ev-detail"); if (!d || d.dataset.state === "pending") return false; const b = d.querySelector(".ev-d-body:not([hidden])"); if (b && b.childElementCount > 0) return true; const e = d.querySelector(".ev-d-empty"); return !!e && !e.hidden && e.textContent !== "" && !e.textContent.startsWith("Loading"); })()`;

const chat = (s: InstanceFacts, id: string): PerfView => ({
  path: `/chat/${id}`,
  tab: s.tabs[id],
  away: s.tabs[s.small[0]],
  container: "#chat-view",
  ready: chatReady(id),
  chat: id,
});
const page = (
  s: InstanceFacts,
  path: string,
  tab: string | null,
  container: string,
  extra = "",
): PerfView => ({
  path,
  tab: tab === null ? undefined : s.tabs[tab],
  away: s.tabs[s.small[0]],
  container,
  ready: `${at(path)} && ${shown(container, 50)}${extra}`,
});

export const VIEWS: Readonly<Record<string, (s: InstanceFacts) => PerfView>> = {
  "chat-long": (s) => chat(s, s.long_chat),
  "chat-stream": (s) => chat(s, s.stream_chat),
  tabs: (s) => ({
    ...chat(s, s.small[0]),
    away: s.tabs[s.small[1]],
    container: "#tab-list",
    ready: `${chatReady(s.small[0])} && document.querySelectorAll("#tab-list [data-tab-id]").length >= ${String(Object.keys(s.tabs).length)}`,
  }),
  "run-dock": (s) => ({
    ...chat(s, s.run_parent),
    container: "#run-bar",
    ready: `${chatReady(s.run_parent)} && ${shown("#run-bar", 3)}`,
  }),
  "run-page": (s) => ({
    ...page(s, `/run/${s.run}`, "run", "#run-view", ` && ${runStepShown}`),
  }),
  files: (s) => ({
    path: `/files${s.work}/tree/big`,
    tab: s.tabs["files"],
    away: s.tabs[s.small[0]],
    container: "#files-view",
    ready: `location.pathname.startsWith("/files") && ${shown("#files-view", 50)} && document.querySelectorAll("#fb-list > *").length >= 10`,
  }),
  "git-changes": (s) => ({
    path: "/git",
    tab: s.tabs["git"],
    away: s.tabs[s.small[0]],
    container: "#git-view",
    ready: `${at("/git")} && ${shown('#git-view [data-git-panel="changes"]', 50)}`,
  }),
  "git-prs": (s) => ({
    path: "/git/prs",
    tab: s.tabs["git"],
    away: s.tabs[s.small[0]],
    container: "#git-view",
    ready: `${at("/git/prs")} && ${shown('#git-view [data-git-panel="prs"]', 50)}`,
  }),
  settings: (s) => page(s, "/settings", "settings", "#settings-view"),
  "settings-tools": (s) => page(s, "/settings/tools", "settings", "#settings-view"),
  docs: (s) => page(s, "/docs", "docs", "#docs-view"),
  "docs-workflows": (s) =>
    page(
      s,
      "/docs/workflows",
      "docs",
      "#docs-view",
      ` && !document.querySelector("#docs-view .list-empty")`,
    ),
  shell: (s) => ({
    ...chat(s, s.small[0]),
    away: null,
    container: "#shell-panel",
    shell: true,
    ready: `${chatReady(s.small[0])} && ${shown("#shell-panel", 5)}`,
  }),
  // History is no tab: More, in the sidebar's History section, reopens it on the list it last
  // showed; a scripted click reaches More even while the section is hidden or folded.
  history: (s) => ({ ...page(s, "/history", null, "#history-view"), opener: "#history-more" }),
  "history-runs": (s) => ({
    ...page(s, "/history/runs", null, "#history-view"),
    opener: "#history-more",
  }),
};

/** The instance facts every view and driver reads: seeded.json plus the small chats, the open chats
 *  that are none of the long, stream and run-parent chats, in tab order. `background` is the third,
 *  the chat whose turn a background driver streams. */
export function instanceFacts(seeded: SeededFacts): InstanceFacts {
  const named = new Set([seeded.long_chat, seeded.stream_chat, seeded.run_parent]);
  const small = Object.keys(seeded.tabs).filter((k) => k.startsWith("c-") && !named.has(k));
  const [first, second, third, ...rest] = small;
  if (first === undefined || second === undefined || third === undefined) {
    throw new Error(`the fixture opens ${String(small.length)} small chats; the drivers need 3`);
  }
  return { ...seeded, small: [first, second, third, ...rest], background: third };
}
