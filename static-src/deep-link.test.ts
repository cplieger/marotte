// `settleDeepLinkedChat` for a pasted `/chat/<id>` with no store row. `router.js` is real: the URL guard's subject is
// the browser's location.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { admitLocation, settleDeepLinkedChat } from "./deep-link.js";

const {
  mockResolve,
  mockListLoaded,
  mockMayAnswer,
  mockToastError,
  mockActiveTabRoute,
  mockTabIdForRoute,
} = vi.hoisted(() => ({
  mockResolve: vi.fn(),
  mockListLoaded: vi.fn(() => true),
  mockMayAnswer: vi.fn(() => true),
  mockToastError: vi.fn(),
  mockActiveTabRoute: vi.fn(() => null as unknown),
  mockTabIdForRoute: vi.fn(() => ""),
}));

vi.mock("./chat.js", () => ({ resolveUnknownChat: mockResolve }));
vi.mock("./store-load.js", () => ({
  chatListLoaded: mockListLoaded,
  serverMayAnswer: mockMayAnswer,
}));
// Async factory with a dynamic import: a hoisted `vi.mock` factory runs before a static import initializes.
vi.mock("./tabs.js", async () => ({
  ...(await import("./__test-helpers__/tabs-mock.js")).tabsMock(),
  getActiveTabRoute: mockActiveTabRoute,
  tabIdForRoute: mockTabIdForRoute,
}));
vi.mock("./toast.js", () => ({ error: mockToastError }));

function at(path: string): void {
  history.replaceState(null, "", path);
}

function offeredRetry(): (() => void) | undefined {
  const retry = mockToastError.mock.calls[0]?.[1] as { onClick?: () => void } | undefined;
  return retry?.onClick;
}

let origin = "";

beforeEach(() => {
  origin = location.pathname + location.search + location.hash;
  vi.clearAllMocks();
  mockListLoaded.mockReturnValue(true);
  mockMayAnswer.mockReturnValue(true);
  mockActiveTabRoute.mockReturnValue(null);
  // The empty answer is `tabs.ts`'s own for a route no open tab carries.
  mockTabIdForRoute.mockReturnValue("");
});

afterEach(() => {
  // Leaving the runner on /chat/... breaks whatever loads next.
  history.replaceState(null, "", origin);
});

describe("settleDeepLinkedChat", () => {
  it("opens the chat and says nothing when the server knows it", async () => {
    // A refusal inside `resolveUnknownChat` raises its own notice, so a second here would report it twice.
    at("/chat/c-elsewhere");
    mockResolve.mockResolvedValue("opened");

    expect(await settleDeepLinkedChat("c-elsewhere")).toBe("opened");
    expect(mockToastError).not.toHaveBeenCalled();
    expect(location.pathname).toBe("/chat/c-elsewhere");
  });

  it("canonicalizes the URL and says so when the SERVER says the chat is gone", async () => {
    // The one terminal outcome, licensed only by the server reading its own store.
    at("/chat/c-deleted");
    mockActiveTabRoute.mockReturnValue({ kind: "chat", id: "c-open" });
    mockResolve.mockResolvedValue("gone");

    expect(await settleDeepLinkedChat("c-deleted")).toBe("gone");
    expect(location.pathname).toBe("/chat/c-open");
    expect(mockToastError).toHaveBeenCalledExactlyOnceWith("That conversation no longer exists.");
  });

  it("falls back to the empty chat route when no tab is active", async () => {
    at("/chat/c-deleted");
    mockResolve.mockResolvedValue("gone");

    expect(await settleDeepLinkedChat("c-deleted")).toBe("gone");
    expect(location.pathname).toBe("/");
  });

  // Nobody answered (5xx, timeout, network): no terminal claim, but a notice with a retry; no boot toast covers this.

  it("RAISES a non-terminal notice with a retry when nobody answered", async () => {
    at("/chat/c-real");
    mockResolve.mockResolvedValue("unresolved");

    expect(await settleDeepLinkedChat("c-real")).toBe("unresolved");
    expect(mockToastError).toHaveBeenCalledTimes(1);
    expect(offeredRetry()).toBeTypeOf("function");
  });

  it("does NOT claim the chat is gone in that notice", async () => {
    // Asserted on the words: anything terminal here is the false claim this path refuses.
    at("/chat/c-real");
    mockResolve.mockResolvedValue("unresolved");

    await settleDeepLinkedChat("c-real");
    const said = String(mockToastError.mock.calls[0]?.[0]);
    expect(said).not.toMatch(/no longer exists|deleted|gone/i);
    expect(said).toMatch(/didn't answer|did not answer/i);
  });

  it("HOLDS the URL, so the retry and a reload both still address the chat", async () => {
    // Canonicalizing would make the retry, a reload or a re-share ask about the fallback view.
    at("/chat/c-real");
    mockResolve.mockResolvedValue("unresolved");

    await settleDeepLinkedChat("c-real");
    expect(location.pathname).toBe("/chat/c-real");
  });

  it("re-ASKS the server on retry rather than reloading the page", async () => {
    // The retry re-enters the same door instead of reloading (which drops SSE and tab state).
    at("/chat/c-real");
    mockResolve.mockResolvedValue("unresolved");
    await settleDeepLinkedChat("c-real");
    expect(mockResolve).toHaveBeenCalledTimes(1);

    mockResolve.mockResolvedValue("opened");
    offeredRetry()?.();
    await vi.waitFor(() => {
      expect(mockResolve).toHaveBeenCalledTimes(2);
    });
    expect(mockResolve).toHaveBeenLastCalledWith("c-real");
  });

  it("stays SILENT on an unresolved when boot has already raised a notice", async () => {
    // Boot already toasted an unlatched list's failure, so this would be a second copy. The URL is held either way.
    at("/chat/c-real");
    mockListLoaded.mockReturnValue(false);
    mockResolve.mockResolvedValue("unresolved");

    expect(await settleDeepLinkedChat("c-real")).toBe("held");
    expect(mockToastError).not.toHaveBeenCalled();
    expect(location.pathname).toBe("/chat/c-real");
  });

  // The ask gate: with evidence the server cannot answer, hold the URL and stay quiet.

  it("does not ask at all when there is evidence the server cannot answer", async () => {
    at("/chat/c-real");
    mockMayAnswer.mockReturnValue(false);

    expect(await settleDeepLinkedChat("c-real")).toBe("held");
    expect(mockResolve).not.toHaveBeenCalled();
    expect(mockToastError).not.toHaveBeenCalled();
    expect(location.pathname).toBe("/chat/c-real");
  });

  // The URL guard: a late verdict about an older location must not replace a newer one.

  it("drops a late `gone` verdict when the reader has moved to another chat", async () => {
    at("/chat/c-asked");
    mockActiveTabRoute.mockReturnValue({ kind: "chat", id: "c-open" });
    mockResolve.mockImplementation(() => {
      at("/chat/c-moved-on");
      return Promise.resolve("gone");
    });

    expect(await settleDeepLinkedChat("c-asked")).toBe("stale");
    expect(location.pathname).toBe("/chat/c-moved-on");
    expect(mockToastError).not.toHaveBeenCalled();
  });

  it("drops a late `gone` verdict when the reader has moved to another VIEW", async () => {
    // The guard compares kind as well as id: a reader on /files is owed no claim about a conversation.
    at("/chat/c-asked");
    mockResolve.mockImplementation(() => {
      at("/files/src");
      return Promise.resolve("gone");
    });

    expect(await settleDeepLinkedChat("c-asked")).toBe("stale");
    expect(location.pathname).toBe("/files/src");
  });

  it("drops a late `unresolved` verdict too, so no notice chases an abandoned link", async () => {
    at("/chat/c-asked");
    mockResolve.mockImplementation(() => {
      at("/chat/c-moved-on");
      return Promise.resolve("unresolved");
    });

    expect(await settleDeepLinkedChat("c-asked")).toBe("stale");
    expect(mockToastError).not.toHaveBeenCalled();
  });

  it("acts on a verdict for the SAME id even if the URL was rewritten to itself", async () => {
    // Compared on id, not on the visit, so an ordinary re-entry is not stale.
    at("/chat/c-asked");
    mockResolve.mockImplementation(() => {
      at("/chat/c-asked");
      return Promise.resolve("gone");
    });

    expect(await settleDeepLinkedChat("c-asked")).toBe("gone");
    expect(mockToastError).toHaveBeenCalledTimes(1);
  });

  it("opens a chat that resolved while the reader was elsewhere", async () => {
    // The guard gates the URL rewrite and notice, not the open.
    at("/chat/c-asked");
    mockResolve.mockImplementation(() => {
      at("/files/src");
      return Promise.resolve("opened");
    });

    expect(await settleDeepLinkedChat("c-asked")).toBe("opened");
    expect(location.pathname).toBe("/files/src");
  });
});

// `admitLocation`: a woken device's restored URL naming a tab closed elsewhere must not re-open it (`openTab` is a
// server mutation that broadcasts). A restored location is `active_view`'s twin and gets the same guard.

describe("admitLocation", () => {
  it("admits a deliberate navigation to a tab nothing has open", () => {
    expect.assertions(1);
    mockTabIdForRoute.mockReturnValue("");

    expect(admitLocation({ kind: "docs", tab: "hooks" }, "deeplink")).toBe("opens");
  });

  it("admits a RESTORED location whose tab is still open", () => {
    expect.assertions(1);
    mockTabIdForRoute.mockReturnValue("t-docs");

    expect(admitLocation({ kind: "docs", tab: "hooks" }, "restore")).toBe("opens");
  });

  it("refuses a RESTORED location whose tab was closed on another device", () => {
    // `openTab` is a server mutation, so admitting this re-created the closed tab on every screen.
    expect.assertions(1);
    mockTabIdForRoute.mockReturnValue("");

    expect(admitLocation({ kind: "docs", tab: "hooks" }, "restore")).toBe("canonicalized");
  });

  // Files routes pass on the same terms, no per-kind carve-out: a non-empty `tabIdForRoute` answer (folder resolution
  // is `tabs.ts`'s) admits.
  it("admits a files location on the strength of the resolver's answer alone", () => {
    expect.assertions(2);
    mockTabIdForRoute.mockReturnValue("t-files");
    expect(admitLocation({ kind: "files", path: "/workspace/_ui-qa" }, "history")).toBe("opens");

    mockTabIdForRoute.mockReturnValue("");
    expect(admitLocation({ kind: "files", path: "/workspace/_ui-qa" }, "history")).toBe(
      "canonicalized",
    );
  });

  it("points a refused location at the tab that IS active", () => {
    expect.assertions(1);
    at("/chat/c-gone");
    mockTabIdForRoute.mockReturnValue("");
    mockActiveTabRoute.mockReturnValue({ kind: "chat", id: "c-survivor" });

    admitLocation({ kind: "chat", id: "c-gone" }, "restore");

    expect(location.pathname).toBe("/chat/c-survivor");
  });

  it("points a refused location at the empty state when no tab survived", () => {
    expect.assertions(1);
    at("/chat/c-gone");
    mockTabIdForRoute.mockReturnValue("");
    mockActiveTabRoute.mockReturnValue(null);

    admitLocation({ kind: "chat", id: "c-gone" }, "restore");

    expect(location.pathname).toBe("/");
  });

  it("refuses a back press onto a tab this device closed, as it always has", () => {
    expect.assertions(1);
    mockTabIdForRoute.mockReturnValue("");

    expect(admitLocation({ kind: "history" }, "history")).toBe("canonicalized");
  });

  // The docs router arm forces a sub-tab, so admitting a back press would also move the panel. No carve-out.
  it("refuses a back press onto a closed docs tab, sub-tab and all", () => {
    expect.assertions(1);
    mockTabIdForRoute.mockReturnValue("");

    expect(admitLocation({ kind: "docs", tab: "hooks" }, "history")).toBe("canonicalized");
  });

  it("leaves the URL alone for every location it admits", () => {
    // A canonicalizing guard would rewrite the address bar under a deliberate navigation.
    expect.assertions(2);
    at("/chat/c-linked");
    mockTabIdForRoute.mockReturnValue("");
    mockActiveTabRoute.mockReturnValue({ kind: "chat", id: "c-other" });

    expect(admitLocation({ kind: "chat", id: "c-linked" }, "deeplink")).toBe("opens");
    expect(location.pathname).toBe("/chat/c-linked");
  });
});
