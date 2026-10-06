// The permission card's Always-allow row.

import { vi, describe, it, expect, beforeEach } from "vitest";
import type { PermissionNeededPayload } from "./types.js";

const mocks = vi.hoisted(() => ({
  openSetting: vi.fn(),
  editDispatch: vi.fn(),
}));

vi.mock("./settings-highlight.js", () => ({ openSetting: mocks.openSetting }));
vi.mock("./actions/permissions.js", () => ({
  editNativeRule: { dispatch: mocks.editDispatch },
}));
vi.mock("./navigate.js", () => ({ openChange: vi.fn() }));

import { buildPermissionCard } from "./permission.js";

/** A shell ask: `kind: "execute"` is what gates the Always-allow slot. */
function ask(over: Partial<PermissionNeededPayload> = {}): PermissionNeededPayload {
  return {
    request_id: 1,
    title: "git status",
    kind: "execute",
    options: [
      { option_id: "a", name: "Allow", kind: "allow_once" },
      { option_id: "r", name: "Reject", kind: "reject_once" },
    ],
    ...over,
  } as PermissionNeededPayload;
}

function card(over: Partial<PermissionNeededPayload> = {}): HTMLElement {
  return buildPermissionCard("chat-1", ask(over), vi.fn());
}

function row(c: HTMLElement): HTMLDetailsElement | null {
  return c.querySelector<HTMLDetailsElement>(".always-allow-details");
}

function note(c: HTMLElement): HTMLElement | null {
  return c.querySelector<HTMLElement>(".always-allow-unavailable");
}

/** The preset patterns offered, in order. Each preset button wraps its pattern in a <code>,
 *  which is what a reader sees. */
function presets(c: HTMLElement): string[] {
  return [...c.querySelectorAll(".always-allow-preset code")].map((e) => e.textContent ?? "");
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("the permission card's Always-allow row", () => {
  it("is offered when the server sends no block", () => {
    const c = card();
    expect(row(c)).not.toBeNull();
    expect(note(c)).toBeNull();
  });

  it("is withdrawn and replaced by a note when the server says a rule could never match", () => {
    const c = card({ always_allow_blocked: "unparseable" });
    expect(row(c)).toBeNull();
    const n = note(c);
    expect(n).not.toBeNull();
    expect(n?.textContent).toBe(
      "Always allow is unavailable. kiro-cli cannot parse this command, so a saved rule would never match it.",
    );
  });

  // A control that does nothing teaches the reader to distrust every other one, so the withdrawn
  // offer is prose. The card already carries the policy pointer to the security-profile picker,
  // which is the real escape hatch.
  it("renders the note as text, never as a disabled control", () => {
    const c = card({ always_allow_blocked: "unparseable" });
    expect(note(c)?.tagName).not.toBe("BUTTON");
    expect(c.querySelector("button[disabled]")).toBeNull();
    expect(c.querySelector("input[disabled]")).toBeNull();
    expect(c.querySelector(".always-allow-custom")).toBeNull();
  });

  // Same slot as the expansion it replaces, so the reader finds the answer where the affordance
  // was.
  it("puts the note where the expansion would have been", () => {
    const c = card({ always_allow_blocked: "unparseable" });
    expect(c.querySelector(".approval-actions .always-allow-unavailable")).not.toBeNull();
  });

  // A double quote does not suppress the row: `git *` matches the command and KAS reports it
  // persistable, and the exact command is one of the presets.
  it("offers a row for a command containing a quote", () => {
    const c = card({ title: 'git commit -m "fix"' });
    expect(row(c)).not.toBeNull();
    expect(presets(c)).toEqual(["git *", "git commit -m *", 'git commit -m "fix"']);
  });

  // The preset derivation is what the persisted rule is built from:
  // base, base + flags, exact — with a leading `sudo` skipped, because a `sudo *` allow would be
  // far broader than intended.
  it("derives base, partial and full presets, skipping a leading sudo", () => {
    expect(presets(card({ title: "sudo apt-get install ripgrep" }))).toEqual([
      "apt-get *",
      "apt-get install *",
      "apt-get install ripgrep",
    ]);
  });

  it("offers only the base preset for a bare command", () => {
    expect(presets(card({ title: "ls" }))).toEqual(["ls *"]);
  });

  // `sudo` alone has no wrapped command to derive from, so it is the base itself rather than an
  // empty pattern.
  it("treats a lone sudo as its own base", () => {
    expect(presets(card({ title: "sudo" }))).toEqual(["sudo *"]);
  });

  // Nothing to approve with means there was never an offer to withdraw, so the slot stays empty
  // rather than explaining an absence.
  it("shows neither row nor note when no allow option is offered", () => {
    const c = buildPermissionCard(
      "chat-1",
      ask({ options: [{ option_id: "r", name: "Reject", kind: "reject_once" }] }),
      vi.fn(),
    );
    expect(row(c)).toBeNull();
    expect(note(c)).toBeNull();
  });

  // The slot is shell-only in both directions: a mode switch grants no shell capability, so a block
  // on one must not paint a note there either.
  it("stays out of a mode-switch card even when blocked", () => {
    const c = card({ kind: "switch_mode", title: "spec", always_allow_blocked: "unparseable" });
    expect(row(c)).toBeNull();
    expect(note(c)).toBeNull();
  });

  // An administrator's ask outranks every allow rule, so offering a rule or a looser profile would
  // offer something that cannot stop the asking.
  it("names the administrator and offers neither a rule nor the profile pointer", () => {
    const c = card({ admin_required: true });
    expect(c.querySelector(".approval-admin")?.textContent).toBe(
      "Your administrator requires approval for this",
    );
    expect(row(c)).toBeNull();
    expect(c.querySelector(".approval-policy-pointer")).toBeNull();
  });
});

// The metacharacter refusal, which gates the DERIVATION and not the row.

function customInput(c: HTMLElement): HTMLInputElement | null {
  return c.querySelector<HTMLInputElement>(".always-allow-custom .chip-input");
}

describe("Always-allow preset derivation refuses a glob-bearing token", () => {
  it("derives no preset from a glob-bearing base", () => {
    expect(presets(card({ title: "[a-z]* --force" }))).toEqual([]);
  });

  it("keeps the row, the custom input and Allow-once when no preset survives", () => {
    const c = card({ title: "[a-z]* --force" });
    expect(presets(c)).toEqual([]); // the premise this case is about
    expect(row(c)).not.toBeNull();
    expect(note(c)).toBeNull();
    expect(customInput(c)).not.toBeNull();
    expect([...c.querySelectorAll(".approval-actions > button")].map((b) => b.textContent)).toEqual(
      ["Allow", "Reject"],
    );
  });

  // The refused pattern must not come back as a suggestion in the box beside it.
  it("keeps the refused pattern out of the custom-pattern placeholder", () => {
    expect(customInput(card({ title: "[a-z]* --force" }))?.placeholder).toBe("command *");
  });

  // Per preset, not per row: the base is clean here, so `rm *` stands while the two patterns
  // derived from the glob-bearing operand are refused.
  it("drops only the presets derived from the glob-bearing token", () => {
    expect(presets(card({ title: "rm -rf build/*" }))).toEqual(["rm *", "rm -rf *"]);
  });

  it.each(["a*b", "a?b", "a[b", "a]b", "a{b", "a}b", "a!b"])(
    "refuses every preset when the base is %s",
    (token) => {
      expect(presets(card({ title: `${token} arg` }))).toEqual([]);
    },
  );

  // The red-check from the brief: nothing here is glob syntax, so the refusal must not fire and all
  // three presets stand.
  it("still derives all three presets for a quoted command", () => {
    expect(presets(card({ title: 'git commit -m "fix"' }))).toEqual([
      "git *",
      "git commit -m *",
      'git commit -m "fix"',
    ]);
  });
});
