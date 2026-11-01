import { describe, it, expect, vi, beforeEach, onTestFinished } from "vitest";
import type { Session } from "./types.js";
import { makeSession } from "./__test-helpers__/model.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type { Entry, EntryTurnWake } from "./wire/types.gen.js";
import type * as Scroll from "./scroll.js";
import type * as ActionsMessages from "./actions/messages.js";
import type * as ApiClientModule from "./api-client.js";
import type * as RunViewModule from "./run-view.js";

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

const openRunView = vi.hoisted(() => vi.fn());
vi.mock("./run-view.js", async (importOriginal) => ({
  ...(await importOriginal<typeof RunViewModule>()),
  openRunView,
}));

/** The run names `GET /api/runs/{id}` answers, which the run tab reads; a run absent here answers a failed read. */
const runNames = vi.hoisted(() => new Map<string, string>());
vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClientModule>()),
  apiGetOrError: vi.fn((path: string) => {
    const id = decodeURIComponent(/^\/api\/runs\/([^/?]+)$/.exec(path)?.[1] ?? "");
    const name = runNames.get(id);
    return Promise.resolve(
      name === undefined
        ? { ok: false, status: 0, data: null, error: "" }
        : {
            ok: true,
            status: 200,
            data: { workflowId: id, state: { workflowId: id, status: "running", runLabel: name } },
            error: "",
          },
    );
  }),
}));

const store = await import("./store.js");
const { noteRunLabel } = await import("./run-store.js");
const messages = await import("./messages.js");
const { resetFoldState } = await import("./fold-state.js");
const { KEY_ATTR } = await import("./reconcile.js");
const { settleWorkflowMessages, turnsInOrder } = await import("./workflow-delivery.js");

mountAppCSS();
messages.mountChatView();

const RUN = "wf_5e1f0c2a9b7d4e36";
const OTHER = "wf_7c3a9e1f0b2d4a58";
const WAKER = "wfmsg-3f2a6c1d-0b4e-4a7f-9c2d-5e8f1a3b6c90";
const MESSAGE =
  "The site build failed before any edit: the install guide has no front matter, so the link " +
  "check cannot run on the built pages. I did not change any file. The full build log is in " +
  "the run's output; the failing page is the only one without a title block. Please add the front " +
  "matter or tell me to skip that page, and I will rerun the build and the link check right after. " +
  "Nothing else in the docs tree needs a change for this release, and the link report from the last " +
  "good build is still in the run if you want to compare the two runs side by side.";
const REPLY = "I'll add the front matter and rerun the build.";

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
    {
      text,
      origin: "step",
      step: "check-links",
      origin_run: RUN,
      severity: "error",
      produced_ts: 1,
    },
    id,
  );
}

function wake(w: EntryTurnWake, turn: string, seq: number): Entry {
  return sealed(turn, seq, "turn_wake", w, `${turn}-wake`);
}

const UNNAMED_WAKE: EntryTurnWake = {
  reason: "step_message",
  steers: [{ id: WAKER, run: RUN, step: "check-links", severity: "error", text: MESSAGE }],
};

const STEP_WAKE: EntryTurnWake = {
  reason: "step_message",
  steers: [
    {
      id: WAKER,
      run: RUN,
      run_name: "publish-docs",
      step: "check-links",
      severity: "error",
      text: MESSAGE,
    },
  ],
};

/** The turn before the wake, closed, with the waking row filed after its close. */
function before(prompt = "publish the docs"): Entry[] {
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

function copyMarkdown(turnID: string): string {
  const btn = card(turnID).querySelector<HTMLButtonElement>(
    '.turn-action-btn[aria-label="Copy as markdown"]',
  );
  copied.length = 0;
  btn?.click();
  return copied[copied.length - 1] ?? "";
}

function cardKeys(): string[] {
  return [...viewRoot().children].map((c) => c.getAttribute(KEY_ATTR) ?? "");
}

const LABEL = "publish-docs \u00b7 check-links reports an error";

beforeEach(() => {
  runNames.clear();
  runNames.set(RUN, "publish-docs");
  runNames.set(OTHER, "nightly-report");
  openRunView.mockClear();
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
      expect(header("t2")).toEqual({ label: LABEL, text: MESSAGE, trigger: "wake" });
    });
    expect(notes()).toEqual([]);
  });

  it("names its run as its wake does, and the name opens the run", async () => {
    load("wake-name", [before(), woken()]);
    const btn = (): HTMLButtonElement | null =>
      card("t2").querySelector<HTMLButtonElement>(".turn-wake button.turn-wake-run");
    await vi.waitFor(() => {
      expect(btn()?.textContent).toBe("publish-docs");
    });

    btn()?.click();
    await vi.waitFor(() => {
      expect(openRunView).toHaveBeenCalledWith(RUN, "publish-docs", "wake-name");
    });
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
      expect(header("t2").label).toBe(LABEL);
    });
    expect(notes()).toEqual([]);
  });

  it("opens Copy as text and Copy as markdown, label then message, before the reply", async () => {
    load("wake-copy", [before(), woken()]);
    await vi.waitFor(() => {
      expect(header("t2").label).toBe(LABEL);
    });

    expect(copyText("t2")).toBe(`${LABEL}\n${MESSAGE}\n\n${REPLY}`);
    expect(copyMarkdown("t2")).toBe(`> ${LABEL}\n> ${MESSAGE}\n\n${REPLY}`);
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

describe("a re-delivery after a stop", () => {
  const SECOND = "wfmsg-7b1c2d3e-4f50-4a61-8b72-9c83d4e5f601";
  const RESULT = "The nightly report is written.";
  const rewake: EntryTurnWake = {
    reason: "rewake",
    steers: [
      {
        id: WAKER,
        run: RUN,
        run_name: "publish-docs",
        step: "check-links",
        severity: "error",
        text: MESSAGE,
      },
      {
        id: SECOND,
        run: OTHER,
        run_name: "nightly-report",
        step: "collect",
        severity: "success",
        text: RESULT,
      },
    ],
  };
  const stopped = (): Entry[] => [
    promptOpen("t1", 1, "keep going"),
    wakerRow("t1", 1),
    sealed(
      "t1",
      2,
      "steer",
      {
        text: RESULT,
        origin: "step",
        step: "collect",
        origin_run: OTHER,
        severity: "success",
        produced_ts: 2,
      },
      SECOND,
    ),
    sealed("t1", 3, "turn_close", { outcome: "cancelled" }),
  ];

  it("draws each message once, in the header, each opening its own run", async () => {
    load("wake-rewake", [stopped(), woken(rewake)]);

    await vi.waitFor(() => {
      expect(header("t2").text).toBe(
        `publish-docs \u00b7 check-links: ${MESSAGE}\nnightly-report \u00b7 collect: ${RESULT}`,
      );
    });
    expect(header("t2").label).toBe(
      "Resumed with 2 workflow messages you stopped before they were read",
    );
    expect(notes()).toEqual([]);

    const buttons = card("t2").querySelectorAll<HTMLButtonElement>(
      ".turn-req-text button.turn-wake-row-run",
    );
    buttons[1]?.click();
    await vi.waitFor(() => {
      expect(openRunView).toHaveBeenCalledWith(OTHER, "nightly-report", "wake-rewake");
    });
  });

  it("takes no focus on a run name its folded clamp hides, and gives it back when the turn opens", async () => {
    const later = [
      promptOpen("t3", 3, "next"),
      sealed("t3", 1, "text", { text: "ok" }, "t3-say"),
      sealed("t3", 2, "turn_close", { outcome: "completed" }),
    ];
    load("wake-rewake-fold", [stopped(), withThought(woken(rewake)), later]);
    const second = (): HTMLButtonElement | undefined =>
      [
        ...card("t2").querySelectorAll<HTMLButtonElement>(
          ".turn-req-text button.turn-wake-row-run",
        ),
      ][1];
    await vi.waitFor(() => {
      expect(card("t2").hasAttribute("data-folded")).toBe(true);
      expect(second()).toBeDefined();
    });
    const clamp = card("t2").querySelector<HTMLElement>(".turn-req-text")!.getBoundingClientRect();
    expect(second()!.getBoundingClientRect().top).toBeGreaterThanOrEqual(clamp.bottom);
    second()!.focus();
    expect(document.activeElement).not.toBe(second());
    const label = card("t2").querySelector<HTMLButtonElement>(".turn-wake button.turn-wake-run");
    expect(label).toBeNull();

    const toggle = (): void => {
      card("t2").querySelector<HTMLButtonElement>(".turn-fold-toggle")!.click();
    };
    toggle();
    await vi.waitFor(() => {
      expect(card("t2").hasAttribute("data-folded")).toBe(false);
    });
    second()!.focus();
    expect(document.activeElement).toBe(second());

    toggle();
    expect(card("t2").hasAttribute("data-folded")).toBe(true);
    expect(second()!.inert).toBe(true);
    toggle();
    expect(second()!.inert).toBe(false);
  });
});

describe("a woken turn whose run's name arrives after it is drawn", () => {
  const PLACEHOLDER = "A workflow \u00b7 check-links reports an error";
  const railLink = (): HTMLAnchorElement | undefined =>
    [...document.querySelectorAll<HTMLAnchorElement>(".turn-map .turn-pill-link")].find((a) =>
      a.getAttribute("href")?.endsWith("#turn-2"),
    );

  it("shows a workflow everywhere, never a generic recipe, then the name everywhere", async () => {
    const chat = "wake-arrival";
    // The rail draws only beside a transcript that scrolls.
    const outer = document.getElementById("messages-wrap-outer")!;
    const wrap = document.getElementById("messages-wrap")!;
    outer.style.cssText = "position:relative;block-size:400px";
    wrap.style.cssText = "block-size:200px;overflow-y:auto";
    onTestFinished(() => {
      outer.style.cssText = "";
      wrap.style.cssText = "";
    });
    runNames.clear();
    noteRunLabel(RUN, "workflow-from-prompt");
    load(chat, [before(), woken(UNNAMED_WAKE)]);
    messages.pointTurnRail(chat);

    await vi.waitFor(() => {
      expect(header("t2").label).toBe(PLACEHOLDER);
      expect(railLink()?.getAttribute("aria-label")).toContain(PLACEHOLDER);
    });
    expect(card("t2").querySelector(".turn-wake button.turn-wake-run")?.textContent).toBe(
      "A workflow",
    );
    expect(railLink()?.dataset["tooltip"]).toContain(PLACEHOLDER);
    expect(copyText("t2")).toBe(`${PLACEHOLDER}\n${MESSAGE}\n\n${REPLY}`);
    expect(document.body.textContent).not.toContain("workflow-from-prompt");

    store.appendEntry(chat, sealed("t2", 4, "run_named", { run: RUN, name: "publish-docs" }));

    await vi.waitFor(() => {
      expect(header("t2").label).toBe(LABEL);
      expect(railLink()?.getAttribute("aria-label")).toContain(LABEL);
    });
    expect(railLink()?.dataset["tooltip"]).toContain(LABEL);
    expect(copyText("t2")).toBe(`${LABEL}\n${MESSAGE}\n\n${REPLY}`);
    expect(card("t2").querySelector(".turn-wake button.turn-wake-run")?.textContent).toBe(
      "publish-docs",
    );
  });
});

describe("a turn whose only drawn row a later wake takes", () => {
  const empty = (): Entry[] => [
    wakeOpen("t1", 1),
    sealed("t1", 1, "turn_close", { outcome: "completed" }),
    wakerRow("t1", 2),
  ];

  it("draws no card after a reload", async () => {
    load("wake-ghost", [empty(), woken()]);
    await vi.waitFor(() => {
      expect(header("t2").label).toBe(LABEL);
    });
    expect(cardKeys()).toEqual(["t2"]);
  });

  it("loses its card when the wake lands", async () => {
    const chat = "wake-ghost-live";
    load(chat, [empty()]);
    await vi.waitFor(() => {
      expect(cardKeys()).toEqual(["t1"]);
    });

    store.openTurn(chat, wakeOpen("t2", 2));
    for (const e of woken().slice(1)) {
      store.appendEntry(chat, e);
    }

    await vi.waitFor(() => {
      expect(cardKeys()).toEqual(["t2"]);
    });
  });
});
