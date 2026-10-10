// The composer menu's pure rules plus composer drives: what is listed, what a query matches, what
// is greyed mid-turn, and that a pick fills the composer, for the `/` commands and the `#` context
// references alike.
import { describe, it, expect, beforeEach, afterEach, onTestFinished, vi } from "vitest";
import type * as ApiClient from "./api-client.js";
import type * as Store from "./store.js";
import type * as NoticeSubject from "./notice-subject.js";

const { mockApiGetTyped, mockGetActiveId, mockOpenFilePicker, mockChatNotice, liveName } =
  vi.hoisted(() => ({
    mockApiGetTyped: vi.fn((_path: string): Promise<unknown> => Promise.resolve(null)),
    mockGetActiveId: vi.fn(() => ""),
    mockOpenFilePicker: vi.fn(),
    mockChatNotice: vi.fn(),
    liveName: { value: "Fix the parser" },
  }));
vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGetTyped: mockApiGetTyped,
}));
vi.mock("./notice-subject.js", async (importOriginal) => ({
  ...(await importOriginal<typeof NoticeSubject>()),
  chatNotice: mockChatNotice,
  subjectName: (id: string) => (id === "" ? "" : liveName.value),
}));
vi.mock("./store.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Store>()),
  getActiveId: mockGetActiveId,
}));

import {
  BUSY_REASON,
  _resetCatalogForTest,
  _setCatalogForTest,
  entryEnabled,
  initSlashMenu,
  invokesCatalogCommand,
  matchEntries,
  menuEntries,
  slashQuery,
} from "./slash-menu.js";
import type { FileMatch, FileSearchResult, SlashCommand } from "./wire/types.gen.js";
import { dispatch } from "./bus.js";
import { setActive } from "./store.js";
import { _setReposForTest } from "./git-status-store.js";
import { _resetForTest as resetAsk } from "@cplieger/ui-primitives/ask";
import { _resetForTest as resetWorkspace, setWorkspaceRoot } from "./workspace.js";

const CATALOG: SlashCommand[] = [
  { name: "review", description: "Review the diff", kind: "prompt", hint: "<path> [focus]" },
  { name: "api-style", description: "API conventions", kind: "steering", hint: "optional context" },
  { name: "goal", description: "Set a goal", kind: "goal" },
  { name: "compact", description: "a prompt named compact", kind: "prompt" },
];

function fileHit(path: string, kind: FileMatch["kind"] = "name"): FileMatch {
  return { path, excerpt: "", kind, ranges: [], line: 0 };
}

function searchReply(matches: FileMatch[]): FileSearchResult {
  return {
    matches,
    scanned: matches.length,
    matched: matches.length,
    truncated: false,
    root_ignored: false,
  };
}

describe("menuEntries", () => {
  it("lists marotte's verbs, then the catalog with a verb-named entry hidden", () => {
    const names = menuEntries(CATALOG).map((e) => `${e.kind}:${e.name}`);
    expect(names).toEqual([
      "marotte:compact",
      "marotte:drop",
      "marotte:memories",
      "marotte:rewind",
      "marotte:tangent",
      "marotte:tangent merge",
      "prompt:review",
      "steering:api-style",
      "goal:goal",
    ]);
  });

  it("offers no /context", () => {
    expect(menuEntries(CATALOG).some((e) => e.name === "context")).toBe(false);
  });
});

describe("slashQuery", () => {
  it("answers the partial name for a lone leading slash at the caret", () => {
    expect(slashQuery("/", 1)).toBe("");
    expect(slashQuery("/rev", 4)).toBe("rev");
  });

  it("closes once the name is followed by anything, or the caret is not at the end", () => {
    expect(slashQuery("/review x", 9)).toBeNull();
    expect(slashQuery("/review ", 8)).toBeNull();
    expect(slashQuery("/rev", 2)).toBeNull();
    expect(slashQuery("hi /rev", 7)).toBeNull();
  });
});

describe("matchEntries", () => {
  it("puts prefix matches ahead of substring ones", () => {
    const got = matchEntries("t", menuEntries(CATALOG)).map((e) => e.name);
    expect(got).toEqual(["tangent", "tangent merge", "compact", "api-style"]);
  });
});

describe("entryEnabled", () => {
  it("greys only server-resolved rows while a turn runs", () => {
    const [verb, , , , , , prompt] = menuEntries(CATALOG);
    expect(verb && entryEnabled(verb, true)).toBe(true);
    expect(prompt && entryEnabled(prompt, true)).toBe(false);
    expect(prompt && entryEnabled(prompt, false)).toBe(true);
  });
});

describe("invokesCatalogCommand", () => {
  beforeEach(() => {
    _setCatalogForTest(CATALOG);
  });
  afterEach(() => {
    _setCatalogForTest([]);
  });

  it("matches a catalog name with or without arguments, never a verb", () => {
    expect(invokesCatalogCommand("/review src/a.ts")).toBe(true);
    expect(invokesCatalogCommand("/API-STYLE")).toBe(true);
    expect(invokesCatalogCommand("/compact")).toBe(false);
    expect(invokesCatalogCommand("/unknown")).toBe(false);
    expect(invokesCatalogCommand("review")).toBe(false);
  });
});

describe("invokesCatalogCommand before a catalog has loaded", () => {
  beforeEach(() => {
    _resetCatalogForTest();
    mockApiGetTyped.mockReset();
    document.body.replaceChildren();
    const menu = document.createElement("ul");
    menu.id = "slash-menu";
    menu.hidden = true;
    const input = document.createElement("textarea");
    input.id = "prompt-input";
    document.body.append(menu, input);
  });
  afterEach(() => {
    _setCatalogForTest([]);
  });

  it("holds any non-verb command while the first read is still pending", () => {
    expect(invokesCatalogCommand("/review src/a.ts")).toBe(true);
    expect(invokesCatalogCommand("/compact")).toBe(false);
    expect(invokesCatalogCommand("/tmp/log is empty")).toBe(false);
    expect(invokesCatalogCommand("review")).toBe(false);
  });

  it("keeps holding after the first read fails", async () => {
    mockApiGetTyped.mockResolvedValue(null);
    initSlashMenu(mockOpenFilePicker);
    await vi.waitFor(() => {
      expect(mockApiGetTyped).toHaveBeenCalled();
    });
    await Promise.resolve();
    expect(invokesCatalogCommand("/review src/a.ts")).toBe(true);
  });

  it("stops holding unknown names once a read succeeds", async () => {
    mockApiGetTyped.mockResolvedValue({ commands: CATALOG, ready: true });
    initSlashMenu(mockOpenFilePicker);
    await vi.waitFor(() => {
      expect(invokesCatalogCommand("/unknown")).toBe(false);
    });
    expect(invokesCatalogCommand("/review src/a.ts")).toBe(true);
  });

  it("keeps holding when the server has no catalog from KAS yet", async () => {
    mockApiGetTyped.mockResolvedValue({ commands: [], ready: false });
    initSlashMenu(mockOpenFilePicker);
    await vi.waitFor(() => {
      expect(mockApiGetTyped).toHaveBeenCalled();
    });
    await Promise.resolve();
    expect(invokesCatalogCommand("/review src/a.ts")).toBe(true);
  });

  it("releases unknown names when KAS reported an empty catalog", async () => {
    mockApiGetTyped.mockResolvedValue({ commands: [], ready: true });
    initSlashMenu(mockOpenFilePicker);
    await vi.waitFor(() => {
      expect(invokesCatalogCommand("/review src/a.ts")).toBe(false);
    });
  });
});

describe("the listbox", () => {
  let input: HTMLTextAreaElement;
  let menu: HTMLUListElement;

  beforeEach(() => {
    document.body.replaceChildren();
    menu = document.createElement("ul");
    menu.id = "slash-menu";
    menu.hidden = true;
    input = document.createElement("textarea");
    input.id = "prompt-input";
    document.body.append(menu, input);
    initSlashMenu(mockOpenFilePicker);
    _setCatalogForTest(CATALOG);
  });

  function type(v: string): void {
    input.value = v;
    input.setSelectionRange(v.length, v.length);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  }

  it("opens on a slash, shows the hint, and Enter inserts the name", () => {
    type("/revi");
    expect(menu.hidden).toBe(false);
    expect(menu.querySelector(".slash-opt-hint")?.textContent).toBe("<path> [focus]");
    const ev = new KeyboardEvent("keydown", { key: "Enter", cancelable: true });
    input.dispatchEvent(ev);
    expect(ev.defaultPrevented).toBe(true);
    expect(input.value).toBe("/review ");
    expect(menu.hidden).toBe(true);
  });

  it("Escape closes without touching the text", () => {
    type("/");
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", cancelable: true }));
    expect(menu.hidden).toBe(true);
    expect(input.value).toBe("/");
  });

  it("says why a row is greyed mid-turn", () => {
    expect(BUSY_REASON).toBe("Runs as a new turn, available when the agent is idle");
  });
});

describe("the # context menu", () => {
  let input: HTMLTextAreaElement;
  let menu: HTMLUListElement;

  beforeEach(() => {
    document.body.replaceChildren();
    menu = document.createElement("ul");
    menu.id = "slash-menu";
    menu.hidden = true;
    input = document.createElement("textarea");
    input.id = "prompt-input";
    document.body.append(menu, input);
    initSlashMenu(mockOpenFilePicker);
    setWorkspaceRoot("/workspace");
    mockApiGetTyped.mockReset();
    mockApiGetTyped.mockImplementation(() => Promise.resolve(null));
    mockGetActiveId.mockReturnValue("c-1");
    mockOpenFilePicker.mockClear();
  });

  afterEach(() => {
    // Closing drops the menu's item cache, which outlives a test otherwise.
    input.dispatchEvent(new Event("blur"));
    resetWorkspace();
    _setReposForTest([]);
    // The shared ask dialog is cached by the library, so a bare remove() would leave the next
    // test's ask writing into a detached element.
    resetAsk();
  });

  function type(v: string): void {
    input.value = v;
    input.setSelectionRange(v.length, v.length);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  }

  function press(key: string): void {
    input.dispatchEvent(new KeyboardEvent("keydown", { key, cancelable: true }));
  }

  function rowNames(): string[] {
    return [...menu.querySelectorAll(".slash-opt-name")].map((e) => e.textContent);
  }

  it("lists the providers after a # and turns a pick into its prefix", () => {
    type("see #");
    expect(menu.getAttribute("aria-label")).toBe("Context");
    expect(rowNames()).toEqual([
      "File",
      "Folder",
      "Attach file…",
      "Git diff",
      "Terminal",
      "Spec",
      "Steering",
      "MCP resource",
    ]);
    press("Enter");
    expect(input.value).toBe("see #file:");
    expect(menu.hidden).toBe(false);
  });

  it("writes the terminal's default reference, and a typed line count capped at the server's limit", async () => {
    type("why #terminal:");
    await vi.waitFor(() => {
      expect(rowNames()).toEqual(["Last 200 lines"]);
    });
    press("Enter");
    expect(input.value).toBe("why #[[terminal:]] ");

    type("why #terminal:9000");
    await vi.waitFor(() => {
      expect(rowNames()).toEqual(["Last 5000 lines"]);
    });
    press("Enter");
    expect(input.value).toBe("why #[[terminal:5000]] ");
  });

  it("offers no terminal row for a line count that is not a whole number", async () => {
    type("#terminal:abc");
    await vi.waitFor(() => {
      expect(menu.textContent).toContain("No matches");
    });
  });

  it("writes a git repository as a token the server resolves", async () => {
    _setReposForTest([
      {
        repo: "marotte",
        is_repo: true,
        branch: "main",
        ahead: 0,
        behind: 0,
        files: [],
        has_dirty: false,
        stashes: 0,
      },
    ]);
    type("diff #git:mar");
    await vi.waitFor(() => {
      expect(rowNames()).toEqual(["marotte"]);
    });
    press("Enter");
    expect(input.value).toBe("diff #[[git:marotte]] ");
    expect(menu.hidden).toBe(true);
  });

  it("finds a file by name and keeps the typed line range", async () => {
    mockApiGetTyped.mockImplementation((path: string) =>
      Promise.resolve(
        path.startsWith("/api/files/search")
          ? searchReply([fileHit("/workspace/src/a.go"), fileHit("/workspace/src", "dir")])
          : null,
      ),
    );
    type("#file:a.go:3-4");
    await vi.waitFor(() => {
      expect(rowNames()).toEqual(["src/a.go"]);
    });
    press("Enter");
    expect(input.value).toBe("#[[file:src/a.go:3-4]] ");
  });

  it("asks for the typed fragment as one literal, so a bracket in a name is not a pattern", async () => {
    mockApiGetTyped.mockImplementation(() => Promise.resolve(searchReply([])));
    type("#file:a[1");
    await vi.waitFor(() => {
      expect(mockApiGetTyped.mock.calls.some(([p]) => p.startsWith("/api/files/search"))).toBe(
        true,
      );
    });
    const [url] = mockApiGetTyped.mock.calls.find(([p]) => p.startsWith("/api/files/search")) ?? [
      "",
    ];
    expect(new URL(url, "http://h").searchParams.get("q")).toBe('"a[1"');
  });

  it("escapes a quote and a backslash in the fragment, so a name holding either stays one literal", async () => {
    mockApiGetTyped.mockImplementation(() => Promise.resolve(searchReply([])));
    type('#file:a"[b\\c');
    await vi.waitFor(() => {
      expect(mockApiGetTyped.mock.calls.some(([p]) => p.startsWith("/api/files/search"))).toBe(
        true,
      );
    });
    const [url] = mockApiGetTyped.mock.calls.find(([p]) => p.startsWith("/api/files/search")) ?? [
      "",
    ];
    expect(new URL(url, "http://h").searchParams.get("q")).toBe('"a\\"[b\\\\c"');
  });

  it("offers a gitignored work file, asking past the ignore rules but not into node_modules", async () => {
    mockApiGetTyped.mockImplementation((path: string) =>
      Promise.resolve(
        path.startsWith("/api/files/search")
          ? searchReply([fileHit("/workspace/.agents/tasks/plan.md")])
          : null,
      ),
    );
    type("#file:plan");
    await vi.waitFor(() => {
      expect(rowNames()).toEqual([".agents/tasks/plan.md"]);
    });
    const [url] = mockApiGetTyped.mock.calls.find(([p]) => p.startsWith("/api/files/search")) ?? [
      "",
    ];
    const params = new URL(url, "http://h").searchParams;
    expect(params.get("ignored")).toBe("1");
    expect(params.get("files")).toBe("!node_modules");
  });

  it("writes a whole-file token for a name that itself ends like a line range", async () => {
    mockApiGetTyped.mockImplementation((path: string) =>
      Promise.resolve(
        path.startsWith("/api/files/search")
          ? searchReply([fileHit("/workspace/report:1-2")])
          : null,
      ),
    );
    type("#file:report");
    await vi.waitFor(() => {
      expect(rowNames()).toEqual(["report:1-2"]);
    });
    press("Enter");
    expect(input.value).toBe("#[[file:report:1-2:]] ");
  });

  it("lists steering documents as workspace-relative paths", async () => {
    mockApiGetTyped.mockImplementation((path: string) =>
      Promise.resolve(
        path === "/api/workspace/kiro-docs"
          ? {
              docs: [
                { category: "steering", name: "Go", path: "workspace/.kiro/steering/go.md" },
                { category: "skills", name: "Skill", path: "workspace/.kiro/skills/x/SKILL.md" },
              ],
              truncated: false,
            }
          : null,
      ),
    );
    type("#steering:");
    await vi.waitFor(() => {
      expect(rowNames()).toEqual(["Go"]);
    });
    press("Enter");
    expect(input.value).toBe("#[[steering:.kiro/steering/go.md]] ");
  });

  function servePool(servers: unknown[], chat = "c-1"): void {
    mockApiGetTyped.mockImplementation((path: string) =>
      Promise.resolve(path === `/api/mcp/pool?chat_id=${chat}` ? servers : null),
    );
  }

  function poolServer(name: string, resources: unknown[], templates: unknown[] = []): unknown {
    return { name, resources, resource_templates: templates };
  }

  it("lists the active chat's own pool, so a server only another chat holds is absent", async () => {
    servePool([poolServer("gh", [{ name: "readme", uri: "gh://readme" }])]);
    type("#mcp:");
    await vi.waitFor(() => {
      expect(rowNames()).toEqual(["readme"]);
    });
    expect(mockApiGetTyped.mock.calls.map(([p]) => p)).toEqual(["/api/mcp/pool?chat_id=c-1"]);
    press("Enter");
    expect(input.value).toBe("#[[mcp:gh:gh://readme]] ");
  });

  it("lists no MCP resource when no chat is active, since no pool exists to resolve one", async () => {
    mockGetActiveId.mockReturnValue("");
    type("#mcp:");
    await vi.waitFor(() => {
      expect(menu.textContent).toContain("No matches");
    });
    expect(mockApiGetTyped).not.toHaveBeenCalled();
  });

  it("fills an MCP template's variables before writing its token", async () => {
    servePool([poolServer("gh", [], [{ name: "issue", uri_template: "gh://issues/{number}" }])]);
    type("#mcp:iss");
    await vi.waitFor(() => {
      expect(rowNames()).toEqual(["issue"]);
    });
    press("Enter");
    const field = await vi.waitFor(() => {
      const f = document.querySelector<HTMLInputElement>(".uip-ask-input");
      expect(f).not.toBeNull();
      return f as HTMLInputElement;
    });
    field.value = "42";
    document.querySelector<HTMLButtonElement>(".uip-ask-ok")?.click();
    await vi.waitFor(() => {
      expect(input.value).toBe("#[[mcp:gh:gh://issues/42]] ");
    });
  });

  it("refuses a filled template whose address the token grammar cannot carry", async () => {
    // No variables, so no dialog: the expansion is the literal address.
    servePool([poolServer("loc", [], [{ name: "v6", uri_template: "http://[::1]/x" }])]);
    mockChatNotice.mockClear();
    mockGetActiveId.mockReturnValue("c-1");
    type("#mcp:v6");
    await vi.waitFor(() => {
      expect(rowNames()).toEqual(["v6"]);
    });
    press("Enter");
    await vi.waitFor(() => {
      expect(mockChatNotice).toHaveBeenCalledWith(
        "c-1",
        "That address contains a ] or a line break, so it cannot be added as a context reference.",
        "error",
        "Fix the parser",
      );
    });
    expect(input.value).toBe("#mcp:v6");
  });

  it("names the chat a refused template was for as it was named when the fill began", async () => {
    servePool([poolServer("loc", [], [{ name: "raw", uri_template: "x://{+v}" }])]);
    mockChatNotice.mockClear();
    mockGetActiveId.mockReturnValue("c-1");
    liveName.value = "Fix the parser";
    type("#mcp:raw");
    await vi.waitFor(() => {
      expect(rowNames()).toEqual(["raw"]);
    });
    press("Enter");
    const field = await vi.waitFor(() => {
      const f = document.querySelector<HTMLInputElement>(".uip-ask-input");
      expect(f).not.toBeNull();
      return f as HTMLInputElement;
    });
    liveName.value = "Renamed meanwhile";
    field.value = "a]b";
    document.querySelector<HTMLButtonElement>(".uip-ask-ok")?.click();
    await vi.waitFor(() => {
      expect(mockChatNotice).toHaveBeenCalledWith(
        "c-1",
        "That address contains a ] or a line break, so it cannot be added as a context reference.",
        "error",
        "Fix the parser",
      );
    });
    liveName.value = "Fix the parser";
  });

  it("does not list a template RFC 6570 cannot expand", async () => {
    servePool([
      poolServer(
        "gh",
        [],
        [
          { name: "bad", uri_template: "gh://{=x}" },
          { name: "good", uri_template: "gh://{x}" },
        ],
      ),
    ]);
    type("#mcp:");
    await vi.waitFor(() => {
      expect(rowNames()).toEqual(["good"]);
    });
  });

  it("re-reads an open #mcp: list when the chat's pool changes", async () => {
    servePool([poolServer("gh", [{ name: "readme", uri: "gh://readme" }])]);
    type("#mcp:");
    await vi.waitFor(() => {
      expect(rowNames()).toEqual(["readme"]);
    });
    servePool([poolServer("wiki", [{ name: "home", uri: "wiki://home" }])]);
    dispatch({ type: "mcp_pool_changed" });
    await vi.waitFor(() => {
      expect(rowNames()).toEqual(["home"]);
    });
  });

  it("keeps a pool read that answers after the pool changed out of the list", async () => {
    const answers: ((v: unknown) => void)[] = [];
    mockApiGetTyped.mockImplementation(
      () =>
        new Promise((resolve) => {
          answers.push(resolve);
        }),
    );
    type("#mcp:");
    await vi.waitFor(() => {
      expect(answers).toHaveLength(1);
    });
    dispatch({ type: "mcp_pool_changed" });
    await vi.waitFor(() => {
      expect(answers.length).toBeGreaterThan(1);
    });
    answers.at(-1)?.([poolServer("wiki", [{ name: "home", uri: "wiki://home" }])]);
    await vi.waitFor(() => {
      expect(rowNames()).toEqual(["home"]);
    });
    answers[0]?.([poolServer("gh", [{ name: "readme", uri: "gh://readme" }])]);
    await new Promise((r) => setTimeout(r, 0));
    expect(rowNames()).toEqual(["home"]);
  });

  it("lists the newly active chat's pool while #mcp: stays open", async () => {
    onTestFinished(() => {
      setActive("");
    });
    setActive("c-1");
    servePool([poolServer("gh", [{ name: "readme", uri: "gh://readme" }])]);
    type("#mcp:");
    await vi.waitFor(() => {
      expect(rowNames()).toEqual(["readme"]);
    });
    mockGetActiveId.mockReturnValue("c-2");
    servePool([poolServer("wiki", [{ name: "home", uri: "wiki://home" }])], "c-2");
    setActive("c-2");
    await vi.waitFor(() => {
      expect(rowNames()).toEqual(["home"]);
    });
  });

  function searchCalls(): number {
    return mockApiGetTyped.mock.calls.filter(([p]) => p.startsWith("/api/files/search")).length;
  }

  it("cancels a file search still waiting out its debounce when the menu closes", async () => {
    type("#file:a");
    input.dispatchEvent(new Event("blur"));
    await new Promise((r) => setTimeout(r, 300));
    expect(searchCalls()).toBe(0);
    expect(menu.hidden).toBe(true);
  });

  it("drops a search answered after the menu closed, and a reopened menu asks again", async () => {
    const answers: ((v: unknown) => void)[] = [];
    mockApiGetTyped.mockImplementation(
      (path: string) =>
        new Promise((resolve) => {
          if (path.startsWith("/api/files/search")) {
            answers.push(resolve);
          } else {
            resolve(null);
          }
        }),
    );
    const hit = searchReply([fileHit("/workspace/src/a.go")]);
    type("#file:a.go");
    await vi.waitFor(() => {
      expect(searchCalls()).toBe(1);
    });
    input.dispatchEvent(new Event("blur"));
    answers[0]?.(hit);
    await new Promise((r) => setTimeout(r, 0));
    expect(menu.hidden).toBe(true);

    type("#file:a.go");
    await vi.waitFor(() => {
      expect(searchCalls()).toBe(2);
    });
    expect(rowNames()).toEqual([]);
    answers[1]?.(hit);
    await vi.waitFor(() => {
      expect(rowNames()).toEqual(["src/a.go"]);
    });
  });

  function kiroDocsCalls(): number {
    return mockApiGetTyped.mock.calls.filter(([p]) => p === "/api/workspace/kiro-docs").length;
  }

  it("offers a retry instead of No matches when the steering list cannot load", async () => {
    type("#steering:");
    await vi.waitFor(() => {
      expect(rowNames()).toEqual(["Retry"]);
    });
    expect(menu.textContent).not.toContain("No matches");
    mockApiGetTyped.mockImplementation((path: string) =>
      Promise.resolve(
        path === "/api/workspace/kiro-docs"
          ? {
              docs: [{ category: "steering", name: "Go", path: "workspace/.kiro/steering/go.md" }],
              truncated: false,
            }
          : null,
      ),
    );
    press("Enter");
    await vi.waitFor(() => {
      expect(rowNames()).toEqual(["Go"]);
    });
    expect(kiroDocsCalls()).toBe(2);
  });

  it("offers a retry when the file search fails", async () => {
    type("#file:a.go");
    await vi.waitFor(() => {
      expect(rowNames()).toEqual(["Retry"]);
    });
    expect(searchCalls()).toBe(1);
    press("Enter");
    await vi.waitFor(() => {
      expect(searchCalls()).toBe(2);
    });
  });

  it("offers a retry when the chat's MCP pool cannot load", async () => {
    type("#mcp:");
    await vi.waitFor(() => {
      expect(rowNames()).toEqual(["Retry"]);
    });
  });

  it("opens the file picker for Attach file and leaves no # behind", () => {
    type("send #att");
    press("Enter");
    expect(mockOpenFilePicker).toHaveBeenCalledTimes(1);
    expect(input.value).toBe("send ");
    expect(menu.hidden).toBe(true);
  });
});
