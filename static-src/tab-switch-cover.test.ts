// The transcript cover is released only by the paint that puts the painted chat's turns or empty
// page on screen, and lifts in that paint's task unless the browser reports input waiting. Only an
// activation or the failure box's Retry raises or holds it. Real store, renderer and scroll
// controller.
import { describe, it, expect, beforeEach } from "vitest";
import type { Session } from "./types.js";
import { makeSession } from "./__test-helpers__/model.js";
import type { Entry } from "./wire/types.gen.js";
import { loadCSS } from "./__test-helpers__/css-rules.js";

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

async function nextTask(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

function press(type: "pointerdown" | "pointerup"): PointerEvent {
  return new PointerEvent(type, { bubbles: true, button: 0, pointerId: 1 });
}

/** Run `fn` with `navigator.scheduling.isInputPending` answering `pending()`. */
async function withInputPending(pending: () => boolean, fn: () => Promise<void>): Promise<void> {
  const nav = navigator as Navigator & { scheduling?: unknown };
  const had = Object.getOwnPropertyDescriptor(nav, "scheduling");
  Object.defineProperty(nav, "scheduling", {
    configurable: true,
    value: { isInputPending: pending },
  });
  try {
    await fn();
  } finally {
    if (had === undefined) {
      delete nav.scheduling;
    } else {
      Object.defineProperty(nav, "scheduling", had);
    }
  }
}

beforeEach(() => {
  coverModule.dropCover();
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
  setPainted("");
});

describe("the cover is released by the paint that shows content", () => {
  it("stays over an evicted chat until its page lands, then lifts in that paint's task", async () => {
    const a = withTurn(makeSession({ id: "cv-a", name: "a" }), "text of a");
    store.setSessions([a, evicted("cv-b")]);
    store.setActive("cv-a");
    setPainted("cv-a");
    store.bumpMessages("cv-a", "load");
    await flushed();

    store.setActive("cv-b");
    coverModule.holdCover("cv-b");
    expect(wrap().getAttribute("aria-busy")).toBe("true");
    expect(wrap().getAttribute("aria-hidden")).toBe("true");
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
    expect(coverUp(), "the paint that mounted B's turns left the cover up").toBe(false);
    expect(wrap().hasAttribute("aria-busy")).toBe(false);
    expect(wrap().hasAttribute("aria-hidden")).toBe(false);
  });

  it("lifts in the activation's paint when the target is parked with its turns", async () => {
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
    expect(coverUp(), "F's repaint released G's cover").toBe(true);
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
    expect(coverUp(), "the cover never lifted").toBe(false);
    for (const e of queued) {
      composer.dispatchEvent(e);
      expect(coverUp(), `a composer ${e.type} raised the cover`).toBe(false);
    }
    composer.dispatchEvent(press("pointerdown"));
    await nextTask();
    expect(coverUp(), "a composer press raised the cover").toBe(false);
    composer.dispatchEvent(press("pointerup"));
  });

  it("defers the lift a task only while the browser reports input waiting", async () => {
    let pending = true;
    await withInputPending(
      () => pending,
      async () => {
        expect(await paintSwitchToB(), "the cover lifted over waiting input").toBe(true);
        expect(wrap().getAttribute("aria-hidden"), "a deferred lift unhid the covered turns").toBe(
          "true",
        );
        pending = false;
        await nextTask();
        expect(coverUp(), "the deferred lift did not run in the next task").toBe(false);
        expect(wrap().hasAttribute("aria-hidden")).toBe(false);
      },
    );
  });

  it("keeps a deferred lift for a cover a later switch handed to another chat", async () => {
    await withInputPending(
      () => true,
      async () => {
        expect(await paintSwitchToB()).toBe(true);
        coverModule.holdCover("cv-a");
        await nextTask();
        await nextTask();
        expect(coverUp(), "B's deferred lift uncovered a cover now held for A").toBe(true);
      },
    );
  });
});

describe("the covered transcript is out of reach with no restyle", () => {
  it("is hidden from assistive tech and turns focus away from the hold to the lift", async () => {
    const a = withTurn(makeSession({ id: "cv-a", name: "a" }), "text of a");
    store.setSessions([a, evicted("cv-b")]);
    store.setActive("cv-a");
    setPainted("cv-a");
    store.bumpMessages("cv-a", "load");
    await flushed();
    const button = document.createElement("button");
    (messages.transcriptViewFor("cv-a") ?? wrap()).appendChild(button);
    // As index.html marks the scroller.
    wrap().tabIndex = -1;
    try {
      coverModule.holdCover("cv-b");
      expect(wrap().getAttribute("aria-hidden")).toBe("true");
      expect(wrap().inert, "inert restyles every resident turn").toBe(false);
      button.focus();
      expect(document.activeElement?.id, "a control under the cover took focus").toBe(
        "prompt-input",
      );
      wrap().focus();
      expect(document.activeElement?.id, "the covered scroller took focus").toBe("prompt-input");

      coverModule.dropCover();
      expect(wrap().hasAttribute("aria-hidden")).toBe(false);
      button.focus();
      expect(document.activeElement, "the lifted transcript refused focus").toBe(button);
    } finally {
      button.remove();
      wrap().removeAttribute("tabindex");
    }
  });
});

describe("the cover's skeleton pulses only while the cover is held", () => {
  /** The play state of every skeleton pulse in the cover. */
  function pulses(): string[] {
    const cover = document.getElementById("transcript-cover");
    if (cover === null) {
      throw new Error("no cover");
    }
    return cover
      .getAnimations({ subtree: true })
      .filter((a) => a instanceof CSSAnimation && a.animationName === "vk-skeleton")
      .map((a) => a.playState);
  }

  it("pauses the pulse where it stands at the release, and runs it again at the next hold", () => {
    const pulse = document.createElement("style");
    pulse.textContent = ["01-tokens.css", "03-base.css", "30-utilities.css"]
      .map(loadCSS)
      .join("\n");
    document.head.appendChild(pulse);
    try {
      coverModule.holdCover("cv-a");
      const held = pulses();
      expect(held.length, "the skeleton's bars pulse").toBeGreaterThan(0);
      expect(new Set(held), "while held").toEqual(new Set(["running"]));

      coverModule.dropCover();
      expect(new Set(pulses()), "after the release").toEqual(new Set(["paused"]));

      coverModule.holdCover("cv-b");
      expect(new Set(pulses()), "at the next hold").toEqual(new Set(["running"]));
    } finally {
      coverModule.dropCover();
      pulse.remove();
    }
  });
});
