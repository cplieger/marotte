import { describe, it, expect, vi, beforeAll, afterAll, afterEach } from "vitest";

import type * as ApiClient from "./api-client.js";
import type * as Scroll from "./scroll.js";
import type { Point } from "./__test-helpers__/turn-rail-dom.js";

vi.mock("./scroll.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Scroll>()),
  ...(await import("./__test-helpers__/scroll-mock.js")).scrollMock,
}));
vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGet: vi.fn(),
}));

// The real scroll.js the spread loads builds its controller over these at import, so they exist
// before the first import that reaches it; the rail itself reads the mocked scroller.
for (const [id, tag] of [
  ["messages-wrap", "div"],
  ["messages", "div"],
  ["scroll-bottom", "button"],
] as const) {
  const e = document.createElement(tag);
  e.id = id;
  e.hidden = true;
  document.body.appendChild(e);
}
const {
  currentTurn,
  dispatchTouch,
  frames,
  mountRail,
  pointOn,
  rowFor,
  scrubbedTurn,
  tooltipText,
} = await import("./__test-helpers__/turn-rail-dom.js");
const { initTooltips } = await import("./tooltip.js");

/** Whether a touch is down, so cleanup cancels only a live gesture (CDP refuses any other). */
let touching = false;

async function touch(
  type: "touchStart" | "touchMove" | "touchEnd" | "touchCancel",
  at?: Point,
): Promise<void> {
  await dispatchTouch(type, at);
  touching = type === "touchStart" || type === "touchMove";
}

let unmount: () => void = () => undefined;

beforeAll(async () => {
  initTooltips();
  unmount = await mountRail("c-touch", 20, 1120);
});

afterAll(() => {
  unmount();
});

afterEach(async () => {
  if (touching) {
    await touch("touchCancel");
  }
  delete document.documentElement.dataset["pointer"];
  await frames();
});

describe("a finger held on the rail scrubs it", () => {
  it("moves the fisheye with the finger, the pointed row full width and its neighbours stepped", async () => {
    await touch("touchStart", pointOn(3));
    await frames();
    expect(scrubbedTurn()).toBe("3");
    for (const n of [5, 7, 9]) {
      await touch("touchMove", pointOn(n));
      await frames();
      expect(scrubbedTurn(), `the finger over turn ${String(n)}`).toBe(String(n));
    }
    const ladder = [7, 8, 9, 10, 11].map((n) =>
      getComputedStyle(rowFor(n)).getPropertyValue("--pill-scrub").trim(),
    );
    expect(ladder).toEqual([
      "calc(12 / 24)",
      "calc(16 / 24)",
      "1",
      "calc(16 / 24)",
      "calc(12 / 24)",
    ]);
    expect(
      getComputedStyle(rowFor(3)).getPropertyValue("--pill-scrub").trim(),
      "the row the finger left",
    ).toBe("0");
  });

  it("shows the turn info for the row under a finger that stops moving, and hides it on the move", async () => {
    await touch("touchStart", pointOn(4));
    await touch("touchMove", pointOn(12));
    await frames();
    expect(tooltipText(), "no info while the finger is still moving").toBeNull();
    await vi.waitFor(
      () => {
        expect(tooltipText()).toContain("#12");
      },
      { timeout: 2000 },
    );
    await touch("touchMove", pointOn(15));
    await frames();
    expect(tooltipText(), "the old row's info goes with the move").toBeNull();
    await vi.waitFor(
      () => {
        expect(tooltipText()).toContain("#15");
      },
      { timeout: 2000 },
    );
  });

  it("holds the info back while the finger moves inside one row, and shows it once it stops", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    try {
      const at = pointOn(10);
      await touch("touchStart", at);
      await frames();
      for (let i = 1; i <= 6; i++) {
        await touch("touchMove", { x: at.x, y: at.y + (i % 2) });
        await frames();
        expect(scrubbedTurn(), "premise: still on the same row").toBe("10");
        vi.advanceTimersByTime(300);
        expect(tooltipText(), `no info ${String(i * 300)}ms into the move`).toBeNull();
      }
      vi.advanceTimersByTime(500);
      expect(tooltipText()).toContain("#10");
    } finally {
      vi.useRealTimers();
    }
  });

  it("keeps a still finger's info coming through sub-pixel contact noise", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    try {
      const at = pointOn(10);
      await touch("touchStart", at);
      await frames();
      for (let i = 1; i <= 3; i++) {
        await touch("touchMove", { x: at.x, y: at.y + (i % 2) * 0.4 });
        await frames();
        vi.advanceTimersByTime(150);
      }
      vi.advanceTimersByTime(100);
      expect(tooltipText(), "500ms after the press").toContain("#10");
    } finally {
      vi.useRealTimers();
    }
  });

  it("lands nothing when a scrub lifts, as a mouse drag's release lands nothing", async () => {
    await touch("touchStart", pointOn(2));
    await touch("touchEnd");
    await frames();
    expect(currentTurn(), "premise: a tap landed turn 2").toBe("2");
    await touch("touchStart", pointOn(8));
    await touch("touchMove", pointOn(14));
    await touch("touchEnd");
    await frames();
    expect(currentTurn()).toBe("2");
    expect(scrubbedTurn()).toBeNull();
    expect(tooltipText()).toBeNull();
  });

  it("lands the held row when a finger that never moved lifts, after its info showed", async () => {
    await touch("touchStart", pointOn(11));
    await vi.waitFor(
      () => {
        expect(tooltipText()).toContain("#11");
      },
      { timeout: 2000 },
    );
    await touch("touchEnd");
    await frames();
    expect(currentTurn()).toBe("11");
    expect(tooltipText()).toBeNull();
  });

  it("lands a tapped row and does not follow its link", async () => {
    const path = location.pathname;
    await touch("touchStart", pointOn(6));
    await touch("touchEnd");
    await frames();
    expect(currentTurn()).toBe("6");
    expect(location.pathname).toBe(path);
  });

  it("lands nothing when the gesture is cancelled", async () => {
    await touch("touchStart", pointOn(1));
    await touch("touchEnd");
    await frames();
    expect(currentTurn()).toBe("1");
    await touch("touchStart", pointOn(18));
    await frames();
    expect(scrubbedTurn()).toBe("18");
    await touch("touchCancel");
    await frames();
    expect(scrubbedTurn()).toBeNull();
    expect(currentTurn()).toBe("1");
  });
});

describe("the press band", () => {
  it("takes the finger on the rail only, so the page scrolls everywhere else", () => {
    const track = document.querySelector<HTMLElement>(".turn-map-track");
    const step = document.querySelector<HTMLElement>(".turn-map-step");
    const outer = document.getElementById("messages-wrap-outer");
    expect(track === null ? "" : getComputedStyle(track).touchAction).toBe("none");
    expect(step === null ? "" : getComputedStyle(step).touchAction).not.toBe("none");
    expect(outer === null ? "" : getComputedStyle(outer).touchAction).not.toBe("none");
  });
});

describe("a touch device in mouse mode", () => {
  it("keeps the finger's step buttons, their glyphs shown at rest", () => {
    expect(matchMedia("(any-pointer: coarse)").matches, "premise: a touchscreen").toBe(true);
    document.documentElement.dataset["pointer"] = "fine";
    for (const b of document.querySelectorAll<HTMLElement>(".turn-map-step")) {
      const r = b.getBoundingClientRect();
      expect(r.width).toBeGreaterThanOrEqual(48 - 0.5);
      expect(r.height).toBeGreaterThanOrEqual(48 - 0.5);
      const glyph = b.querySelector("svg");
      expect(glyph === null ? "" : getComputedStyle(glyph).opacity).toBe("1");
    }
  });

  it("keeps the resume control's centred labelled pill, with no hover to reveal a docked label", () => {
    document.documentElement.dataset["pointer"] = "fine";
    const btn = document.getElementById("scroll-bottom");
    const outer = document.getElementById("messages-wrap-outer");
    if (btn === null || outer === null) {
      throw new Error("no resume control or wrap-outer");
    }
    const home = btn.parentElement;
    const label = document.createElement("span");
    label.textContent = "Latest";
    btn.className = "scroll-resume";
    btn.append(label);
    btn.hidden = false;
    outer.append(btn);
    try {
      expect(getComputedStyle(label).display).not.toBe("none");
      const o = outer.getBoundingClientRect();
      const b = btn.getBoundingClientRect();
      expect(Math.abs(b.left + b.width / 2 - (o.left + o.width / 2))).toBeLessThanOrEqual(1);
    } finally {
      label.remove();
      btn.hidden = true;
      home?.append(btn);
    }
  });
});
