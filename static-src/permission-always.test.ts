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

  it.each([
    "sudo apt-get install x",
    "time make build",
    "command rm -rf x",
    "nohup server start",
    "npx eslint fix",
    "bunx tsc build",
    "uvx ruff",
    "timeout 60 rm -rf x",
    "nice make build",
    "xargs rm -f",
  ])("does not generalise from the wrapper command in %s", (subject) => {
    const { card } = mount(ask({ consent: { capability: "shell", subject } }));
    button(card, "Always allow").click();
    expect(patterns(card)).toEqual([subject, "*"]);
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

  it("offers the subcommand width between the exact command and the first-word pattern", () => {
    const { card } = mount(ask({ consent: { capability: "shell", subject: "git stash pop" } }));
    button(card, "Always allow").click();
    expect(patterns(card)).toEqual(["git stash pop", "git stash *", "git *", "*"]);
    expect(coversOf(card, "git stash *")).toBe("any git stash command");
  });

  it("answers the always option with the subcommand pattern", () => {
    const { card, answers } = mount(
      ask({ consent: { capability: "shell", subject: "git stash pop" } }),
    );
    button(card, "Always allow").click();
    [...card.querySelectorAll<HTMLButtonElement>(".always-choice-option")][1]?.click();

    expect(answers).toEqual([{ optionID: "always-accept", alwaysResource: "git stash *" }]);
  });

  it.each([
    ["git -C /r status", ["git -C /r status", "git *", "*"]],
    ["cat a.txt", ["cat a.txt", "cat *", "*"]],
    ["make build:prod", ["make build:prod", "make *", "*"]],
    ["git 2stash x", ["git 2stash x", "git *", "*"]],
  ])(
    "does not offer a subcommand width when the second word of %s is not a plain name",
    (subject, want) => {
      const { card } = mount(ask({ consent: { capability: "shell", subject } }));
      button(card, "Always allow").click();
      expect(patterns(card)).toEqual(want);
    },
  );

  it("does not offer a subcommand width when the second word is a wrapper", () => {
    const { card } = mount(ask({ consent: { capability: "shell", subject: "watch sudo ls" } }));
    button(card, "Always allow").click();
    expect(patterns(card)).toEqual(["watch sudo ls", "watch *", "*"]);
  });

  it.each([
    ["npm exec eslint .", "npm"],
    ["npm x eslint .", "npm"],
    ["pnpm exec tsc", "pnpm"],
    ["pnpm dlx create-vite app", "pnpm"],
    ["yarn dlx cowsay hi", "yarn"],
    ["yarn exec tsc", "yarn"],
    ["bun x prettier .", "bun"],
    ["uv run pytest", "uv"],
    ["uv tool run ruff", "uv"],
    ["pipx run black .", "pipx"],
  ])("does not offer a subcommand width for the program runner in %s", (subject, first) => {
    const { card } = mount(ask({ consent: { capability: "shell", subject } }));
    button(card, "Always allow").click();
    expect(patterns(card)).toEqual([subject, `${first} *`, "*"]);
  });

  it("offers a subject that already is its subcommand pattern once, as that pattern", () => {
    const { card } = mount(ask({ consent: { capability: "shell", subject: "git stash *" } }));
    button(card, "Always allow").click();
    expect(patterns(card)).toEqual(["git stash *", "git *", "*"]);
    expect(coversOf(card, "git stash *")).toBe("any git stash command");
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
    button(card, "Allow once").click();
    expect(answers).toEqual([{ optionID: "accept" }]);
  });
});

describe("Allow for this chat answers the always option with session scope", () => {
  function names(card: HTMLElement): (string | null)[] {
    return [...card.querySelectorAll(".approval-actions button")].map((b) => b.textContent);
  }

  it("sits between the one-time and the always allow, with deny left at two answers", () => {
    const { card } = mount(ask());
    expect(names(card)).toEqual([
      "Allow once",
      "Allow for this chat",
      "Always allow",
      "Deny",
      "Always deny",
    ]);
  });

  it("renames only the one-time answer, keeping kiro-cli's other labels", () => {
    const { card } = mount(
      ask({
        options: [
          { option_id: "accept", name: "Yes", kind: "allow_once" },
          { option_id: "always-accept", name: "Trust", kind: "allow_always" },
          { option_id: "reject", name: "No", kind: "reject_once" },
        ],
      }),
    );
    expect(names(card)).toEqual(["Allow once", "Allow for this chat", "Trust", "No"]);
  });

  it("opens the same patterns and answers nothing until one is picked", () => {
    const { card, answers } = mount(ask());
    button(card, "Allow for this chat").click();

    expect(answers).toEqual([]);
    expect(button(card, "Allow for this chat").getAttribute("aria-expanded")).toBe("true");
    expect(button(card, "Always allow").getAttribute("aria-expanded")).toBe("false");
    expect(card.querySelector(".always-choice-title")?.textContent).toBe("Allow for this chat:");
    expect(card.querySelector(".always-choice")?.getAttribute("aria-label")).toBe(
      "Allow for this chat",
    );
    expect(patterns(card)).toEqual(["head -1", "head *", "*"]);
  });

  it("answers the allow_always option with the picked pattern and session scope", () => {
    const { card, answers } = mount(ask());
    button(card, "Allow for this chat").click();
    [...card.querySelectorAll<HTMLButtonElement>(".always-choice-option")][1]?.click();

    expect(answers).toEqual([
      { optionID: "always-accept", alwaysResource: "head *", alwaysScope: "session" },
    ]);
  });

  it("says the rule is kept for this chat only and changes no permissions", () => {
    const { card } = mount(ask());
    button(card, "Allow for this chat").click();
    expect(card.querySelector(".always-choice-hint")?.textContent).toBe(
      "kiro-cli keeps this rule for this chat's session only. Nothing is saved and Permissions stay " +
        "as they are. It may ask again after the chat closes or reloads.",
    );
  });

  it("switches to Always allow's own chooser, which still answers user scope", () => {
    const { card, answers } = mount(ask());
    button(card, "Allow for this chat").click();
    button(card, "Always allow").click();

    expect(button(card, "Allow for this chat").getAttribute("aria-expanded")).toBe("false");
    expect(card.querySelector(".always-choice-title")?.textContent).toBe("Always allow:");
    expect(card.querySelector(".always-choice-hint")?.textContent).toMatch(/^Saves the rule/);
    [...card.querySelectorAll<HTMLButtonElement>(".always-choice-option")][0]?.click();
    expect(answers).toEqual([{ optionID: "always-accept", alwaysResource: "head -1" }]);
  });

  it("closes on a second click", () => {
    const { card } = mount(ask());
    button(card, "Allow for this chat").click();
    button(card, "Allow for this chat").click();
    expect(card.querySelector(".always-choice")).toBeNull();
    expect(button(card, "Allow for this chat").getAttribute("aria-expanded")).toBe("false");
  });

  it("is absent when the ask names nothing to save", () => {
    const payload = ask();
    delete payload.consent;
    expect(names(mount(payload).card)).toEqual(["Allow", "Deny"]);
  });

  it("is absent when KAS offers no always allow", () => {
    const { card } = mount(
      ask({
        options: [
          { option_id: "accept", name: "Allow", kind: "allow_once" },
          { option_id: "reject", name: "Deny", kind: "reject_once" },
          { option_id: "always-reject", name: "Always deny", kind: "reject_always" },
        ],
      }),
    );
    expect(names(card)).toEqual(["Allow", "Deny", "Always deny"]);
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
