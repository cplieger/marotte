// The mode pill's choices as a native select, for the Chat options card when the prompt row has no room for the
// pill: the same catalog, the chat's mode selected, and a pick sent through the pill's own action.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type { SessionMode } from "./types.js";

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

const mocks = vi.hoisted(() => ({
  setModeDispatch: vi.fn(),
  createSession: vi.fn<() => Promise<string>>(),
}));

vi.mock("./chat.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  createSession: mocks.createSession,
}));
vi.mock("./actions/chat.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  setMode: { dispatch: mocks.setModeDispatch },
}));
const live = await vi.hoisted(async () => {
  const { signal } = await import("@cplieger/reactive");
  return { active: signal<{ id: string; current_mode_id: string } | undefined>(undefined) };
});
vi.mock("./store.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  getActive: () => live.active.peek(),
  activeSession: live.active,
}));

const { modeSelect } = await import("./role-picker.js");
const { setCatalogModes } = await import("./roles.js");

const CATALOG: readonly SessionMode[] = [
  { id: "vibe", name: "Default", source: "bundled" },
  { id: "plan", name: "Plan", source: "bundled" },
  { id: "reviewer", name: "reviewer", source: "workspace" },
];

let select: HTMLSelectElement;

beforeEach(() => {
  vi.clearAllMocks();
  setCatalogModes(CATALOG);
  live.active.value = { id: "c-1", current_mode_id: "plan" };
  select = modeSelect("mode-select");
  document.body.append(select);
});

afterEach(() => {
  select.remove();
});

function pick(value: string): void {
  select.value = value;
  select.dispatchEvent(new Event("change"));
}

describe("the mode select", () => {
  it("lists the card's modes, custom agents in their own group, with the chat's mode selected", () => {
    expect([...select.options].map((o) => [o.value, o.text])).toEqual([
      ["vibe", "Default"],
      ["plan", "Plan"],
      ["reviewer", "Reviewer"],
    ]);
    expect(select.querySelector("optgroup")?.label).toBe("Custom agents");
    expect(select.querySelector("optgroup option")?.getAttribute("value")).toBe("reviewer");
    expect(select.value).toBe("plan");
  });

  it("sends a pick for the chat on screen, as the pill does", () => {
    pick("reviewer");
    expect(mocks.setModeDispatch).toHaveBeenCalledWith({ chatID: "c-1", modeID: "reviewer" });
  });

  it("follows the chat: another chat's mode, and a mode put back after a refusal", () => {
    live.active.value = { id: "c-2", current_mode_id: "vibe" };
    expect(select.value).toBe("vibe");
    pick("plan");
    live.active.value = { id: "c-2", current_mode_id: "vibe" };
    expect(select.value).toBe("vibe");
  });

  it("follows the catalog when a custom agent lands", () => {
    setCatalogModes([...CATALOG, { id: "late", name: "late", source: "global" }]);
    expect([...select.options].map((o) => o.value)).toContain("late");
    expect(select.value).toBe("plan");
  });
});
