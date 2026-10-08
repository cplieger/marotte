// Find in files: the request, the note's honesty, and the second-press escape hatch that justifies overriding Ctrl-F.
import { describe, it, expect, vi, beforeAll, afterAll, beforeEach, afterEach } from "vitest";
import type { FileMatch, FileSearchResult } from "./wire/types.gen.js";

/** A non-2xx answer: what the mocked transport resolves to when the server refuses the query. */
class HttpStatus {
  constructor(readonly status: number) {}
}

/** Typed as the reply so a case cannot build one the decoder refuses by accident. */
const apiGet = vi.fn<(url: string, signal?: AbortSignal) => Promise<unknown>>();
const openAtLine = vi.fn();
const openFileOrPage = vi.fn();
const activateBrowser = vi.fn();
const openFolder = vi.fn();
const shortcutsSheet = vi.fn();

vi.mock("./api-client.js", () => ({
  apiGet: (url: string, signal?: AbortSignal) => apiGet(url, signal),
  apiGetOrError: vi.fn(() => Promise.resolve({ ok: false, status: 0, data: null, error: "" })),
  CancellableSlot: class {
    private ctrl: AbortController | null = null;
    start(): AbortSignal {
      this.ctrl?.abort();
      this.ctrl = new AbortController();
      return this.ctrl.signal;
    }
    abort(): void {
      this.ctrl?.abort();
      this.ctrl = null;
    }
  },
  // Through the real generated decoder, so an unreadable reply fails as in production.
  apiGetTypedOrError: async (
    url: string,
    decoder: (v: unknown) => unknown,
    signal?: AbortSignal,
  ): Promise<unknown> => {
    const raw = await apiGet(url, signal);
    if (raw instanceof HttpStatus) {
      return { ok: false, status: raw.status, data: null, error: "refused" };
    }
    if (raw === null) {
      return { ok: false, status: 0, data: null, error: "network" };
    }
    try {
      return { ok: true, status: 200, data: decoder(raw), error: "" };
    } catch {
      return { ok: false, status: 0, data: null, error: "decode" };
    }
  },
  apiGetTyped: async (
    url: string,
    decoder: (v: unknown) => unknown,
    signal?: AbortSignal,
  ): Promise<unknown> => {
    const raw = await apiGet(url, signal);
    if (raw === null) {
      return null;
    }
    try {
      return decoder(raw);
    } catch {
      return null;
    }
  },
}));
vi.mock("./navigate.js", () => ({
  openAtLine: (path: string, line?: number) => openAtLine(path, line),
  openFileOrPage: (path: string, line?: number) => openFileOrPage(path, line),
}));
// ICON_CLOSE_UI is inert: search-shell.ts imports it.
vi.mock("./icons.js", () => ({
  fileIcon: () => "<svg></svg>",
  ICON_CLOSE_UI: "<svg></svg>",
  ICON_EYE_UI: "<svg></svg>",
  ICON_FILE_TEXT_UI: "<svg></svg>",
}));
vi.mock("./icon-el.js", () => ({ iconEl: () => document.createElement("span") }));
// The whole tab store: Browser Mode links ESM for real and tabs.ts drags a dozen modules behind it.
vi.mock("./tabs.js", async () => ({
  ...(await import("./__test-helpers__/tabs-mock.js")).tabsMock(),
  getActiveTabId: () => activeTabID,
  getActiveTabKind: () => activeTabKind,
}));
// keys.ts's only non-bus dependency, so its real listener can be installed.
vi.mock("./modals.js", () => ({ closeTopModal: () => false, openModal: vi.fn() }));

let activeTabID = "files-a";
let activeTabKind: string | null = "files";

const mod = await import("./files-search.js");
const { initKeyboardShortcuts } = await import("./keys.js");
const bus = await import("./bus.js");
const {
  searchURL,
  hitLabel,
  hitKey,
  initFilesSearch,
  openFilesSearch,
  closeFilesSearch,
  resetFilesSearch,
  handleFindInFilesHotkey,
  handleFilesTypeAhead,
  _isFilesSearchOpen,
  _filesSearchBar,
  _filesSearchResults,
} = mod;

function result(over: Partial<FileSearchResult> = {}): FileSearchResult {
  const matches = over.matches ?? [];
  return {
    matches,
    scanned: 0,
    matched: matches.length,
    truncated: false,
    root_ignored: false,
    ...over,
  };
}

function input(): HTMLInputElement {
  const el = document.getElementById("fb-search-input");
  if (!(el instanceof HTMLInputElement)) {
    throw new Error("search input not built");
  }
  return el;
}

function filesField(): HTMLInputElement {
  return document.getElementById("fb-search-files") as HTMLInputElement;
}

function toggle(label: string): HTMLButtonElement {
  const btn = _filesSearchBar()?.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`);
  if (btn === null || btn === undefined) {
    throw new Error(`no ${label} toggle`);
  }
  return btn;
}

const CONTENTS = "Search file contents";
const IGNORED = "Include ignored files";

function contentHit(path: string, line: number, excerpt = "x"): FileMatch {
  return { path, excerpt, kind: "content", line, ranges: [] };
}

function nameHit(
  path: string,
  kind: "name" | "dir" = "name",
  ranges: FileMatch["ranges"] = [],
): FileMatch {
  return { path, excerpt: "", kind, line: 0, ranges };
}

function ctrlF(): KeyboardEvent {
  return new KeyboardEvent("keydown", { key: "f", ctrlKey: true, cancelable: true });
}

async function settle(): Promise<void> {
  await vi.advanceTimersByTimeAsync(150);
  await Promise.resolve();
}

let searchPath = "workspace/src";

// Built once: the module keeps a reference to its lazily built bar, as against the real files view.
beforeAll(() => {
  document.body.innerHTML = `
    <div class="fb-list-wrap">
      <div class="fb-list" id="fb-list"></div>
    </div>
    <button type="button" id="find-btn" aria-pressed="false"></button>`;
  initFilesSearch({ getSearchPath: () => searchPath, activateBrowser, openFolder });
  // keys.ts's real document listener, so the `?` case measures the real interaction.
  initKeyboardShortcuts({
    newChat: vi.fn(),
    toggleShell: vi.fn(),
    toggleFiles: vi.fn(),
    toggleGit: vi.fn(),
    toggleSettings: vi.fn(),
    sendMessage: vi.fn(),
    showShortcuts: shortcutsSheet,
  });
  document.addEventListener("keydown", handleFilesTypeAhead);
});

afterAll(() => {
  document.body.innerHTML = "";
});

beforeEach(() => {
  vi.useFakeTimers();
  apiGet.mockReset();
  apiGet.mockResolvedValue(result());
  openAtLine.mockReset();
  openFileOrPage.mockReset();
  activateBrowser.mockReset();
  openFolder.mockReset();
  searchPath = "workspace/src";
  activeTabID = "files-a";
  activeTabKind = "files";
  resetFilesSearch();
  // No stylesheet loads, so a closed bar keeps focus; the blur restores what display:none would.
  if (document.activeElement instanceof HTMLElement) {
    document.activeElement.blur();
  }
});

afterEach(() => {
  resetFilesSearch();
  vi.useRealTimers();
});

describe("searchURL", () => {
  it("asks the files search endpoint with the encoded root and query", () => {
    expect(searchURL("workspace/a b", "func Foo")).toBe(
      "/api/files/search?path=workspace%2Fa+b&q=func+Foo",
    );
  });

  it("omits case unless asked, so an unset toggle keeps the server default", () => {
    expect(searchURL("workspace", "x")).not.toContain("case=");
    expect(searchURL("workspace", "x", { caseSensitive: true })).toContain("case=1");
  });

  it("sends mode, files and ignored only when set", () => {
    const bare = searchURL("workspace", "x", { mode: "names", files: "", ignored: false });
    expect(bare).toBe("/api/files/search?path=workspace&q=x");
    const full = searchURL("workspace", "x", {
      mode: "contents",
      files: "*.css, !node_modules",
      ignored: true,
    });
    expect(new URL(full, "http://h").searchParams.get("mode")).toBe("contents");
    expect(new URL(full, "http://h").searchParams.get("files")).toBe("*.css, !node_modules");
    expect(new URL(full, "http://h").searchParams.get("ignored")).toBe("1");
  });
});

describe("hitLabel", () => {
  it("shows a path relative to the folder searched", () => {
    expect(hitLabel("/workspace/src", "/workspace/src/a/b.go")).toBe("a/b.go");
  });

  it("falls back to the absolute path for a root search, which spans mounts", () => {
    expect(hitLabel("/", "/config/notes/x.md")).toBe("/config/notes/x.md");
    expect(hitLabel("", "/config/notes/x.md")).toBe("/config/notes/x.md");
  });

  it("keeps the absolute form for a path outside the folder searched", () => {
    expect(hitLabel("/workspace/src", "/config/x.md")).toBe("/config/x.md");
  });
});

describe("hitKey", () => {
  it("separates two hits that differ only by line", () => {
    const a = hitKey(contentHit("/w/a.go", 1));
    const b = hitKey(contentHit("/w/a.go", 2));
    expect(a).not.toBe(b);
  });

  it("separates hits whose paths differ only where a colon falls", () => {
    // A colon is a legal filename character, hence keyenc.
    const a = hitKey(contentHit("/w/a:1", 2));
    const b = hitKey(contentHit("/w/a", 12));
    expect(a).not.toBe(b);
  });
});

describe("the search bar", () => {
  it("hides the directory listing while it is showing results", () => {
    openFilesSearch();
    expect(document.getElementById("fb-list")?.classList.contains("hidden")).toBe(true);
    expect(_filesSearchResults().classList.contains("hidden")).toBe(false);
    closeFilesSearch();
    expect(document.getElementById("fb-list")?.classList.contains("hidden")).toBe(false);
  });

  it("brings the browser into view, so Ctrl-F from an editor tab has somewhere to land", () => {
    openFilesSearch();
    expect(activateBrowser).toHaveBeenCalled();
  });

  it("renders one line row per content hit and opens the editor at the line", async () => {
    apiGet.mockResolvedValue(
      result({
        scanned: 3,
        matches: [
          contentHit("/workspace/src/a.go", 12, "func Foo()"),
          contentHit("/workspace/src/b.go", 4, "Foo()"),
        ],
      }),
    );
    openFilesSearch();
    toggle(CONTENTS).click();
    input().value = "Foo";
    input().dispatchEvent(new Event("input"));
    await settle();

    const rows = _filesSearchResults().querySelectorAll(".fb-search-line");
    expect(rows).toHaveLength(2);
    expect(document.getElementById("fb-search-note")?.textContent).toBe(
      "2 matches; 3 files scanned",
    );
    (rows[0] as HTMLElement).click();
    expect(openAtLine).toHaveBeenCalledWith("/workspace/src/a.go", 12);
  });

  it("counts entries in names mode, the unit that mode scans", async () => {
    apiGet.mockResolvedValue(result({ scanned: 1560, matches: [nameHit("/workspace/src/a.css")] }));
    openFilesSearch();
    input().value = ".css";
    input().dispatchEvent(new Event("input"));
    await settle();
    expect(document.getElementById("fb-search-note")?.textContent).toBe(
      "1 entry; 1,560 entries scanned",
    );
  });

  // The words are copy.ts's; these pin which facts this surface hands it.

  async function noteFor(res: FileSearchResult): Promise<string | null | undefined> {
    apiGet.mockResolvedValue(res);
    openFilesSearch();
    toggle(CONTENTS).click();
    input().value = "needle";
    input().dispatchEvent(new Event("input"));
    await settle();
    return document.getElementById("fb-search-note")?.textContent;
  }

  it("states the cut when the server counted more lines than it sent rows", async () => {
    const matches: FileMatch[] = Array.from({ length: 20 }, (_, i) =>
      contentHit("/workspace/src/many.txt", i + 1, "needle"),
    );
    expect(await noteFor(result({ matches, matched: 25, scanned: 1 }))).toBe(
      "20 of 25 matches shown; 1 file scanned",
    );
  });

  it("says a stopped scan did not read everything, beside the rows it did find", async () => {
    const matches: FileMatch[] = [contentHit("/workspace/src/a.go", 1, "needle")];
    expect(await noteFor(result({ matches, scanned: 5000, truncated: true }))).toBe(
      "1 match; 5,000 files scanned, not everything was read",
    );
  });

  it("says a stopped scan did not read everything when it found NOTHING, so an empty answer cannot imply the text is nowhere", async () => {
    expect(await noteFor(result({ scanned: 5000, truncated: true }))).toBe(
      "No matches in 5,000 files, but not everything was searched",
    );
  });

  it("says the folder is ignored, and names the toggle that searches it, when the ignore rules hid the root", async () => {
    expect(await noteFor(result({ root_ignored: true }))).toBe(
      "This folder is ignored. Turn on Include ignored files to search it.",
    );
  });

  it("says plainly that nothing matched when the scan finished", async () => {
    expect(await noteFor(result({ scanned: 12 }))).toBe("No matches");
  });

  it("coalesces a burst of keystrokes into one request for the query in the box", async () => {
    openFilesSearch();
    input().value = "on";
    input().dispatchEvent(new Event("input"));
    input().value = "one";
    input().dispatchEvent(new Event("input"));
    await settle();
    expect(apiGet).toHaveBeenCalledTimes(1);
    expect(apiGet.mock.calls[0]?.[0]).toContain("q=one");
  });

  it("aborts the previous request when a new query starts", async () => {
    const signals: (AbortSignal | undefined)[] = [];
    apiGet.mockImplementation((_url, signal) => {
      signals.push(signal);
      return Promise.resolve(result());
    });
    openFilesSearch();
    input().value = "one";
    input().dispatchEvent(new Event("input"));
    await settle();
    input().value = "two";
    input().dispatchEvent(new Event("input"));
    await settle();

    expect(signals).toHaveLength(2);
    expect(signals[0]?.aborted).toBe(true);
    expect(signals[1]?.aborted).toBe(false);
  });

  it("drops a stale reply that lands after a newer query, rather than repainting with it", async () => {
    let releaseFirst: (() => void) | undefined;
    apiGet.mockImplementation((url) =>
      url.includes("q=one")
        ? new Promise((resolve) => {
            releaseFirst = () => {
              resolve(
                result({
                  scanned: 1,
                  matches: [nameHit("/workspace/src/stale.go")],
                }),
              );
            };
          })
        : Promise.resolve(
            result({
              scanned: 2,
              matches: [nameHit("/workspace/src/fresh.go")],
            }),
          ),
    );
    openFilesSearch();
    input().value = "one";
    input().dispatchEvent(new Event("input"));
    await settle();
    input().value = "two";
    input().dispatchEvent(new Event("input"));
    await settle();
    // The first query's answer arrives last and must not land.
    releaseFirst?.();
    await settle();

    const rows = _filesSearchResults().querySelectorAll(".fb-search-hit");
    expect(rows).toHaveLength(1);
    expect((rows[0] as HTMLElement).dataset["path"]).toBe("/workspace/src/fresh.go");
  });

  it("clears the results without asking the server when the query and the filter empty", async () => {
    apiGet.mockResolvedValue(result({ scanned: 1, matches: [nameHit("/workspace/src/a.go")] }));
    openFilesSearch();
    input().value = "x";
    input().dispatchEvent(new Event("input"));
    await settle();
    expect(_filesSearchResults().querySelectorAll(".fb-search-hit")).toHaveLength(1);

    apiGet.mockClear();
    input().value = "";
    input().dispatchEvent(new Event("input"));
    await settle();
    expect(apiGet).not.toHaveBeenCalled();
    expect(_filesSearchResults().querySelectorAll(".fb-search-hit")).toHaveLength(0);
    expect(document.getElementById("fb-search-note")?.textContent).toBe(
      "Type a name, or a pattern like *.css",
    );
  });

  it("sends case=1 once the Aa toggle is latched", async () => {
    openFilesSearch();
    input().value = "Foo";
    const aa = _filesSearchBar()?.querySelector<HTMLButtonElement>(".fb-search-case");
    expect(aa?.getAttribute("aria-pressed")).toBe("false");
    aa?.click();
    await settle();
    expect(aa?.getAttribute("aria-pressed")).toBe("true");
    expect(apiGet.mock.calls.at(-1)?.[0]).toContain("case=1");
  });

  it("opens in names mode with the contents toggle unpressed", async () => {
    openFilesSearch();
    input().value = "Foo";
    input().dispatchEvent(new Event("input"));
    await settle();
    expect(toggle(CONTENTS).getAttribute("aria-pressed")).toBe("false");
    expect(toggle(IGNORED).getAttribute("aria-pressed")).toBe("false");
    expect(input().placeholder).toBe("Find files by name\u2026");
    expect(apiGet.mock.calls.at(-1)?.[0]).not.toContain("mode=");
  });

  it("reopens in names mode with both toggles unpressed after a close", async () => {
    openFilesSearch();
    toggle(CONTENTS).click();
    toggle(IGNORED).click();
    await settle();
    // The precondition, or the reopen below could not tell a reset from toggles never pressed.
    expect(toggle(CONTENTS).getAttribute("aria-pressed")).toBe("true");
    expect(toggle(IGNORED).getAttribute("aria-pressed")).toBe("true");
    closeFilesSearch();

    apiGet.mockClear();
    openFilesSearch();
    input().value = "Foo";
    input().dispatchEvent(new Event("input"));
    await settle();
    expect(toggle(CONTENTS).getAttribute("aria-pressed")).toBe("false");
    expect(toggle(IGNORED).getAttribute("aria-pressed")).toBe("false");
    expect(input().placeholder).toBe("Find files by name\u2026");
    const url = apiGet.mock.calls.at(-1)?.[0] ?? "";
    expect(url).not.toContain("mode=");
    expect(url).not.toContain("ignored=");
  });

  it("keeps the toggles when an open bar is only refocused", async () => {
    openFilesSearch();
    toggle(CONTENTS).click();
    await settle();
    openFilesSearch();
    expect(toggle(CONTENTS).getAttribute("aria-pressed")).toBe("true");
  });

  it("pressing the contents toggle re-runs as mode=contents and retitles the field", async () => {
    openFilesSearch();
    input().value = "Foo";
    await settle();
    apiGet.mockClear();
    toggle(CONTENTS).click();
    await settle();
    expect(apiGet).toHaveBeenCalledTimes(1);
    expect(apiGet.mock.calls[0]?.[0]).toContain("mode=contents");
    expect(input().placeholder).toBe("Find text in files\u2026");
    expect(input().getAttribute("aria-label")).toBe("Find text in files\u2026");
  });

  it("pressing the ignored toggle re-runs with ignored=1", async () => {
    openFilesSearch();
    input().value = "Foo";
    await settle();
    apiGet.mockClear();
    toggle(IGNORED).click();
    await settle();
    expect(apiGet.mock.calls.at(-1)?.[0]).toContain("ignored=1");
  });

  it("an empty query with a files filter searches in names mode but not in contents mode", async () => {
    openFilesSearch();
    await settle();
    apiGet.mockClear();
    filesField().value = ".css";
    filesField().dispatchEvent(new Event("input"));
    await settle();
    expect(apiGet).toHaveBeenCalledTimes(1);
    expect(apiGet.mock.calls[0]?.[0]).toContain("files=.css");

    apiGet.mockClear();
    toggle(CONTENTS).click();
    await settle();
    expect(apiGet).not.toHaveBeenCalled();
  });

  it("renders the basename with marked ranges and the parent path beside it", async () => {
    searchPath = "/workspace";
    const [row] = await search([
      nameHit("/workspace/src/css/01-tokens.css", "name", [
        { start: 3, end: 9 },
        { start: 9, end: 40 },
      ]),
    ]);
    const base = row?.querySelector(".fb-search-base");
    expect(base?.textContent).toBe("01-tokens.css");
    expect(
      [...(base?.querySelectorAll("mark.fb-search-mark") ?? [])].map((m) => m.textContent),
    ).toEqual(["tokens"]);
    expect(row?.querySelector(".fb-search-parent")?.textContent).toBe("src/css");
  });

  it("omits the parent path for a hit directly in the folder searched", async () => {
    searchPath = "/workspace/src";
    const [row] = await search([nameHit("/workspace/src/a.css")]);
    expect(row?.querySelector(".fb-search-parent")).toBeNull();
  });

  it("groups content rows under one header per file", async () => {
    searchPath = "/workspace";
    apiGet.mockResolvedValue(
      result({
        scanned: 2,
        matches: [
          contentHit("/workspace/a.go", 3, "a needle"),
          contentHit("/workspace/a.go", 9, "needle again"),
          contentHit("/workspace/b.go", 1, "needle"),
        ],
      }),
    );
    openFilesSearch();
    toggle(CONTENTS).click();
    input().value = "needle";
    input().dispatchEvent(new Event("input"));
    await settle();

    const rows = [..._filesSearchResults().querySelectorAll<HTMLElement>(".fb-search-hit")];
    expect(
      rows.map((r) =>
        r.classList.contains("fb-search-group") ? `# ${r.textContent ?? ""}` : r.textContent,
      ),
    ).toEqual(["# a.go2", ":3a needle", ":9needle again", "# b.go1", ":1needle"]);
    rows[0]?.click();
    expect(openAtLine).toHaveBeenCalledWith("/workspace/a.go", undefined);
  });

  it("ArrowDown from the query focuses the first row and ArrowUp returns", async () => {
    await search([nameHit("/workspace/src/a.go"), nameHit("/workspace/src/b.go")]);
    const rows = [..._filesSearchResults().querySelectorAll<HTMLElement>(".fb-search-hit")];
    input().focus();
    input().dispatchEvent(
      new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true, cancelable: true }),
    );
    expect(document.activeElement).toBe(rows[0]);
    rows[0]?.dispatchEvent(
      new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true, cancelable: true }),
    );
    expect(document.activeElement).toBe(rows[1]);
    rows[1]?.dispatchEvent(
      new KeyboardEvent("keydown", { key: "ArrowUp", bubbles: true, cancelable: true }),
    );
    rows[0]?.dispatchEvent(
      new KeyboardEvent("keydown", { key: "ArrowUp", bubbles: true, cancelable: true }),
    );
    expect(document.activeElement).toBe(input());
  });

  it("opens a row on Enter, once", async () => {
    const rows = await search([nameHit("/workspace/src/a.go")]);
    rows[0]?.focus();
    rows[0]?.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }),
    );
    expect(openFileOrPage).toHaveBeenCalledTimes(1);
    expect(openFileOrPage).toHaveBeenCalledWith("/workspace/src/a.go", undefined);
  });

  it("opens a row on Space, once", async () => {
    const rows = await search([nameHit("/workspace/src/a.go")]);
    rows[0]?.focus();
    rows[0]?.dispatchEvent(
      new KeyboardEvent("keydown", { key: " ", bubbles: true, cancelable: true }),
    );
    expect(openFileOrPage).toHaveBeenCalledTimes(1);
    expect(openFileOrPage).toHaveBeenCalledWith("/workspace/src/a.go", undefined);
  });

  it("ArrowDown from the files field focuses the first row", async () => {
    await search([nameHit("/workspace/src/a.go"), nameHit("/workspace/src/b.go")]);
    const first = _filesSearchResults().querySelector<HTMLElement>(".fb-search-hit");
    filesField().focus();
    filesField().dispatchEvent(
      new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true, cancelable: true }),
    );
    expect(document.activeElement).toBe(first);
  });

  it("stays on the last row at ArrowDown rather than wrapping to the first", async () => {
    const rows = await search([nameHit("/workspace/src/a.go"), nameHit("/workspace/src/b.go")]);
    rows[1]?.focus();
    rows[1]?.dispatchEvent(
      new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true, cancelable: true }),
    );
    expect(document.activeElement).toBe(rows[1]);
  });

  it("sends a contents needle verbatim, edge spaces included, and searches one made only of spaces", async () => {
    openFilesSearch();
    toggle(CONTENTS).click();
    for (const q of [" needle", "needle ", "  "]) {
      apiGet.mockClear();
      input().value = q;
      input().dispatchEvent(new Event("input"));
      await settle();
      expect(apiGet).toHaveBeenCalledTimes(1);
      expect(new URL(String(apiGet.mock.calls[0]?.[0]), "http://h").searchParams.get("q")).toBe(q);
    }
  });

  it("trims a names query, so spaces alone ask nothing", async () => {
    openFilesSearch();
    input().value = "  a.css ";
    input().dispatchEvent(new Event("input"));
    await settle();
    expect(new URL(String(apiGet.mock.calls[0]?.[0]), "http://h").searchParams.get("q")).toBe(
      "a.css",
    );

    apiGet.mockClear();
    input().value = "   ";
    input().dispatchEvent(new Event("input"));
    await settle();
    expect(apiGet).not.toHaveBeenCalled();
  });

  // Kind decides a row's shape and destination; it is a registered wire enum, so the decoder refuses an unknown value.

  async function search(matches: FileMatch[]): Promise<HTMLElement[]> {
    apiGet.mockResolvedValue(result({ scanned: 1, matches }));
    openFilesSearch();
    input().value = "book";
    input().dispatchEvent(new Event("input"));
    await settle();
    return [..._filesSearchResults().querySelectorAll<HTMLElement>(".fb-search-hit")];
  }

  it("renders a name hit with no :line and no excerpt, because a name has neither", async () => {
    const [row] = await search([nameHit("/workspace/src/cover-book.png")]);
    expect(row?.querySelector(".fb-search-lineno")).toBeNull();
    expect(row?.querySelector(".fb-search-excerpt")).toBeNull();
    expect(row?.querySelector(".fb-name")?.textContent).toBe("cover-book.png");
    // The folder is hitLabel's; this search path is rootless, so the absolute form is honest.
    expect(row?.querySelector(".fb-search-parent")?.textContent).toBe("/workspace/src");
  });

  it("opens a file name hit with NO line, so it lands at the top", async () => {
    const [row] = await search([nameHit("/workspace/src/cover-book.png")]);
    row?.click();
    expect(openFileOrPage).toHaveBeenCalledWith("/workspace/src/cover-book.png", undefined);
    expect(openAtLine).not.toHaveBeenCalled();
  });

  it("opens a page's name hit by its role, with no line", async () => {
    const [row] = await search([nameHit("/workspace/demo/index.html")]);
    row?.click();
    expect(openFileOrPage).toHaveBeenCalledWith("/workspace/demo/index.html", undefined);
    expect(openAtLine).not.toHaveBeenCalled();
  });

  it("navigates the browser to a dir hit and closes the bar, in that order", async () => {
    const [row] = await search([nameHit("/workspace/src/notebook-dir", "dir")]);
    row?.click();
    expect(openFolder).toHaveBeenCalledWith("/workspace/src/notebook-dir");
    // A folder is not a file: the editor would open a directory.
    expect(openFileOrPage).not.toHaveBeenCalled();
  });

  it("refuses a reply naming a kind this bundle does not know, rather than rendering a row nothing can open", async () => {
    // An unknown kind fails the whole reply, reported as a search that could not run.
    apiGet.mockResolvedValue({
      matches: [
        { path: "/workspace/src/book.bin", excerpt: "", kind: "sigil", line: 0, ranges: [] },
      ],
      scanned: 1,
      matched: 1,
      truncated: false,
      root_ignored: false,
    });
    openFilesSearch();
    input().value = "book";
    input().dispatchEvent(new Event("input"));
    await settle();
    expect(_filesSearchResults().querySelectorAll(".fb-search-hit")).toHaveLength(0);
    expect(document.getElementById("fb-search-note")?.textContent).toBe("Could not search");
    expect(openFolder).not.toHaveBeenCalled();
    expect(openAtLine).not.toHaveBeenCalled();
    expect(openFileOrPage).not.toHaveBeenCalled();
  });

  it("keeps the :line row for a content hit, so the two shapes stay distinguishable", async () => {
    const [name, , content] = await search([
      nameHit("/workspace/src/book.md"),
      contentHit("/workspace/src/book.md", 7, "a book here"),
    ]);
    // matchLines starts at 1, so a name hit's line 0 cannot collide with a content hit.
    expect(name).not.toBe(content);
    expect(name?.querySelector(".fb-search-lineno")).toBeNull();
    expect(content?.querySelector(".fb-search-lineno")?.textContent).toBe(":7");
    expect(content?.querySelector(".fb-search-excerpt")?.textContent).toBe("a book here");
  });

  it("says so when the fetch fails rather than showing an empty result", async () => {
    apiGet.mockResolvedValue(null);
    openFilesSearch();
    input().value = "Foo";
    input().dispatchEvent(new Event("input"));
    await settle();
    expect(document.getElementById("fb-search-note")?.textContent).toBe("Could not search");
  });

  it("names a refused pattern as the pattern's fault, and any other refusal as a failed search", async () => {
    apiGet.mockResolvedValue(new HttpStatus(400));
    openFilesSearch();
    input().value = "*.{css";
    input().dispatchEvent(new Event("input"));
    await settle();
    expect(document.getElementById("fb-search-note")?.textContent).toBe(
      "Incomplete or invalid pattern",
    );

    apiGet.mockResolvedValue(new HttpStatus(500));
    input().value = "*.{css}";
    input().dispatchEvent(new Event("input"));
    await settle();
    expect(document.getElementById("fb-search-note")?.textContent).toBe("Could not search");
  });

  // A kept row is the same element, so focus and hover survive; its content must still follow the reply.
  async function reply(query: string, matches: FileMatch[]): Promise<HTMLElement[]> {
    apiGet.mockResolvedValue(result({ scanned: 1, matches }));
    input().value = query;
    input().dispatchEvent(new Event("input"));
    await settle();
    return [..._filesSearchResults().querySelectorAll<HTMLElement>(".fb-search-hit")];
  }

  function marks(row: HTMLElement | undefined): (string | null)[] {
    return [...(row?.querySelectorAll("mark.fb-search-mark") ?? [])].map((m) => m.textContent);
  }

  it("repaints a kept name row when the next reply moves its marks", async () => {
    searchPath = "/workspace";
    openFilesSearch();
    const [first] = await reply("tok", [
      nameHit("/workspace/tokens.css", "name", [{ start: 0, end: 3 }]),
    ]);
    expect(marks(first)).toEqual(["tok"]);

    const [second] = await reply("tokens", [
      nameHit("/workspace/tokens.css", "name", [{ start: 0, end: 6 }]),
    ]);
    expect(second).toBe(first);
    expect(marks(second)).toEqual(["tokens"]);
  });

  it("repaints a kept file header when the next reply changes its line count", async () => {
    searchPath = "/workspace";
    openFilesSearch();
    toggle(CONTENTS).click();
    const [header] = await reply("needle", [
      contentHit("/workspace/a.go", 3, "a needle"),
      contentHit("/workspace/a.go", 9, "needle again"),
    ]);
    expect(header?.querySelector(".fb-search-count")?.textContent).toBe("2");

    const [kept, line] = await reply("needle a", [
      { ...contentHit("/workspace/a.go", 3, "a needle"), ranges: [{ start: 2, end: 8 }] },
    ]);
    expect(kept).toBe(header);
    expect(kept?.querySelector(".fb-search-count")?.textContent).toBe("1");
    expect(marks(line)).toEqual(["needle"]);
  });

  it("opens what a kept row shows now, not what it showed when it was built", async () => {
    openFilesSearch();
    const [file] = await reply("book", [nameHit("/workspace/src/book")]);
    const [folder] = await reply("books", [nameHit("/workspace/src/book", "dir")]);
    expect(folder).toBe(file);
    folder?.click();
    expect(openFolder).toHaveBeenCalledWith("/workspace/src/book");
    expect(openFileOrPage).not.toHaveBeenCalled();
  });
});

describe("the Ctrl-F hotkey", () => {
  it("opens the bar and pre-empts the browser's native find", () => {
    const e = ctrlF();
    handleFindInFilesHotkey(e);
    expect(_isFilesSearchOpen()).toBe(true);
    expect(e.defaultPrevented).toBe(true);
  });

  it("falls through on a SECOND press while our field has focus, so native find stays reachable", () => {
    handleFindInFilesHotkey(ctrlF());
    input().focus();
    const second = ctrlF();
    handleFindInFilesHotkey(second);
    expect(second.defaultPrevented).toBe(false);
  });

  it("ignores every other chord, including the Ctrl+Shift+F that toggles the view", () => {
    for (const init of [
      { key: "f" },
      { key: "f", ctrlKey: true, shiftKey: true },
      { key: "f", ctrlKey: true, altKey: true },
      { key: "g", ctrlKey: true },
    ]) {
      const e = new KeyboardEvent("keydown", { ...init, cancelable: true });
      handleFindInFilesHotkey(e);
      expect(e.defaultPrevented).toBe(false);
    }
    expect(_isFilesSearchOpen()).toBe(false);
  });
});

describe("a tab switch", () => {
  // The bar now closes on leaving the browser, as the transcript's and editor's do.
  function leaveBrowser(): void {
    bus.emitBus(bus.BUS_TAB_CHANGED, { to: "c-1", kind: "chat" });
  }

  it("closes the bar and restores the listing when you LEAVE the browser", async () => {
    apiGet.mockResolvedValue(result({ scanned: 3 }));
    openFilesSearch();
    input().value = "Foo";
    input().dispatchEvent(new Event("input"));
    await settle();
    expect(_isFilesSearchOpen()).toBe(true);

    leaveBrowser();
    expect(_isFilesSearchOpen()).toBe(false);
    expect(document.getElementById("fb-list")?.classList.contains("hidden")).toBe(false);
    expect(_filesSearchResults().children).toHaveLength(0);
  });

  it("reset forgets the query, the filter and both toggles", async () => {
    openFilesSearch();
    input().value = "Foo";
    filesField().value = "!node_modules";
    toggle(CONTENTS).click();
    toggle(IGNORED).click();
    await settle();
    // The precondition, or a toggle left pressed by an earlier case would make the click unpress it.
    expect(toggle(CONTENTS).getAttribute("aria-pressed")).toBe("true");
    expect(toggle(IGNORED).getAttribute("aria-pressed")).toBe("true");

    leaveBrowser();
    expect(input().value).toBe("");
    expect(filesField().value, "a stale filter silently narrows a search nobody scoped").toBe("");
    expect(toggle(CONTENTS).getAttribute("aria-pressed")).toBe("false");
    expect(toggle(IGNORED).getAttribute("aria-pressed")).toBe("false");
    expect(input().placeholder).toBe("Find files by name\u2026");

    apiGet.mockClear();
    openFilesSearch();
    input().value = "Foo";
    input().dispatchEvent(new Event("input"));
    await settle();
    const url = apiGet.mock.calls.at(-1)?.[0] ?? "";
    expect(url).not.toContain("mode=");
    expect(url).not.toContain("ignored=");
  });

  it("does NOT close when the switch is ARRIVING at the tab that owns the bar", () => {
    // The tab switch is announced from a batched effect after the open, so the teardown keys on identity.
    openFilesSearch();
    expect(_isFilesSearchOpen()).toBe(true);
    bus.emitBus(bus.BUS_TAB_CHANGED, { to: activeTabID, kind: "files" });
    expect(_isFilesSearchOpen()).toBe(true);
  });

  it("DOES close on a switch to another FILES tab, which the kind test could not see", () => {
    // Two browsers are two subjects: tab B must not inherit A's query.
    openFilesSearch();
    expect(_isFilesSearchOpen()).toBe(true);
    bus.emitBus(bus.BUS_TAB_CHANGED, { to: "files-b", kind: "files" });
    expect(_isFilesSearchOpen()).toBe(false);
  });

  it("resets nothing for an owner nobody recorded, so an empty strip is left alone", () => {
    // An empty strip answers ""; a bar opened then has no owner, so no teardown fires.
    activeTabID = "";
    openFilesSearch();
    expect(_isFilesSearchOpen()).toBe(true);
    bus.emitBus(bus.BUS_TAB_CHANGED, { to: "files-b", kind: "files" });
    expect(_isFilesSearchOpen()).toBe(true);
  });

  it("cannot be reset by a later switch once the bar is CLOSED", () => {
    openFilesSearch();
    closeFilesSearch();
    input().value = "Foo";
    bus.emitBus(bus.BUS_TAB_CHANGED, { to: "files-b", kind: "files" });
    expect(input().value).toBe("Foo");
  });
});

// A synthetic KeyboardEvent inserts no text, so "the character lands" is measured as focus at keydown.
describe("type-to-search", () => {
  function typeAhead(key: string, init: KeyboardEventInit = {}): KeyboardEvent {
    const e = new KeyboardEvent("keydown", { key, cancelable: true, ...init });
    handleFilesTypeAhead(e);
    return e;
  }

  function pressOnDocument(key: string): void {
    document.body.dispatchEvent(
      new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }),
    );
  }

  it("opens the bar on a printable key and leaves the character to land in the field", () => {
    const e = typeAhead("b");
    expect(_isFilesSearchOpen()).toBe(true);
    expect(document.activeElement).toBe(input());
    expect(e.defaultPrevented, "preventDefault is what would drop the character").toBe(false);
  });

  it("does not open on Space, which activates a focused control", () => {
    typeAhead(" ");
    expect(_isFilesSearchOpen()).toBe(false);
  });

  it("does not open on a chord or a composition", () => {
    for (const init of [
      { ctrlKey: true },
      { metaKey: true },
      { altKey: true },
      { isComposing: true },
    ]) {
      typeAhead("b", init);
      expect(_isFilesSearchOpen()).toBe(false);
    }
  });

  it("does not open on a key that is not a printable character", () => {
    for (const key of ["Enter", "Escape", "Tab", "ArrowDown", "F2"]) {
      typeAhead(key);
      expect(_isFilesSearchOpen()).toBe(false);
    }
  });

  it("does not open when focus sits in a text-entry surface", () => {
    for (const tag of ["input", "textarea", "select"] as const) {
      const el = document.createElement(tag);
      document.body.append(el);
      el.focus();
      typeAhead("b");
      expect(_isFilesSearchOpen(), `focus in <${tag}>`).toBe(false);
      el.remove();
    }
  });

  it("does not open when focus sits in a contenteditable rename box", () => {
    const box = document.createElement("div");
    box.contentEditable = "true";
    document.body.append(box);
    box.focus();
    typeAhead("b");
    expect(_isFilesSearchOpen()).toBe(false);
    box.remove();
  });

  it("does not open from inside an open dialog or the terminal surface", () => {
    for (const html of [
      `<dialog open><button id="probe"></button></dialog>`,
      `<div class="wt-root"><button id="probe"></button></div>`,
    ]) {
      const host = document.createElement("div");
      host.innerHTML = html;
      document.body.append(host);
      host.querySelector<HTMLButtonElement>("#probe")?.focus();
      typeAhead("b");
      expect(_isFilesSearchOpen(), html).toBe(false);
      host.remove();
    }
  });

  it("does not open on a tab that is not the file browser", () => {
    activeTabKind = "chat";
    typeAhead("b");
    expect(_isFilesSearchOpen()).toBe(false);
  });

  it("does not re-open a bar that is already open", () => {
    // Observed as the selection: `shell.focus()` selects, so the next character would replace the query.
    openFilesSearch();
    input().value = "Foo";
    input().setSelectionRange(3, 3);
    // A hit row is a tab stop, so focus leaves the field while open; the open test does the work.
    input().blur();
    typeAhead("b");
    expect([input().selectionStart, input().selectionEnd]).toEqual([3, 3]);
  });

  it("does not open when no browser is BOUND, which would search the mounts root", () => {
    // The search root answers "" between activation and the lazy bind.
    searchPath = "";
    typeAhead("b");
    expect(_isFilesSearchOpen()).toBe(false);
  });

  it("never sees a bare ?, because keys.ts stops the event for the shortcuts sheet", () => {
    // keys.ts calls stopImmediatePropagation so `?` is not typed into the composer, which also stops this handler.
    shortcutsSheet.mockClear();
    pressOnDocument("?");
    expect(shortcutsSheet).toHaveBeenCalledTimes(1);
    expect(_isFilesSearchOpen()).toBe(false);
  });

  it("lets every other bare printable key through to it", () => {
    shortcutsSheet.mockClear();
    pressOnDocument("b");
    expect(shortcutsSheet).not.toHaveBeenCalled();
    expect(_isFilesSearchOpen()).toBe(true);
  });
});
