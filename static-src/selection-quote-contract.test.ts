// The quotability contract over the transcript as messages.ts draws it: every shown text is quotable unless it sits
// in a control or chrome. A surface kind added later is walked with no change here, so one that refuses fails.
import { describe, it, expect, vi, beforeAll, afterEach } from "vitest";
import type { Session } from "./types.js";
import type { Entry } from "./wire/types.gen.js";
import type * as Scroll from "./scroll.js";
import type * as ApiClientModule from "./api-client.js";
import { CHROME_ATTR } from "./chrome-attr.js";
import { makeSession } from "./__test-helpers__/model.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

// Nested as index.html nests them: `#messages-wrap` is absolutely placed, so as a sibling it would cover the
// transcript.
const host = (id: string, parent: HTMLElement, tag = "div"): HTMLElement => {
  const d = document.createElement(tag);
  d.id = id;
  parent.appendChild(d);
  return d;
};
const outer = host("messages-wrap-outer", document.body);
// A box for the absolutely placed scroller: with none the transcript lays out above the page, where Chromium's
// `Selection.toString()` reads nothing.
outer.style.cssText = "position:relative;height:600px;";
host("messages", host("messages-wrap", outer));
for (const id of ["chat-view", "scroll-bottom", "send-btn", "run-bar-fold"]) {
  host(id, document.body);
}
host("run-bar", document.body, "ul");
host("prompt-input", document.body, "textarea");

vi.mock("./scroll.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Scroll>()),
  ...(await import("./__test-helpers__/scroll-mock.js")).scrollMock,
}));

// The paint's GETs (run state, turn index) answer nothing.
vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClientModule>()),
  apiGet: vi.fn(() => Promise.resolve(null)),
  apiGetOrError: vi.fn(() => Promise.resolve({ ok: false, status: 0, data: null, error: "" })),
}));

const store = await import("./store.js");
const messages = await import("./messages.js");
const { initSelectionQuote } = await import("./selection-quote.js");
const { toolResultID } = await import("./entry-ids.js");

mountAppCSS();
messages.mountChatView();

beforeAll(() => {
  initSelectionQuote();
});

afterEach(() => {
  document.getSelection()?.removeAllRanges();
});

function sealed(
  turn: string,
  seq: number,
  kind: Entry["kind"],
  payload: unknown,
  id = `${turn}-e${String(seq)}`,
): Entry {
  return { id, turn, kind, seq, ts: seq + 1, payload } as Entry;
}

/** One turn holding an entry of every kind the transcript draws, closed as a failure so it carries a notice. */
function everyKind(turn: string): Entry[] {
  const kinds: [Entry["kind"], unknown][] = [
    ["thinking", { text: "Weighing the two build orders." }],
    [
      "text",
      {
        text: "Here is the build:\n\n```bash\nnpm run build\n```\n\nIt runs [the docs](https://example.com).",
      },
    ],
    [
      "tool_call",
      { id: `${turn}-tc`, title: "go test ./...", kind: "execute", status: "completed", ts: 1 },
    ],
    [
      "plan",
      { entries: [{ content: "Write the failing test", priority: "high", status: "in_progress" }] },
    ],
    ["model_switched", { from: "sonnet-5", to: "opus-5" }],
    ["mode_switched", { from: "plan", to: "vibe", source: "user" }],
    ["context_reset", { cause: "empty_retry" }],
    ["compaction", { summary: "Kept the plan and the failing test." }],
    ["compaction_failed", { reason: "the summary was empty" }],
    ["safety_blocked", { properties: ["no public S3 buckets"] }],
    [
      "tool_call",
      {
        id: `${turn}-run`,
        title: "Run Workflow",
        kind: "other",
        status: "completed",
        ts: 1,
        workflow_id: "wf_contract",
      },
    ],
    [
      "steer",
      {
        text: "All steps passed.",
        origin: "agent",
        origin_run: "wf_contract",
        step: "verify",
        produced_ts: 1,
      },
    ],
    ["text", { text: "Done for now." }],
  ];
  const entries: Entry[] = [
    sealed(turn, 0, "turn_open", {
      prompt: { id: `${turn}-p`, text: "publish the docs" },
      source: "prompt",
      n: 1,
    }),
  ];
  for (const [kind, payload] of kinds) {
    entries.push(sealed(turn, entries.length, kind, payload));
  }
  entries.push(
    // Paired by id with the first tool call, at seq 3.
    sealed(
      turn,
      entries.length,
      "tool_result",
      { status: "completed", output: "ok marotte 0.4s" },
      toolResultID(`${turn}-e3`),
    ),
  );
  entries.push(
    sealed(turn, entries.length, "turn_close", {
      outcome: "failed",
      failure_reason: "The engine stopped: the context window is full.",
      changed_files: { "docs/install.md": { added: 3, removed: 1 } },
    }),
  );
  return entries;
}

function load(chatID: string, entries: Entry[]): void {
  const s: Session = makeSession({ id: chatID, name: chatID });
  const first = entries[0];
  if (first !== undefined) {
    s.turns.set(first.turn, { entries, openEntries: new Map(), closeAt: entries.length - 1 });
    s.turn_order.push(first.turn);
  }
  s.turn_count = s.turn_order.length;
  store.setSessions([s]);
  store.setActive(chatID);
  store.bumpMessages(chatID, "load");
}

function frames(): Promise<void> {
  return new Promise((r) => {
    setTimeout(() => {
      requestAnimationFrame(() => requestAnimationFrame(() => r()));
    }, 0);
  });
}

/** Until the openers' slides end and the scroller stops moving: a scroll hides the popover the walk reads. */
async function settled(): Promise<void> {
  const scroller = document.getElementById("messages-wrap")!;
  let last = -1;
  for (let i = 0; i < 100; i++) {
    await frames();
    const running = document.getAnimations().some((a) => a.playState === "running");
    if (!running && scroller.scrollTop === last) {
      return;
    }
    last = scroller.scrollTop;
  }
  throw new Error("the transcript never settled");
}

/** The spec's controls: elements that act or take input, and UI chrome. A run's or a file's name drawn as a control
 *  is content. */
const ACTS = `button, [role="button"], summary, input, textarea, select, [contenteditable="true"], [role^="menu"], [${CHROME_ATTR}]`;
const NAMES =
  ".run-bar-open, .run-head, .run-step-head, .turn-wake-run, .turn-wake-row-run, .tool-file-link, .steer-note-run";

function inControl(t: Text): boolean {
  for (let e = t.parentElement; e !== null; e = e.parentElement) {
    if (e.matches(ACTS) && (!e.matches(NAMES) || e.matches(`[${CHROME_ATTR}]`))) {
      return true;
    }
  }
  return false;
}

function shownTexts(root: Element): Text[] {
  const out: Text[] = [];
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  for (let n = walker.nextNode(); n !== null; n = walker.nextNode()) {
    if (n instanceof Text && n.data.trim() !== "" && n.parentElement?.checkVisibility() === true) {
      out.push(n);
    }
  }
  return out;
}

const REACHED = [
  ".turn-header",
  ".reasoning-block",
  ".message.assistant:not(.compaction-body)",
  ".tool-call .tool-output",
  ".plan-message",
  ".boundary",
  ".compaction-body",
  ".run-card",
  ".steer-note",
  ".turn-notice",
  ".turn-info-panel",
];

function offered(): boolean {
  const panel = document.querySelector<HTMLElement>(".selection-quote");
  return panel !== null && !panel.hidden && panel.classList.contains("is-open");
}

describe("the transcript's quotability", () => {
  it("offers a quote for every shown text outside a control, and none inside one", async () => {
    load("c-contract", everyKind("t1"));
    await frames();
    const view = messages.activeTranscriptView();
    expect(view).not.toBeNull();
    for (const d of view?.querySelectorAll("details") ?? []) {
      d.open = true;
    }
    for (const opener of view?.querySelectorAll<HTMLElement>(
      '.tool-disclosure[aria-expanded="false"], .turn-ledger-summary[aria-expanded="false"]',
    ) ?? []) {
      opener.click();
    }
    await settled();
    // `Selection.toString()` reads a focused text field's selection instead, and the paint focuses the composer.
    if (document.activeElement instanceof HTMLElement) {
      document.activeElement.blur();
    }
    const texts = shownTexts(view!);
    // The walk reaches every kind the transcript drew from the entries above, so a paint that drew less cannot pass.
    for (const kind of REACHED) {
      expect(
        texts.some((t) => t.parentElement?.closest(kind) !== null),
        kind,
      ).toBe(true);
    }
    const wrong: string[] = [];
    for (const t of texts) {
      document.getSelection()?.setBaseAndExtent(t, 0, t, t.length);
      await frames();
      if (offered() === inControl(t)) {
        const where = t.parentElement?.className ?? "";
        wrong.push(
          `${inControl(t) ? "offered in a control" : "refused"}: "${t.data.trim()}" (${where})`,
        );
      }
    }
    expect(wrong).toEqual([]);
  });
});
