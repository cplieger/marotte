// The chat-actions menu: pins each row's dispatch (the card is TS-built, so this is also
// the markup test). The goal row's string is run through KAS's transcribed parser.
import { beforeEach, describe, expect, it, vi } from "vitest";

import { uploadLimitHint } from "./upload-policy.js";

const {
  openFilePicker,
  openTangentChat,
  mergeTangentChat,
  openRunView,
  setSupervisedDispatch,
  setInterruptDispatch,
  compactDispatch,
  launchDispatch,
  recipesDispatch,
  submitPrompt,
  sendPromptTo,
  transportSend,
  toastError,
  chatNotice,
  collapseAll,
  renameDispatch,
  sessionDispatch,
  copyDispatch,
  downloadChatExport,
} = vi.hoisted(() => ({
  renameDispatch: vi.fn(),
  sessionDispatch: vi.fn(),
  copyDispatch: vi.fn(),
  downloadChatExport: vi.fn(),
  openFilePicker: vi.fn(),
  openTangentChat: vi.fn(),
  openRunView: vi.fn(),
  setSupervisedDispatch: vi.fn(),
  setInterruptDispatch: vi.fn(),
  compactDispatch: vi.fn(),
  launchDispatch: vi.fn(),
  recipesDispatch: vi.fn(),
  submitPrompt: vi.fn(),
  sendPromptTo: vi.fn(),
  transportSend: vi.fn(),
  toastError: vi.fn(),
  chatNotice: vi.fn(),
  collapseAll: vi.fn(),
  mergeTangentChat: vi.fn(),
}));

let activeID = "";
let supervised: boolean | undefined;
let interruptMode: "steer" | "queue" | undefined;
let thinking = false;
let messageCount = 3;
let tangent = false;

/** The active chat as the menu sees it; `message_count` unlocks the tangent row. */
function session():
  | {
      id: string;
      name: string;
      supervised_mode: boolean | undefined;
      interrupt_mode: "steer" | "queue" | undefined;
      message_count: number;
      tangent: boolean;
    }
  | undefined {
  return activeID === ""
    ? undefined
    : {
        id: activeID,
        name: "My chat",
        supervised_mode: supervised,
        interrupt_mode: interruptMode,
        message_count: messageCount,
        tangent,
      };
}

vi.mock("./store.js", () => ({
  activeSession: {
    peek: () => session(),
    get value() {
      return session();
    },
  },
  isThinking: (id: string) => id === activeID && thinking,
  isEmptyChat: (s: { message_count: number } | undefined) =>
    s === undefined || s.message_count === 0,
  // Present-but-inert so real-ESM linking succeeds; no case calls them.
  get: vi.fn(() => undefined),
  getActive: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
}));
vi.mock("./pill-expand.js", () => ({ makeExpandable: vi.fn(), collapseAll }));
vi.mock("./files-picker.js", () => ({ openFilePicker }));
vi.mock("./chat.js", () => ({ openTangentChat }));
vi.mock("./tangent-merge.js", () => ({ mergeTangentChat }));
vi.mock("./run-view.js", () => ({ openRunView }));
vi.mock("./toast.js", () => ({ error: toastError, success: vi.fn(), info: vi.fn() }));
// A refusal about the active chat is named for it through the notice door.
vi.mock("./notice-subject.js", () => ({ chatNotice }));
vi.mock("./actions/chat.js", () => ({
  setSupervised: { dispatch: setSupervisedDispatch },
  setInterruptMode: { dispatch: setInterruptDispatch },
  compactChat: { dispatch: compactDispatch },
  renameChat: { dispatch: renameDispatch },
  downloadKiroSession: { dispatch: sessionDispatch },
  MAX_CHAT_NAME_UNITS: 128,
}));
vi.mock("./actions/messages.js", () => ({ copyClipboard: { dispatch: copyDispatch } }));
vi.mock("./chat-export.js", () => ({ downloadChatExport }));
vi.mock("./actions/runs.js", () => ({
  launchRun: { dispatch: launchDispatch },
  loadRecipes: { dispatch: recipesDispatch },
}));
// submit.ts alone decides prompt-versus-steer, so the goal row goes through it; the lower
// senders are mocked so a bypass is observable.
vi.mock("./submit.js", () => ({ submitPrompt }));
vi.mock("./chat-commands.js", () => ({ sendPromptTo }));
vi.mock("./transport.js", () => ({ send: transportSend, newMessageID: () => "m-1" }));

import type * as ChatOptionsModule from "./chat-options.js";

/**
 * Cache-buster for the re-imports below: `vi.resetModules()` does not re-evaluate in
 * Browser Mode (URL-keyed map), so busting the specifier mints a fresh instance (`.ts` for
 * coverage attribution). Only the module under test is busted, so `vi.mock` still applies.
 */
let bootSeq = 0;

/**
 * KAS's `parseGoalCommand`, transcribed verbatim from the 2.18.1 bundle
 * (`@kiro/agent/dist/server/acp-server.js`). The tests RUN it, so our reading of the regex
 * is not what is pinned. A null return sends the text to the MODEL as prose, so null is
 * never acceptable for anything the row sends.
 */
function parseGoalCommand(userText: string): { description: string; maxIterations: number } | null {
  const trimmed = userText.trim();
  if (!trimmed.startsWith("/goal ") && trimmed !== "/goal") {
    return null;
  }
  const body = trimmed.slice(6).trim();
  if (body === "") {
    return null;
  }
  const maxMatch = /\s+--max\s+(\d+)$/.exec(body);
  let maxIterations = 5;
  let description = body;
  if (maxMatch?.[1] !== undefined) {
    maxIterations = Math.min(Math.max(parseInt(maxMatch[1], 10), 1), 200);
    description = body.slice(0, maxMatch.index).trim();
  }
  if (description === "") {
    return null;
  }
  return { description, maxIterations };
}

/**
 * The minimum composer DOM initChatOptions touches, plus fresh module state. Returns the
 * initialised INSTANCE: the latch is module-level.
 */
async function mountMenu(): Promise<{ card: HTMLElement; mod: typeof ChatOptionsModule }> {
  document.body.innerHTML = `
    <span class="pill-slot">
      <button id="chat-options-btn" class="pill pill-expandable" type="button"></button>
      <span id="chat-options-card" class="pill-expand-content chat-options-card hidden"></span>
    </span>
  `;
  vi.resetModules();
  bootSeq++;
  const mod = (await import(
    /* @vite-ignore */ `./chat-options.ts?boot=${bootSeq}`
  )) as typeof ChatOptionsModule;
  mod.initChatOptions();
  return { card: document.getElementById("chat-options-card") as HTMLElement, mod };
}

function clickRow(card: HTMLElement, name: string): void {
  rowButton(card, name).click();
}

function rowButton(card: HTMLElement, name: string): HTMLButtonElement {
  for (const btn of Array.from(card.querySelectorAll<HTMLButtonElement>(".chat-opt-btn"))) {
    if (btn.querySelector(".chat-opt-name")?.textContent === name) {
      return btn;
    }
  }
  throw new Error(`no row named ${name}`);
}

beforeEach(() => {
  vi.clearAllMocks();
  activeID = "c-active";
  supervised = false;
  interruptMode = undefined;
  thinking = false;
  messageCount = 3;
  tangent = false;
  // Left armed: a regression reaching the recipe list proceeds to the explicit negative.
  recipesDispatch.mockResolvedValue({
    recipes: [
      { name: "publish", source: "bundled://publish" },
      { name: "goal", source: "bundled://goal", inputs: { goal: "string" } },
    ],
  });
});

describe("the chat-actions menu", () => {
  // Six actions, then the two switches: an addition or loss shows here.
  it("holds exactly eight entries, the switches last", async () => {
    const { card } = await mountMenu();
    const names = Array.from(card.querySelectorAll(".chat-opt-name")).map((n) => n.textContent);
    expect(names).toEqual([
      "Attach a file",
      "Set a goal",
      "Start a tangent",
      "Merge into parent chat",
      "Compact the context",
      "Rename chat",
      "Queue messages",
      "Supervised mode",
    ]);
  });

  // The switches are labels with a checkbox; the rest are buttons.
  it("renders the switches as labels with a checkbox and the rest as buttons", async () => {
    const { card } = await mountMenu();
    expect(card.querySelectorAll(".chat-opt-btn")).toHaveLength(6);
    for (const id of ["chat-opt-queue", "chat-opt-supervised"]) {
      const row = card.querySelector<HTMLLabelElement>(`label.chat-opt-row[for="${id}"]`);
      expect(row?.querySelector<HTMLInputElement>("input")?.type, id).toBe("checkbox");
    }
  });

  // A card of buttons inside its trigger <button> is invalid HTML (as in pill-expand.test.ts).
  it("keeps every row out of the trigger button", async () => {
    const { card } = await mountMenu();
    for (const btn of Array.from(card.querySelectorAll(".chat-opt-btn"))) {
      expect(btn.closest("#chat-options-btn")).toBeNull();
    }
  });

  // No em dashes in any hint, in either tangent state.
  it("carries no em dash in any hint, in either tangent state", async () => {
    for (const count of [0, 3]) {
      messageCount = count;
      const { card } = await mountMenu();
      for (const hint of Array.from(card.querySelectorAll(".chat-opt-hint"))) {
        expect(hint.textContent).not.toContain("\u2014");
      }
    }
  });
});

describe("the chat strip", () => {
  function strip(card: HTMLElement): HTMLButtonElement[] {
    return Array.from(card.querySelectorAll<HTMLButtonElement>(".chat-opt-strip-btn"));
  }

  it("leads the card with four captioned buttons, each with a full name", async () => {
    const { card } = await mountMenu();
    expect(card.firstElementChild?.classList.contains("chat-opt-strip")).toBe(true);
    expect(strip(card).map((b) => b.querySelector(".chat-opt-strip-caption")?.textContent)).toEqual(
      ["Link", "Markdown", "JSON", "Session"],
    );
    expect(strip(card).map((b) => b.getAttribute("aria-label"))).toEqual([
      "Copy link to this chat",
      "Export chat as Markdown",
      "Export chat as JSON",
      "Download Kiro session",
    ]);
  });

  it("copies the chat's full URL and flashes Copied", async () => {
    copyDispatch.mockImplementation((_t: string, o?: { onSuccess?: () => void }) => {
      o?.onSuccess?.();
      return Promise.resolve();
    });
    const { card } = await mountMenu();
    const link = strip(card)[0]!;
    link.click();
    expect(copyDispatch).toHaveBeenCalledWith(
      `${location.origin}/chat/c-active`,
      expect.objectContaining({ silent: true }),
    );
    expect(link.querySelector(".chat-opt-strip-caption")?.textContent).toBe("Copied");
  });

  it("routes the three downloads to their exports", async () => {
    const { card } = await mountMenu();
    const [, md, json, session] = strip(card);
    md!.click();
    json!.click();
    session!.click();
    expect(downloadChatExport).toHaveBeenNthCalledWith(1, "c-active", "My chat", "md");
    expect(downloadChatExport).toHaveBeenNthCalledWith(2, "c-active", "My chat", "json");
    expect(sessionDispatch).toHaveBeenCalledWith({ chatID: "c-active", name: "My chat" });
  });

  it("is unavailable with no chat", async () => {
    activeID = "";
    const { card } = await mountMenu();
    expect(strip(card).every((b) => b.disabled)).toBe(true);
  });
});

describe("rename chat", () => {
  it("opens a bounded field holding the current name and sends the new one", async () => {
    const { card } = await mountMenu();
    clickRow(card, "Rename chat");
    const input = card.querySelector<HTMLInputElement>('input[aria-label="Chat name"]')!;
    expect(input.value).toBe("My chat");
    expect(input.maxLength).toBe(128);
    input.value = "  Release notes ";
    input.form!.requestSubmit();
    expect(renameDispatch).toHaveBeenCalledWith({ chatID: "c-active", name: "Release notes" });
  });

  it("sends nothing for an unchanged name", async () => {
    const { card } = await mountMenu();
    clickRow(card, "Rename chat");
    card.querySelector<HTMLInputElement>('input[aria-label="Chat name"]')!.form!.requestSubmit();
    expect(renameDispatch).not.toHaveBeenCalled();
  });

  it("names the chat when it refuses an empty name", async () => {
    const { card } = await mountMenu();
    clickRow(card, "Rename chat");
    const input = card.querySelector<HTMLInputElement>('input[aria-label="Chat name"]')!;
    input.value = "   ";
    input.form!.requestSubmit();
    expect(renameDispatch).not.toHaveBeenCalled();
    expect(chatNotice).toHaveBeenCalledWith("c-active", "Type a name first", "error");
  });
});

describe("attach a file", () => {
  // No await between the menu click and the picker open: a file input cannot regain the
  // gesture's activation window. Asserted SYNCHRONOUSLY for that reason.
  it("opens the picker synchronously on the click", async () => {
    const { card } = await mountMenu();
    clickRow(card, "Attach a file");
    expect(openFilePicker).toHaveBeenCalledTimes(1);
  });

  // The picker opens a modal over the composer; an expanded card left behind it
  // sits under the modal still carrying pointer-events.
  it("collapses the card before opening the modal", async () => {
    const { card } = await mountMenu();
    clickRow(card, "Attach a file");
    expect(collapseAll).toHaveBeenCalledTimes(1);
    expect(collapseAll.mock.invocationCallOrder[0]).toBeLessThan(
      openFilePicker.mock.invocationCallOrder[0] as number,
    );
  });

  // Against uploadLimitHint, not a copied numeral that could disagree with the pre-flight.
  it("states the upload cap on the row", async () => {
    const { card } = await mountMenu();
    const hint = card.querySelector(".chat-opt-hint")?.textContent ?? "";
    expect(hint).toContain(uploadLimitHint().toLowerCase());
    expect(hint).not.toContain("\u2014");
  });
});

describe("merge into parent chat", () => {
  function mergeRowOf(card: HTMLElement): HTMLElement | null {
    return rowButton(card, "Merge into parent chat").closest(".chat-opt-entry");
  }

  it("is offered on a tangent only", async () => {
    const plain = await mountMenu();
    expect(mergeRowOf(plain.card)?.classList.contains("hidden")).toBe(true);
    tangent = true;
    const forked = await mountMenu();
    expect(mergeRowOf(forked.card)?.classList.contains("hidden")).toBe(false);
  });

  it("merges the active tangent", async () => {
    tangent = true;
    const { card } = await mountMenu();
    clickRow(card, "Merge into parent chat");
    expect(mergeTangentChat).toHaveBeenCalledWith("c-active");
  });

  it("is unavailable mid-turn and says what unlocks it", async () => {
    tangent = true;
    thinking = true;
    const { card } = await mountMenu();
    const btn = rowButton(card, "Merge into parent chat");
    expect(btn.disabled).toBe(true);
    expect(btn.querySelector(".chat-opt-hint")?.textContent).toBe(
      "Wait for this turn to finish, then merge",
    );
  });
});

describe("start a tangent", () => {
  it("opens a tangent off the active chat", async () => {
    const { card } = await mountMenu();
    clickRow(card, "Start a tangent");
    expect(openTangentChat).toHaveBeenCalledWith("c-active");
    expect(collapseAll).toHaveBeenCalled();
  });

  // Unavailable, not error-toasting, with no conversation: the fork would 404
  // (errForkParentUnknown) after the sub-tab opened.
  it.each([
    ["a brand-new chat with no messages", "c-active", 0],
    ["no active chat at all", "", 3],
  ])("disables the row on %s", async (_desc, id, count) => {
    activeID = id;
    messageCount = count;
    const { card } = await mountMenu();
    expect(rowButton(card, "Start a tangent").disabled).toBe(true);
  });

  it("enables the row once the chat holds a conversation", async () => {
    const { card } = await mountMenu();
    expect(rowButton(card, "Start a tangent").disabled).toBe(false);
  });

  // The hint names what unlocks the row.
  it("swaps the hint for one naming what unlocks the row", async () => {
    messageCount = 0;
    const { card } = await mountMenu();
    const hint = rowButton(card, "Start a tangent").querySelector(".chat-opt-hint")?.textContent;
    expect(hint).toContain("Send a message first");
  });

  // The disabled attribute is the refusal, so a press says nothing.
  it("dispatches nothing and says nothing when pressed with no conversation", async () => {
    messageCount = 0;
    const { card } = await mountMenu();
    clickRow(card, "Start a tangent");
    expect(openTangentChat).not.toHaveBeenCalled();
    expect(toastError).not.toHaveBeenCalled();
    expect(chatNotice).not.toHaveBeenCalled();
  });

  it("refuses in the handler as well, so a stale enabled row cannot fork nothing", async () => {
    const { card } = await mountMenu();
    messageCount = 0;
    const btn = rowButton(card, "Start a tangent");
    btn.disabled = false;
    btn.click();
    expect(openTangentChat).not.toHaveBeenCalled();
    expect(chatNotice).toHaveBeenCalledTimes(1);
    expect(chatNotice).toHaveBeenCalledWith("c-active", expect.any(String), "error");
  });
});

describe("set a goal", () => {
  function openForm(card: HTMLElement): HTMLFormElement {
    clickRow(card, "Set a goal");
    const form = card.querySelector<HTMLFormElement>(".chat-opt-form");
    if (form === null) {
      throw new Error("the goal row opened no form");
    }
    return form;
  }

  function field(form: HTMLFormElement, label: string): HTMLInputElement {
    const input = form.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`);
    if (input === null) {
      throw new Error(`the goal form has no ${label} field`);
    }
    return input;
  }

  /** Fill the form and submit it. Returns the text the row sent, or undefined
   *  when it sent nothing at all. */
  function setGoal(card: HTMLElement, objective: string, cap = ""): string | undefined {
    const form = openForm(card);
    field(form, "Goal").value = objective;
    field(form, "Max iterations").value = cap;
    form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    const call = submitPrompt.mock.calls[0] as [string, string] | undefined;
    return call?.[1];
  }

  // The exact string and its parse: a null parse reaches the model as prose, a wrong one sets
  // a different goal.
  it("sends the objective alone when no cap is given", async () => {
    const { card } = await mountMenu();
    const sent = setGoal(card, "make the test suite pass");
    expect(sent).toBe("/goal make the test suite pass");
    expect(submitPrompt).toHaveBeenCalledWith("c-active", "/goal make the test suite pass");
    // No suffix, so KAS's own default applies rather than a number marotte
    // restates. 5 here is the parser's value, read back out of the parser.
    expect(parseGoalCommand(sent as string)).toEqual({
      description: "make the test suite pass",
      maxIterations: 5,
    });
  });

  it("appends the cap as the last thing in the command", async () => {
    const { card } = await mountMenu();
    const sent = setGoal(card, "make the test suite pass", "5");
    expect(sent).toBe("/goal make the test suite pass --max 5");
    // The regex is anchored at the end (`/\s+--max\s+(\d+)$/`), so a suffix that
    // is not last is not a cap at all — it becomes part of the objective.
    expect(sent as string).toMatch(/ --max 5$/);
    expect(parseGoalCommand(sent as string)).toEqual({
      description: "make the test suite pass",
      maxIterations: 5,
    });
  });

  // Clamped by marotte with KAS's arithmetic, so the suffix is always kept; `\d+` cannot
  // match `-3`, which would otherwise join the objective.
  it.each([
    ["0", 1],
    ["-3", 1],
    ["1", 1],
    ["7", 7],
    ["200", 200],
    ["201", 200],
    ["9000", 200],
  ])("clamps a cap of %s to %i", async (typed, want) => {
    const { card } = await mountMenu();
    const sent = setGoal(card, "ship it", typed);
    expect(sent).toBe(`/goal ship it --max ${want}`);
    expect(parseGoalCommand(sent as string)?.maxIterations).toBe(want);
  });

  // A non-integer cap is DROPPED: `--max soon` would become part of the objective.
  it.each([
    ["a word", "soon"],
    ["a fraction", "5.5"],
    ["whitespace", "   "],
    ["a numeral with units", "5 iterations"],
  ])("ignores %s as a cap", async (_desc, typed) => {
    const { card } = await mountMenu();
    const sent = setGoal(card, "ship it", typed);
    expect(sent).toBe("/goal ship it");
    expect(parseGoalCommand(sent as string)?.description).toBe("ship it");
  });

  // No recipe route: a launch by source ran to 200 iterations whatever was asked.
  it("launches no run and fetches no recipe", async () => {
    const { card } = await mountMenu();
    setGoal(card, "ship it", "5");
    expect(recipesDispatch).not.toHaveBeenCalled();
    expect(launchDispatch).not.toHaveBeenCalled();
    expect(openRunView).not.toHaveBeenCalled();
  });

  // Through submit.ts, the one prompt-versus-steer decision and the shared send lifecycle.
  it("sends through the composer's own send path", async () => {
    const { card } = await mountMenu();
    setGoal(card, "ship it");
    expect(submitPrompt).toHaveBeenCalledTimes(1);
    expect(sendPromptTo).not.toHaveBeenCalled();
    expect(transportSend).not.toHaveBeenCalled();
  });

  // A bare `/goal` parses to null and would reach the model; refused here instead.
  it.each([
    ["empty", ""],
    ["whitespace only", "   "],
  ])("refuses a %s objective rather than sending an unparseable command", async (_d, objective) => {
    const { card } = await mountMenu();
    expect(setGoal(card, objective)).toBeUndefined();
    expect(submitPrompt).not.toHaveBeenCalled();
    expect(chatNotice).toHaveBeenCalledWith("c-active", expect.any(String), "error");
  });

  // Mid-turn Send means STEER, and parseGoalCommand only runs on `session/prompt`.
  it("refuses while a turn is running", async () => {
    thinking = true;
    const { card } = await mountMenu();
    expect(setGoal(card, "ship it")).toBeUndefined();
    expect(submitPrompt).not.toHaveBeenCalled();
    expect(chatNotice).toHaveBeenCalledWith("c-active", expect.any(String), "error");
  });

  it("refuses when no chat is active", async () => {
    activeID = "";
    const { card } = await mountMenu();
    expect(setGoal(card, "ship it")).toBeUndefined();
    expect(submitPrompt).not.toHaveBeenCalled();
    expect(toastError).toHaveBeenCalledTimes(1);
  });

  // No clear verb: `/goal clear` would be a goal named "clear". Stopping is cancelling the run.
  it("offers no clear verb and composes none", async () => {
    const { card } = await mountMenu();
    const form = openForm(card);
    expect(Array.from(form.querySelectorAll("button")).map((b) => b.textContent)).toEqual([
      "Set goal",
    ]);
    expect((card.textContent ?? "").toLowerCase()).not.toContain("clear");
    for (const btn of Array.from(card.querySelectorAll<HTMLButtonElement>(".chat-opt-btn"))) {
      btn.click();
    }
    for (const call of submitPrompt.mock.calls as [string, string][]) {
      expect(parseGoalCommand(call[1])?.description).not.toBe("clear");
    }
  });

  // Inline, not a modal — the Workflows tab's idiom. A second click closes the
  // form, so the row is a toggle rather than a form-stacker.
  it("toggles the inline form rather than stacking one per click", async () => {
    const { card } = await mountMenu();
    clickRow(card, "Set a goal");
    expect(card.querySelectorAll(".chat-opt-form")).toHaveLength(1);
    clickRow(card, "Set a goal");
    expect(card.querySelectorAll(".chat-opt-form")).toHaveLength(0);
  });

  // The card outlives every chat switch (it is built once at init), so the chat id
  // has to be read when the form is submitted rather than when it was built.
  it("sends to the chat that is active at submit time", async () => {
    const { card } = await mountMenu();
    const form = openForm(card);
    field(form, "Goal").value = "ship it";
    activeID = "c-other";
    form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    expect(submitPrompt).toHaveBeenCalledWith("c-other", "/goal ship it");
  });
});

describe("compact the context", () => {
  // The row is the second door onto the action `/compact` already dispatches, so
  // what matters is that it reaches THAT action once with the active chat's id.
  it("dispatches the shared compact action for the active chat", async () => {
    const { card } = await mountMenu();
    clickRow(card, "Compact the context");
    expect(compactDispatch).toHaveBeenCalledTimes(1);
    expect(compactDispatch).toHaveBeenCalledWith({ chatID: "c-active" });
    expect(collapseAll).toHaveBeenCalled();
  });

  // The two states CmdCompact refuses (errNoBridge, errCompactRefused); disabled like the
  // tangent row.
  it.each([
    ["a brand-new chat with no messages", "c-active", 0, false],
    ["no active chat at all", "", 3, false],
    ["a turn in flight", "c-active", 3, true],
  ])("disables the row on %s", async (_desc, id, count, busy) => {
    activeID = id;
    messageCount = count;
    thinking = busy;
    const { card } = await mountMenu();
    expect(rowButton(card, "Compact the context").disabled).toBe(true);
  });

  it("enables the row on an idle chat that holds a conversation", async () => {
    const { card } = await mountMenu();
    expect(rowButton(card, "Compact the context").disabled).toBe(false);
  });

  // Two distinct hints for two distinct refusals: the action to take differs, so a
  // shared sentence would be wrong for one of them.
  it.each([
    ["Send a message first", "", 0, false],
    ["Wait for this turn to finish", "c-active", 3, true],
  ])("swaps the hint to name %s", async (fragment, id, count, busy) => {
    activeID = id === "" ? "c-active" : id;
    messageCount = count;
    thinking = busy;
    const { card } = await mountMenu();
    const hint = rowButton(card, "Compact the context").querySelector(".chat-opt-hint");
    expect(hint?.textContent).toContain(fragment);
  });

  // The disabled attribute IS the refusal, so pressing it says nothing at all.
  it("dispatches nothing and says nothing when pressed with no conversation", async () => {
    messageCount = 0;
    const { card } = await mountMenu();
    clickRow(card, "Compact the context");
    expect(compactDispatch).not.toHaveBeenCalled();
    expect(toastError).not.toHaveBeenCalled();
    expect(chatNotice).not.toHaveBeenCalled();
  });

  it.each([
    ["with no conversation", 0, false],
    ["mid-turn", 3, true],
  ])("refuses in the handler as well when a stale row is enabled %s", async (_d, count, busy) => {
    const { card } = await mountMenu();
    messageCount = count;
    thinking = busy;
    const btn = rowButton(card, "Compact the context");
    btn.disabled = false;
    btn.click();
    expect(compactDispatch).not.toHaveBeenCalled();
    expect(chatNotice).toHaveBeenCalledTimes(1);
    expect(chatNotice).toHaveBeenCalledWith("c-active", expect.any(String), "error");
  });

  // The card is built once at init and outlives every chat switch.
  it("compacts the chat that is active at click time", async () => {
    const { card } = await mountMenu();
    activeID = "c-other";
    clickRow(card, "Compact the context");
    expect(compactDispatch).toHaveBeenCalledWith({ chatID: "c-other" });
  });

  // No second action, and no other verb: this row is a door onto chat.compact.
  it("adds no second send path", async () => {
    const { card } = await mountMenu();
    clickRow(card, "Compact the context");
    expect(submitPrompt).not.toHaveBeenCalled();
    expect(sendPromptTo).not.toHaveBeenCalled();
    expect(transportSend).not.toHaveBeenCalled();
  });
});

describe("supervised mode", () => {
  it("dispatches the toggle for the active chat", async () => {
    const { card } = await mountMenu();
    const box = card.querySelector<HTMLInputElement>("#chat-opt-supervised");
    box!.checked = true;
    box!.dispatchEvent(new Event("change"));
    expect(setSupervisedDispatch).toHaveBeenCalledWith({ chatID: "c-active", enabled: true });
  });

  // No chat: the visual resets and nothing persists; the new-chat default lives in Settings.
  it("resets the visual and persists nothing with no active chat", async () => {
    activeID = "";
    const { card } = await mountMenu();
    const box = card.querySelector<HTMLInputElement>("#chat-opt-supervised");
    box!.checked = true;
    box!.dispatchEvent(new Event("change"));
    expect(setSupervisedDispatch).not.toHaveBeenCalled();
    expect(box!.checked).toBe(false);
  });
});

// What Send means while a turn runs: a per-chat switch, on for queue, off (steer) the default.
describe("the interrupt mode", () => {
  function box(card: HTMLElement): HTMLInputElement {
    const el = card.querySelector<HTMLInputElement>("#chat-opt-queue");
    if (el === null) {
      throw new Error("no queue switch");
    }
    return el;
  }

  it("is off for a chat that has recorded no mode", async () => {
    const { card } = await mountMenu();
    expect(box(card).checked).toBe(false);
  });

  it("mirrors the active chat's recorded mode", async () => {
    interruptMode = "queue";
    const { card } = await mountMenu();
    expect(box(card).checked).toBe(true);
  });

  it("dispatches queue when switched on and steer when switched off", async () => {
    const { card } = await mountMenu();
    box(card).checked = true;
    box(card).dispatchEvent(new Event("change"));
    expect(setInterruptDispatch).toHaveBeenLastCalledWith({ chatID: "c-active", mode: "queue" });
    box(card).checked = false;
    box(card).dispatchEvent(new Event("change"));
    expect(setInterruptDispatch).toHaveBeenLastCalledWith({ chatID: "c-active", mode: "steer" });
  });

  it("records nothing and resets the visual with no active chat", async () => {
    activeID = "";
    const { card } = await mountMenu();
    box(card).checked = true;
    box(card).dispatchEvent(new Event("change"));
    expect(setInterruptDispatch).not.toHaveBeenCalled();
    expect(box(card).checked).toBe(false);
  });
});

// initChatOptions is called once from app.ts, but the latch is what makes a
// second call safe — without it a re-init would append a second set of rows.
describe("initChatOptions", () => {
  it("is idempotent", async () => {
    const { card, mod } = await mountMenu();
    mod.initChatOptions();
    expect(card.querySelectorAll(".chat-opt-name")).toHaveLength(8);
  });
});
