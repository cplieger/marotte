import { describe, it, expect, vi, beforeAll, afterAll, afterEach, onTestFinished } from "vitest";

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
const { loadTurnRail } = await import("./turn-rail.js");
const { apiGet } = await import("./api-client.js");

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

describe("a re-bin under a held finger", () => {
  function track(): HTMLElement {
    const t = document.querySelector<HTMLElement>(".turn-map-track");
    if (t === null) {
      throw new Error("no track");
    }
    return t;
  }

  afterEach(async () => {
    vi.useRealTimers();
    track().style.removeProperty("flex");
    await vi.waitFor(() => {
      expect(
        document.querySelectorAll(".turn-map-stack:not([data-parked]) > .turn-pill"),
      ).toHaveLength(20);
    });
  });

  const endings = {
    lift: () => touch("touchEnd"),
    cancel: () => touch("touchCancel"),
    "lift reaching only the document once the track released the capture": async (
      pointerId: number,
      at: Point,
    ) => {
      track().releasePointerCapture(pointerId);
      await touch("touchMove", { x: at.x, y: at.y + 2 });
      await touch("touchEnd");
    },
    // Dispatched directly: the other endings clear the scrub through pointerup or pointercancel.
    "lostpointercapture with the finger still down": (pointerId: number) => {
      track().dispatchEvent(new PointerEvent("lostpointercapture", { pointerId, bubbles: true }));
      return Promise.resolve();
    },
  };

  it.each(Object.entries(endings))(
    "leaves no scrub mark and no turn info after a %s",
    async (_, end) => {
      let pointerId = -1;
      track().addEventListener(
        "pointerdown",
        (e) => {
          pointerId = e.pointerId;
        },
        { once: true },
      );
      const at = pointOn(12);
      await touch("touchStart", at);
      await vi.waitFor(
        () => {
          expect(tooltipText()).toContain("#12");
        },
        { timeout: 2000 },
      );
      vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
      // A 30px track holds 15 rows, so the 20 turns re-bin two to a row under new keys.
      track().style.flex = "0 0 30px";
      await frames();
      await frames();
      expect(
        document.querySelectorAll(".turn-map-stack:not([data-parked]) > .turn-pill").length,
        "premise: the rows re-binned",
      ).toBe(10);
      expect(tooltipText(), "the replaced row's info goes with it").toBeNull();
      expect(scrubbedTurn(), "the row now under the finger is scrubbed").not.toBeNull();
      await end(pointerId, at);
      // Read the ending a frame after the input, as every other case does: its handlers can run
      // after `end` resolves.
      await frames();
      vi.advanceTimersByTime(1000);
      expect(scrubbedTurn()).toBeNull();
      expect(tooltipText()).toBeNull();
    },
  );

  it("moves a held finger's scrub onto the map of a chat switched to mid-gesture", async () => {
    const answer = {
      turns: Array.from({ length: 20 }, (_, i) => ({
        id: `m${String(i + 1)}`,
        n: i + 1,
        outcome: "completed",
        ts: (i + 1) * 60_000,
        first_line: `Question ${String(i + 1)}`,
      })),
    };
    onTestFinished(async () => {
      vi.mocked(apiGet).mockResolvedValue(answer);
      await loadTurnRail("c-touch");
      await frames();
    });
    await touch("touchStart", pointOn(5));
    await frames();
    expect(scrubbedTurn(), "the premise").toBe("5");

    // The same twenty turns under another chat, so the finger's row exists on both maps.
    vi.mocked(apiGet).mockResolvedValue(answer);
    await loadTurnRail("c-touch-other");
    await frames();

    expect(
      document.querySelectorAll(".turn-map-stack[data-parked] [data-scrub]"),
      "the map switched away from keeps no scrub mark",
    ).toHaveLength(0);
    expect(scrubbedTurn(), "the finger's row on the shown map").toBe("5");
  });

  it("takes the turn info down when the track collapses under the finger", async () => {
    await touch("touchStart", pointOn(12));
    await vi.waitFor(
      () => {
        expect(tooltipText()).toContain("#12");
      },
      { timeout: 2000 },
    );
    track().style.flex = "0 0 0px";
    await frames();
    await frames();
    expect(
      document.querySelectorAll(".turn-map-stack:not([data-parked]) > .turn-pill"),
      "premise: no rows",
    ).toHaveLength(0);
    expect(tooltipText()).toBeNull();
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
  it("keeps the finger's 44px targets on the step buttons, their glyphs shown at rest", () => {
    expect(matchMedia("(any-pointer: coarse)").matches, "premise: a touchscreen").toBe(true);
    document.documentElement.dataset["pointer"] = "fine";
    const steps = document.querySelectorAll<HTMLElement>(".turn-map-step");
    expect(steps.length, "premise: the step buttons").toBe(2);
    for (const b of steps) {
      const r = b.getBoundingClientRect();
      const cx = r.left + r.width / 2;
      const cy = r.top + r.height / 2;
      const reach = 44 / 2 - 0.5;
      for (const [x, y] of [
        [cx - reach, cy],
        [cx + reach, cy],
        [cx, cy - reach],
        [cx, cy + reach],
      ] as const) {
        const hit = document.elementFromPoint(x, y);
        expect(hit !== null && b.contains(hit), `a press at ${x},${y} reaches the step`).toBe(true);
      }
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
