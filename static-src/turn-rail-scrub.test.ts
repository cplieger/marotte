import { describe, it, expect, vi, beforeAll, afterAll } from "vitest";

import type * as ApiClient from "./api-client.js";
import type * as Scroll from "./scroll.js";

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
const { currentTurn, dispatchMouse, frames, mountRail, pointOn, scrubbedTurn } =
  await import("./__test-helpers__/turn-rail-dom.js");

let unmount: () => void = () => undefined;

beforeAll(async () => {
  unmount = await mountRail("c-mouse", 20, 1120);
});

afterAll(() => {
  unmount();
});

describe("a mouse keeps the rail's native hover and click", () => {
  it("drags across the rows as the link's native drag, with no scrub mark and no landing", async () => {
    await dispatchMouse("mouseMoved", pointOn(3), false);
    await dispatchMouse("mousePressed", pointOn(3), true);
    await dispatchMouse("mouseMoved", pointOn(9), true);
    await frames();
    expect(scrubbedTurn(), "a pressed mouse writes no scrub state").toBeNull();
    await dispatchMouse("mouseReleased", pointOn(9), false);
    await frames();
    expect(currentTurn()).toBeNull();
  });

  it("lands a clicked row", async () => {
    await dispatchMouse("mouseMoved", pointOn(15), false);
    await dispatchMouse("mousePressed", pointOn(15), true);
    await dispatchMouse("mouseReleased", pointOn(15), false);
    await frames();
    expect(currentTurn()).toBe("15");
    expect(scrubbedTurn()).toBeNull();
  });
});
