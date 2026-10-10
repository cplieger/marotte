// The agent-ignore chip rendering and the supervised-default toggle in permissions-ui.ts.
import { describe, it, expect, beforeEach, vi } from "vitest";
import indexHtml from "../static/index.html?raw";
import type { EffectiveSettings } from "./persist.js";
import { settingsPayload } from "./__test-helpers__/settings.js";

const mocks = vi.hoisted(() => ({
  apiGet: vi.fn<(path: string, signal?: AbortSignal) => Promise<unknown>>(),
  patchSettings: vi.fn<(patch: Partial<EffectiveSettings>) => Promise<void>>(),
}));

// Mock only the I/O edges. ui-primitives (buildChip), reconcile,
// @cplieger/reactive (el), icons, and dom (byId/maybeEl over the real
// document) stay real so we exercise the actual DOM behaviour.
vi.mock("./api-client.js", () => ({ apiGet: mocks.apiGet }));
vi.mock("./persist.js", () => ({ patchSettings: mocks.patchSettings }));
vi.mock("./actions/index.js", () => ({
  registerCleanup: vi.fn(),
  bindLoadingState: vi.fn(() => vi.fn()),
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened this graph and these
  // names are imported somewhere in it. No case here calls them.
  apiGetTyped: vi.fn(),
}));
vi.mock("./actions/permissions.js", () => ({
  editNativeRule: { dispatch: vi.fn() },
  explainPolicy: { dispatch: vi.fn() },
  setSecurityProfile: { dispatch: vi.fn() },
}));

import { initPermissionsUI, agentIgnoreEntryError, AGENT_IGNORE_FLOOR } from "./permissions-ui.js";
import { byId } from "./dom.js";

function div(id: string): HTMLDivElement {
  const e = document.createElement("div");
  e.id = id;
  return e;
}
function hint(id: string): HTMLParagraphElement {
  const e = document.createElement("p");
  e.id = id;
  e.className = "section-hint hidden";
  return e;
}
function input(id: string): HTMLInputElement {
  const e = document.createElement("input");
  e.id = id;
  e.type = "text";
  return e;
}
function button(id: string): HTMLButtonElement {
  const e = document.createElement("button");
  e.id = id;
  e.type = "button";
  return e;
}
function checkbox(id: string): HTMLInputElement {
  const e = document.createElement("input");
  e.id = id;
  e.type = "checkbox";
  return e;
}

async function flush(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
}

function initWith(ignoreFiles: string[], supervised = false): void {
  initPermissionsUI(
    settingsPayload({ agent_ignore_files: ignoreFiles, supervised_default: supervised }),
  );
}

function ignoreChips(): HTMLElement[] {
  const c = byId<HTMLDivElement>("agent-ignore-chips");
  return Array.from(c.querySelectorAll<HTMLElement>(".chip"));
}
function chipLabel(ch: HTMLElement): string | null {
  return ch.querySelector<HTMLElement>(".chip-label")?.textContent ?? null;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();

  // Supervised default toggle.
  document.body.appendChild(checkbox("supervised-default-checkbox"));

  // Agent ignore section.
  document.body.appendChild(div("agent-ignore-chips"));
  document.body.appendChild(hint("agent-ignore-empty-hint"));
  document.body.appendChild(input("agent-ignore-input"));
  document.body.appendChild(button("agent-ignore-add"));

  mocks.patchSettings.mockResolvedValue(undefined);
});

// Supervised default toggle.

describe("supervised default toggle", () => {
  it("seeds from settings and PATCHes on change", async () => {
    initWith([], true);
    const box = byId<HTMLInputElement>("supervised-default-checkbox");
    expect(box.checked).toBe(true);

    box.checked = false;
    box.dispatchEvent(new Event("change"));
    await flush();

    expect(mocks.patchSettings).toHaveBeenCalledWith({ supervised_default: false });
  });
});

// Agent ignore chips: keyed reconcile add/remove preserves node identity.

describe("renderIgnoreChips — keyed reconcile", () => {
  it("clears and refocuses the path input after an add (repeat entry)", async () => {
    initWith([]);

    const pathInput = byId<HTMLInputElement>("agent-ignore-input");
    pathInput.value = ".dockerignore";
    byId<HTMLButtonElement>("agent-ignore-add").click();
    await flush();

    expect(ignoreChips()).toHaveLength(1);
    expect(pathInput.value).toBe("");
    expect(document.activeElement).toBe(pathInput);
  });

  it("preserves existing chip nodes on add and drops only the removed one", async () => {
    initWith([".gitignore", ".env.dec"]);

    expect(ignoreChips()).toHaveLength(2);
    const gitignore = ignoreChips().find((c) => chipLabel(c) === ".gitignore");
    expect(gitignore).toBeDefined();
    expect(byId("agent-ignore-empty-hint").classList.contains("hidden")).toBe(true);

    // Add a third entry; the keyed reconcile must reuse the existing .gitignore node rather than
    // rebuilding the whole row.
    byId<HTMLInputElement>("agent-ignore-input").value = ".dockerignore";
    byId<HTMLButtonElement>("agent-ignore-add").click();
    await flush();

    expect(ignoreChips()).toHaveLength(3);
    const gitignoreAfter = ignoreChips().find((c) => chipLabel(c) === ".gitignore");
    expect(gitignoreAfter).toBe(gitignore);

    const added = ignoreChips().find((c) => chipLabel(c) === ".dockerignore");
    added?.querySelector<HTMLButtonElement>(".chip-remove")?.click();
    await flush();

    expect(ignoreChips()).toHaveLength(2);
    expect(ignoreChips().some((c) => chipLabel(c) === ".dockerignore")).toBe(false);
    expect(ignoreChips().find((c) => chipLabel(c) === ".gitignore")).toBe(gitignore);
  });

  it("renders the seeded defaults, and an add persists them beside the new entry", async () => {
    // The server now resolves that default into the response, so the seeded values arrive as
    // ordinary values and the row has nothing to invent. settingsPayload() with no override is the
    // fresh-volume payload.
    initPermissionsUI(settingsPayload());

    expect(ignoreChips().map(chipLabel)).toEqual([".gitignore", ".kiroignore"]);
    expect(byId("agent-ignore-empty-hint").classList.contains("hidden")).toBe(true);

    byId<HTMLInputElement>("agent-ignore-input").value = ".env.dec";
    byId<HTMLButtonElement>("agent-ignore-add").click();
    await flush();

    expect(mocks.patchSettings).toHaveBeenCalledWith({
      agent_ignore_files: [".gitignore", ".kiroignore", ".env.dec"],
    });
  });

  it("ignores duplicate adds", async () => {
    initWith([".gitignore"]);

    byId<HTMLInputElement>("agent-ignore-input").value = ".gitignore";
    byId<HTMLButtonElement>("agent-ignore-add").click();
    await flush();

    expect(ignoreChips()).toHaveLength(1);
    expect(mocks.patchSettings).not.toHaveBeenCalled();
  });
});

// The copy under these two controls, read out of the shipped markup rather than a fixture, because
// the copy IS the deliverable: both hints make a claim about a SECURITY boundary, and an over-broad
// one leaves an operator with a wrong model of what they achieved, which is the same class of harm
// as an under-enforced one.

function sectionFor(saveStatusKey: string): Element {
  const doc = new DOMParser().parseFromString(indexHtml, "text/html");
  const section = doc
    .querySelector(`[data-save-status="${saveStatusKey}"]`)
    ?.closest(".page-section");
  if (!section) {
    throw new Error(`no section carrying data-save-status="${saveStatusKey}"`);
  }
  return section;
}

function textOf(el: Element | null | undefined): string {
  return (el?.textContent ?? "").replace(/\s+/g, " ").trim();
}

describe("the agent-ignore hint", () => {
  const hint = (): string =>
    textOf(sectionFor("agent_ignore_files").querySelector(".section-hint"));

  // The hint says what the list does for the reader in two sentences, and the one limit a reader
  // acts on: a shell command still reads a listed file, so the list is not a way to hide a secret.
  it("says the floor is always used", () => {
    expect(hint()).toContain(".kiroignore");
    expect(hint()).toContain("always used");
  });

  it("names the file tools as what the list reaches", () => {
    expect(hint()).toContain("file tools cannot read");
  });

  it("says a shell command still reads a listed file", () => {
    expect(hint()).toContain("Shell commands can still read them");
    expect(hint()).toContain("does not hide secrets");
  });

  // The knowledge index is built by a separate KAS path this list was never shown to reach. Copy
  // that promised it would be a claim nobody measured.
  it("promises nothing about the knowledge index", () => {
    expect(hint()).not.toContain("knowledge");
    expect(hint()).not.toContain("index");
  });

  // The same over-broad claim, one element down: the empty state offered .gitignore as keeping
  // every gitignored path "off-limits to reads".
  it("scopes the empty state's suggestion the same way", () => {
    const doc = new DOMParser().parseFromString(indexHtml, "text/html");
    const empty = textOf(doc.querySelector("#agent-ignore-empty-hint"));
    expect(empty).toContain("away from the file tools");
    expect(empty).not.toContain("off-limits");
  });
});

describe("agentIgnoreEntryError", () => {
  // The FLOOR arm is marotte's own and has no server counterpart, so nothing in internal/settings
  // can fail if it goes: .kiroignore is a perfectly valid entry that is already sent on every
  // spawn, and accepting it produces a duplicate row the panel then renders twice.
  it("refuses the floor, because it is already in the list", () => {
    expect(agentIgnoreEntryError(AGENT_IGNORE_FLOOR)).not.toBeNull();
  });

  // The control: an ordinary basename the floor arm must not swallow.
  it("accepts an ordinary ignore-file name", () => {
    expect(agentIgnoreEntryError(".dockerignore")).toBeNull();
  });
});

describe("the supervised-mode hint", () => {
  const section = (): Element => sectionFor("supervised_default");
  const lead = (): string => textOf(section().querySelector(":scope > .section-hint"));

  // One hint: what the mode does, the per-chat override, and the on-disk hazard a reader needs
  // before turning it on.
  it("states the review, the per-chat override and the on-disk hazard", () => {
    expect(lead()).toContain("review its file changes");
    expect(lead()).toContain("default for new chats");
    expect(lead()).toContain("chat-actions menu");
    expect(lead()).toContain("on disk");
  });

  it("keeps the review mechanics in the one hint, with no disclosure", () => {
    expect(section().querySelector("details")).toBeNull();
    expect(lead()).toContain("every file starts ticked");
  });
});

// Empty states live in sibling hint elements, not inside the chip containers.

describe("empty states", () => {
  it("shows the sibling hint and keeps the chip container free of stray nodes", () => {
    initWith([]);

    const ignoreContainer = byId<HTMLDivElement>("agent-ignore-chips");
    expect(ignoreContainer.querySelectorAll(".chip")).toHaveLength(0);
    expect(ignoreContainer.querySelector("p")).toBeNull();
    expect(byId("agent-ignore-empty-hint").classList.contains("hidden")).toBe(false);
  });

  it("hides the hint once an ignore file is present", () => {
    initWith([".gitignore"]);
    expect(byId("agent-ignore-empty-hint").classList.contains("hidden")).toBe(true);
    expect(ignoreChips()).toHaveLength(1);
  });
});
