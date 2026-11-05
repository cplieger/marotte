// The permission card's optional deny note: when it renders, what a deny sends, that an allow
// never carries it, and its plan-approval form, which asks for edits.

import { vi, describe, it, expect, beforeEach } from "vitest";
import type * as SettingsHighlight from "./settings-highlight.js";
import type * as Navigate from "./navigate.js";
import type { PermissionNeededPayload } from "./types.js";

vi.mock("./settings-highlight.js", async (importOriginal) => ({
  ...(await importOriginal<typeof SettingsHighlight>()),
  openSetting: vi.fn(),
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
    rejection_note: "reason",
    consent: { capability: "fs_write", subject: "src/main.go" },
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
    const { rejection_note: _note, ...withoutNote } = ask();
    const card = buildPermissionCard("c1", withoutNote, onSelect);
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
    card.querySelector<HTMLButtonElement>(".always-choice-option")?.click();
    expect(onSelect.mock.calls).toEqual([
      [{ optionID: "a" }],
      [{ optionID: "ra", alwaysResource: "src/main.go" }],
    ]);
  });
});

describe("the deny note on a plan approval", () => {
  function planAsk(): PermissionNeededPayload {
    return ask({
      title: "Add 2 workflow steps to wf_1",
      kind: "other",
      rejection_note: "plan_edits",
      options: [
        { option_id: "a", name: "Allow", kind: "allow_once" },
        { option_id: "r", name: "Deny", kind: "reject_once" },
      ],
    });
  }

  function type(box: HTMLTextAreaElement, value: string): void {
    box.value = value;
    box.dispatchEvent(new Event("input"));
  }

  function rejectButton(card: HTMLElement): HTMLButtonElement | undefined {
    return card.querySelectorAll<HTMLButtonElement>(".approval-actions button")[1];
  }

  it("asks for the edits the user wants", () => {
    const box = note(buildPermissionCard("c1", planAsk(), onSelect));
    expect(box?.placeholder).toBe("Optional: type the edits you want");
    expect(box?.getAttribute("aria-label")).toBe("Edits you want (optional)");
  });

  it("labels the deny 'Request edits' while the note has text, and sends the edits", () => {
    const card = buildPermissionCard("c1", planAsk(), onSelect);
    const box = note(card);
    if (box === null) {
      throw new Error("no note");
    }
    type(box, "drop the review step");
    expect(rejectButton(card)?.textContent).toBe("Request edits");
    click(card, "Request edits");
    expect(onSelect).toHaveBeenCalledWith({
      optionID: "r",
      rejectionReason: "drop the review step",
    });
  });

  it("restores the option's own name when the note is cleared, and sends a plain deny", () => {
    const card = buildPermissionCard("c1", planAsk(), onSelect);
    const box = note(card);
    if (box === null) {
      throw new Error("no note");
    }
    type(box, "drop the review step");
    type(box, "  ");
    expect(rejectButton(card)?.textContent).toBe("Deny");
    click(card, "Deny");
    expect(onSelect).toHaveBeenCalledWith({ optionID: "r" });
  });

  it("keeps the deny's label on an ordinary tool approval while its note has text", () => {
    const card = buildPermissionCard("c1", ask(), onSelect);
    const box = note(card);
    if (box === null) {
      throw new Error("no note");
    }
    type(box, "wrong directory");
    click(card, "Reject");
    expect(onSelect).toHaveBeenCalledWith({ optionID: "r", rejectionReason: "wrong directory" });
  });
});
