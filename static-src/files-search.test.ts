// Find in files: the request, the note's honesty, and the second-press escape hatch that justifies overriding Ctrl-F.
import { describe, it, expect, vi, beforeAll, afterAll, beforeEach, afterEach } from "vitest";
import type { FileMatch, FileSearchResult } from "./wire/types.gen.js";

/** Typed as the reply so a case cannot build one the decoder refuses by accident. */
const apiGet = vi.fn<(url: string, signal?: AbortSignal) => Promise<unknown>>();
const openAtLine = vi.fn();
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
  // Through the real generated decoder, so an unreadable reply collapses to null as in production.
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
}));
// ICON_CLOSE_UI is inert: search-shell.ts imports it.
vi.mock("./icons.js", () => ({ fileIcon: () => "<svg></svg>", ICON_CLOSE_UI: "<svg></svg>" }));
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
  return { matches, scanned: 0, matched: matches.length, truncated: false, ...over };
}

function input(): HTMLInputElement {
  const el = document.getElementById("fb-search-input");
  if (!(el instanceof HTMLInputElement)) {
    throw new Error("search input not built");
  }
  return el;
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

  it("omits an empty glob field rather than sending a pattern nothing typed", () => {
    const url = searchURL("workspace", "x", { include: "", exclude: "node_modules" });
    expect(url).not.toContain("include=");
    expect(url).toContain("exclude=node_modules");
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
    const a = hitKey({ path: "/w/a.go", excerpt: "", kind: "content", line: 1 });
    const b = hitKey({ path: "/w/a.go", excerpt: "", kind: "content", line: 2 });
    expect(a).not.toBe(b);
  });

  it("separates hits whose paths differ only where a colon falls", () => {
    // A colon is a legal filename character, hence keyenc.
    const a = hitKey({ path: "/w/a:1", excerpt: "", kind: "content", line: 2 });
    const b = hitKey({ path: "/w/a", excerpt: "", kind: "content", line: 12 });
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

  it("renders one row per hit and opens the editor at the line", async () => {
    apiGet.mockResolvedValue(
      result({
        scanned: 3,
        matches: [
          { path: "/workspace/src/a.go", excerpt: "func Foo()", kind: "content", line: 12 },
          { path: "/workspace/src/b.go", excerpt: "Foo()", kind: "content", line: 4 },
        ],
      }),
    );
    openFilesSearch();
    input().value = "Foo";
    input().dispatchEvent(new Event("input"));
    await settle();

    const rows = _filesSearchResults().querySelectorAll(".fb-search-hit");
    expect(rows).toHaveLength(2);
    expect(document.getElementById("fb-search-note")?.textContent).toBe(
      "2 matches; 3 files scanned",
    );
    (rows[0] as HTMLElement).click();
    expect(openAtLine).toHaveBeenCalledWith("/workspace/src/a.go", 12);
  });

  // The words are copy.ts's; these pin which facts this surface hands it.

  async function noteFor(res: FileSearchResult): Promise<string | null | undefined> {
    apiGet.mockResolvedValue(res);
    openFilesSearch();
    input().value = "needle";
    input().dispatchEvent(new Event("input"));
    await settle();
    return document.getElementById("fb-search-note")?.textContent;
  }

  it("states the cut when the server counted more lines than it sent rows", async () => {
    const matches: FileMatch[] = Array.from({ length: 20 }, (_, i) => ({
      path: "/workspace/src/many.txt",
      excerpt: "needle",
      kind: "content",
      line: i + 1,
    }));
    expect(await noteFor(result({ matches, matched: 25, scanned: 1 }))).toBe(
      "20 of 25 matches shown; 1 file scanned",
    );
  });

  it("says a stopped scan did not read everything, beside the rows it did find", async () => {
    const matches: FileMatch[] = [
      { path: "/workspace/src/a.go", excerpt: "needle", kind: "content", line: 1 },
    ];
    expect(await noteFor(result({ matches, scanned: 5000, truncated: true }))).toBe(
      "1 match; 5,000 files scanned, not everything was read",
    );
  });

  it("says a stopped scan did not read everything when it found NOTHING, so an empty answer cannot imply the text is nowhere", async () => {
    expect(await noteFor(result({ scanned: 5000, truncated: true }))).toBe(
      "No matches in 5,000 files, but not everything was searched",
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
                  matches: [
                    { path: "/workspace/src/stale.go", excerpt: "s", kind: "content", line: 1 },
                  ],
                }),
              );
            };
          })
        : Promise.resolve(
            result({
              scanned: 2,
              matches: [
                { path: "/workspace/src/fresh.go", excerpt: "f", kind: "content", line: 3 },
              ],
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

  it("clears the results without asking the server when the query empties", async () => {
    apiGet.mockResolvedValue(
      result({
        scanned: 1,
        matches: [{ path: "/workspace/src/a.go", excerpt: "x", kind: "content", line: 1 }],
      }),
    );
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
    expect(document.getElementById("fb-search-note")?.textContent).toBe("");
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

  it("passes the glob fields through", async () => {
    openFilesSearch();
    input().value = "Foo";
    const include = document.getElementById("fb-search-include") as HTMLInputElement;
    include.value = "*.go";
    include.dispatchEvent(new Event("input"));
    await settle();
    expect(apiGet.mock.calls.at(-1)?.[0]).toContain("include=*.go");
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
    const [row] = await search([
      { path: "/workspace/src/cover-book.png", excerpt: "", kind: "name", line: 0 },
    ]);
    expect(row?.querySelector(".fb-search-lineno")).toBeNull();
    expect(row?.querySelector(".fb-search-excerpt")).toBeNull();
    // The label is hitLabel's; this search path is rootless, so the absolute form is honest.
    expect(row?.querySelector(".fb-name")?.textContent).toBe("/workspace/src/cover-book.png");
  });

  it("opens a file name hit in the editor with NO line, so it lands at the top", async () => {
    const [row] = await search([
      { path: "/workspace/src/cover-book.png", excerpt: "", kind: "name", line: 0 },
    ]);
    row?.click();
    expect(openAtLine).toHaveBeenCalledWith("/workspace/src/cover-book.png", undefined);
  });

  it("navigates the browser to a dir hit and closes the bar, in that order", async () => {
    const [row] = await search([
      { path: "/workspace/src/notebook-dir", excerpt: "", kind: "dir", line: 0 },
    ]);
    row?.click();
    expect(openFolder).toHaveBeenCalledWith("/workspace/src/notebook-dir");
    // A folder is not a file: the editor would open a directory.
    expect(openAtLine).not.toHaveBeenCalled();
  });

  it("refuses a reply naming a kind this bundle does not know, rather than rendering a row nothing can open", async () => {
    // An unknown kind fails the whole reply, reported as a search that could not run.
    apiGet.mockResolvedValue({
      matches: [{ path: "/workspace/src/book.bin", excerpt: "", kind: "sigil", line: 0 }],
      scanned: 1,
      matched: 1,
      truncated: false,
    });
    openFilesSearch();
    input().value = "book";
    input().dispatchEvent(new Event("input"));
    await settle();
    expect(_filesSearchResults().querySelectorAll(".fb-search-hit")).toHaveLength(0);
    expect(document.getElementById("fb-search-note")?.textContent).toBe("Could not search");
    expect(openFolder).not.toHaveBeenCalled();
    expect(openAtLine).not.toHaveBeenCalled();
  });

  it("keeps the :line row for a content hit, so the two shapes stay distinguishable", async () => {
    const [name, content] = await search([
      { path: "/workspace/src/book.md", excerpt: "", kind: "name", line: 0 },
      { path: "/workspace/src/book.md", excerpt: "a book here", kind: "content", line: 7 },
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

  it("forgets the query AND the globs, so nothing narrows a later search invisibly", async () => {
    openFilesSearch();
    input().value = "Foo";
    const include = document.getElementById("fb-search-include") as HTMLInputElement;
    const exclude = document.getElementById("fb-search-exclude") as HTMLInputElement;
    include.value = "*.go";
    exclude.value = "node_modules";
    await settle();

    leaveBrowser();
    expect(input().value).toBe("");
    expect(include.value).toBe("");
    expect(exclude.value, "a stale exclude silently narrows a search nobody scoped").toBe("");
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
