// The composer's security profile pill: what it names, what a pick sends, and the notes its card owes.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type { SecurityProfileView } from "./wire/types.gen.js";
import { ICON_SHIELD } from "./icons.js";

// The real chat.js the spreads below load reads these app-shell elements at module load.
vi.hoisted(() => {
  for (const id of [
    "messages",
    "messages-wrap",
    "messages-wrap-outer",
    "chat-view",
    "scroll-bottom",
  ]) {
    const el = document.createElement("div");
    el.id = id;
    document.body.append(el);
  }
});

type SSEFn = (chatID: string, payload?: unknown) => void;
type Outcome = { status: "success"; value: unknown } | { status: "error"; error: unknown };
/** What `setChatProfile.dispatch` returns: the result, and the outcome the pill reads a refusal from. */
type Handle = Promise<unknown> & { outcome: Promise<Outcome>; abort: () => void };

const mocks = vi.hoisted(() => ({
  apiGetTyped: vi.fn<(path: string) => Promise<SecurityProfileView | null>>(),
  dispatch: vi.fn<(args: { chatID: string; profile: string }) => Handle>(),
  createSession: vi.fn<() => Promise<string>>(),
  confirm: vi.fn<() => Promise<boolean>>(),
  sse: new Map<string, SSEFn>(),
  expand: undefined as (() => void) | undefined,
}));

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  apiGetTyped: mocks.apiGetTyped,
}));
vi.mock("./actions/chat.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  setChatProfile: { dispatch: mocks.dispatch },
}));
vi.mock("./chat.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  createSession: mocks.createSession,
}));
vi.mock("./confirm.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  confirm: mocks.confirm,
}));
vi.mock("./bus.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  onSSE: (type: string, fn: SSEFn) => {
    mocks.sse.set(type, fn);
    return () => mocks.sse.delete(type);
  },
}));
vi.mock("./pill-expand.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  makeExpandable: (_pill: HTMLElement, _card: HTMLElement, opts: { onExpand: () => void }) => {
    mocks.expand = opts.onExpand;
    return { open: opts.onExpand };
  },
  collapseAll: vi.fn(),
}));
vi.mock("./legacy-ui/roving-focus.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  rovingFocus: () => ({ refresh: vi.fn() }),
}));
// The chat on screen and whether its turn runs, as the store's tracked reads report them.
const live = await vi.hoisted(async () => {
  const { signal } = await import("@cplieger/reactive");
  return {
    active: signal<{ id: string; security_profile?: string } | undefined>(undefined),
    busy: signal<ReadonlySet<string>>(new Set()),
  };
});
vi.mock("./store.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  activeSession: live.active,
  getActive: () => live.active.peek(),
  watchBusy: (chatID: string) => live.busy.value.has(chatID),
}));

const { initProfilePill, profileNote, profileSelect } = await import("./profile-pill.js");

/** The ladder as policyfile.Profiles() orders it, Custom last. */
const LADDER = ["guarded", "read-only", "trusted", "delegated", "unrestricted", "custom"].map(
  (id) => ({ id, presets: [] }),
);

function view(over: Partial<SecurityProfileView> = {}): SecurityProfileView {
  return { profiles: LADDER, default: "trusted", saved_grants: false, ...over };
}

async function flush(): Promise<void> {
  for (let i = 0; i < 8; i++) {
    await Promise.resolve();
  }
}

function pill(): HTMLButtonElement {
  return document.getElementById("profile-pill") as HTMLButtonElement;
}
function card(): HTMLElement {
  return document.getElementById("profile-list") as HTMLElement;
}
function notes(): string[] {
  return [...card().querySelectorAll(".pill-role-hint")].map((n) => n.textContent ?? "");
}
function row(id: string): HTMLButtonElement {
  const found = card().querySelector<HTMLButtonElement>(`[data-profile-id="${id}"]`);
  if (found === null) {
    throw new Error(`no row for ${id}`);
  }
  return found;
}
async function open(): Promise<void> {
  mocks.expand?.();
  await flush();
}

/** `setChatProfile` as the pill sees it: the pick shows on its chat at once, and a refusal puts the old one back. */
function settlePick(chatID: string, profile: string, outcome: Promise<Outcome>): Handle {
  const before = live.active.peek();
  const prev = before?.id === chatID ? before.security_profile : undefined;
  if (before?.id === chatID) {
    live.active.value = { ...before, security_profile: profile };
  }
  const settled = outcome.then((o) => {
    const now = live.active.peek();
    if (o.status === "error" && now?.id === chatID) {
      live.active.value = prev === undefined ? { id: chatID } : { ...now, security_profile: prev };
    }
    return o;
  });
  return Object.assign(
    settled.then(() => undefined),
    { outcome: settled, abort: vi.fn() },
  );
}
function answerPicks(outcome: Promise<Outcome>): void {
  mocks.dispatch.mockImplementation(({ chatID, profile }) => settlePick(chatID, profile, outcome));
}
/** Each pick waits for its own answer: the returned functions accept or refuse the nth pick sent. */
function answerEachPick(): { accept: (n: number) => void; refuse: (n: number) => void } {
  const answers: ((o: Outcome) => void)[] = [];
  mocks.dispatch.mockImplementation(({ chatID, profile }) =>
    settlePick(
      chatID,
      profile,
      new Promise((resolve) => {
        answers.push(resolve);
      }),
    ),
  );
  const answer = (n: number, o: Outcome): void => {
    const resolve = answers[n];
    if (resolve === undefined) {
      throw new Error(`no pick ${String(n)} was sent`);
    }
    resolve(o);
  };
  return {
    accept: (n) => {
      answer(n, { status: "success", value: undefined });
    },
    refuse: (n) => {
      answer(n, { status: "error", error: new Error("refused") });
    },
  };
}

function rowIds(): (string | undefined)[] {
  return [...card().querySelectorAll<HTMLElement>("[data-profile-id]")].map(
    (r) => r.dataset["profileId"],
  );
}

let initialised = false;

beforeEach(async () => {
  vi.clearAllMocks();
  document.body.innerHTML = `
    <button id="profile-pill" class="pill hidden"><span id="profile-pill-icon"></span><span id="profile-pill-label"></span></button>
    <span id="profile-list"></span>`;
  mocks.apiGetTyped.mockResolvedValue(view());
  answerPicks(Promise.resolve({ status: "success", value: undefined }));
  mocks.confirm.mockResolvedValue(true);
  live.busy.value = new Set();
  live.active.value = { id: "c-1" };
  if (!initialised) {
    initProfilePill();
    initialised = true;
  } else {
    mocks.sse.get("permissions_changed")?.("");
  }
  await flush();
});

afterEach(() => {
  document.body.replaceChildren();
});

describe("the profile pill's name", () => {
  it("names the chat's own profile over the one new chats start on", async () => {
    live.active.value = { id: "c-1", security_profile: "read-only" };
    await flush();
    expect(pill().getAttribute("aria-label")).toBe("Security profile, currently Read-only");
    expect(pill().getAttribute("data-tooltip")).toBe(pill().getAttribute("aria-label"));
    expect(document.getElementById("profile-pill-label")?.textContent).toBe("Read-only");
  });

  it("names the Settings profile for a chat that has none yet", async () => {
    mocks.apiGetTyped.mockResolvedValue(view({ default: "guarded" }));
    mocks.sse.get("permissions_changed")?.("");
    await flush();
    expect(pill().getAttribute("aria-label")).toBe("Security profile, currently Guarded");
    expect(pill().classList.contains("hidden")).toBe(false);
  });

  it("reads the profiles from the profile endpoint again on a permissions change", async () => {
    mocks.apiGetTyped.mockClear();
    mocks.sse.get("permissions_changed")?.("");
    await flush();
    expect(mocks.apiGetTyped).toHaveBeenCalledWith(
      "/api/permissions/profile",
      expect.any(Function),
      expect.any(AbortSignal),
    );
  });
});

describe("picking a profile", () => {
  it("marks the profile in force as the selected row, in the server's order", async () => {
    live.active.value = { id: "c-1", security_profile: "delegated" };
    await open();
    expect(rowIds()).toEqual(["guarded", "read-only", "trusted", "delegated", "unrestricted"]);
    expect(row("delegated").getAttribute("aria-selected")).toBe("true");
    expect(row("trusted").getAttribute("aria-selected")).toBe("false");
  });

  it("sends the picked profile for the chat on screen", async () => {
    await open();
    row("read-only").click();
    await flush();
    expect(mocks.dispatch).toHaveBeenCalledWith({ chatID: "c-1", profile: "read-only" });
  });

  it("sends nothing for the profile already in force", async () => {
    await open();
    row("trusted").click();
    await flush();
    expect(mocks.dispatch).not.toHaveBeenCalled();
  });

  it("asks before Unrestricted and sends nothing when declined", async () => {
    mocks.confirm.mockResolvedValue(false);
    await open();
    row("unrestricted").click();
    await flush();
    expect(mocks.confirm).toHaveBeenCalledTimes(1);
    expect(mocks.dispatch).not.toHaveBeenCalled();
  });

  it("describes each profile without pointing at the Settings page's layout", async () => {
    mocks.apiGetTyped.mockResolvedValue(view({ default: "custom" }));
    mocks.sse.get("permissions_changed")?.("");
    await flush();
    await open();
    for (const p of LADDER) {
      expect(row(p.id).dataset["tooltip"], p.id).not.toMatch(/below|above/);
    }
  });

  it("creates the chat first when none is on screen", async () => {
    live.active.value = undefined;
    mocks.createSession.mockResolvedValue("c-new");
    await open();
    row("guarded").click();
    await flush();
    expect(mocks.dispatch).toHaveBeenCalledWith({ chatID: "c-new", profile: "guarded" });
  });
});

describe("the card's notes", () => {
  it("says a pick applies after the turn while one runs", async () => {
    live.busy.value = new Set(["c-1"]);
    await open();
    expect(notes()).toContain("Applies after this turn");
  });

  it("says nothing about the turn when the chat is idle", async () => {
    await open();
    expect(notes()).not.toContain("Applies after this turn");
  });

  it("says saved rules still apply where the files grant on their own", async () => {
    mocks.apiGetTyped.mockResolvedValue(view({ saved_grants: true }));
    mocks.sse.get("permissions_changed")?.("");
    await flush();
    await open();
    expect(notes()).toContain("Your saved rules still apply");
  });

  it("says nothing about saved rules on Unrestricted or Custom, or with none saved", async () => {
    mocks.apiGetTyped.mockResolvedValue(view({ saved_grants: true }));
    mocks.sse.get("permissions_changed")?.("");
    for (const id of ["unrestricted", "custom"]) {
      live.active.value = { id: "c-1", security_profile: id };
      await open();
      expect(notes(), id).not.toContain("Your saved rules still apply");
    }
    mocks.apiGetTyped.mockResolvedValue(view({ saved_grants: false }));
    mocks.sse.get("permissions_changed")?.("");
    live.active.value = { id: "c-1", security_profile: "trusted" };
    await open();
    expect(notes()).not.toContain("Your saved rules still apply");
  });
});

describe("the Custom profile in the card", () => {
  it("is not listed while neither Settings nor the chat uses it", async () => {
    await open();
    expect(rowIds()).not.toContain("custom");
    expect(rowIds()).toContain("trusted");
  });

  it("is listed when it is the profile new chats start on", async () => {
    mocks.apiGetTyped.mockResolvedValue(view({ default: "custom" }));
    mocks.sse.get("permissions_changed")?.("");
    live.active.value = { id: "c-1", security_profile: "trusted" };
    await flush();
    await open();
    expect(rowIds()).toContain("custom");
    expect(row("custom").getAttribute("aria-selected")).toBe("false");
  });

  it("is listed, and selected, for a chat already on it", async () => {
    live.active.value = { id: "c-1", security_profile: "custom" };
    await open();
    expect(rowIds()).toContain("custom");
    expect(row("custom").getAttribute("aria-selected")).toBe("true");
  });

  it("leaves the card once the Settings profile moves off it", async () => {
    mocks.apiGetTyped.mockResolvedValue(view({ default: "custom" }));
    mocks.sse.get("permissions_changed")?.("");
    await flush();
    await open();
    expect(rowIds()).toContain("custom");
    mocks.apiGetTyped.mockResolvedValue(view({ default: "guarded" }));
    mocks.sse.get("permissions_changed")?.("");
    await flush();
    expect(rowIds()).not.toContain("custom");
  });
});

describe("a pick made during a turn", () => {
  const PENDING = /applies after this turn/i;

  it("shows the picked profile at once, marked as waiting for the turn", async () => {
    live.busy.value = new Set(["c-1"]);
    await open();
    row("read-only").click();
    await flush();
    expect(document.getElementById("profile-pill-label")?.textContent).toBe("Read-only");
    expect(pill().classList.contains("pending"), "the pending mark").toBe(true);
    const name = pill().getAttribute("aria-label") ?? "";
    expect(name).toContain("Read-only");
    expect(name).toMatch(PENDING);
    expect(pill().getAttribute("data-tooltip")).toBe(name);
  });

  it("drops the mark when the turn ends and the chat reloads on it", async () => {
    live.busy.value = new Set(["c-1"]);
    await open();
    row("read-only").click();
    await flush();
    expect(pill().classList.contains("pending")).toBe(true);
    live.busy.value = new Set();
    await flush();
    expect(pill().classList.contains("pending")).toBe(false);
    expect(pill().getAttribute("aria-label")).toBe("Security profile, currently Read-only");
    expect(pill().getAttribute("data-tooltip")).toBe(pill().getAttribute("aria-label"));
  });

  it("puts the old profile back, unmarked, when the server refuses it", async () => {
    let refuse!: () => void;
    answerPicks(
      new Promise((resolve) => {
        refuse = () => {
          resolve({ status: "error", error: new Error("refused") });
        };
      }),
    );
    live.busy.value = new Set(["c-1"]);
    await open();
    row("read-only").click();
    await flush();
    expect(pill().classList.contains("pending"), "marked while the server answers").toBe(true);
    refuse();
    await flush();
    expect(document.getElementById("profile-pill-label")?.textContent).toBe("Trusted");
    expect(pill().classList.contains("pending")).toBe(false);
    expect(pill().getAttribute("aria-label")).toBe("Security profile, currently Trusted");
  });

  it("marks nothing for a pick made while the chat is idle", async () => {
    await open();
    row("read-only").click();
    await flush();
    expect(document.getElementById("profile-pill-label")?.textContent).toBe("Read-only");
    expect(pill().classList.contains("pending")).toBe(false);
    expect(pill().getAttribute("aria-label")).not.toMatch(PENDING);
  });

  it("marks only the chat the pick was made in", async () => {
    live.busy.value = new Set(["c-1"]);
    await open();
    row("read-only").click();
    await flush();
    live.active.value = { id: "c-2", security_profile: "guarded" };
    await flush();
    expect(pill().classList.contains("pending")).toBe(false);
    expect(pill().getAttribute("aria-label")).toBe("Security profile, currently Guarded");
  });

  it("keeps a chat's mark while a pick in another busy chat waits too", async () => {
    live.busy.value = new Set(["c-1", "c-2"]);
    await open();
    row("read-only").click();
    await flush();
    live.active.value = { id: "c-2", security_profile: "trusted" };
    await open();
    row("guarded").click();
    await flush();
    expect(pill().classList.contains("pending"), "the second chat's mark").toBe(true);
    live.active.value = { id: "c-1", security_profile: "read-only" };
    await flush();
    expect(pill().classList.contains("pending"), "the first chat's mark").toBe(true);
    expect(pill().getAttribute("aria-label")).toMatch(PENDING);
  });

  async function pickRow(id: string): Promise<void> {
    await open();
    row(id).click();
    await flush();
  }
  function shown(): string {
    return document.getElementById("profile-pill-label")?.textContent ?? "";
  }

  it("keeps the newer pick's mark when the server refuses an earlier one", async () => {
    const server = answerEachPick();
    live.busy.value = new Set(["c-1"]);
    await pickRow("read-only");
    await pickRow("guarded");
    server.refuse(0);
    await flush();
    expect(shown()).toBe("Guarded");
    expect(pill().classList.contains("pending")).toBe(true);
    expect(pill().getAttribute("aria-label")).toBe(
      "Security profile, Guarded, applies after this turn",
    );
  });

  it("keeps a re-picked profile's mark when the server refuses its first pick", async () => {
    const server = answerEachPick();
    live.busy.value = new Set(["c-1"]);
    await pickRow("read-only");
    await pickRow("guarded");
    await pickRow("read-only");
    server.refuse(0);
    await flush();
    expect(shown()).toBe("Read-only");
    expect(pill().getAttribute("aria-label")).toBe(
      "Security profile, Read-only, applies after this turn",
    );
  });

  it("goes back to the accepted pick, still marked, when the server refuses the next", async () => {
    const server = answerEachPick();
    live.busy.value = new Set(["c-1"]);
    await pickRow("read-only");
    server.accept(0);
    await flush();
    await pickRow("guarded");
    server.refuse(1);
    await flush();
    expect(shown()).toBe("Read-only");
    expect(pill().getAttribute("aria-label")).toBe(
      "Security profile, Read-only, applies after this turn",
    );
  });

  it("settles a refusal in its own chat only", async () => {
    const server = answerEachPick();
    live.busy.value = new Set(["c-1", "c-2"]);
    await pickRow("read-only");
    live.active.value = { id: "c-2", security_profile: "trusted" };
    await pickRow("guarded");
    server.refuse(1);
    await flush();
    expect(pill().classList.contains("pending"), "c-2's refused pick").toBe(false);
    expect(shown()).toBe("Trusted");
    live.active.value = { id: "c-1", security_profile: "read-only" };
    await flush();
    expect(pill().getAttribute("aria-label")).toBe(
      "Security profile, Read-only, applies after this turn",
    );
  });

  it("drops only the mark of the chat whose turn ended", async () => {
    live.busy.value = new Set(["c-1", "c-2"]);
    await pickRow("read-only");
    live.active.value = { id: "c-2", security_profile: "trusted" };
    await pickRow("guarded");
    live.busy.value = new Set(["c-1"]);
    await flush();
    expect(pill().classList.contains("pending"), "c-2, whose turn ended").toBe(false);
    expect(pill().getAttribute("aria-label")).toBe("Security profile, currently Guarded");
    live.active.value = { id: "c-1", security_profile: "read-only" };
    await flush();
    expect(pill().classList.contains("pending"), "c-1, whose turn runs").toBe(true);
  });
});

describe("the profile select the Chat options card shows in the pill's place", () => {
  function mountSelect(): HTMLSelectElement {
    const select = profileSelect("profile-select");
    document.body.append(select);
    return select;
  }
  function pick(select: HTMLSelectElement, value: string): void {
    select.value = value;
    select.dispatchEvent(new Event("change"));
  }
  const values = (select: HTMLSelectElement): string[] => [...select.options].map((o) => o.value);

  it("lists the ladder with the chat's profile selected, Custom only where it applies", async () => {
    const select = mountSelect();
    expect(values(select)).toEqual([
      "guarded",
      "read-only",
      "trusted",
      "delegated",
      "unrestricted",
    ]);
    expect(select.value).toBe("trusted");
    expect(select.selectedOptions[0]?.text).toBe("Trusted");
    live.active.value = { id: "c-1", security_profile: "custom" };
    expect(values(select)).toContain("custom");
    expect(select.value).toBe("custom");
    live.active.value = { id: "c-1", security_profile: "trusted" };
    mocks.apiGetTyped.mockResolvedValue(view({ default: "custom" }));
    mocks.sse.get("permissions_changed")?.("");
    await flush();
    expect(values(select)).toContain("custom");
    expect(select.value).toBe("trusted");
  });

  it("sends a pick for the chat on screen, as the pill does", async () => {
    const select = mountSelect();
    pick(select, "read-only");
    await flush();
    expect(mocks.dispatch).toHaveBeenCalledWith({ chatID: "c-1", profile: "read-only" });
    expect(select.value).toBe("read-only");
    expect(document.getElementById("profile-pill-label")?.textContent).toBe("Read-only");
  });

  it("asks before Unrestricted, and goes back to the chat's profile when declined", async () => {
    mocks.confirm.mockResolvedValue(false);
    const select = mountSelect();
    pick(select, "unrestricted");
    await flush();
    expect(mocks.confirm).toHaveBeenCalledTimes(1);
    expect(mocks.dispatch).not.toHaveBeenCalled();
    expect(select.value).toBe("trusted");
  });

  it("marks a pick made during a turn as waiting, as the pill does, and says why", async () => {
    const select = mountSelect();
    live.busy.value = new Set(["c-1"]);
    await flush();
    expect(profileNote()).toBe("Applies after this turn");
    pick(select, "guarded");
    await flush();
    expect(select.classList.contains("pending"), "the select's pending mark").toBe(true);
    expect(pill().classList.contains("pending"), "the pill's, the same state").toBe(true);
    live.busy.value = new Set();
    await flush();
    expect(select.classList.contains("pending")).toBe(false);
    expect(profileNote()).toBe("");
  });
});

describe("each profile's shield", () => {
  /** The ranked rungs, most protective first, as policyfile.Profiles orders what each one grants. */
  const BY_PROTECTION = ["guarded", "read-only", "trusted", "delegated", "unrestricted"];
  const LABELS: Record<string, string> = {
    guarded: "Guarded",
    "read-only": "Read-only",
    trusted: "Trusted",
    delegated: "Approve for me",
    unrestricted: "Unrestricted",
    custom: "Custom",
  };
  const OUTLINE = new DOMParser()
    .parseFromString(ICON_SHIELD, "image/svg+xml")
    .querySelector("path")
    ?.getAttribute("d");

  function iconOf(host: Element): SVGSVGElement {
    const svg = host.querySelector("svg");
    if (svg === null) {
      throw new Error("no icon");
    }
    return svg;
  }
  function shapes(svg: SVGSVGElement): SVGGraphicsElement[] {
    return [...svg.querySelectorAll<SVGGraphicsElement>("path, line, rect, circle, polyline")];
  }
  /** A line is a stroke with width and no height. */
  function lines(svg: SVGSVGElement): number {
    return shapes(svg).filter((g) => {
      const b = g.getBBox();
      return b.height < 0.01 && b.width > 1;
    }).length;
  }

  async function openOn(defaultProfile: string): Promise<void> {
    mocks.apiGetTyped.mockResolvedValue(view({ default: defaultProfile }));
    mocks.sse.get("permissions_changed")?.("");
    await flush();
    await open();
  }

  it("draws the shield's outline on every row, before the profile's name", async () => {
    await openOn("custom");
    for (const id of Object.keys(LABELS)) {
      const svg = iconOf(row(id));
      expect(
        shapes(svg).some((g) => g.getAttribute("d") === OUTLINE),
        `${id}: the shield's outline`,
      ).toBe(true);
      const name = row(id).querySelector(".pill-role-name");
      expect(name?.textContent, `${id}: the name`).toBe(LABELS[id]);
      expect(
        svg.compareDocumentPosition(name!) & Node.DOCUMENT_POSITION_FOLLOWING,
        `${id}: the icon comes first`,
      ).toBeTruthy();
      expect(row(id).textContent, `${id}: the icon adds no text`).toBe(LABELS[id]);
      expect(svg.closest('[aria-hidden="true"]'), `${id}: decorative`).not.toBeNull();
    }
  });

  it("draws more lines for more protection, and none for Unrestricted", async () => {
    await openOn("custom");
    const counts = BY_PROTECTION.map((id) => lines(iconOf(row(id))));
    expect(counts).toEqual([4, 3, 2, 1, 0]);
  });

  it("marks Custom, which has no rank, with something other than lines", async () => {
    await openOn("custom");
    const custom = iconOf(row("custom"));
    expect(lines(custom)).toBe(0);
    expect(shapes(custom).length, "a mark inside the outline").toBeGreaterThan(1);
    expect(custom.innerHTML).not.toBe(iconOf(row("unrestricted")).innerHTML);
  });

  it("shows the chat's profile's shield on the pill", async () => {
    await openOn("custom");
    const icon = document.getElementById("profile-pill-icon")!;
    for (const id of Object.keys(LABELS)) {
      live.active.value = { id: "c-1", security_profile: id };
      await flush();
      expect(iconOf(icon).outerHTML, id).toBe(iconOf(row(id)).outerHTML);
      expect(iconOf(icon).closest('[aria-hidden="true"]'), `${id}: decorative`).not.toBeNull();
      expect(document.getElementById("profile-pill-label")?.textContent, id).toBe(LABELS[id]);
    }
  });

  it("keeps the pill's shield on the level in force across a pick, a turn's pick and its reload", async () => {
    await openOn("trusted");
    const pillShield = (): string =>
      iconOf(document.getElementById("profile-pill-icon")!).innerHTML;
    const rowShield = (id: string): string => iconOf(row(id)).innerHTML;

    row("read-only").click();
    await flush();
    await open();
    expect(pillShield(), "an idle pick").toBe(rowShield("read-only"));

    live.busy.value = new Set(["c-1"]);
    await flush();
    row("guarded").click();
    await flush();
    await open();
    expect(pillShield(), "a pick waiting for the turn").toBe(rowShield("guarded"));

    live.busy.value = new Set();
    live.active.value = { id: "c-1", security_profile: "guarded" };
    await flush();
    expect(pillShield(), "the reload landed").toBe(rowShield("guarded"));
    expect(pill().classList.contains("pending")).toBe(false);
  });
});
