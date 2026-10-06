import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("./dom.js", () => ({
  $: new Proxy({}, { get: () => document.createElement("div") }),
  el: () => document.createElement("div"),
  // The real body for real-ESM linking: `skeleton.ts` marks its host busy through this name.
  setBusy: (el: Element, busy: boolean) => {
    if (busy) {
      el.setAttribute("aria-busy", "true");
    } else {
      el.removeAttribute("aria-busy");
    }
  },
}));
vi.mock("./bus.js", () => ({
  // Present for Browser Mode's real-ESM linking.
  onSSE: undefined,
  onBus: vi.fn(),
  BUS_KEYS_ESCAPE: "escape",
}));
vi.mock("./tabs.js", () => ({
  // Present for Browser Mode's real-ESM linking.
  activateTab: undefined,
  parentChatRef: undefined,
  setTabParent: undefined,
  tabIdFor: undefined,
  setGitTab: undefined,
  openGitView: undefined,
  toggleFilesView: vi.fn(() => Promise.resolve()),
  openFilesView: vi.fn(() => Promise.resolve()),
  getActiveTabKind: vi.fn(() => "files"),
  setFilesRoute: vi.fn(),
  renameTab: vi.fn(),
  filesTabIdFor: vi.fn(() => ""),
  openTab: vi.fn(() => Promise.resolve("opened")),
}));
vi.mock("./editor-openers.js", () => ({
  // Present for Browser Mode's real-ESM linking.
  openFileGitDiff: undefined,
  openFileDiff: undefined,
  openFile: vi.fn(),
  openFileInBackground: vi.fn(),
}));
vi.mock("./modals.js", () => ({ closeModal: vi.fn() }));
vi.mock("./confirm.js", () => ({ confirm: vi.fn().mockResolvedValue(true) }));
vi.mock("./upload.js", () => ({ uploadFiles: vi.fn() }));
// Linked because persist.ts reaches save-indicator.ts.
vi.mock("./icons.js", () => ({
  fileIcon: vi.fn(() => ""),
  FILE_ICONS: {},
  ICON_SAVE_OK: "",
  ICON_SAVE_FAIL: "",
  ICON_TAB_WEB: "",
}));
vi.mock("./chat.js", () => ({ attachPathsToActiveChat: vi.fn() }));
vi.mock("./files-browser-drop.js", () => ({ initBrowserDragDrop: vi.fn() }));
// The search bar is files-search.test.ts's subject; closeFilesSearch is inert here.
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
  skeletonTiming: () => armSkeleton(),
}));
vi.mock("./scroll.js", () => ({
  scroll: vi.fn(),
  setUserScrolledUp: vi.fn(),
  // Present for Browser Mode's real-ESM linking.
  apiGetTyped: vi.fn(),
}));
vi.mock("./transport.js", () => ({ send: vi.fn() }));
vi.mock("./store.js", () => ({
  // Present for Browser Mode's real-ESM linking.
  activeSession: undefined,
  getActiveId: vi.fn(() => ""),
  // Present for Browser Mode's real-ESM linking.
  newOpID: vi.fn(() => "op-test"),
  // Present for Browser Mode's real-ESM linking.
  get: vi.fn(() => undefined),
  getActive: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
}));

import { FileBrowserState, pointFilesTab, releaseFilesTab, showFilesTab } from "./files.js";
import { FB_ROOT } from "./files-shared.js";
import { apiGet } from "./api-client.js";

/** The mock never runs the paint closure and `$` hands out fresh elements, so the arm is the observable. */
const armSkeleton = vi.fn(() => ({
  commit: (render: () => void) => {
    render();
  },
  cancel: vi.fn(),
}));

describe("FileBrowserState", () => {
  describe("navigate", () => {
    const cases = [
      {
        name: "navigate from root pushes to history",
        steps: (s: FileBrowserState) => {
          s.navigate("src");
        },
        check: (s: FileBrowserState) => {
          expect(s.currentPath).toBe("src");
          expect(s.history).toEqual(["/", "src"]);
          expect(s.historyIdx).toBe(1);
        },
      },
      {
        name: "navigate clears selection",
        steps: (s: FileBrowserState) => {
          s.selected.add("file.txt");
          s.lastClickedName = "file.txt";
          s.navigate("lib");
        },
        check: (s: FileBrowserState) => {
          expect(s.selected.size).toBe(0);
          expect(s.lastClickedName).toBe("");
        },
      },
      {
        name: "navigate truncates forward history",
        steps: (s: FileBrowserState) => {
          s.navigate("a");
          s.navigate("b");
          s.goBack();
          s.navigate("c");
        },
        check: (s: FileBrowserState) => {
          expect(s.history).toEqual(["/", "a", "c"]);
          expect(s.historyIdx).toBe(2);
          expect(s.currentPath).toBe("c");
        },
      },
    ];

    for (const { name, steps, check } of cases) {
      it(name, () => {
        const s = new FileBrowserState();
        steps(s);
        check(s);
      });
    }
  });

  describe("goBack", () => {
    const cases = [
      {
        name: "returns false at start of history",
        steps: (_s: FileBrowserState) => {
          /* noop */
        },
        check: (s: FileBrowserState) => {
          expect(s.goBack()).toBe(false);
          expect(s.currentPath).toBe("/");
          expect(s.historyIdx).toBe(0);
        },
      },
      {
        name: "moves back one step",
        steps: (s: FileBrowserState) => {
          s.navigate("src");
        },
        check: (s: FileBrowserState) => {
          expect(s.goBack()).toBe(true);
          expect(s.currentPath).toBe("/");
          expect(s.historyIdx).toBe(0);
        },
      },
      {
        name: "clears selection on goBack",
        steps: (s: FileBrowserState) => {
          s.navigate("src");
          s.selected.add("x");
        },
        check: (s: FileBrowserState) => {
          s.goBack();
          expect(s.selected.size).toBe(0);
        },
      },
    ];

    for (const { name, steps, check } of cases) {
      it(name, () => {
        const s = new FileBrowserState();
        steps(s);
        check(s);
      });
    }
  });

  describe("goForward", () => {
    const cases = [
      {
        name: "returns false at end of history",
        steps: (s: FileBrowserState) => {
          s.navigate("a");
        },
        check: (s: FileBrowserState) => {
          expect(s.goForward()).toBe(false);
          expect(s.currentPath).toBe("a");
        },
      },
      {
        name: "moves forward after goBack",
        steps: (s: FileBrowserState) => {
          s.navigate("a");
          s.navigate("b");
          s.goBack();
          s.goBack();
        },
        check: (s: FileBrowserState) => {
          expect(s.goForward()).toBe(true);
          expect(s.currentPath).toBe("a");
          expect(s.historyIdx).toBe(1);
        },
      },
      {
        name: "clears selection on goForward",
        steps: (s: FileBrowserState) => {
          s.navigate("a");
          s.goBack();
          s.selected.add("y");
        },
        check: (s: FileBrowserState) => {
          s.goForward();
          expect(s.selected.size).toBe(0);
        },
      },
    ];

    for (const { name, steps, check } of cases) {
      it(name, () => {
        const s = new FileBrowserState();
        steps(s);
        check(s);
      });
    }
  });

  // A browser opens at a folder, so the constructor makes a one-entry trail; `updateNavButtons` reads these two fields.
  describe("constructed at a directory", () => {
    it("starts with a one-entry trail, so both nav buttons are disabled", () => {
      const s = new FileBrowserState("/workspace/x");
      expect(s.currentPath).toBe("/workspace/x");
      expect(s.history).toEqual(["/workspace/x"]);
      expect(s.historyIdx).toBe(0);
      expect(s.historyIdx <= 0).toBe(true);
      expect(s.historyIdx >= s.history.length - 1).toBe(true);
    });

    it("defaults to the mounts listing", () => {
      expect(new FileBrowserState().currentPath).toBe("/");
    });

    it("gives each browser its own trail", () => {
      const a = new FileBrowserState("/a");
      const b = new FileBrowserState("/b");
      a.navigate("/a/deep");
      expect(b.history).toEqual(["/b"]);
      expect(b.currentPath).toBe("/b");
    });
  });

  // While the browser is active the document trail and the tab's trail are one, so an adjacent move steps rather than
  // pushes, or repeated Back grows `history` without bound.
  describe("pointTo", () => {
    it("steps BACK onto the previous entry rather than pushing", () => {
      const s = new FileBrowserState("/a");
      s.navigate("/b");
      s.pointTo("/a");
      expect(s.currentPath).toBe("/a");
      expect(s.history).toEqual(["/a", "/b"]);
      expect(s.historyIdx).toBe(0);
    });

    // Three entries: a two-entry trail cannot tell a forward step from a push.
    it("steps FORWARD onto the next entry, keeping the trail beyond it", () => {
      const s = new FileBrowserState("/a");
      s.navigate("/b");
      s.navigate("/c");
      s.goBack();
      s.goBack();
      s.pointTo("/b");
      expect(s.currentPath).toBe("/b");
      expect(s.history).toEqual(["/a", "/b", "/c"]);
      expect(s.historyIdx).toBe(1);
    });

    it("pushes an unrelated folder, because that is a genuine arrival", () => {
      const s = new FileBrowserState("/a");
      s.navigate("/b");
      s.pointTo("/z");
      expect(s.currentPath).toBe("/z");
      expect(s.history).toEqual(["/a", "/b", "/z"]);
      expect(s.historyIdx).toBe(2);
    });

    it("is a no-op for the folder already showing", () => {
      const s = new FileBrowserState("/a");
      s.selectEntry("keep.txt");
      s.pointTo("/a");
      expect(s.history).toEqual(["/a"]);
      expect(s.historyIdx).toBe(0);
      // navigate() clears the selection, so a pushing no-op would show here.
      expect(s.selected.has("keep.txt")).toBe(true);
    });
  });

  describe("reset", () => {
    // The auto-heal is the one caller; healing back to an unreachable origin would loop.
    it("lands on the mounts listing, not on the folder the browser opened at", () => {
      const s = new FileBrowserState("/workspace/x");
      s.reset();
      expect(s.currentPath).toBe("/");
      expect(s.history).toEqual(["/"]);
      expect(s.historyIdx).toBe(0);
    });

    it("restores initial state", () => {
      const s = new FileBrowserState();
      s.navigate("deep/path");
      s.selected.add("file");
      s.entries = [{ name: "x", isDir: false, size: 0, modTime: 0, mode: "" }];
      s.reset();
      expect(s.currentPath).toBe("/");
      expect(s.history).toEqual(["/"]);
      expect(s.historyIdx).toBe(0);
      expect(s.selected.size).toBe(0);
      expect(s.lastClickedName).toBe("");
      expect(s.entries).toEqual([]);
    });
  });

  describe("selection", () => {
    it("selectEntry adds to set and tracks last clicked", () => {
      const s = new FileBrowserState();
      s.selectEntry("a.txt");
      s.selectEntry("b.txt");
      expect(s.selected.has("a.txt")).toBe(true);
      expect(s.selected.has("b.txt")).toBe(true);
      expect(s.lastClickedName).toBe("b.txt");
    });

    it("deselectEntry removes from set", () => {
      const s = new FileBrowserState();
      s.selectEntry("a.txt");
      s.deselectEntry("a.txt");
      expect(s.selected.has("a.txt")).toBe(false);
      expect(s.lastClickedName).toBe("a.txt");
    });

    it("deselectAll clears set", () => {
      const s = new FileBrowserState();
      s.selectEntry("a");
      s.selectEntry("b");
      s.deselectAll();
      expect(s.selected.size).toBe(0);
    });
  });
});

describe("pointFilesTab normalises what it is handed", () => {
  // Both callers pass outside paths (history entry, deep link), so the listing request is the observable.
  beforeEach(() => {
    releaseFilesTab(FB_ROOT);
    showFilesTab(FB_ROOT);
    vi.mocked(apiGet).mockClear();
  });

  const cases: [string, string][] = [
    ["a rootless path an older build persisted", "workspace/marotte"],
    ["the same path spelled absolutely", "/workspace/marotte"],
    ["a trailing slash", "/workspace/marotte/"],
  ];

  for (const [name, saved] of cases) {
    it(`fetches the absolute listing for ${name}`, () => {
      pointFilesTab(FB_ROOT, saved);
      expect(vi.mocked(apiGet).mock.calls[0]?.[0]).toBe("/api/files?path=%2Fworkspace%2Fmarotte");
    });
  }

  it("leaves the browser where it is when handed nothing", () => {
    pointFilesTab(FB_ROOT, "");
    showFilesTab(FB_ROOT);
    expect(vi.mocked(apiGet).mock.calls[0]?.[0]).toBe("/api/files?path=%2F");
  });
});

describe("the rows placeholder's arm", () => {
  beforeEach(() => {
    armSkeleton.mockClear();
    vi.mocked(apiGet).mockReset();
    vi.mocked(apiGet).mockResolvedValue({ files: [], writable: true });
    releaseFilesTab(FB_ROOT);
  });

  // bindFilesTab early-returns once bound, so a repeat call is a pure load.
  it("arms one for a directory this client has never read", () => {
    showFilesTab(FB_ROOT);
    expect(armSkeleton).toHaveBeenCalledTimes(1);
  });

  it("arms nothing for an EMPTY directory the route has already answered", async () => {
    showFilesTab(FB_ROOT);
    expect(armSkeleton).toHaveBeenCalledTimes(1);
    // A macrotask, so the answer has provably landed.
    await new Promise((r) => setTimeout(r, 0));

    showFilesTab(FB_ROOT);
    expect(armSkeleton).toHaveBeenCalledTimes(1);
  });

  it("a navigation returns the state to no-record, so the next directory can arm one", () => {
    const s = new FileBrowserState();
    s.answered = true;
    s.navigate("src");
    expect(s.answered).toBe(false);
  });

  it("a reset returns it too", () => {
    const s = new FileBrowserState();
    s.answered = true;
    s.reset();
    expect(s.answered).toBe(false);
  });
});
