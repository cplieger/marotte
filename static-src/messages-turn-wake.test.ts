// A woken turn through the real store and renderer: its header names what woke it, the message that
// woke it is drawn there once and leaves the previous turn, on reload and live, and Copy as text
// starts with it. Fixtures are a real wake of this chat's log (run wf_1729f9b5688ec7bf, step ratify).

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { Session } from "./types.js";
import { makeSession } from "./__test-helpers__/model.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type { Entry, EntryTurnWake } from "./wire/types.gen.js";
import type * as Scroll from "./scroll.js";
import type * as ActionsMessages from "./actions/messages.js";

for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
  "send-btn",
  "prompt-input",
]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "div");
  d.id = id;
  document.body.appendChild(d);
}

vi.mock("./scroll.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Scroll>()),
  ...(await import("./__test-helpers__/scroll-mock.js")).scrollMock,
}));

const { copied } = vi.hoisted(() => ({ copied: [] as string[] }));
vi.mock("./actions/messages.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ActionsMessages>()),
  copyClipboard: {
    dispatch: (text: string) => {
      copied.push(text);
      return Promise.resolve();
    },
  },
}));

const store = await import("./store.js");
const messages = await import("./messages.js");
const { resetFoldState } = await import("./fold-state.js");
const { KEY_ATTR } = await import("./reconcile.js");
const { settleWorkflowMessages, turnsInOrder } = await import("./workflow-delivery.js");

mountAppCSS();
messages.mountChatView();

const RUN = "wf_1729f9b5688ec7bf";
const WAKER = "wfmsg-01a14782-572f-722a-b51c-a6ce3e86aaba";
const MESSAGE =
  "Handoff blocked: update_workflow is not in my toolset (not loadable by tool_load and not callable " +
  "with tool_call), so I cannot replace the remaining steps with the build tail. I did not change the " +
  "workflow. The ratified packet list is ready at " +
  "/workspace/_scratch/release-impact/leg2-crew090-cli232/tail-packets.txt. It covers P1 to P5, all " +
  "marotte, read from implement-set.yaml on disk, with your cli-u18 URL-strip sentence appended to carry " +
  "into the P4 implementer and both review seats. It is the <PACKETS> value for the tail brief. Please " +
  "run update_workflow action replace_remaining on wf_1729f9b5688ec7bf with that text, or give this " +
  "step the tool.";
const REPLY = "I'll run replace_remaining myself.";

function sealed(
  turn: string,
  seq: number,
  kind: Entry["kind"],
  payload: unknown,
  id = `${turn}-e${String(seq)}`,
): Entry {
  return { id, turn, kind, seq, ts: seq + 1, payload };
}

function promptOpen(turn: string, n: number, text: string): Entry {
  return sealed(turn, 0, "turn_open", { prompt: { id: `${turn}-p`, text }, source: "prompt", n });
}

function wakeOpen(turn: string, n: number): Entry {
  return sealed(turn, 0, "turn_open", { source: "wire_turn_start", n });
}

function wakerRow(turn: string, seq: number, id = WAKER, text = MESSAGE): Entry {
  return sealed(
    turn,
    seq,
    "steer",
    { text, origin: "step", step: "ratify", origin_run: RUN, severity: "error", produced_ts: 1 },
    id,
  );
}

function wake(w: EntryTurnWake, turn: string, seq: number): Entry {
  return sealed(turn, seq, "turn_wake", w, `${turn}-wake`);
}

const STEP_WAKE: EntryTurnWake = {
  reason: "step_message",
  steers: [
    {
      id: WAKER,
      run: RUN,
      run_label: "release-impact-leg2",
      step: "ratify",
      severity: "error",
      text: MESSAGE,
    },
  ],
};

/** The turn before the wake, closed, with the waking row filed after its close. */
function before(prompt = "launch the leg"): Entry[] {
  return [
    promptOpen("t1", 1, prompt),
    sealed("t1", 1, "text", { text: "Launched." }, "t1-say"),
    sealed("t1", 2, "turn_close", { outcome: "completed" }),
    wakerRow("t1", 3),
  ];
}

function woken(w: EntryTurnWake = STEP_WAKE): Entry[] {
  return [
    wakeOpen("t2", 2),
    wake(w, "t2", 1),
    sealed("t2", 2, "text", { text: REPLY }, "t2-say"),
    sealed("t2", 3, "turn_close", { outcome: "completed" }),
  ];
}

/** `turn` with a thought before its last entries, so its folded face hides something and it folds. */
function withThought(entries: Entry[]): Entry[] {
  const at = entries.length - 2;
  const turn = entries[0]?.turn ?? "";
  const thought = sealed(turn, at, "thinking", { text: "Checking the run." }, `${turn}-think`);
  return [
    ...entries.slice(0, at),
    thought,
    ...entries.slice(at).map((e) => ({ ...e, seq: e.seq + 1 })),
  ];
}

function load(chatID: string, turns: readonly (readonly Entry[])[]): void {
  const s: Session = makeSession({ id: chatID, name: chatID });
  for (const entries of turns) {
    const first = entries[0];
    if (first === undefined) {
      continue;
    }
    s.turns.set(first.turn, { entries: [...entries], openEntries: new Map() });
    s.turn_order.push(first.turn);
  }
  s.turn_count = s.turn_order.length;
  // What every page install runs (store-load.ts) before the transcript draws it.
  settleWorkflowMessages(turnsInOrder(s.turn_order, s.turns));
  store.setSessions([s]);
  store.setActive(chatID);
  store.bumpMessages(chatID, "load");
}

function viewRoot(): HTMLElement {
  return messages.activeTranscriptView() ?? document.getElementById("messages")!;
}

function card(turnID: string): HTMLElement {
  for (const child of viewRoot().children) {
    if (child.getAttribute(KEY_ATTR) === turnID) {
      return child as HTMLElement;
    }
  }
  throw new Error(`no card for turn ${turnID}`);
}

function header(turnID: string): { label: string; text: string; trigger: string } {
  const h = card(turnID).querySelector<HTMLElement>(":scope > .turn-header")!;
  return {
    label: h.querySelector(".turn-wake")?.textContent ?? "",
    text: h.querySelector(".turn-req-text")?.textContent ?? "",
    trigger: h.dataset["trigger"] ?? "",
  };
}

function notes(): string[] {
  return [...viewRoot().querySelectorAll<HTMLElement>(".steer-note")].map(
    (n) => n.querySelector(".steer-note-text")?.textContent ?? "",
  );
}

function copyText(turnID: string): string {
  const btn = card(turnID).querySelector<HTMLButtonElement>(
    '.turn-action-btn[aria-label="Copy as text"]',
  );
  copied.length = 0;
  btn?.click();
  return copied[copied.length - 1] ?? "";
}

beforeEach(() => {
  messages.teardownAll();
  localStorage.clear();
  resetFoldState();
  store.setSessions([]);
  store.setActive("");
  copied.length = 0;
});

describe("a step's message that woke the chat", () => {
  it("is the woken turn's trigger, drawn once, after a reload", async () => {
    load("wake-reload", [before(), woken()]);

    await vi.waitFor(() => {
      expect(header("t2")).toEqual({
        label: "release-impact-leg2 \u00b7 ratify reports an error",
        text: MESSAGE,
        trigger: "wake",
      });
    });
    expect(notes()).toEqual([]);
    expect(card("t2").querySelector(".turn-wake button.turn-wake-run")?.textContent).toBe(
      "release-impact-leg2",
    );
  });

  it("leaves the previous turn when the wake lands after its card was drawn", async () => {
    const chat = "wake-live";
    load(chat, [before()]);
    await vi.waitFor(() => {
      expect(notes()).toEqual([MESSAGE]);
    });

    store.openTurn(chat, wakeOpen("t2", 2));
    for (const e of woken().slice(1)) {
      store.appendEntry(chat, e);
    }

    await vi.waitFor(() => {
      expect(header("t2").label).toBe("release-impact-leg2 \u00b7 ratify reports an error");
    });
    expect(notes()).toEqual([]);
  });

  it("keeps its card when the wake that names it re-delivers it after a stop", async () => {
    const rewake: EntryTurnWake = {
      reason: "rewake",
      steers: [
        { id: WAKER, run: RUN, run_label: "release-impact-leg2", step: "ratify", text: MESSAGE },
      ],
    };
    load("wake-rewake", [before(), woken(rewake)]);

    await vi.waitFor(() => {
      expect(header("t2").label).toBe(
        "Resumed with 1 workflow message you stopped before it was read",
      );
    });
    expect(header("t2").text).toBe(`release-impact-leg2 \u00b7 ratify: ${MESSAGE}`);
    expect(notes()).toEqual([MESSAGE]);
  });

  it("opens Copy as text, label then message, before the reply", async () => {
    load("wake-copy", [before(), woken()]);
    await vi.waitFor(() => {
      expect(header("t2").trigger).toBe("wake");
    });

    expect(copyText("t2")).toBe(
      `release-impact-leg2 \u00b7 ratify reports an error\n${MESSAGE}\n\n${REPLY}`,
    );
  });

  it("collapses on a folded turn as a folded prompt does, and shows in full on the last turn", async () => {
    const later = [
      promptOpen("t3", 3, "next"),
      sealed("t3", 1, "text", { text: "ok" }, "t3-say"),
      sealed("t3", 2, "turn_close", { outcome: "completed" }),
    ];
    load("wake-fold", [withThought(before(MESSAGE)), withThought(woken()), later]);
    const reqText = (turn: string): HTMLElement =>
      card(turn).querySelector<HTMLElement>(":scope > .turn-header .turn-req-text")!;
    await vi.waitFor(() => {
      expect(card("t2").hasAttribute("data-folded")).toBe(true);
      expect(reqText("t2").hasAttribute("data-cut")).toBe(true);
      expect(reqText("t1").hasAttribute("data-cut")).toBe(true);
    });
    const folded = reqText("t2").clientHeight;
    expect(folded).toBe(reqText("t1").clientHeight);

    load("wake-fold-last", [before(), woken()]);
    await vi.waitFor(() => {
      expect(card("t2").hasAttribute("data-folded")).toBe(false);
      expect(reqText("t2").clientHeight).toBeGreaterThan(folded);
    });
    expect(reqText("t2").hasAttribute("data-cut")).toBe(false);
    expect(reqText("t2").scrollHeight).toBe(reqText("t2").clientHeight);
  });
});
