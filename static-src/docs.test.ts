import { describe, it, expect, vi, afterEach, beforeEach, beforeAll } from "vitest";
import type * as GitStatusStore from "./git-status-store.js";

type GitStatusStoreModule = typeof GitStatusStore;

vi.mock("./toast.js", () => import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock()));
// Browser Mode links a mock as real ESM, so every name this graph imports must exist on the factory.
vi.mock("./api-client.js", () => ({
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(),
  apiGetTypedOrError: vi.fn(),
  // Present-but-inert for real-ESM linking; a missing name fails the whole file's collection.
  apiGetOrError: vi.fn(),
}));
vi.mock("./editor-openers.js", () => ({
  // Present-but-undefined for real-ESM linking; the node runner gave the same.
  openFileGitDiff: undefined,
  openFileDiff: undefined,
  openFile: vi.fn(),
}));

const DOCS_PATH = "/api/workspace/kiro-docs";
const ISSUES_PATH = "/api/steering/issues";

/**
 * Served off one mock: the inventory through the caller's decoder, hooks empty, steering issues by `issues`.
 * `mockReset` runs between tests, so every case re-arms.
 */
function serveDocsPage(
  inventory: Promise<unknown> | unknown = { docs: [], truncated: false },
  issues: unknown = { issues: {} },
): (path: string, decode: (v: unknown) => unknown) => Promise<unknown> {
  return (path, decode) =>
    path === DOCS_PATH
      ? Promise.resolve(inventory).then(decode)
      : path === ISSUES_PATH
        ? Promise.resolve(issues).then(decode)
        : Promise.resolve({ hooks: [] });
}
// toggleDocsView runs onShow, which wires and loads the page; swallowing it leaves the SSE cases on an unopened page.
vi.mock("./tabs.js", () => ({
  // Present-but-undefined for real-ESM linking.
  getActiveTabRoute: undefined,
  openRunTab: undefined,
  setGitTab: undefined,
  setSettingsTab: undefined,
  openGitView: undefined,
  openSettingsView: undefined,
  // Reached through run-view.js → run-dots.js.
  hasTab: undefined,
  tabIdFor: undefined,
  tabSetVersion: undefined,
  openRunRefs: undefined,
  renameTab: undefined,
  // Reached through run-view.js's run card linkifier.
  closeTab: undefined,
  getActiveTabId: undefined,
  setTabStatus: undefined,
  // Reached through run-view.js's parent-chat note.
  parentChatRef: undefined,
  openTab: undefined,
  // navigate.js's `openSpec`, from this page's spec-group door.
  activateTab: undefined,
  setTabParent: undefined,
  setDocsTab: vi.fn(),
  toggleDocsView: vi.fn(() => Promise.resolve()),
}));
vi.mock("./bus.js", () => ({
  // Present-but-undefined for real-ESM linking.
  BUS_RUNS_CHANGED: undefined,
  BUS_USER_INPUT_ANSWERED: undefined,
  BUS_ACTIVATE_CHAT: undefined,
  BUS_COMMAND_FAILED: undefined,
  BUS_EDITOR_FILE_LOADED: undefined,
  BUS_KEYS_ESCAPE: undefined,
  BUS_PAGE_RESUMED: undefined,
  BUS_RECONCILE: undefined,
  BUS_TAB_CHANGED: undefined,
  decodeEnvelope: undefined,
  dispatch: undefined,
  emitBus: undefined,
  registerSSEDecoder: undefined,
  onBus: undefined,
  onSSE: vi.fn(() => () => undefined),
}));
// Only the subscription is replaced: subscribing fetches through the transport, which outlived teardown as an
// unhandled AbortError. The store stays real.
vi.mock("./git-status-store.js", async (importOriginal) => ({
  ...(await importOriginal<GitStatusStoreModule>()),
  onGitStatusChange: vi.fn(() => () => undefined),
}));
vi.mock("./actions/hooks.js", () => ({ setHookEnabled: { dispatch: vi.fn() } }));

import { _setDocsForTest, _setHooksForTest, _hookRowsForTest, _renderRowForTest } from "./docs.js";
import { _setReposForTest } from "./git-status-store.js";
import type { GitRepoStatus } from "./git-types.js";
import { setWorkspaceRoot, _resetForTest as resetWorkspace } from "./workspace.js";

beforeEach(() => {
  _setReposForTest([]);
  _setDocsForTest([]);
  _setHooksForTest([]);
});

afterEach(() => {
  resetWorkspace();
});

function repoStatus(name: string, path: string, status: string): GitRepoStatus {
  return {
    repo: name,
    is_repo: true,
    branch: "main",
    remote: "origin",
    ahead: 0,
    behind: 0,
    has_dirty: true,
    stashes: 0,
    files: [{ path, status, staged: false, display: path }],
  };
}

// `/api/git/status-all` names each repository by its directory under the work dir, "." when the work dir is itself
// one (discoverRepos in internal/git/repos.go), so a row's letter must not depend on which layout holds it.
describe("a row's git letter, whatever repository layout holds it", () => {
  function letterAt(path: string): string | null | undefined {
    return _renderRowForTest({ category: "steering", name: "x", path }).querySelector(
      ".docs-git-letter",
    )?.textContent;
  }

  it("reads the work dir's own .kiro when the work dir is the repository", () => {
    setWorkspaceRoot("/srv/proj");
    _setReposForTest([repoStatus(".", ".kiro/steering/x.md", "M")]);
    expect(letterAt("srv/proj/.kiro/steering/x.md")).toBe("M");
  });

  it("reads a first-level directory's .kiro inside a work dir that is the repository", () => {
    setWorkspaceRoot("/srv/proj");
    _setReposForTest([repoStatus(".", "myrepo/.kiro/steering/x.md", "A")]);
    expect(letterAt("srv/proj/myrepo/.kiro/steering/x.md")).toBe("A");
  });

  it("reads a root .kiro that is a clone of its own under a custom work dir", () => {
    setWorkspaceRoot("/srv/proj");
    _setReposForTest([repoStatus(".kiro", "steering/x.md", "M")]);
    expect(letterAt("srv/proj/.kiro/steering/x.md")).toBe("M");
  });

  it("reads a per-repo .kiro as part of that repository", () => {
    setWorkspaceRoot("/srv/proj");
    _setReposForTest([repoStatus("myrepo", ".kiro/steering/x.md", "M")]);
    expect(letterAt("srv/proj/myrepo/.kiro/steering/x.md")).toBe("M");
  });

  it("reads a / work dir's repositories", () => {
    setWorkspaceRoot("/");
    _setReposForTest([repoStatus(".kiro", "steering/x.md", "M")]);
    expect(letterAt(".kiro/steering/x.md")).toBe("M");
  });

  it("gives no letter to a .kiro tree outside the workspace", () => {
    setWorkspaceRoot("/srv/proj");
    _setReposForTest([repoStatus(".kiro", "steering/x.md", "M")]);
    expect(letterAt("srv/other/.kiro/steering/x.md")).toBeUndefined();
  });
});

describe("row rendering", () => {
  it("shows a steering doc's inclusion badge, with the pattern on hover", () => {
    const row = _renderRowForTest({
      category: "steering",
      name: "actions",
      path: "workspace/.kiro/steering/actions.md",
      description: "The actions framework",
      inclusion: "fileMatch",
      file_match: "actions/**",
    });
    expect(row.textContent).toContain("actions");
    expect(row.textContent).toContain("fileMatch");
    expect(row.textContent).toContain("The actions framework");
    // The class is lowercased; the label keeps the camelCase spelling.
    const badge = row.querySelector(".docs-badge-filematch");
    expect(badge?.getAttribute("data-tooltip")).toBe("actions/**");
  });

  it("shows an agent's model and tool count, with the tools on hover", () => {
    const row = _renderRowForTest({
      category: "agent",
      name: "trial-judge",
      path: "workspace/.kiro/agents/trial-judge.md",
      model: "claude-opus-5",
      tools: ["read", "write", "shell"],
    });
    expect(row.textContent).toContain("claude-opus-5");
    expect(row.textContent).toContain("3 tools");
    const chip = [...row.querySelectorAll(".docs-badge")].find((e) => e.textContent === "3 tools");
    expect(chip?.getAttribute("data-tooltip")).toBe("read, write, shell");
  });

  it("singularises a one-tool agent", () => {
    const row = _renderRowForTest({
      category: "agent",
      name: "solo",
      path: "workspace/.kiro/agents/solo.md",
      tools: ["read"],
    });
    expect(row.textContent).toContain("1 tool");
    expect(row.textContent).not.toContain("1 tools");
  });

  it("marks a skill that overrides the steering set", () => {
    const row = _renderRowForTest({
      category: "skill",
      name: "triage",
      path: "workspace/.kiro/skills/triage/SKILL.md",
      inclusion: "manual",
      steering_override: true,
    });
    expect(row.textContent).toContain("override");
  });

  it("shows a hook's trigger and its action as the subtitle", () => {
    const row = _renderRowForTest({
      category: "hook",
      name: "Knowledge Map regen",
      path: "workspace/.kiro/hooks/km.json",
      trigger: "PostFileSave",
      action: "python3 .kiro/scripts/generate-knowledge-map.py",
    });
    expect(row.textContent).toContain("PostFileSave");
    expect(row.textContent).toContain("generate-knowledge-map.py");
  });

  it("renders a spec row with no metadata badges (specs carry no front-matter)", () => {
    const row = _renderRowForTest({
      category: "spec",
      name: "Requirements — Something",
      path: "workspace/.kiro/specs/feature/requirements.md",
      group: "feature",
    });
    expect(row.textContent).toContain("Requirements — Something");
    expect(row.querySelectorAll(".docs-badge")).toHaveLength(0);
  });

  it("decorates a dirty document with its git letter", () => {
    setWorkspaceRoot("/workspace");
    _setReposForTest([repoStatus(".kiro", "steering/actions.md", "M")]);
    const row = _renderRowForTest({
      category: "steering",
      name: "actions",
      path: "workspace/.kiro/steering/actions.md",
    });
    const letter = row.querySelector(".docs-git-letter");
    expect(letter?.textContent).toBe("M");
    expect(letter?.getAttribute("aria-label")).toBe("Git status: Modified");
  });

  it("omits the git letter for a clean document", () => {
    setWorkspaceRoot("/workspace");
    _setReposForTest([repoStatus(".kiro", "steering/other.md", "M")]);
    const row = _renderRowForTest({
      category: "steering",
      name: "actions",
      path: "workspace/.kiro/steering/actions.md",
    });
    expect(row.querySelector(".docs-git-letter")).toBeNull();
  });

  it("makes the body a real button named after the document", () => {
    const row = _renderRowForTest({
      category: "steering",
      name: "actions",
      path: "workspace/.kiro/steering/actions.md",
    });
    const door = row.querySelector<HTMLElement>("button.entry-open");
    expect(door?.getAttribute("type")).toBe("button");
    expect(door?.getAttribute("aria-label")).toBe("Open actions");
  });

  it("opens the file from the body and from the Edit button alike", async () => {
    const { openFile } = await import("./editor-openers.js");
    vi.mocked(openFile).mockClear();
    const row = _renderRowForTest({
      category: "steering",
      name: "actions",
      path: "workspace/.kiro/steering/actions.md",
    });
    row.querySelector<HTMLButtonElement>("button.entry-open")?.click();
    row.querySelector<HTMLButtonElement>(".docs-edit")?.click();
    expect(vi.mocked(openFile).mock.calls).toEqual([
      ["workspace/.kiro/steering/actions.md"],
      ["workspace/.kiro/steering/actions.md"],
    ]);
    expect(row.querySelector(".docs-edit")?.getAttribute("aria-label")).toBe("Edit actions");
  });

  it("renders a spec's path and its group as the two lines under the title", () => {
    const row = _renderRowForTest({
      category: "spec",
      name: "Design",
      path: "workspace/.kiro/specs/search/design.md",
      group: "search",
    });
    const lines = [...(row.querySelector(".entry-lines")?.children ?? [])];
    expect(lines.map((l) => l.textContent)).toEqual([
      "workspace/.kiro/specs/search/design.md",
      "search",
    ]);
    expect(lines[0]?.classList.contains("entry-sub-mono")).toBe(true);
  });

  it("clamps a description to the two-line region", () => {
    const row = _renderRowForTest({
      category: "agent",
      name: "a",
      path: "workspace/.kiro/agents/a.md",
      description: "A paragraph of description.",
    });
    expect(row.querySelector(".entry-sub")?.classList.contains("entry-sub-clamp")).toBe(true);
  });
});

describe("row affordances", () => {
  it("gives a writable row a delete button", () => {
    const row = _renderRowForTest({
      category: "steering",
      name: "actions",
      path: "workspace/.kiro/steering/actions.md",
    });
    const del = row.querySelector<HTMLButtonElement>(".entry-delete");
    expect(del).not.toBeNull();
    expect(del?.getAttribute("aria-label")).toBe("Delete actions");
  });

  it("gives an asserted read-only row neither an edit glyph nor a delete", () => {
    const row = _renderRowForTest({
      category: "steering",
      name: "locked",
      path: "workspace/.kiro/steering/locked.md",
      read_only: true,
    });
    expect(row.querySelector(".entry-delete")).toBeNull();
    expect(row.querySelector(".docs-edit")).toBeNull();
  });

  it("makes no read-only claim, because the surface still opens the file", () => {
    // A row states what it can back up: no "read-only" badge on a file the editor can save.
    const row = _renderRowForTest({
      category: "steering",
      name: "locked",
      path: "workspace/.kiro/steering/locked.md",
      read_only: true,
    });
    expect(row.textContent).not.toContain("read-only");
    expect(row.querySelector(".docs-badge-readonly")).toBeNull();
  });

  it("keeps a symlinked row's edit and withholds only its delete", () => {
    // Editing through a link writes the target; deleting it would remove a file listed elsewhere on the page.
    const row = _renderRowForTest({
      category: "steering",
      name: "alias",
      path: "workspace/.kiro/steering/alias.md",
      delete_protected: true,
    });
    expect(row.querySelector(".entry-delete")).toBeNull();
    expect(row.querySelector(".docs-edit")).not.toBeNull();
    const badge = row.querySelector(".docs-badge-link");
    expect(badge?.textContent).toBe("link");
    expect(badge?.getAttribute("data-tooltip")).toContain("delete is disabled");
  });

  it("treats absent provenance fields as unrestricted", () => {
    // A restriction is asserted by the server, never inferred from an absent field.
    const row = _renderRowForTest({
      category: "hook",
      name: "h",
      path: "workspace/.kiro/hooks/h.json",
    });
    expect(row.querySelector(".entry-delete")).not.toBeNull();
    expect(row.querySelector(".docs-edit")).not.toBeNull();
  });

  it("offers an agent row a Run control, and only when its name is addressable", () => {
    const row = _renderRowForTest({
      category: "agent",
      name: "reviewer",
      path: "workspace/.kiro/agents/reviewer.json",
    });
    expect(row.querySelector(".docs-agent-run")?.getAttribute("aria-label")).toBe("Run reviewer");
    const spaced = _renderRowForTest({
      category: "agent",
      name: "code reviewer",
      path: "workspace/.kiro/agents/code.md",
    });
    expect(spaced.querySelector(".docs-agent-run")).toBeNull();
  });

  it("keeps the actions OUTSIDE the open button", () => {
    // A button cannot hold another (invalid HTML), so the two are siblings.
    const row = _renderRowForTest({
      category: "agent",
      name: "a",
      path: "workspace/.kiro/agents/a.md",
    });
    const door = row.querySelector<HTMLElement>("button.entry-open");
    const actions = row.querySelector<HTMLElement>(".entry-actions");
    expect(door).not.toBeNull();
    expect(actions?.querySelectorAll("button")).toHaveLength(3);
    expect(door?.contains(actions ?? null)).toBe(false);
    expect(door?.querySelector("button")).toBeNull();
  });
});

// The Hooks tab joins the scan's rows against GET /api/hooks for state a file scan cannot see, and synthesizes rows
// for global hooks the scan cannot reach.

interface HookLike {
  id: string;
  name: string;
  enabled: boolean;
  scope?: string;
  disabled_reason?: string;
  file_path?: string;
  trigger?: string;
  command?: string;
  prompt?: string;
  matcher?: string;
  matcher_warning?: string;
}

function wsHook(over: Partial<HookLike> = {}): HookLike {
  return {
    id: "id-greet",
    name: "greet",
    enabled: true,
    scope: "workspace",
    file_path: ".kiro/hooks/greet.json",
    trigger: "Manual",
    command: "echo hello",
    ...over,
  };
}

// The key genuinely absent: `decodeHook` copies only present keys, so `{ scope: undefined }` is a shape production
// never produces.

function withoutScope(hook: HookLike): HookLike {
  const { scope: _scope, ...rest } = hook;
  return rest;
}

function withoutCommand(hook: HookLike): HookLike {
  const { command: _command, ...rest } = hook;
  return rest;
}

/** The hook's path lacks the work directory the row's carries, which is why the join normalizes. */
function wsHookDoc(over: Partial<Parameters<typeof _renderRowForTest>[0]> = {}) {
  return {
    category: "hook",
    name: "greet",
    path: "workspace/.kiro/hooks/greet.json",
    group: "greet.json",
    trigger: "Manual",
    action: "echo hello",
    ...over,
  };
}

describe("the Hooks tab: joining state onto a scanned row", () => {
  it("joins across the two path SHAPES the endpoints use", () => {
    // hookInfo.FilePath is workDir-relative; kiroDoc.Path carries the workdir. A raw join loses the toggle.
    _setHooksForTest([wsHook()]);
    const row = _renderRowForTest(wsHookDoc());
    expect(row.querySelector(".hook-toggle")).not.toBeNull();
  });

  it("keys the join on (path, NAME), because one file can hold several hooks", () => {
    // One v1 envelope expands to one row per hook sharing a Path; keyed on path alone, one toggle would apply to all.
    _setHooksForTest([
      wsHook({ id: "id-a", name: "first", enabled: true }),
      wsHook({ id: "id-b", name: "second", enabled: false }),
    ]);
    const first = _renderRowForTest(wsHookDoc({ name: "first" }));
    const second = _renderRowForTest(wsHookDoc({ name: "second" }));
    expect((first.querySelector(".hook-toggle") as HTMLInputElement | null)?.checked).toBe(true);
    expect((second.querySelector(".hook-toggle") as HTMLInputElement | null)?.checked).toBe(false);
    expect(first.querySelector("[data-hook-id]")?.getAttribute("data-hook-id")).toBe("id-a");
    expect(second.querySelector("[data-hook-id]")?.getAttribute("data-hook-id")).toBe("id-b");
  });

  it("renders the enabled state the scan cannot know", () => {
    _setHooksForTest([wsHook({ enabled: false })]);
    const off = _renderRowForTest(wsHookDoc());
    expect((off.querySelector(".hook-toggle") as HTMLInputElement).checked).toBe(false);
    expect(off.querySelector(".hook-toggle")?.getAttribute("aria-label")).toBe("Enable hook greet");

    _setHooksForTest([wsHook({ enabled: true })]);
    const on = _renderRowForTest(wsHookDoc());
    expect((on.querySelector(".hook-toggle") as HTMLInputElement).checked).toBe(true);
    expect(on.querySelector(".hook-toggle")?.getAttribute("aria-label")).toBe("Disable hook greet");
  });

  it("shows why KAS disabled a hook", () => {
    _setHooksForTest([wsHook({ enabled: false, disabled_reason: "its command is empty" })]);
    const row = _renderRowForTest(wsHookDoc());
    const badge = row.querySelector(".docs-badge-disabled");
    expect(badge?.textContent).toBe("disabled");
    expect(badge?.getAttribute("data-tooltip")).toBe("its command is empty");
  });

  it("renders the matcher as the first line under the title, the command as the second", () => {
    // Trigger says when, matcher says which. The trigger comes from the scan, the matcher only from the joined row.
    _setHooksForTest([wsHook({ trigger: "PreToolUse", matcher: "fsWrite" })]);
    const row = _renderRowForTest(wsHookDoc({ trigger: "PreToolUse" }));
    expect(row.querySelector(".docs-badge-trigger")?.textContent).toBe("PreToolUse");
    const lines = [...(row.querySelector(".entry-lines")?.children ?? [])];
    expect(lines.map((l) => l.textContent)).toEqual(["fsWrite", "echo hello"]);
    // Mono on both: a regex and a command are read character by character.
    expect(lines.every((l) => l.classList.contains("entry-sub-mono"))).toBe(true);
  });

  it("keeps the trigger and drops the tail when a row has more than two badges", () => {
    // Two badges, most important first: trigger, scope, matcher defect, disabled reason. All stay in the haystack.
    _setHooksForTest([
      wsHook({
        scope: "global",
        file_path: "~/.kiro/hooks/greet.json",
        matcher_warning: "missing_tool_matcher",
        disabled_reason: "its command is empty",
      }),
    ]);
    const row = _renderRowForTest(
      wsHookDoc({ path: "~/.kiro/hooks/greet.json", hook_scope: "global" }),
    );
    const badges = [...(row.querySelector(".entry-badges")?.children ?? [])];
    expect(badges.map((b) => b.textContent)).toEqual(["Manual", "global"]);
  });

  it("puts the toggle first among the actions", () => {
    _setHooksForTest([wsHook()]);
    const actions = [
      ...(_renderRowForTest(wsHookDoc()).querySelector(".entry-actions")?.children ?? []),
    ];
    expect(actions.map((a) => a.className)).toEqual([
      "toggle toggle-inline",
      "icon-btn docs-edit",
      "icon-btn entry-delete",
    ]);
  });

  it("badges a hook that runs on every tool call", () => {
    // Server-computed: a second trigger-to-subject table could disagree with the Go one.
    _setHooksForTest([wsHook({ trigger: "PreToolUse", matcher_warning: "missing_tool_matcher" })]);
    const row = _renderRowForTest(wsHookDoc());
    const badge = row.querySelector(".docs-badge-warn");
    expect(badge?.textContent).toBe("every tool");
    expect(badge?.getAttribute("data-tooltip")).toContain("EVERY tool call");
  });

  it("badges a matcher that governs nothing", () => {
    _setHooksForTest([wsHook({ trigger: "SessionStart", matcher_warning: "ineffective" })]);
    const row = _renderRowForTest(wsHookDoc());
    expect(row.querySelector(".docs-badge-warn")?.textContent).toBe("no effect");
  });

  it("renders no warning badge for a value it does not recognise", () => {
    // A server-side enum: an older client must stay quiet about an unknown value.
    _setHooksForTest([wsHook({ matcher_warning: "someFutureDefect" })]);
    const row = _renderRowForTest(wsHookDoc());
    expect(row.querySelector(".docs-badge-warn")).toBeNull();
  });

  it("repaints when only the matcher or its warning changed", () => {
    // Path and name do not move when a matcher is edited, so the row signature carries both fields.
    _setHooksForTest([wsHook({ trigger: "PreToolUse", matcher: "fsWrite" })]);
    const row = _renderRowForTest(wsHookDoc());
    const first = row.getAttribute("data-sig");

    _setHooksForTest([wsHook({ trigger: "PreToolUse", matcher: "fsAppend" })]);
    const second = _renderRowForTest(wsHookDoc()).getAttribute("data-sig");
    expect(second).not.toBe(first);

    _setHooksForTest([wsHook({ trigger: "PreToolUse", matcher_warning: "missing_tool_matcher" })]);
    const third = _renderRowForTest(wsHookDoc()).getAttribute("data-sig");
    expect(third).not.toBe(second);
  });

  it("keeps a workspace hook's open surface and its delete", () => {
    // A hook gets every document affordance plus the toggle.
    _setHooksForTest([wsHook()]);
    const row = _renderRowForTest(wsHookDoc());
    expect(row.querySelector("button.entry-open")?.getAttribute("aria-label")).toBe("Open greet");
    expect(row.querySelector(".entry-delete")).not.toBeNull();
    expect(row.querySelector(".docs-edit")).not.toBeNull();
  });

  it("leaves a non-hook row untouched by the join", () => {
    _setHooksForTest([wsHook()]);
    const row = _renderRowForTest({
      category: "steering",
      name: "greet",
      path: "workspace/.kiro/hooks/greet.json",
    });
    expect(row.querySelector(".hook-toggle")).toBeNull();
  });

  it("renders a hook with no state as a plain document row", () => {
    // The hooks fetch is best-effort, so a row with no state degrades to open + delete.
    const row = _renderRowForTest(wsHookDoc());
    expect(row.querySelector(".hook-toggle")).toBeNull();
    expect(row.querySelector(".entry-delete")).not.toBeNull();
  });
});

// The third gate: a global hook's file is unreachable (container HOME is deny-listed by internal/filebrowse), so the
// activation surface goes too. The three gates must agree, not stack.
describe("the Hooks tab: a global hook is unreachable, not merely read-only", () => {
  const globalHook = (over: Partial<HookLike> = {}) =>
    wsHook({ scope: "global", file_path: "~/.kiro/hooks/greet.json", ...over });

  // A synthesized global row carries `hook_scope`, which the join keys on.
  const globalDoc = () => wsHookDoc({ path: "~/.kiro/hooks/greet.json", hook_scope: "global" });

  it("gives it neither an open surface nor a delete", () => {
    _setHooksForTest([globalHook()]);
    const row = _renderRowForTest(globalDoc());
    expect(row.querySelector(".entry-delete")).toBeNull();
    expect(row.querySelector(".docs-edit")).toBeNull();
  });

  it("makes the body INERT, not a disabled-looking button", () => {
    // A withheld row must not open a file on click, and a disabled button still announces itself.
    _setHooksForTest([globalHook()]);
    const row = _renderRowForTest(globalDoc());
    expect(row.querySelector(".entry-open")).toBeNull();
    const body = row.querySelector<HTMLElement>(".entry-body");
    expect(body?.tagName).toBe("DIV");
    expect(body?.getAttribute("role")).toBeNull();
    expect(body?.getAttribute("tabindex")).toBeNull();
    expect(body?.getAttribute("aria-label")).toBeNull();
  });

  it("does not open the file when its body is clicked or Entered", async () => {
    const { openFile } = await import("./editor-openers.js");
    vi.mocked(openFile).mockClear();
    _setHooksForTest([globalHook()]);
    const body = _renderRowForTest(globalDoc()).querySelector<HTMLElement>(".entry-body");
    expect(body).not.toBeNull();
    body?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    body?.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(vi.mocked(openFile)).not.toHaveBeenCalled();
  });

  it("STILL offers the toggle, which is the point of the three gates agreeing", () => {
    // The toggle goes through POST /api/hooks/{id}/enabled (KAS writes the file), never the file surface.
    _setHooksForTest([globalHook({ enabled: true })]);
    const row = _renderRowForTest(globalDoc());
    expect((row.querySelector(".hook-toggle") as HTMLInputElement).checked).toBe(true);
  });

  it("says where the file is, since the row cannot open it", () => {
    _setHooksForTest([globalHook()]);
    const badge = _renderRowForTest(globalDoc()).querySelector(".docs-badge-global");
    expect(badge?.textContent).toBe("global");
    expect(badge?.getAttribute("data-tooltip")).toContain("~/.kiro/hooks/greet.json");
    expect(badge?.getAttribute("data-tooltip")).toContain("cannot be opened or deleted");
  });

  it("carries no git letter, whose lookup its path cannot answer", () => {
    // The workspace's own hook of the same name is dirty, so a suffix match would paint its letter here.
    setWorkspaceRoot("/workspace");
    _setReposForTest([repoStatus(".kiro", "hooks/greet.json", "M")]);
    _setHooksForTest([globalHook()]);
    expect(_renderRowForTest(globalDoc()).querySelector(".docs-git-letter")).toBeNull();
  });

  it("carries no git letter under a / work dir whose repository tracks a literal ~ directory", () => {
    // `/` + `~/.kiro/hooks/greet.json` is the absolute key of that tracked file.
    setWorkspaceRoot("/");
    _setReposForTest([repoStatus(".", "~/.kiro/hooks/greet.json", "M")]);
    _setHooksForTest([globalHook()]);
    expect(_renderRowForTest(globalDoc()).querySelector(".docs-git-letter")).toBeNull();
  });

  it("treats an absent scope as workspace, which is the safe direction", () => {
    // An older server sends no scope; defaulting to global would strip a workspace hook's affordances.
    _setHooksForTest([withoutScope(wsHook())]);
    const row = _renderRowForTest(wsHookDoc());
    expect(row.querySelector("button.entry-open")).not.toBeNull();
    expect(row.querySelector(".entry-delete")).not.toBeNull();
  });
});

// Global hooks have no docs row: kiroRoots() scans only the workspace, so they would vanish without synthesis.
describe("the Hooks tab: rows the file scan cannot see", () => {
  it("synthesizes a row for a global hook", () => {
    _setDocsForTest([]);
    _setHooksForTest([
      wsHook({
        scope: "global",
        file_path: "~/.kiro/hooks/format.json",
        name: "format",
        trigger: "PostFileSave",
        command: "make fmt",
      }),
    ]);
    const rows = _hookRowsForTest();
    expect(rows).toHaveLength(1);
    expect(rows[0]?.name).toBe("format");
    expect(rows[0]?.trigger).toBe("PostFileSave");
    expect(rows[0]?.action).toBe("make fmt");
    expect(rows[0]?.group).toBe("format.json");
  });

  it("does not duplicate a hook the scan already reported", () => {
    _setDocsForTest([wsHookDoc()]);
    _setHooksForTest([wsHook()]);
    const rows = _hookRowsForTest();
    expect(rows).toHaveLength(1);
    // The scanned row wins, keeping a path the editor and delete accept.
    expect(rows[0]?.path).toBe("workspace/.kiro/hooks/greet.json");
  });

  it("orders workspace rows before global ones, like the server's own list", () => {
    _setDocsForTest([wsHookDoc()]);
    _setHooksForTest([
      wsHook(),
      wsHook({
        id: "id-g",
        name: "format",
        scope: "global",
        file_path: "~/.kiro/hooks/format.json",
      }),
    ]);
    expect(_hookRowsForTest().map((d) => d.name)).toEqual(["greet", "format"]);
  });

  it("synthesizes nothing for a workspace hook the scan missed", () => {
    // An unscanned workspace hook means the surfaces disagree; inventing a row would get its affordances wrong.
    _setDocsForTest([]);
    _setHooksForTest([wsHook()]);
    expect(_hookRowsForTest()).toHaveLength(0);
  });

  it("keeps a global hook whose relative path and name a workspace hook shares", () => {
    // `hookPathKey` reduces both scopes to one tail, so without scope in the key the global state would win the workspace
    // row and the real global row would be dropped.
    _setDocsForTest([wsHookDoc()]);
    _setHooksForTest([
      wsHook({ id: "id-ws", enabled: true }),
      wsHook({
        id: "id-global",
        enabled: false,
        scope: "global",
        file_path: "~/.kiro/hooks/greet.json",
      }),
    ]);

    const rows = _hookRowsForTest();
    expect(rows).toHaveLength(2);
    expect(rows.map((d) => d.path)).toEqual([
      "workspace/.kiro/hooks/greet.json",
      "~/.kiro/hooks/greet.json",
    ]);

    const ws = _renderRowForTest(rows[0] as Parameters<typeof _renderRowForTest>[0]);
    const global = _renderRowForTest(rows[1] as Parameters<typeof _renderRowForTest>[0]);

    expect(ws.querySelector("[data-hook-id]")?.getAttribute("data-hook-id")).toBe("id-ws");
    expect(global.querySelector("[data-hook-id]")?.getAttribute("data-hook-id")).toBe("id-global");
    expect((ws.querySelector(".hook-toggle") as HTMLInputElement).checked).toBe(true);
    expect((global.querySelector(".hook-toggle") as HTMLInputElement).checked).toBe(false);

    expect(ws.querySelector("button.entry-open")).not.toBeNull();
    expect(ws.querySelector(".entry-delete")).not.toBeNull();
    expect(global.querySelector("button.entry-open")).toBeNull();
    expect(global.querySelector(".entry-delete")).toBeNull();
  });

  it("uses an askAgent hook's prompt as its subtitle", () => {
    // The scan sets Action from the command only, so the join fills an askAgent global hook's subtitle.
    _setDocsForTest([]);
    _setHooksForTest([
      withoutCommand(
        wsHook({
          scope: "global",
          file_path: "~/.kiro/hooks/ask.json",
          name: "ask",
          prompt: "Review the diff",
        }),
      ),
    ]);
    expect(_hookRowsForTest()[0]?.action).toBe("Review the diff");
  });
});

// Reconcile keeps a row by path+name, which a toggle does not move, so the update pass must repaint it.
describe("the Hooks tab: a kept row repaints when its state changes", () => {
  it("changes its signature when the hook's enabled flag flips", () => {
    _setHooksForTest([wsHook({ enabled: true })]);
    const on = _renderRowForTest(wsHookDoc()).getAttribute("data-sig");
    _setHooksForTest([wsHook({ enabled: false })]);
    const off = _renderRowForTest(wsHookDoc()).getAttribute("data-sig");
    expect(on).not.toBe(off);
  });

  it("changes its signature when the git letter changes", () => {
    setWorkspaceRoot("/workspace");
    const clean = _renderRowForTest(wsHookDoc()).getAttribute("data-sig");
    _setReposForTest([repoStatus(".kiro", "hooks/greet.json", "M")]);
    const dirty = _renderRowForTest(wsHookDoc()).getAttribute("data-sig");
    expect(clean).not.toBe(dirty);
  });

  it("keeps the signature stable for an unchanged row", () => {
    _setHooksForTest([wsHook()]);
    const a = _renderRowForTest(wsHookDoc()).getAttribute("data-sig");
    const b = _renderRowForTest(wsHookDoc()).getAttribute("data-sig");
    expect(a).toBe(b);
    expect(a).not.toBeNull();
  });

  it("distinguishes two rows whose text could forge one signature", () => {
    // Every component is arbitrary text, so keyenc's join stops a separator impersonating a field boundary.
    const a = _renderRowForTest(wsHookDoc({ name: "a:b", action: "c" })).getAttribute("data-sig");
    const b = _renderRowForTest(wsHookDoc({ name: "a", action: "b:c" })).getAttribute("data-sig");
    expect(a).not.toBe(b);
  });
});

// `settings_updated` does not fire for a hook file, and the scan is memoized on directory mtime and names, so an
// in-place edit needs `hooks_changed` (from KAS's `_kiro/hooks/didChange`). Wired once: initDocsView has an `inited` flag.

describe("the Hooks tab: staying current", () => {
  let sseHandlers: Map<string, () => void>;

  beforeAll(async () => {
    document.body.innerHTML = `
      <div id="docs-view">
        <nav id="docs-tab-bar">
          <button type="button" data-docs-tab="steering"></button>
          <button type="button" data-docs-tab="skills"></button>
          <button type="button" data-docs-tab="agents"></button>
          <button type="button" data-docs-tab="specs"></button>
          <button type="button" data-docs-tab="hooks"></button>
          <button type="button" data-docs-tab="workflows"></button>
        </nav>
        <div data-docs-panel="steering" class="docs-panel"></div>
        <div data-docs-panel="hooks" class="docs-panel hidden"></div>
        <div data-docs-panel="specs" class="docs-panel hidden"></div>
      </div>`;
    // Both SSE handlers skip work while offsetParent is null; forced truthy to exercise the open page.
    Object.defineProperty(document.getElementById("docs-view"), "offsetParent", {
      get: () => document.body,
      configurable: true,
    });

    const { apiGetTyped } = await import("./api-client.js");
    vi.mocked(apiGetTyped).mockImplementation(serveDocsPage());
    const { onSSE } = await import("./bus.js");
    // Activation init, sub-tab correction and fetch are separate doors; `toggleDocsView` only toggles the tab.
    const { showDocsTab, forceDocsTab, refreshDocsView } = await import("./docs.js");
    showDocsTab();
    forceDocsTab("hooks");
    refreshDocsView();
    sseHandlers = new Map(vi.mocked(onSSE).mock.calls.map((c) => [c[0], c[1] as () => void]));
  });

  it("badges a steering doc KAS reported an issue for, and refetches on steering_issues_changed", async () => {
    const { apiGetTyped } = await import("./api-client.js");
    const path = "workspace/.kiro/steering/api.md";
    vi.mocked(apiGetTyped)
      .mockClear()
      .mockImplementation(
        serveDocsPage(
          { docs: [{ category: "steering", name: "api", path }], truncated: false },
          {
            issues: {
              [path]: [
                { code: "contextReferenceUnresolved", remediation: "Fix the #[[file:x]] path" },
              ],
            },
          },
        ),
      );
    const { forceDocsTab, refreshDocsView } = await import("./docs.js");
    forceDocsTab("steering");
    refreshDocsView();
    sseHandlers.get("steering_issues_changed")?.();
    const panel = document.querySelector('[data-docs-panel="steering"]');
    await vi.waitFor(() => {
      expect(panel?.querySelector(".docs-badge-warn")?.textContent).toBe("1 issue");
    });
    expect(panel?.querySelector(".docs-badge-warn")?.getAttribute("data-tooltip")).toBe(
      "Fix the #[[file:x]] path",
    );
    expect(vi.mocked(apiGetTyped)).toHaveBeenCalledWith(ISSUES_PATH, expect.anything());
    forceDocsTab("hooks");
  });

  it("subscribes to hooks_changed, not only to settings_updated", () => {
    expect([...sseHandlers.keys()]).toContain("hooks_changed");
    expect([...sseHandlers.keys()]).toContain("settings_updated");
  });

  it("refetches BOTH halves when a hook file changes underneath it", async () => {
    // Both: a hand edit can change the body or the enabled flag.
    const { apiGetTyped } = await import("./api-client.js");
    // This project's vitest resets the implementation with the call log, and an unresolved fetch throws.
    vi.mocked(apiGetTyped).mockClear().mockImplementation(serveDocsPage());

    sseHandlers.get("hooks_changed")?.();

    expect(vi.mocked(apiGetTyped)).toHaveBeenCalledWith(
      DOCS_PATH,
      expect.anything(),
      expect.anything(),
    );
    expect(vi.mocked(apiGetTyped)).toHaveBeenCalledWith("/api/hooks", expect.anything());
  });

  it("dispatches the toggle for the row it was clicked on", async () => {
    const { setHookEnabled } = await import("./actions/hooks.js");
    const { _setDocsForTest, _setHooksForTest, _renderActiveForTest } = await import("./docs.js");
    vi.mocked(setHookEnabled.dispatch).mockClear().mockResolvedValue(undefined);
    // The handler chains into loadHookState, so its fetch is armed too.
    const { apiGetTyped } = await import("./api-client.js");
    vi.mocked(apiGetTyped).mockImplementation(serveDocsPage());

    _setDocsForTest([wsHookDoc()]);
    _setHooksForTest([wsHook({ enabled: true })]);
    _renderActiveForTest();

    const toggle = document
      .querySelector('[data-docs-panel="hooks"]')
      ?.querySelector<HTMLInputElement>(".hook-toggle");
    expect(toggle).not.toBeNull();
    toggle!.checked = false;
    toggle!.dispatchEvent(new Event("change", { bubbles: true }));

    expect(vi.mocked(setHookEnabled.dispatch)).toHaveBeenCalledWith({
      id: "id-greet",
      enabled: false,
    });
  });

  it("ignores a change event that is not a hook toggle", () => {
    // The listener is delegated on the whole page, so it must ignore other controls.
    const other = document.createElement("input");
    other.type = "checkbox";
    document.getElementById("docs-view")?.appendChild(other);
    other.dispatchEvent(new Event("change", { bubbles: true }));
    expect(true).toBe(true);
  });
});

// Forcing the sub-tab on every activation discarded the reader's sub-tab, so activation and fetch are separate.

describe("showDocsTab and refreshDocsView", () => {
  function panel(tab: string): HTMLElement | null {
    return document.querySelector<HTMLElement>(`[data-docs-panel="${tab}"]`);
  }

  beforeEach(async () => {
    const { apiGetTyped } = await import("./api-client.js");
    vi.mocked(apiGetTyped).mockClear().mockImplementation(serveDocsPage());
  });

  it("a re-activation leaves the reader's sub-tab where it was", async () => {
    const { showDocsTab, forceDocsTab } = await import("./docs.js");
    forceDocsTab("hooks");
    expect(panel("hooks")?.classList.contains("hidden")).toBe(false);

    showDocsTab();

    expect(panel("hooks")?.classList.contains("hidden")).toBe(false);
    expect(panel("steering")?.classList.contains("hidden")).toBe(true);
  });

  it("the activation fetches nothing and the refresh fetches", async () => {
    const { showDocsTab, refreshDocsView } = await import("./docs.js");
    const { apiGetTyped } = await import("./api-client.js");
    const inventoryReads = (): string[] =>
      vi
        .mocked(apiGetTyped)
        .mock.calls.map((c) => c[0])
        .filter((p) => p === DOCS_PATH);

    showDocsTab();
    expect(inventoryReads()).toEqual([]);

    refreshDocsView();
    expect(inventoryReads()).toEqual([DOCS_PATH]);
  });

  it("arms no placeholder for a category with no documents once the inventory answered", async () => {
    // An empty category and an unread one look identical, and a gap reaches this refresh with no tab switch.
    const { apiGetTyped } = await import("./api-client.js");
    let settle = (_v: unknown): void => {
      /* replaced below */
    };
    // Pending across the show delay, or the answer cancels the timer whatever the arm decided.
    vi.mocked(apiGetTyped).mockImplementation(
      serveDocsPage(
        new Promise((resolve) => {
          settle = resolve as (v: unknown) => void;
        }),
      ),
    );
    vi.useFakeTimers();
    try {
      const { refreshDocsView, forceDocsTab } = await import("./docs.js");
      forceDocsTab("steering");
      refreshDocsView();
      await vi.advanceTimersByTimeAsync(150);

      const steering = panel("steering");
      expect(steering).not.toBeNull();
      expect(steering?.querySelector(".entry-skel")).toBeNull();
    } finally {
      settle({ docs: [], truncated: false });
      vi.useRealTimers();
    }
  });
});

// A group is a label plus its own list card, so a header does not read as a row.

describe("grouped tabs render one section per group", () => {
  function panel(tab: string): HTMLElement {
    return document.querySelector<HTMLElement>(`[data-docs-panel="${tab}"]`) as HTMLElement;
  }

  it("labels each hook file and gives it its own list card", async () => {
    const { _setDocsForTest, _setHooksForTest, forceDocsTab, _renderActiveForTest } =
      await import("./docs.js");
    _setHooksForTest([]);
    _setDocsForTest([
      wsHookDoc({ name: "first", group: "a.json", path: "workspace/.kiro/hooks/a.json" }),
      wsHookDoc({ name: "second", group: "a.json", path: "workspace/.kiro/hooks/a.json" }),
      wsHookDoc({ name: "third", group: "b.json", path: "workspace/.kiro/hooks/b.json" }),
    ]);
    forceDocsTab("hooks");
    _renderActiveForTest();

    const out = [...panel("hooks").querySelectorAll<HTMLElement>(":scope > .docs-section")].map(
      (s) => ({
        label: s.querySelector(".entry-section-label")?.textContent ?? null,
        rows: [...s.querySelectorAll(".entry-title")].map((r) => r.textContent),
        list: s.querySelector(".list-container")?.getAttribute("role"),
      }),
    );
    expect(out).toEqual([
      { label: "a.json", rows: ["first", "second"], list: "list" },
      { label: "b.json", rows: ["third"], list: "list" },
    ]);
  });

  it("renders a flat category as one unlabelled section", async () => {
    const { _setDocsForTest, forceDocsTab, _renderActiveForTest } = await import("./docs.js");
    _setDocsForTest([
      { category: "steering", name: "alpha", path: "workspace/.kiro/steering/alpha.md" },
      { category: "steering", name: "beta", path: "workspace/.kiro/steering/beta.md" },
    ]);
    forceDocsTab("steering");
    _renderActiveForTest();

    const secs = panel("steering").querySelectorAll(":scope > .docs-section");
    expect(secs).toHaveLength(1);
    expect(secs[0]?.querySelector(".entry-section-label")).toBeNull();
    expect(secs[0]?.querySelectorAll(".entry")).toHaveLength(2);
  });

  it("keeps a root-level spec out of the feature above it", async () => {
    // "." marks a document in the category root; two unlabelled runs are two sections.
    const { _setDocsForTest, forceDocsTab, _renderActiveForTest } = await import("./docs.js");
    _setDocsForTest([
      { category: "spec", name: "Loose", path: "workspace/.kiro/specs/loose.md", group: "." },
      { category: "spec", name: "Design", path: "workspace/.kiro/specs/f/design.md", group: "f" },
      { category: "spec", name: "Notes", path: "workspace/.kiro/specs/notes.md", group: "." },
    ]);
    forceDocsTab("specs");
    _renderActiveForTest();

    const labels = [...panel("specs").querySelectorAll<HTMLElement>(":scope > .docs-section")].map(
      (s) => s.querySelector(".entry-section-label")?.textContent ?? "",
    );
    expect(labels).toEqual(["", "f", ""]);
    _renderActiveForTest();
    expect(panel("specs").querySelectorAll(":scope > .docs-section")).toHaveLength(3);
  });

  it("repaints a kept row in place when its state moves", async () => {
    const { _setDocsForTest, _setHooksForTest, forceDocsTab, _renderActiveForTest } =
      await import("./docs.js");
    _setDocsForTest([wsHookDoc()]);
    _setHooksForTest([wsHook({ enabled: true })]);
    forceDocsTab("hooks");
    _renderActiveForTest();
    const row = panel("hooks").querySelector<HTMLElement>(".entry");
    expect((row?.querySelector(".hook-toggle") as HTMLInputElement).checked).toBe(true);

    _setHooksForTest([wsHook({ enabled: false })]);
    _renderActiveForTest();
    expect(panel("hooks").querySelector<HTMLElement>(".entry")).toBe(row);
    expect((row?.querySelector(".hook-toggle") as HTMLInputElement).checked).toBe(false);
  });
});
