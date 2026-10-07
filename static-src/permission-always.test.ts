import { vi, describe, it, expect } from "vitest";
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
    title: "echo a | head -1",
    kind: "execute",
    consent: { capability: "shell", subject: "head -1" },
    options: [
      { option_id: "accept", name: "Allow", kind: "allow_once" },
      { option_id: "always-accept", name: "Always allow", kind: "allow_always" },
      { option_id: "reject", name: "Deny", kind: "reject_once" },
      { option_id: "always-reject", name: "Always deny", kind: "reject_always" },
    ],
    ...over,
  } as PermissionNeededPayload;
}

function mount(payload: PermissionNeededPayload): {
  card: HTMLElement;
  answers: PermissionAnswer[];
} {
  const answers: PermissionAnswer[] = [];
  const card = buildPermissionCard("chat-1", payload, (a) => answers.push(a));
  return { card, answers };
}

function button(card: HTMLElement, name: string): HTMLButtonElement {
  const found = [...card.querySelectorAll<HTMLButtonElement>(".approval-actions button")].find(
    (b) => b.textContent === name,
  );
  if (found === undefined) {
    throw new Error(`no "${name}" button`);
  }
  return found;
}

function patterns(card: HTMLElement): string[] {
  return [...card.querySelectorAll(".always-choice-option code")].map((c) => c.textContent);
}

function coversOf(card: HTMLElement, resource: string): string | null | undefined {
  return [...card.querySelectorAll(".always-choice-option")]
    .find((o) => o.querySelector("code")?.textContent === resource)
    ?.querySelector(".always-choice-covers")?.textContent;
}

describe("each consent round names the part it asks about", () => {
  it("names the part on a later round", () => {
    const { card } = mount(ask({ consent_round: 2 }));
    expect(card.querySelector(".approval-subject")?.textContent).toBe(
      "Approval 2 for this command, about head -1",
    );
    expect(card.querySelector(".approval-round")).toBeNull();
  });

  it("names the part on the first round when it is not the whole command", () => {
    const { card } = mount(
      ask({ consent_round: 1, consent: { capability: "shell", subject: "echo a" } }),
    );
    expect(card.querySelector(".approval-subject")?.textContent).toBe(
      "This approval is about echo a",
    );
  });

  it("adds no line when the part is the whole command", () => {
    const { card } = mount(
      ask({ title: "ls -la", consent: { capability: "shell", subject: "ls -la" } }),
    );
    expect(card.querySelector(".approval-subject")).toBeNull();
  });
});

describe("an always option opens a chooser instead of answering", () => {
  it("answers nothing until a pattern is picked", () => {
    const { card, answers } = mount(ask());
    button(card, "Always allow").click();

    expect(answers).toEqual([]);
    expect(button(card, "Always allow").getAttribute("aria-expanded")).toBe("true");
    expect(patterns(card)).toEqual(["head -1", "head *", "*"]);
  });

  it("answers the always option with the picked pattern", () => {
    const { card, answers } = mount(ask());
    button(card, "Always allow").click();
    [...card.querySelectorAll<HTMLButtonElement>(".always-choice-option")][1]?.click();

    expect(answers).toEqual([{ optionID: "always-accept", alwaysResource: "head *" }]);
  });

  it("offers the same patterns for Always deny and answers its own option", () => {
    const { card, answers } = mount(ask());
    button(card, "Always deny").click();
    expect(card.querySelector(".always-choice-title")?.textContent).toBe("Always deny:");
    [...card.querySelectorAll<HTMLButtonElement>(".always-choice-option")][0]?.click();

    expect(answers).toEqual([{ optionID: "always-reject", alwaysResource: "head -1" }]);
  });

  it("closes on a second click and switches between the two always options", () => {
    const { card } = mount(ask());
    button(card, "Always allow").click();
    button(card, "Always deny").click();
    expect(card.querySelector(".always-choice-title")?.textContent).toBe("Always deny:");
    expect(button(card, "Always allow").getAttribute("aria-expanded")).toBe("false");

    button(card, "Always deny").click();
    expect(card.querySelector(".always-choice")).toBeNull();
  });

  it("says the rule reaches every kiro-cli session and switches Permissions to Custom", () => {
    const { card } = mount(ask());
    button(card, "Always allow").click();
    expect(card.querySelector(".always-choice-hint")?.textContent).toBe(
      "Saves the rule to your user permissions, which every kiro-cli session on this account reads, " +
        "not only these chats. Switches Permissions to Custom if another profile is selected.",
    );
  });

  it.each([
    ["shell", "any command"],
    ["fs_write", "any file write, by any tool"],
    ["fs_read", "any file read, by any tool"],
    ["web_fetch", "any web_fetch request, by any tool"],
  ])("names the whole %s capability on the * pattern", (capability, covers) => {
    const { card } = mount(ask({ kind: "edit", consent: { capability, subject: "x" } }));
    button(card, "Always allow").click();
    expect(coversOf(card, "*")).toBe(covers);
  });

  it("does not generalise from a wrapper command", () => {
    const { card } = mount(
      ask({ consent: { capability: "shell", subject: "sudo apt-get install x" } }),
    );
    button(card, "Always allow").click();
    expect(patterns(card)).toEqual(["sudo apt-get install x", "*"]);
  });

  it("does not generalise a single-word command", () => {
    const { card } = mount(ask({ consent: { capability: "shell", subject: "ls" } }));
    button(card, "Always allow").click();
    expect(patterns(card)).toEqual(["ls", "*"]);
  });

  it("offers a subject that already is its first-word pattern once, as that pattern", () => {
    const { card } = mount(ask({ consent: { capability: "shell", subject: "echo *" } }));
    button(card, "Always allow").click();
    expect(patterns(card)).toEqual(["echo *", "*"]);
    expect(coversOf(card, "echo *")).toBe("any echo command");
  });

  it("names a subject carrying * as a pattern and does not generalise from a * in its first word", () => {
    const { card } = mount(ask({ consent: { capability: "shell", subject: "./tool* --help" } }));
    button(card, "Always allow").click();
    expect(patterns(card)).toEqual(["./tool* --help", "*"]);
    expect(coversOf(card, "./tool* --help")).toBe("anything this pattern matches");
  });

  it("names a plain subject as only this", () => {
    const { card } = mount(ask());
    button(card, "Always allow").click();
    expect(coversOf(card, "head -1")).toBe("only this");
  });

  it("offers the folder the server named for a path", () => {
    const { card } = mount(
      ask({
        kind: "edit",
        title: "Write",
        consent: { capability: "fs_write", subject: "/w/src/a.ts", folder: "/w/src/**" },
      }),
    );
    button(card, "Always allow").click();
    expect(patterns(card)).toEqual(["/w/src/a.ts", "/w/src/**", "*"]);
  });

  it("offers no folder the server did not name", () => {
    const { card } = mount(
      ask({
        kind: "edit",
        title: "Write",
        consent: { capability: "fs_write", subject: "/w/src/a.ts" },
      }),
    );
    button(card, "Always allow").click();
    expect(patterns(card)).toEqual(["/w/src/a.ts", "*"]);
  });

  // The server maps a display copy back to its raw rule key only when it arrives byte for byte.
  it.each([
    ["a part cut at the display cap", `python3 -c '${"x".repeat(498)}...`],
    ["a part whose newlines the server replaced", "git commit -m 'a  b'"],
  ])("sends %s back exactly as shown", (_, subject) => {
    const { card, answers } = mount(ask({ consent: { capability: "shell", subject } }));
    button(card, "Always allow").click();
    [...card.querySelectorAll<HTMLButtonElement>(".always-choice-option")][0]?.click();

    expect(answers).toEqual([{ optionID: "always-accept", alwaysResource: subject }]);
  });

  it("sends the server's folder back exactly as shown", () => {
    const folder = "/w/a b/**";
    const { card, answers } = mount(
      ask({ kind: "edit", consent: { capability: "fs_read", subject: "/w/a b/n.md", folder } }),
    );
    button(card, "Always allow").click();
    [...card.querySelectorAll<HTMLButtonElement>(".always-choice-option")][1]?.click();

    expect(answers).toEqual([{ optionID: "always-accept", alwaysResource: folder }]);
  });

  it("leaves an always option out when the ask names nothing to save", () => {
    const payload = ask();
    delete payload.consent;
    const { card } = mount(payload);
    const names = [...card.querySelectorAll(".approval-actions button")].map((b) => b.textContent);
    expect(names).toEqual(["Allow", "Deny"]);
  });

  it("answers Allow once with no pattern", () => {
    const { card, answers } = mount(ask());
    button(card, "Allow").click();
    expect(answers).toEqual([{ optionID: "accept" }]);
  });
});

describe("the note when a rule could never match", () => {
  it("stands in the actions row as text", () => {
    const { card } = mount(
      ask({
        always_allow_blocked: "unparseable",
        options: [{ option_id: "accept", name: "Allow", kind: "allow_once" }],
      }),
    );
    const note = card.querySelector(".approval-actions .always-allow-unavailable");
    expect(note?.textContent).toBe(
      "Always allow is unavailable. kiro-cli cannot parse this command, so a saved rule would never match it.",
    );
    expect(note?.tagName).toBe("DIV");
  });

  it("stays off a mode-switch card", () => {
    const { card } = mount(ask({ kind: "switch_mode", always_allow_blocked: "unparseable" }));
    expect(card.querySelector(".always-allow-unavailable")).toBeNull();
  });
});
