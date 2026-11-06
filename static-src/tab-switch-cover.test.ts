// The transcript cover is released only by the paint that puts the painted chat's turns or empty
// page on screen, in that paint's own task. Real store, renderer and scroll controller.
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

beforeEach(() => {
  coverModule.dropCover();
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
  setPainted("");
});

describe("the cover is released by the paint that shows content", () => {
  it("stays over an evicted chat until its page lands, then releases in the landing paint's task", async () => {
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
    expect(coverUp(), "the paint that mounted B's turns left the cover up").toBe(false);
    expect(wrap().hasAttribute("aria-busy")).toBe(false);
    expect(wrap().inert).toBe(false);
  });

  it("releases in the activation's paint when the target is parked with its turns", async () => {
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
