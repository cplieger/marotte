// The composer's security profile pill: what it names, what a pick sends, and the notes its card owes.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type { SecurityProfileView } from "./wire/types.gen.js";

type SSEFn = (chatID: string, payload?: unknown) => void;

const mocks = vi.hoisted(() => ({
  apiGetTyped: vi.fn<(path: string) => Promise<SecurityProfileView | null>>(),
  dispatch: vi.fn<(args: { chatID: string; profile: string }) => Promise<void>>(),
  createSession: vi.fn<() => Promise<string>>(),
  confirm: vi.fn<() => Promise<boolean>>(),
  sse: new Map<string, SSEFn>(),
  expand: undefined as (() => void) | undefined,
}));

vi.mock("./api-client.js", () => ({ apiGetTyped: mocks.apiGetTyped }));
vi.mock("./actions/chat.js", () => ({ setChatProfile: { dispatch: mocks.dispatch } }));
vi.mock("./chat.js", () => ({ createSession: mocks.createSession }));
vi.mock("./confirm.js", () => ({ confirm: mocks.confirm }));
vi.mock("./bus.js", () => ({
  onSSE: (type: string, fn: SSEFn) => {
    mocks.sse.set(type, fn);
    return () => mocks.sse.delete(type);
  },
}));
vi.mock("./pill-expand.js", () => ({
  makeExpandable: (_pill: HTMLElement, _card: HTMLElement, opts: { onExpand: () => void }) => {
    mocks.expand = opts.onExpand;
    return { open: opts.onExpand };
  },
  collapseAll: vi.fn(),
}));
vi.mock("./legacy-ui/roving-focus.js", () => ({ rovingFocus: () => ({ refresh: vi.fn() }) }));
// The chat on screen and whether its turn runs, as the store's tracked reads report them.
const live = await vi.hoisted(async () => {
  const { signal } = await import("@cplieger/reactive");
  return {
    active: signal<{ id: string; security_profile?: string } | undefined>(undefined),
    busy: signal(false),
  };
});
vi.mock("./store.js", () => ({
  activeSession: live.active,
  getActive: () => live.active.peek(),
  watchBusy: () => live.busy.value,
}));

const { initProfilePill } = await import("./profile-pill.js");

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

let initialised = false;

beforeEach(async () => {
  vi.clearAllMocks();
  document.body.innerHTML = `
    <button id="profile-pill" class="pill hidden"><span id="profile-pill-icon"></span><span id="profile-pill-label"></span></button>
    <span id="profile-list"></span>`;
  mocks.apiGetTyped.mockResolvedValue(view());
  mocks.dispatch.mockResolvedValue(undefined);
  mocks.confirm.mockResolvedValue(true);
  live.busy.value = false;
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
    const ids = [...card().querySelectorAll<HTMLElement>("[data-profile-id]")].map(
      (r) => r.dataset["profileId"],
    );
    expect(ids).toEqual(LADDER.map((p) => p.id));
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
    live.busy.value = true;
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
