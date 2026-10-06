// Ctrl-F and the toolbar button are scoped by the active tab: one destination per press, and native find on a second.
import { describe, it, expect, vi, beforeEach } from "vitest";
import type { Mock } from "vitest";
import type { TabKind } from "./tabs.js";
import type { PageFind } from "./find-registry.js";

const chatFind = vi.fn();
const chatToggle = vi.fn();
const filesFind = vi.fn();
const filesToggle = vi.fn();
const editorToggle = vi.fn();
let editorClaims = true;
let activeKind: TabKind | null = "chat";

vi.mock("./tabs.js", () => ({ getActiveTabKind: () => activeKind }));
vi.mock("./find-in-chat.js", () => ({
  handleFindHotkey: (e: KeyboardEvent) => chatFind(e),
  toggleChatFind: () => chatToggle(),
}));
vi.mock("./files-search.js", () => ({
  handleFindInFilesHotkey: (e: KeyboardEvent) => filesFind(e),
  toggleFilesSearch: () => filesToggle(),
}));
vi.mock("./editor-find.js", () => ({
  handleEditorFindHotkey: (e: KeyboardEvent) => {
    if (editorClaims) {
      e.preventDefault();
    }
    return editorClaims;
  },
  toggleEditorFind: () => editorToggle(),
  editorFindAvailable: () => editorClaims,
}));

const { handleFindKey, toggleFindForActiveTab, findAffordanceForActiveTab } =
  await import("./find-dispatch.js");
const { registerFind, _resetFindRegistry } = await import("./find-registry.js");

/** The contract every destination answers: the chord opens, the button toggles, `focused` marks a second press. */
function pageFindStub(over: Partial<PageFind> = {}): PageFind & { open: Mock; toggle: Mock } {
  return {
    open: vi.fn(() => true),
    toggle: vi.fn(),
    focused: () => false,
    kind: () => "filter",
    ...over,
  } as PageFind & { open: Mock; toggle: Mock };
}

function ctrlF(): KeyboardEvent {
  return new KeyboardEvent("keydown", { key: "f", ctrlKey: true, cancelable: true });
}

beforeEach(() => {
  chatFind.mockReset();
  chatToggle.mockReset();
  filesFind.mockReset();
  filesToggle.mockReset();
  editorToggle.mockReset();
  editorClaims = true;
  activeKind = "chat";
  _resetFindRegistry();
});

describe("handleFindKey", () => {
  it("means find-in-files over a files tab", () => {
    activeKind = "files";
    handleFindKey(ctrlF());
    expect(filesFind).toHaveBeenCalledTimes(1);
    expect(chatFind).not.toHaveBeenCalled();
  });

  it("means find-in-FILE over an editor tab, not find-in-files", () => {
    // The tab's kind, not its route's (an editor tab routes as `file`).
    activeKind = "editor";
    handleFindKey(ctrlF());
    expect(filesFind).not.toHaveBeenCalled();
    expect(chatFind).not.toHaveBeenCalled();
  });

  it("falls through to native find when the editor DECLINES the press", () => {
    // No line geometry here, so every handler declines and native find gets the key.
    activeKind = "editor";
    editorClaims = false;
    const e = ctrlF();
    handleFindKey(e);
    expect(chatFind).toHaveBeenCalledTimes(1);
    expect(e.defaultPrevented).toBe(false);
  });

  it("leaves a web preview to the browser's own find", () => {
    activeKind = "web";
    const e = ctrlF();
    handleFindKey(e);
    expect(chatFind).not.toHaveBeenCalled();
    expect(filesFind).not.toHaveBeenCalled();
    expect(e.defaultPrevented).toBe(false);
  });

  it("means find-in-chat over a chat tab", () => {
    activeKind = "chat";
    handleFindKey(ctrlF());
    expect(chatFind).toHaveBeenCalledTimes(1);
    expect(filesFind).not.toHaveBeenCalled();
  });

  it("opens a registered page box on docs, history and git, consuming the press", () => {
    for (const kind of ["docs", "history", "git"] as TabKind[]) {
      _resetFindRegistry();
      chatFind.mockReset();
      const find = pageFindStub();
      registerFind(kind, find);
      activeKind = kind;
      const e = ctrlF();
      handleFindKey(e);
      expect(find.open).toHaveBeenCalledTimes(1);
      expect(e.defaultPrevented).toBe(true);
      expect(chatFind).not.toHaveBeenCalled();
    }
  });

  it("falls through when a page box DECLINES (the git Sources tab filters nothing)", () => {
    registerFind("git", pageFindStub({ open: () => false }));
    activeKind = "git";
    const e = ctrlF();
    handleFindKey(e);
    expect(chatFind).toHaveBeenCalledTimes(1);
    expect(e.defaultPrevented).toBe(false);
  });

  it("leaves a SECOND press to the browser, which is the escape hatch", () => {
    // A press from inside the open box is not consumed, so native find still opens: the a11y justification.
    const find = pageFindStub({ focused: () => true });
    registerFind("docs", find);
    activeKind = "docs";
    const e = ctrlF();
    handleFindKey(e);
    expect(find.open).not.toHaveBeenCalled();
    expect(e.defaultPrevented).toBe(false);
    expect(chatFind).toHaveBeenCalledTimes(1);
  });

  it("falls through when a page never registered at all", () => {
    activeKind = "history";
    handleFindKey(ctrlF());
    expect(chatFind).toHaveBeenCalledTimes(1);
  });

  it("does not reach a page box on a non-find chord", () => {
    const find = pageFindStub();
    registerFind("docs", find);
    activeKind = "docs";
    handleFindKey(new KeyboardEvent("keydown", { key: "g", ctrlKey: true, cancelable: true }));
    expect(find.open).not.toHaveBeenCalled();
  });

  it("leaves the chord ALONE on a page with no search at all", () => {
    // Settings and a run view have their own table entry rather than a default branch.
    for (const kind of ["settings", "run"] as TabKind[]) {
      chatFind.mockReset();
      filesFind.mockReset();
      activeKind = kind;
      const e = ctrlF();
      handleFindKey(e);
      expect(chatFind).not.toHaveBeenCalled();
      expect(filesFind).not.toHaveBeenCalled();
      expect(e.defaultPrevented, "native find must still open").toBe(false);
    }
  });

  it("does nothing on the toolbar button there either, rather than half-acting", () => {
    for (const kind of ["settings", "run"] as TabKind[]) {
      chatToggle.mockReset();
      activeKind = kind;
      toggleFindForActiveTab();
      expect(chatToggle).not.toHaveBeenCalled();
    }
  });

  it("routes with no tab open at all", () => {
    activeKind = null;
    handleFindKey(ctrlF());
    expect(chatFind).toHaveBeenCalledTimes(1);
  });

  it("never consumes the press itself, so each find owns its escape hatch", () => {
    for (const kind of ["files", "chat"] as TabKind[]) {
      activeKind = kind;
      const e = ctrlF();
      handleFindKey(e);
      expect(e.defaultPrevented).toBe(false);
    }
  });

  it("hands the SAME event to the destination, not a copy", () => {
    activeKind = "files";
    const e = ctrlF();
    handleFindKey(e);
    expect(filesFind).toHaveBeenCalledWith(e);
  });
});

describe("toggleFindForActiveTab", () => {
  // The button routes like the chord, so it never hits find-in-chat's guard on a files or editor tab.
  it("toggles the destination that belongs to the active tab", () => {
    const cases: { kind: TabKind; fn: () => void }[] = [
      { kind: "chat", fn: chatToggle },
      { kind: "files", fn: filesToggle },
      { kind: "editor", fn: editorToggle },
    ];
    for (const c of cases) {
      chatToggle.mockReset();
      filesToggle.mockReset();
      editorToggle.mockReset();
      activeKind = c.kind;
      toggleFindForActiveTab();
      expect(c.fn, `${c.kind} button should reach its own find`).toHaveBeenCalledTimes(1);
    }
  });

  it("reaches the same registered box the hotkey does, so button and chord agree", () => {
    // The button toggles where the chord opens.
    const find = pageFindStub();
    registerFind("docs", find);
    activeKind = "docs";
    toggleFindForActiveTab();
    expect(find.toggle).toHaveBeenCalledTimes(1);
    expect(find.open).not.toHaveBeenCalled();
    expect(chatToggle).not.toHaveBeenCalled();
  });

  it("is inert rather than wrong on a page whose box is not there", () => {
    activeKind = "history";
    expect(() => {
      toggleFindForActiveTab();
    }).not.toThrow();
    expect(chatToggle).not.toHaveBeenCalled();
  });
});

describe("findAffordanceForActiveTab", () => {
  // A visible magnifier with no answer is a dead door.
  it("is available on a chat tab and on the files browser", () => {
    for (const kind of ["chat", "files"] as TabKind[]) {
      activeKind = kind;
      expect(findAffordanceForActiveTab().available).toBe(true);
    }
  });

  it("is UNAVAILABLE on a page with no search at all", () => {
    for (const kind of ["settings", "run"] as TabKind[]) {
      activeKind = kind;
      expect(findAffordanceForActiveTab().available).toBe(false);
    }
  });

  it("follows the editor's own answer about the surface", () => {
    activeKind = "editor";
    editorClaims = true;
    expect(findAffordanceForActiveTab().available).toBe(true);
    editorClaims = false;
    expect(findAffordanceForActiveTab().available).toBe(false);
  });

  it("asks a page's own predicate, and defaults to available when it has none", () => {
    activeKind = "git";
    registerFind("git", pageFindStub({ available: () => false }));
    expect(findAffordanceForActiveTab().available).toBe(false);
    registerFind("git", pageFindStub());
    expect(findAffordanceForActiveTab().available).toBe(true);
  });

  it("is unavailable on a page that registered nothing", () => {
    activeKind = "docs";
    expect(findAffordanceForActiveTab().available).toBe(false);
  });

  it("calls the three built-in finds SEARCHES, because each reaches past the viewport", () => {
    // None of the three filters what is painted: each searches past the screen.
    for (const kind of ["chat", "files", "editor"] as TabKind[]) {
      activeKind = kind;
      expect(findAffordanceForActiveTab().kind, kind).toBe("search");
    }
  });

  it("takes a page's OWN word for which of the two it is", () => {
    activeKind = "history";
    registerFind("history", pageFindStub({ kind: () => "search" }));
    expect(findAffordanceForActiveTab().kind).toBe("search");
    registerFind("history", pageFindStub({ kind: () => "filter" }));
    expect(findAffordanceForActiveTab().kind).toBe("filter");
  });
});
