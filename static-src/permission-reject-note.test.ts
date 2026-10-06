// The permission card's optional deny note: when it renders, what a deny sends, and that an allow
// never carries it.

import { vi, describe, it, expect, beforeEach } from "vitest";
import type * as SettingsHighlight from "./settings-highlight.js";
import type * as PermissionActions from "./actions/permissions.js";
import type * as Navigate from "./navigate.js";
import type { PermissionNeededPayload } from "./types.js";

vi.mock("./settings-highlight.js", async (importOriginal) => ({
  ...(await importOriginal<typeof SettingsHighlight>()),
  openSetting: vi.fn(),
}));
vi.mock("./actions/permissions.js", async (importOriginal) => ({
  ...(await importOriginal<typeof PermissionActions>()),
  editNativeRule: { dispatch: vi.fn() },
}));
vi.mock("./navigate.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Navigate>()),
  openChange: vi.fn(),
}));

import { buildPermissionCard, type PermissionAnswer } from "./permission.js";

function ask(over: Partial<PermissionNeededPayload> = {}): PermissionNeededPayload {
  return {
    request_id: 1,
    title: "Write src/main.go",
    kind: "edit",
    accepts_rejection_reason: true,
    options: [
      { option_id: "a", name: "Allow", kind: "allow_once" },
      { option_id: "r", name: "Reject", kind: "reject_once" },
      { option_id: "ra", name: "Always reject", kind: "reject_always" },
    ],
    ...over,
  } as PermissionNeededPayload;
}

function note(card: HTMLElement): HTMLTextAreaElement | null {
  return card.querySelector<HTMLTextAreaElement>("textarea.approval-reason");
}

function click(card: HTMLElement, label: string): void {
  const btn = [...card.querySelectorAll<HTMLButtonElement>(".approval-actions button")].find(
    (b) => b.textContent === label,
  );
  if (btn === undefined) {
    throw new Error(`no button ${label}`);
  }
  btn.click();
}

let onSelect: ReturnType<typeof vi.fn<(answer: PermissionAnswer) => void>>;

beforeEach(() => {
  onSelect = vi.fn<(answer: PermissionAnswer) => void>();
});

describe("the permission card's deny note", () => {
  it("renders above the answer buttons when the ask accepts one", () => {
    const card = buildPermissionCard("c1", ask(), onSelect);
    const box = note(card);
    expect(box).not.toBeNull();
    expect(box?.nextElementSibling?.classList.contains("approval-actions")).toBe(true);
    expect(box?.placeholder).toBe("Optional: tell the agent why you're denying");
    expect(box?.maxLength).toBe(1000);
  });

  it("does not render when the ask does not accept one", () => {
    const card = buildPermissionCard("c1", ask({ accepts_rejection_reason: false }), onSelect);
    expect(note(card)).toBeNull();
  });

  it("sends the trimmed note with a reject_once answer", () => {
    const card = buildPermissionCard("c1", ask(), onSelect);
    const box = note(card);
    if (box === null) {
      throw new Error("no note");
    }
    box.value = "  wrong directory \n";
    click(card, "Reject");
    expect(onSelect).toHaveBeenCalledWith({ optionID: "r", rejectionReason: "wrong directory" });
  });

  it("sends a plain deny when the note is blank", () => {
    const card = buildPermissionCard("c1", ask(), onSelect);
    const box = note(card);
    if (box === null) {
      throw new Error("no note");
    }
    box.value = "   ";
    click(card, "Reject");
    expect(onSelect).toHaveBeenCalledWith({ optionID: "r" });
  });

  it("ignores the note on allow and on reject_always", () => {
    const card = buildPermissionCard("c1", ask(), onSelect);
    const box = note(card);
    if (box === null) {
      throw new Error("no note");
    }
    box.value = "wrong directory";
    click(card, "Allow");
    click(card, "Always reject");
    expect(onSelect.mock.calls).toEqual([[{ optionID: "a" }], [{ optionID: "ra" }]]);
  });
});
