// The transcript cover shows its skeleton only when the chat it stands for has not painted
// `COVER_SHOW_DELAY_MS` after the switch: the hold marks the transcript busy at once, and a paint
// inside the window shows the reader no cover frame at all. Real cover module over a bare shell.
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

let parent: HTMLElement = document.body;
for (const id of ["messages-wrap-outer", "messages-wrap", "messages"]) {
  const d = document.createElement("div");
  d.id = id;
  parent.appendChild(d);
  parent = d;
}
const composer = document.createElement("textarea");
composer.id = "prompt-input";
document.body.appendChild(composer);

const coverModule = await import("./transcript-cover.js");

/** The cover's show window. */
const DELAY_MS = 250;

function wrap(): HTMLElement {
  return document.getElementById("messages-wrap") as HTMLElement;
}

/** `node` is on screen and not lifting: no stylesheet here ends a lift's fade. */
function onScreen(node: HTMLElement | null): boolean {
  return node !== null && !node.hidden && node.getAttribute("data-state") !== "released";
}

function shown(): boolean {
  return onScreen(document.getElementById("transcript-cover"));
}

/** Every `hidden` flip and fade the cover's node goes through, from now on. */
function watchCover(): { changes(): string[]; stop(): void } {
  const seen: string[] = [];
  const node = document.getElementById("transcript-cover");
  if (node === null) {
    throw new Error("no cover");
  }
  const note = (records: MutationRecord[]): void => {
    for (const r of records) {
      seen.push(`${r.attributeName ?? ""}=${node.getAttribute(r.attributeName ?? "") ?? "∅"}`);
    }
  };
  const mo = new MutationObserver(note);
  mo.observe(node, { attributes: true, attributeFilter: ["hidden", "data-state"] });
  return {
    // Records are delivered in a microtask; the cases read them in the task that made them.
    changes: () => {
      note(mo.takeRecords());
      return seen;
    },
    stop: () => {
      mo.disconnect();
    },
  };
}

beforeEach(() => {
  vi.useFakeTimers();
  coverModule.dropCover();
  // No stylesheet runs the lift's fade here, so its end is dispatched as the engine would.
  document.getElementById("transcript-cover")?.dispatchEvent(new AnimationEvent("animationend"));
});

afterEach(() => {
  coverModule.dropCover();
  vi.useRealTimers();
});

describe("a held cover waits before it shows", () => {
  it("shows nothing for the first quarter second, then the skeleton", () => {
    coverModule.holdCover("cd-a");
    expect(shown(), "the cover showed in the switch's own frame").toBe(false);
    expect(wrap().getAttribute("aria-busy"), "the switching transcript is not marked busy").toBe(
      "true",
    );
    expect(coverModule.coverHeld("cd-a")).toBe(true);

    vi.advanceTimersByTime(DELAY_MS - 1);
    expect(shown(), "the skeleton showed before the window ended").toBe(false);
    vi.advanceTimersByTime(1);
    expect(shown(), "a chat still unpainted after the window shows no skeleton").toBe(true);
  });

  it("counts the window from when the reader began waiting, not from the hold", () => {
    coverModule.holdCover("cd-a", performance.now() - 200);
    vi.advanceTimersByTime(40);
    expect(shown(), "the skeleton showed before the reader had waited the window").toBe(false);
    vi.advanceTimersByTime(20);
    expect(shown(), "a wait that began before the hold waited a fresh window").toBe(true);
  });

  it("shows in the hold's own task for a reader who has already waited past the window", () => {
    coverModule.holdCover("cd-a", performance.now() - DELAY_MS * 4);
    expect(shown(), "a wait already past the window waited for a timer").toBe(true);
    expect(coverModule.swapDeferred("cd-a"), "a shown cover still deferred the swap").toBe(false);
  });

  it("never shows for a paint inside the window, and plays no fade over the content", () => {
    coverModule.holdCover("cd-a");
    const watch = watchCover();
    vi.advanceTimersByTime(DELAY_MS - 50);
    coverModule.releaseCover("cd-a");
    vi.advanceTimersByTime(DELAY_MS * 4);
    expect(shown()).toBe(false);
    expect(watch.changes(), "a fast paint flashed the cover").toEqual([]);
    expect(wrap().hasAttribute("aria-busy")).toBe(false);
    watch.stop();
  });

  it("never shows when the switch leaves the chat inside the window", () => {
    coverModule.holdCover("cd-a");
    vi.advanceTimersByTime(DELAY_MS / 2);
    coverModule.dropCover();
    vi.advanceTimersByTime(DELAY_MS * 4);
    expect(shown()).toBe(false);
  });

  it("measures the window from the latest switch when a second one comes inside it", () => {
    coverModule.holdCover("cd-a");
    vi.advanceTimersByTime(DELAY_MS - 50);
    coverModule.holdCover("cd-b");
    vi.advanceTimersByTime(DELAY_MS - 1);
    expect(shown(), "the first switch's window showed the second chat's skeleton").toBe(false);
    vi.advanceTimersByTime(1);
    expect(shown()).toBe(true);
  });

  it("shows nothing at all when the second switch inside the window paints fast", () => {
    coverModule.holdCover("cd-a");
    const watch = watchCover();
    vi.advanceTimersByTime(DELAY_MS - 50);
    coverModule.holdCover("cd-b");
    vi.advanceTimersByTime(100);
    coverModule.releaseCover("cd-b");
    vi.advanceTimersByTime(DELAY_MS * 4);
    expect(watch.changes(), "the burst flashed the cover").toEqual([]);
    watch.stop();
  });

  it("hands a shown cover to the next switch without hiding or re-showing it", () => {
    coverModule.holdCover("cd-a");
    vi.advanceTimersByTime(DELAY_MS);
    expect(shown()).toBe(true);
    const watch = watchCover();
    coverModule.holdCover("cd-b");
    vi.advanceTimersByTime(DELAY_MS * 2);
    expect(shown()).toBe(true);
    expect(watch.changes(), "the hand-over re-animated the cover").toEqual([]);
    watch.stop();
  });

  it("shows nothing when the cover left the page before its window ended", () => {
    coverModule.holdCover("cd-a");
    const detached = document.getElementById("transcript-cover");
    detached?.remove();
    vi.advanceTimersByTime(DELAY_MS * 2);
    expect(onScreen(detached), "a detached cover was un-hidden").toBe(false);
    expect(document.getElementById("transcript-cover")).toBeNull();

    coverModule.holdCover("cd-b");
    vi.advanceTimersByTime(DELAY_MS);
    expect(shown(), "the next hold's cover never showed").toBe(true);
  });

  it("never shows once the chat's rows are painted and the cover waits only on their builds", () => {
    coverModule.holdCover("cd-a");
    vi.advanceTimersByTime(DELAY_MS / 2);
    coverModule.coverContentPainted("cd-a");
    vi.advanceTimersByTime(DELAY_MS * 4);
    expect(shown(), "the skeleton showed over rows already painted").toBe(false);
    expect(coverModule.coverHeld("cd-a"), "the builds wait lost the hold").toBe(true);
  });

  it("still shows for a chat whose rows another chat's paint reported", () => {
    coverModule.holdCover("cd-a");
    coverModule.coverContentPainted("cd-other");
    vi.advanceTimersByTime(DELAY_MS);
    expect(shown()).toBe(true);
  });
});

describe("the outgoing view stays on screen while the held cover waits", () => {
  /** The owner's calls, in order, with whether the cover was on screen at each. */
  function recordingOwner(): string[] {
    const log: string[] = [];
    coverModule.registerTranscriptOwner({
      take: () => log.push(`take shown=${String(shown())}`),
      drop: () => log.push("drop"),
      painted: () => false,
      swap: (chatID) => log.push(`swap ${chatID} shown=${String(shown())}`),
    });
    return log;
  }

  it("defers the swap to the window's end, then swaps under the shown cover at fresh park reads", () => {
    const log = recordingOwner();
    coverModule.holdCover("cd-a");
    coverModule.deferReveal(() => log.push(`reveal shown=${String(shown())}`));

    vi.advanceTimersByTime(DELAY_MS - 1);
    expect(log, "the outgoing view was swapped out inside the window").toEqual([]);
    expect(coverModule.swapDeferred("cd-a")).toBe(true);
    vi.advanceTimersByTime(1);
    expect(log, "the swap ran before the cover hid the outgoing view").toEqual([
      "take shown=false",
      "reveal shown=true",
      "swap cd-a shown=true",
    ]);
    expect(coverModule.swapDeferred("cd-a")).toBe(false);
  });

  it("swaps straight to the content for a paint inside the window, with no cover frame", () => {
    const log = recordingOwner();
    coverModule.holdCover("cd-a");
    const watch = watchCover();
    coverModule.deferReveal(() => log.push("reveal"));
    vi.advanceTimersByTime(DELAY_MS - 50);

    coverModule.swapTranscript();
    expect(log).toEqual(["take shown=false", "reveal"]);
    coverModule.releaseCover("cd-a");
    vi.advanceTimersByTime(DELAY_MS * 4);
    expect(watch.changes(), "the fast answer flashed the cover").toEqual([]);
    expect(log, "the swap ran twice").toEqual(["take shown=false", "reveal"]);
    watch.stop();
  });

  it("still swaps when the switch drops its cover inside the window", () => {
    const log = recordingOwner();
    coverModule.holdCover("cd-a");
    coverModule.deferReveal(() => log.push("reveal"));
    vi.advanceTimersByTime(DELAY_MS / 2);

    coverModule.dropCover();
    expect(log, "a failed or abandoned switch left the outgoing view up").toEqual([
      "take shown=false",
      "reveal",
      "swap cd-a shown=false",
    ]);
    vi.advanceTimersByTime(DELAY_MS * 4);
    expect(shown()).toBe(false);
  });

  it("defers nothing once the cover is on screen", () => {
    const log = recordingOwner();
    coverModule.holdCover("cd-a");
    vi.advanceTimersByTime(DELAY_MS);
    log.length = 0;

    coverModule.holdCover("cd-b");
    expect(coverModule.swapDeferred("cd-b")).toBe(false);
    coverModule.deferReveal(() => log.push("reveal"));
    expect(log, "a reveal under a shown cover waited").toEqual(["reveal"]);
  });

  it("runs only the latest switch's reveal when a second switch comes inside the window", () => {
    const log = recordingOwner();
    coverModule.holdCover("cd-a");
    coverModule.deferReveal(() => log.push("reveal a"));
    vi.advanceTimersByTime(DELAY_MS - 50);
    coverModule.holdCover("cd-b");
    coverModule.deferReveal(() => log.push("reveal b"));
    vi.advanceTimersByTime(DELAY_MS);

    expect(log.filter((l) => l.startsWith("reveal"))).toEqual(["reveal b"]);
    expect(log.at(-1)).toBe("swap cd-b shown=true");
  });
});
