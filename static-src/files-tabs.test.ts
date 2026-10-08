// The file browser is multi-instance over one shared view: each browser keeps its own directory, selection and offset,
// and middle-click opens another in the background.

import { describe, it, expect, vi, beforeEach } from "vitest";

// One hoisted block: `vi.mock` factories run above module consts, and this file value-imports two mocked modules.
const h = vi.hoisted(() => {
  // A memoizing registry, so a row built into `$.fbList` is still there to click.
  const els = new Map<string, HTMLElement>();
  function stub(key: string): HTMLElement {
    const existing = els.get(key);
    if (existing !== undefined) {
      return existing;
    }
    const made =
      key === "fbPath" || key === "fbSearchInput"
        ? document.createElement("input")
        : key === "fbBack" || key === "fbFwd" || key === "fbUp"
          ? document.createElement("button")
          : document.createElement("div");
    els.set(key, made);
    return made;
  }
  return {
    els,
    stub,
    openTab: vi.fn((_subject: { kind: string; ref: string; activate?: boolean }) =>
      Promise.resolve("opened"),
    ),
    setFilesRoute: vi.fn(),
    renameTab: vi.fn(),
    openFile: vi.fn(),
    openFileInBackground: vi.fn(),
    /** Paint the rows placeholder at once instead of never, for the cases that read the loading cue. */
    paintSkeleton: false,
  };
});

vi.mock("./dom.js", () => ({
  $: new Proxy({} as Record<string, HTMLElement>, { get: (_t, k: string) => h.stub(k) }),
  el: () => document.createElement("div"),
  byId: (id: string) => h.stub(id),
  // `skeleton.ts` imports it and Browser Mode links for real; the real body rather than `undefined`.
  setBusy: (el: Element, busy: boolean) => {
    if (busy) {
      el.setAttribute("aria-busy", "true");
    } else {
      el.removeAttribute("aria-busy");
    }
  },
}));
vi.mock("./bus.js", () => ({
  onSSE: undefined,
  onBus: vi.fn(),
  BUS_KEYS_ESCAPE: "escape",
}));

vi.mock("./tabs.js", () => ({
  // navigate.js's `openSpec` imports these; one missing name fails this file's import.
  activateTab: undefined,
  parentChatRef: undefined,
  setTabParent: undefined,
  tabIdFor: undefined,
  setGitTab: undefined,
  openGitView: undefined,
  toggleFilesView: vi.fn(() => Promise.resolve()),
  openFilesView: vi.fn(() => Promise.resolve()),
  getActiveTabKind: vi.fn(() => "files"),
  setFilesRoute: h.setFilesRoute,
  renameTab: h.renameTab,
  filesTabIdFor: vi.fn(() => "t-files"),
  openTab: h.openTab,
}));

vi.mock("./editor-openers.js", () => ({
  openFileGitDiff: undefined,
  openFileDiff: undefined,
  openFile: h.openFile,
  openFileInBackground: h.openFileInBackground,
}));
vi.mock("./modals.js", () => ({ closeModal: vi.fn() }));
vi.mock("./confirm.js", () => ({ confirm: vi.fn().mockResolvedValue(true) }));
vi.mock("./upload.js", () => ({ uploadFiles: vi.fn() }));
vi.mock("./icons.js", () => ({
  fileIcon: vi.fn(() => ""),
  FILE_ICONS: {},
  ICON_SAVE_OK: "",
  ICON_SAVE_FAIL: "",
  ICON_TAB_WEB: "",
}));
vi.mock("./chat.js", () => ({ attachPathsToActiveChat: vi.fn() }));
vi.mock("./files-browser-drop.js", () => ({ initBrowserDragDrop: vi.fn() }));
vi.mock("./files-search.js", () => ({
  initFilesSearch: vi.fn(),
  resetFilesSearch: vi.fn(),
  closeFilesSearch: vi.fn(),
}));
vi.mock("./files-picker.js", () => ({ setOnUploadComplete: vi.fn() }));
vi.mock("./api-client.js", () => ({
  apiPost: vi.fn(),
  apiGet: vi.fn(),
  // Linked through the settings actions, never called.
  apiGetTyped: undefined,
  apiGetOrError: vi.fn(() => Promise.resolve({ ok: false, status: 0, data: null, error: "" })),
}));
vi.mock("@cplieger/ui-primitives/skeleton", () => ({
  skeletonTiming: (show: () => (() => void) | undefined) => {
    const teardown = h.paintSkeleton ? show() : undefined;
    return { cancel: () => teardown?.(), commit: (r: () => void) => r() };
  },
}));
vi.mock("./scroll.js", () => ({
  scroll: vi.fn(),
  setUserScrolledUp: vi.fn(),
  apiGetTyped: vi.fn(),
}));
vi.mock("./transport.js", () => ({ send: vi.fn() }));
vi.mock("./store.js", () => ({
  activeSession: undefined,
  getActiveId: vi.fn(() => ""),
  newOpID: vi.fn(() => "op-test"),
  get: vi.fn(() => undefined),
  getActive: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
}));

import { $ } from "./dom.js";
import { apiGetOrError } from "./api-client.js";
import { FB_CHECK, FB_NAME } from "./files-shared.js";
import {
  bindFilesTab,
  initFileBrowser,
  pointFilesTab,
  releaseFilesTab,
  showFilesTab,
} from "./files.js";

function listing(): { files: { name: string; isDir: boolean }[]; writable: boolean } {
  return {
    files: [
      { name: "sub", isDir: true },
      { name: "note.txt", isDir: false },
    ],
    writable: true,
  };
}

function okAnswer(body: unknown): unknown {
  return { ok: true, status: 200, data: body, error: "" };
}

/** Per-path answers, each path's queue consumed in order and its last entry repeated; unlisted paths list. */
const answers = new Map<string, unknown[]>();

function answerFor(url: string): unknown {
  const path = new URLSearchParams(url.slice(url.indexOf("?") + 1)).get("path") ?? "";
  const queue = answers.get(path);
  if (queue === undefined || queue.length === 0) {
    return okAnswer(listing());
  }
  return queue.length > 1 ? queue.shift() : queue[0];
}

async function show(ref: string): Promise<void> {
  showFilesTab(ref);
  await new Promise((r) => setTimeout(r, 0));
}

/** A request that stays pending until `release` answers it, so another load can supersede it first. */
function held(): { answer: Promise<unknown>; release: (v: unknown) => void } {
  let release: (v: unknown) => void = () => undefined;
  const answer = new Promise<unknown>((r) => {
    release = r;
  });
  return { answer, release };
}

/** Narrows without a `!`. */
function must<T>(v: T | null | undefined): T {
  if (v === null || v === undefined) {
    throw new Error("expected the node to exist");
  }
  return v;
}

/** Throws rather than asserting, so `expect.assertions` counts stay true. */
function rowFor(name: string): HTMLElement {
  const row = [...$.fbList.children].find((c) => (c as HTMLElement).dataset["name"] === name) as
    HTMLElement | undefined;
  if (row === undefined) {
    throw new Error(`no row for ${name}`);
  }
  return row;
}

/** Mousedown then auxclick; returns the mousedown for `defaultPrevented`. */
function middleClick(target: HTMLElement): MouseEvent {
  const down = new MouseEvent("mousedown", { button: 1, bubbles: true, cancelable: true });
  target.dispatchEvent(down);
  target.dispatchEvent(new MouseEvent("auxclick", { button: 1, bubbles: true, cancelable: true }));
  return down;
}

beforeEach(() => {
  releaseFilesTab("/a");
  releaseFilesTab("/b");
  releaseFilesTab("/w");
  h.els.clear();
  h.paintSkeleton = false;
  vi.clearAllMocks();
  answers.clear();
  vi.mocked(apiGetOrError).mockImplementation((url: string) =>
    Promise.resolve(answerFor(url) as never),
  );
});

describe("two browsers over one view element", () => {
  it("acts on nothing from a closed browser's late answer", async () => {
    const late = held();
    answers.set("/a", [late.answer]);
    showFilesTab("/a");
    releaseFilesTab("/a");
    late.release({ ok: false, status: 404, data: null, error: "not found" });
    await new Promise((r) => setTimeout(r, 0));
    expect(h.setFilesRoute, "no route is written for a tab that is gone").not.toHaveBeenCalled();
    expect(vi.mocked(apiGetOrError), "and nothing is fetched on its behalf").toHaveBeenCalledTimes(
      1,
    );
  });

  it("gives each tab its own directory, and a switch re-points rather than merging", async () => {
    expect.assertions(2);
    await show("/a");
    rowFor("sub").querySelector(`.${FB_NAME}`)?.dispatchEvent(new MouseEvent("click"));
    await new Promise((r) => setTimeout(r, 0));
    expect(($.fbPath as HTMLInputElement).value).toBe("/a/sub");

    await show("/b");
    expect(($.fbPath as HTMLInputElement).value).toBe("/b");
  });

  it("keeps each tab's selection across a switch", async () => {
    expect.assertions(2);
    await show("/a");
    const check = rowFor("note.txt").querySelector<HTMLInputElement>(`.${FB_CHECK}`);
    must(check).checked = true;
    check?.dispatchEvent(new Event("change"));

    await show("/b");
    expect(
      rowFor("note.txt").querySelector<HTMLInputElement>(`.${FB_CHECK}`)?.checked,
      "the second browser starts with nothing selected",
    ).toBe(false);

    await show("/a");
    expect(rowFor("note.txt").querySelector<HTMLInputElement>(`.${FB_CHECK}`)?.checked).toBe(true);
  });

  it("keeps each tab's nav trail, so Back never walks into another browser's", async () => {
    expect.assertions(2);
    await show("/a");
    rowFor("sub").querySelector(`.${FB_NAME}`)?.dispatchEvent(new MouseEvent("click"));
    await new Promise((r) => setTimeout(r, 0));

    await show("/b");
    expect(($.fbBack as HTMLButtonElement).disabled).toBe(true);

    await show("/a");
    expect(($.fbBack as HTMLButtonElement).disabled).toBe(false);
  });
});

describe("middle-click opens in the background", () => {
  it("opens a folder row in an unfocused browser at that folder", async () => {
    expect.assertions(2);
    await show("/a");
    middleClick(rowFor("sub"));
    expect(h.openTab).toHaveBeenCalledWith({ kind: "files", ref: "/a/sub", activate: false });
    // `activate: false` never reaches activateTab, so no view swap or pushRoute runs.
    expect(h.setFilesRoute).not.toHaveBeenCalled();
  });

  it("leaves the on-screen listing exactly where it was", async () => {
    expect.assertions(2);
    await show("/a");
    middleClick(rowFor("sub"));
    await new Promise((r) => setTimeout(r, 0));
    expect(($.fbPath as HTMLInputElement).value).toBe("/a");
    expect(rowFor("note.txt")).toBeDefined();
  });

  it("opens a file row in an unfocused editor tab", async () => {
    expect.assertions(3);
    await show("/a");
    middleClick(rowFor("note.txt"));
    expect(h.openFileInBackground).toHaveBeenCalledWith("/a/note.txt");
    // No current engine emits `click` for a middle button, so the two paths are disjoint.
    expect(h.openFile).not.toHaveBeenCalled();
    expect(h.setFilesRoute).not.toHaveBeenCalled();
  });

  it("opens the .. row's parent in the background too", async () => {
    expect.assertions(1);
    await show("/a/deep");
    const parent = [...$.fbList.children].find(
      (c) => (c as HTMLElement).dataset["name"] === undefined,
    ) as HTMLElement;
    middleClick(parent);
    expect(h.openTab).toHaveBeenCalledWith({ kind: "files", ref: "/a", activate: false });
  });

  it("cancels the mousedown default, which is what suppresses autoscroll and paste", async () => {
    expect.assertions(1);
    await show("/a");
    // Autoscroll is not observable headless; the cancelled default is. `.fb-list-wrap` scrolls, so auxclick alone is not enough.
    expect(middleClick(rowFor("sub")).defaultPrevented).toBe(true);
  });

  it("does nothing at all for the LEFT button", async () => {
    expect.assertions(1);
    await show("/a");
    rowFor("sub").dispatchEvent(
      new MouseEvent("auxclick", { button: 0, bubbles: true, cancelable: true }),
    );
    expect(h.openTab).not.toHaveBeenCalled();
  });

  it("is excluded on the checkbox", async () => {
    expect.assertions(2);
    await show("/a");
    const check = rowFor("sub").querySelector<HTMLElement>(`.${FB_CHECK}`);
    middleClick(must(check));
    expect(h.openTab).not.toHaveBeenCalled();
    expect(h.openFileInBackground).not.toHaveBeenCalled();
  });

  it("is excluded on the git badge", async () => {
    expect.assertions(2);
    await show("/a");
    // Asserts the gate: a synthetic child with the class is what `closest` looks for.
    const row = rowFor("sub");
    const badge = document.createElement("span");
    badge.className = "fb-git-letter";
    row.appendChild(badge);
    middleClick(badge);
    expect(h.openTab).not.toHaveBeenCalled();
    expect(h.openFileInBackground).not.toHaveBeenCalled();
  });

  it("names ONE subject for two clicks on one row, which is what the dedupe reads", async () => {
    expect.assertions(2);
    await show("/a");
    const row = rowFor("sub");
    middleClick(row);
    middleClick(row);
    // The collapse is the action framework's `dedupe` on (kind, ref) (actions/tabs-actions.test.ts); this owes it one key.
    expect(h.openTab).toHaveBeenCalledTimes(2);
    expect(new Set(h.openTab.mock.calls.map((c) => JSON.stringify(c[0]))).size).toBe(1);
  });

  it("survives a bind with no load, so a queued row is clickable before its fetch", async () => {
    expect.assertions(1);
    await show("/a");
    bindFilesTab("/a");
    middleClick(rowFor("sub"));
    expect(h.openTab).toHaveBeenCalledWith({ kind: "files", ref: "/a/sub", activate: false });
  });
});

describe("each browser keeps its own scroll position over the shared list", () => {
  function mountScroller(): HTMLElement {
    const wrap = document.createElement("div");
    wrap.className = "fb-list-wrap";
    wrap.style.cssText = "height:200px;overflow-y:auto";
    wrap.appendChild($.fbList);
    document.body.appendChild(wrap);
    return wrap;
  }

  function tall(): { files: { name: string; isDir: boolean }[]; writable: boolean } {
    return {
      files: Array.from({ length: 120 }, (_, i) => ({ name: `f${String(i)}.txt`, isDir: false })),
      writable: true,
    };
  }

  async function frame(): Promise<void> {
    await new Promise<void>((r) => requestAnimationFrame(() => r()));
  }

  it("lands A, then B, then A each where it was left", async () => {
    vi.mocked(apiGetOrError).mockImplementation(() => Promise.resolve(okAnswer(tall()) as never));
    const wrap = mountScroller();
    initFileBrowser();
    try {
      await show("/a");
      expect(wrap.scrollHeight).toBeGreaterThan(wrap.clientHeight);
      wrap.scrollTop = 600;
      await frame();
      await show("/b");
      expect(wrap.scrollTop, "a browser opened for the first time starts at the top").toBe(0);
      wrap.scrollTop = 250;
      await frame();
      await show("/a");
      expect(wrap.scrollTop).toBe(600);
      await show("/b");
      expect(wrap.scrollTop).toBe(250);
    } finally {
      wrap.remove();
    }
  });

  // The offset belongs to the folder.
  it("brings a browser back at the top of a folder it moved to and was not scrolled in", async () => {
    vi.mocked(apiGetOrError).mockImplementation(() => Promise.resolve(okAnswer(tall()) as never));
    const wrap = mountScroller();
    initFileBrowser();
    try {
      await show("/a");
      wrap.scrollTop = 600;
      await frame();
      pointFilesTab("/a", "/a/deeper");
      await new Promise((r) => setTimeout(r, 0));
      await show("/b");
      await show("/a");
      expect(wrap.scrollTop).toBe(0);
    } finally {
      wrap.remove();
    }
  });
});

describe("the path field and the listing error", () => {
  const notADirectory = {
    ok: false,
    status: 400,
    data: null,
    error: "not a directory",
    code: "not_a_directory",
  };
  const notFound = { ok: false, status: 404, data: null, error: "not found" };

  function typePath(path: string): void {
    const input = $.fbPath as HTMLInputElement;
    input.click();
    input.value = path;
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
  }

  function notices(): HTMLElement[] {
    return [...$.fbList.querySelectorAll<HTMLElement>(".fb-notice")];
  }

  function buttonLabels(notice: HTMLElement): string[] {
    return [...notice.querySelectorAll("button")].map((b) => b.textContent);
  }

  function clickButton(label: string): void {
    const btn = [...$.fbList.querySelectorAll("button")].find((b) => b.textContent === label);
    must(btn).click();
  }

  async function showFailedListing(): Promise<void> {
    answers.set("/w/gone", [notFound]);
    initFileBrowser();
    await show("/w");
    typePath("/w/gone");
    await vi.waitFor(() => {
      expect(notices()).toHaveLength(1);
    });
  }

  it("opens a FILE typed in the field and shows its folder, with no file in the trail", async () => {
    answers.set("/a/note.txt", [notADirectory]);
    initFileBrowser();
    await show("/w");
    typePath("/a/note.txt");
    await vi.waitFor(() => {
      expect(($.fbPath as HTMLInputElement).value).toBe("/a");
      expect(rowFor("note.txt")).toBeDefined();
    });
    expect(h.openFile.mock.calls).toEqual([["/a/note.txt"]]);
    expect(notices()).toEqual([]);
    expect(h.setFilesRoute.mock.calls.at(-1)?.[1]).toBe("/a");
    expect(h.setFilesRoute.mock.calls.map((c) => c[1] as string)).not.toContain("/a/note.txt");

    ($.fbBack as HTMLButtonElement).click();
    await new Promise((r) => setTimeout(r, 0));
    expect(($.fbPath as HTMLInputElement).value).toBe("/w");
    expect(($.fbBack as HTMLButtonElement).disabled, "Back now sits at the trail's start").toBe(
      true,
    );
  });

  it("lands an outside pointer at a file on its folder without opening it", async () => {
    answers.set("/a/note.txt", [notADirectory]);
    await show("/a");
    pointFilesTab("/a", "/a/note.txt");
    await vi.waitFor(() => {
      expect(h.setFilesRoute.mock.calls.at(-1)?.[1]).toBe("/a");
    });
    await new Promise((r) => setTimeout(r, 0));
    expect(($.fbPath as HTMLInputElement).value).toBe("/a");
    expect(rowFor("note.txt")).toBeDefined();
    expect(h.openFile).not.toHaveBeenCalled();
  });

  it("shows the server's own words for a failed listing, with Retry and Go to root", async () => {
    await showFailedListing();
    const [notice] = notices();
    expect(must(notice).textContent).toContain("not found");
    expect(must(notice).textContent).not.toContain("fetch failed");
    expect(buttonLabels(must(notice))).toEqual(["Retry", "Go to root"]);
    expect($.fbList.children, "the notice replaces every row").toHaveLength(1);
    expect($.fbList.hasAttribute("role"), "a notice is not a list").toBe(false);
  });

  it("leaves no error behind after Go to root", async () => {
    await showFailedListing();
    clickButton("Go to root");
    await vi.waitFor(() => {
      expect(rowFor("sub")).toBeDefined();
    });
    expect(notices()).toEqual([]);
    expect($.fbList.getAttribute("role")).toBe("list");
    expect(($.fbPath as HTMLInputElement).value).toBe("/");
  });

  it("takes the error down the moment Go to root is pressed, before the root answers", async () => {
    await showFailedListing();
    const root = held();
    answers.set("/", [root.answer]);
    clickButton("Go to root");
    await vi.waitFor(() => {
      expect(notices()).toEqual([]);
    });
    expect(($.fbPath as HTMLInputElement).value).toBe("/");
    expect($.fbList.getAttribute("role")).toBe("list");
    root.release(okAnswer(listing()));
    await vi.waitFor(() => {
      expect(rowFor("sub")).toBeDefined();
    });
  });

  it("shows a typed folder's error even when the opening folder had not answered yet", async () => {
    const origin = held();
    answers.set("/w", [origin.answer]);
    answers.set("/w/gone", [notFound]);
    initFileBrowser();
    showFilesTab("/w");
    typePath("/w/gone");
    await vi.waitFor(() => {
      expect(notices()).toHaveLength(1);
    });
    expect(must(notices()[0]).textContent).toContain("not found");
    expect(buttonLabels(must(notices()[0]))).toEqual(["Retry", "Go to root"]);
    expect(($.fbPath as HTMLInputElement).value).toBe("/w/gone");
    expect(h.setFilesRoute.mock.calls.map((c) => c[1] as string)).not.toContain("/");
    origin.release(okAnswer(listing()));
  });

  it("takes the error down the moment Retry is pressed, showing the loading rows until the answer", async () => {
    await showFailedListing();
    h.paintSkeleton = true;
    const again = held();
    answers.set("/w/gone", [again.answer]);
    clickButton("Retry");
    expect(notices()).toEqual([]);
    expect($.fbList.getAttribute("role")).toBe("list");
    expect($.fbList.querySelector(".fb-skeleton")).not.toBeNull();
    again.release(okAnswer(listing()));
    await vi.waitFor(() => {
      expect(rowFor("sub")).toBeDefined();
    });
    expect($.fbList.querySelector(".fb-skeleton")).toBeNull();
  });

  it("takes the error down the moment the failed path is entered again, showing the loading rows", async () => {
    await showFailedListing();
    h.paintSkeleton = true;
    const again = held();
    answers.set("/w/gone", [again.answer]);
    typePath("/w/gone");
    expect(notices()).toEqual([]);
    expect($.fbList.getAttribute("role")).toBe("list");
    expect($.fbList.querySelector(".fb-skeleton")).not.toBeNull();
    again.release(okAnswer(listing()));
    await vi.waitFor(() => {
      expect(rowFor("sub")).toBeDefined();
    });
  });

  it("shows the loading rows beside .. while Back leaves a failed folder", async () => {
    await showFailedListing();
    h.paintSkeleton = true;
    const back = held();
    answers.set("/w", [back.answer]);
    ($.fbBack as HTMLButtonElement).click();
    expect(notices()).toEqual([]);
    expect($.fbList.querySelector('[data-reconcile-key="__parent__"]')).not.toBeNull();
    expect($.fbList.querySelector(".fb-skeleton")).not.toBeNull();
    back.release(okAnswer(listing()));
    await vi.waitFor(() => {
      expect(rowFor("sub")).toBeDefined();
    });
    expect($.fbList.querySelector(".fb-skeleton")).toBeNull();
  });

  it("shows the loading rows on a Retry after a failed refresh of a folder it had read", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(new Response("{}", { status: 200 }))),
    );
    answers.set("/w", [okAnswer(listing()), notFound]);
    initFileBrowser();
    await show("/w");
    $.fbNewFile.click();
    await vi.waitFor(() => {
      expect(notices()).toHaveLength(1);
    });
    h.paintSkeleton = true;
    const again = held();
    answers.set("/w", [again.answer]);
    clickButton("Retry");
    expect($.fbList.querySelector(".fb-skeleton")).not.toBeNull();
    again.release(okAnswer(listing()));
  });

  it("leaves no error behind after a Retry that succeeds", async () => {
    answers.set("/w/gone", [notFound, okAnswer(listing())]);
    initFileBrowser();
    await show("/w");
    typePath("/w/gone");
    await vi.waitFor(() => {
      expect(notices()).toHaveLength(1);
    });
    clickButton("Retry");
    await vi.waitFor(() => {
      expect(rowFor("sub")).toBeDefined();
    });
    expect(notices()).toEqual([]);
    expect($.fbList.getAttribute("role")).toBe("list");
  });

  it("keeps the error with its own browser across a tab switch", async () => {
    answers.set("/a/gone", [notFound]);
    initFileBrowser();
    await show("/a");
    typePath("/a/gone");
    await vi.waitFor(() => {
      expect(notices()).toHaveLength(1);
    });
    await show("/b");
    expect(notices(), "the healthy browser shows its rows").toEqual([]);
    expect(rowFor("sub")).toBeDefined();
    bindFilesTab("/a");
    expect(notices(), "the failed browser still shows its error").toHaveLength(1);
  });

  it("still opens a typed file after another browser superseded its request", async () => {
    const first = held();
    answers.set("/a/note.txt", [first.answer, notADirectory]);
    initFileBrowser();
    await show("/a");
    typePath("/a/note.txt");
    await show("/b");
    first.release(notADirectory);
    await new Promise((r) => setTimeout(r, 0));
    expect(h.openFile, "the superseded answer paints nothing").not.toHaveBeenCalled();

    await show("/a");
    await vi.waitFor(() => {
      expect(h.openFile.mock.calls).toEqual([["/a/note.txt"]]);
    });
    expect(($.fbPath as HTMLInputElement).value).toBe("/a");
    expect(h.renameTab.mock.calls.map((c) => c[1] as string)).not.toContain("note.txt");
  });

  it("publishes a typed folder once a load after a supersession answers it", async () => {
    const first = held();
    answers.set("/a/sub", [first.answer, okAnswer(listing())]);
    initFileBrowser();
    await show("/a");
    typePath("/a/sub");
    await show("/b");
    first.release(okAnswer(listing()));
    h.setFilesRoute.mockClear();

    await show("/a");
    await vi.waitFor(() => {
      expect(h.setFilesRoute.mock.calls).toEqual([["/a", "/a/sub"]]);
    });
  });

  it("shows the server's words when the refresh after a create fails", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(new Response("{}", { status: 200 }))),
    );
    answers.set("/w", [okAnswer(listing()), notFound]);
    initFileBrowser();
    await show("/w");
    $.fbNewFile.click();
    await vi.waitFor(() => {
      expect(notices()).toHaveLength(1);
    });
    expect(must(notices()[0]).textContent).toContain("not found");
  });

  it("treats Enter on the unchanged path as a reload, not a move", async () => {
    initFileBrowser();
    await show("/a");
    vi.mocked(apiGetOrError).mockClear();
    typePath("/a");
    await new Promise((r) => setTimeout(r, 0));
    expect(($.fbBack as HTMLButtonElement).disabled).toBe(true);
    expect(vi.mocked(apiGetOrError)).toHaveBeenCalledTimes(1);
  });
});
