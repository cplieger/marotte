// The transcript cover is released only by the paint that puts the painted chat's turns or empty
// page on screen, and lifts once that paint is on screen unless a newer press or key may switch
// again. Real store, renderer and scroll controller.
import { describe, it, expect, beforeEach } from "vitest";
import type { Session } from "./types.js";
import { makeSession } from "./__test-helpers__/model.js";
import type { Entry } from "./wire/types.gen.js";
import { loadCSS } from "./__test-helpers__/css-rules.js";
import { until } from "./__test-helpers__/until.js";

for (const id of [
  "chat-view",
  "messages-wrap-outer",
  "messages-wrap",
  "messages",
  "scroll-bottom",
]) {
  const d = document.createElement("div");
  d.id = id;
  if (id === "scroll-bottom") {
    d.appendChild(document.createElement("span"));
  }
  if (id === "messages-wrap") {
    document.getElementById("messages-wrap-outer")?.appendChild(d);
  } else if (id === "messages") {
    document.getElementById("messages-wrap")?.appendChild(d);
  } else {
    document.body.appendChild(d);
  }
}
for (const id of ["send-btn", "prompt-input"]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "button");
  d.id = id;
  document.body.appendChild(d);
}
const style = document.createElement("style");
style.textContent = loadCSS("13-messages.css") + "#messages-wrap-outer { height: 400px; }";
document.head.appendChild(style);

const store = await import("./store.js");
const messages = await import("./messages.js");
const coverModule = await import("./transcript-cover.js");

messages.mountChatView();

const setPainted = (id: string): void => {
  (store as unknown as { setPainted: (id: string) => void }).setPainted(id);
};

function sealed(turnID: string, at: number, kind: Entry["kind"], payload: unknown): Entry {
  return { id: `${turnID}-e${String(at)}`, turn: turnID, kind, seq: at, ts: at + 1, payload };
}

function withTurn(s: Session, reply: string): Session {
  const t = `${s.id}-t1`;
  s.turns.set(t, {
    entries: [
      sealed(t, 0, "turn_open", { prompt: { id: `${t}-p`, text: "go" }, source: "prompt", n: 1 }),
      sealed(t, 1, "text", { text: reply }),
      sealed(t, 2, "turn_close", { outcome: "completed" }),
    ],
    openEntries: new Map(),
  });
  s.turn_order.push(t);
  s.turn_count = 1;
  return s;
}

/** A chat the server knows has turns, with none resident: an evicted window. */
function evicted(id: string): Session {
  return { ...makeSession({ id, name: id }), turn_count: 4, has_more: true };
}

function wrap(): HTMLElement {
  const el = document.getElementById("messages-wrap");
  if (el === null) {
    throw new Error("no #messages-wrap");
  }
  return el;
}

function coverUp(): boolean {
  const c = document.getElementById("transcript-cover");
  return c !== null && !c.hidden && c.getAttribute("data-state") !== "released";
}

async function flushed(): Promise<void> {
  await Promise.resolve();
}

/** One frame, then one task: a release asked for before it has had its chance to lift. */
async function onScreen(): Promise<void> {
  await new Promise((resolve) => requestAnimationFrame(resolve));
  await new Promise((resolve) => setTimeout(resolve, 0));
}

function press(type: "pointerdown" | "pointerup", pointerId = 1): PointerEvent {
  return new PointerEvent(type, { bubbles: true, button: 0, pointerId });
}

/** B's turns painted under a cover held for B, the release asked for and not yet lifted. */
async function paintedUnderCover(): Promise<void> {
  store.setSessions([
    withTurn(makeSession({ id: "cv-a", name: "a" }), "text of a"),
    withTurn(makeSession({ id: "cv-b", name: "b" }), "text of b"),
  ]);
  store.setActive("cv-a");
  setPainted("cv-a");
  store.bumpMessages("cv-a", "load");
  await flushed();
  store.setActive("cv-b");
  coverModule.holdCover("cv-b");
  setPainted("cv-b");
  await flushed();
  expect(coverUp(), "the cover lifted inside the paint's own task").toBe(true);
}

beforeEach(() => {
  coverModule.dropCover();
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
  setPainted("");
});

describe("the cover is released by the paint that shows content", () => {
  it("stays over an evicted chat until its page lands, then lifts once that paint is on screen", async () => {
    const a = withTurn(makeSession({ id: "cv-a", name: "a" }), "text of a");
    store.setSessions([a, evicted("cv-b")]);
    store.setActive("cv-a");
    setPainted("cv-a");
    store.bumpMessages("cv-a", "load");
    await flushed();

    store.setActive("cv-b");
    coverModule.holdCover("cv-b");
    expect(wrap().getAttribute("aria-busy")).toBe("true");
    expect(wrap().inert, "holding the cover alone restyles nothing").toBe(false);
    coverModule.inertUnderCover();
    expect(wrap().inert).toBe(true);
    setPainted("cv-b");
    await flushed();
    expect(coverUp(), "a paint with no turns released the cover").toBe(true);

    const b = store.get("cv-b");
    expect(b).toBeDefined();
    if (b !== undefined) {
      withTurn(b, "text of b");
    }
    store.bumpMessages("cv-b", "load");
    await flushed();
    const view = messages.transcriptViewFor("cv-b");
    expect(view?.querySelectorAll(".turn").length ?? 0).toBeGreaterThan(0);
    expect(coverUp(), "the cover lifted before B's turns reached the screen").toBe(true);
    await onScreen();
    expect(coverUp(), "the paint that mounted B's turns left the cover up").toBe(false);
    expect(wrap().hasAttribute("aria-busy")).toBe(false);
    expect(wrap().inert).toBe(false);
  });

  it("lifts after the activation's paint when the target is parked with its turns", async () => {
    store.setSessions([
      withTurn(makeSession({ id: "cv-c", name: "c" }), "text of c"),
      withTurn(makeSession({ id: "cv-d", name: "d" }), "text of d"),
    ]);
    store.setActive("cv-d");
    setPainted("cv-d");
    store.bumpMessages("cv-d", "load");
    await flushed();
    store.setActive("cv-c");
    setPainted("cv-c");
    store.bumpMessages("cv-c", "load");
    await flushed();

    store.setActive("cv-d");
    coverModule.holdCover("cv-d");
    setPainted("cv-d");
    await flushed();
    expect(
      messages.transcriptViewFor("cv-d")?.querySelectorAll(".turn").length ?? 0,
    ).toBeGreaterThan(0);
    await onScreen();
    expect(coverUp()).toBe(false);
  });

  it("releases when the paint shows an empty chat's page", async () => {
    store.setSessions([
      withTurn(makeSession({ id: "cv-e", name: "e" }), "text of e"),
      makeSession({ id: "cv-empty", name: "empty" }),
    ]);
    store.setActive("cv-e");
    setPainted("cv-e");
    store.bumpMessages("cv-e", "load");
    await flushed();

    store.setActive("cv-empty");
    coverModule.holdCover("cv-empty");
    setPainted("cv-empty");
    await flushed();
    await onScreen();
    expect(coverUp()).toBe(false);
  });

  it("is not released by a paint of another chat than its holder", async () => {
    store.setSessions([
      withTurn(makeSession({ id: "cv-f", name: "f" }), "text of f"),
      evicted("cv-g"),
    ]);
    store.setActive("cv-f");
    setPainted("cv-f");
    store.bumpMessages("cv-f", "load");
    await flushed();

    // A burst re-targeted the cover to G while F, still painted, repaints.
    coverModule.holdCover("cv-g");
    store.bumpMessages("cv-f", "load");
    await flushed();
    await onScreen();
    expect(coverUp(), "F's repaint released G's cover").toBe(true);
  });
});

describe("a gesture after the paint keeps the cover until its own handler decides", () => {
  it("waits out a press that is still down, then lifts once it ends", async () => {
    await paintedUnderCover();
    document.body.dispatchEvent(press("pointerdown"));
    await onScreen();
    await onScreen();
    expect(coverUp(), "the cover lifted under a press that may still click").toBe(true);
    document.body.dispatchEvent(press("pointerup"));
    expect(coverUp()).toBe(true);
    await until(() => !coverUp(), "the cover lifts once the press ended");
  });

  it("lifts after a blur ends a press whose release the page never saw", async () => {
    await paintedUnderCover();
    document.body.dispatchEvent(press("pointerdown"));
    await onScreen();
    expect(coverUp()).toBe(true);
    window.dispatchEvent(new FocusEvent("blur"));
    await until(() => !coverUp(), "the cover lifts once a blur ended the press");
  });

  it("checks again a task after a key, and lifts when the key switched nothing", async () => {
    await paintedUnderCover();
    let upInKeyTask = false;
    requestAnimationFrame(() => {
      document.body.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "x" }));
      setTimeout(() => {
        upInKeyTask = coverUp();
      }, 0);
    });
    await onScreen();
    await onScreen();
    expect(upInKeyTask, "the cover lifted in the task the key's handler ran in").toBe(true);
    expect(coverUp()).toBe(false);
  });

  it("is handed to the chat a later press switches to, never lifted for the superseded one", async () => {
    await paintedUnderCover();
    document.body.dispatchEvent(press("pointerdown"));
    document.body.dispatchEvent(press("pointerup"));
    coverModule.holdCover("cv-a");
    await onScreen();
    await onScreen();
    expect(coverUp(), "B's release lifted a cover now held for A").toBe(true);
    coverModule.dropCover();
  });

  it("goes back up for a press made before the lift and dispatched after it", async () => {
    await paintedUnderCover();
    // Created now, so its timeStamp predates the lift, as a press queued behind a long task's does.
    const queued = press("pointerdown");
    await onScreen();
    expect(coverUp(), "the release never lifted").toBe(false);
    document.body.dispatchEvent(queued);
    expect(coverUp(), "a press made over the cover found it lifted").toBe(true);
    document.body.dispatchEvent(press("pointerup"));
    await until(() => !coverUp(), "the cover lifts again once the press switched nothing");
  });

  it("stays down for a press made after the lift", async () => {
    await paintedUnderCover();
    await onScreen();
    expect(coverUp()).toBe(false);
    document.body.dispatchEvent(press("pointerdown"));
    expect(coverUp(), "a press after the lift raised the cover").toBe(false);
    document.body.dispatchEvent(press("pointerup"));
  });
});

/** B's turns painted under a cover held for B; answers whether the cover was still up at the end of
 *  the paint's own task. */
async function paintSwitchToB(): Promise<boolean> {
  store.setSessions([
    withTurn(makeSession({ id: "cv-a", name: "a" }), "text of a"),
    withTurn(makeSession({ id: "cv-b", name: "b" }), "text of b"),
  ]);
  store.setActive("cv-a");
  setPainted("cv-a");
  store.bumpMessages("cv-a", "load");
  await flushed();
  store.setActive("cv-b");
  coverModule.holdCover("cv-b");
  setPainted("cv-b");
  await flushed();
  return coverUp();
}

describe("a switch's cover lifts with its paint, and only a switch raises it", () => {
  it("lifts in the task of the paint that shows the target's turns", async () => {
    expect(await paintSwitchToB(), "the cover outlived the paint that showed B").toBe(false);
    expect(wrap().hasAttribute("aria-busy")).toBe(false);
  });

  it("never comes back for typing and clicking in the composer after the switch", async () => {
    const composer = document.getElementById("prompt-input");
    if (composer === null) {
      throw new Error("no #prompt-input");
    }
    // Made before the paint, as input queued behind the switch's long task is.
    const queued = [
      press("pointerdown"),
      press("pointerup"),
      new KeyboardEvent("keydown", { bubbles: true, key: "h" }),
      new KeyboardEvent("keydown", { bubbles: true, key: "i" }),
    ];
    await paintSwitchToB();
    await onScreen();
    expect(coverUp(), "the cover never lifted").toBe(false);
    for (const e of queued) {
      composer.dispatchEvent(e);
      expect(coverUp(), `a composer ${e.type} raised the cover`).toBe(false);
    }
    composer.dispatchEvent(press("pointerdown"));
    expect(coverUp()).toBe(false);
    await onScreen();
    expect(coverUp(), "a composer press held the cover").toBe(false);
    composer.dispatchEvent(press("pointerup"));
  });

  it("defers the lift a task only while the browser reports input waiting", async () => {
    const nav = navigator as Navigator & { scheduling?: unknown };
    const had = Object.getOwnPropertyDescriptor(nav, "scheduling");
    let pending = true;
    Object.defineProperty(nav, "scheduling", {
      configurable: true,
      value: { isInputPending: () => pending },
    });
    try {
      expect(await paintSwitchToB(), "the cover lifted over waiting input").toBe(true);
      pending = false;
      await new Promise((resolve) => setTimeout(resolve, 0));
      expect(coverUp(), "the deferred lift did not run in the next task").toBe(false);
    } finally {
      if (had === undefined) {
        delete nav.scheduling;
      } else {
        Object.defineProperty(nav, "scheduling", had);
      }
    }
  });
});
