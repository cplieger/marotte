// The Recently closed list at the client's decoders: GET /api/tabs always carries it as an array,
// and a tabs_changed frame carries it only when the mutation changed it, so an absent key and an
// emptied list must stay two different values after decoding.

import { describe, it, expect } from "vitest";

import { decodeTabList, decodeTabsChangedPayload } from "./decoders.gen.js";
import { decodeEventFrame } from "./events.gen.js";

const ALPHA = { ref: "c-a", name: "Alpha", closed_at: 1_700_000_000_000, pinned: true };
const UNTITLED = { ref: "c-b", name: "", closed_at: 1_699_999_000_000 };

describe("GET /api/tabs", () => {
  it("keeps every recently closed entry, in order, with its fields", () => {
    const list = decodeTabList({ tabs: [], recent: [ALPHA, UNTITLED], version: 4 });
    expect(list.recent).toEqual([ALPHA, UNTITLED]);
  });

  it("decodes an empty list as an empty array", () => {
    expect(decodeTabList({ tabs: [], recent: [], version: 0 }).recent).toEqual([]);
  });

  it("refuses an entry with no ref or no close time", () => {
    expect(() =>
      decodeTabList({ tabs: [], recent: [{ name: "x", closed_at: 1 }], version: 1 }),
    ).toThrow(TypeError);
    expect(() =>
      decodeTabList({ tabs: [], recent: [{ ref: "c-a", name: "x" }], version: 1 }),
    ).toThrow(TypeError);
  });
});

describe("a tabs_changed frame", () => {
  it("leaves recent absent when the mutation did not change the list", () => {
    const frame = decodeEventFrame("tabs_changed", { order: ["t1"], version: 3 });
    expect(frame.event).toBe("tabs_changed");
    expect(frame.data).not.toHaveProperty("recent");
  });

  it("keeps the whole list when the mutation changed it", () => {
    const frame = decodeEventFrame("tabs_changed", {
      removed_ids: ["t1"],
      recent: { items: [ALPHA, UNTITLED] },
      version: 5,
    });
    expect(frame.data).toEqual({
      removed_ids: ["t1"],
      recent: { items: [ALPHA, UNTITLED] },
      version: 5,
    });
  });

  it("keeps an emptied list as an empty list, not as an absent one", () => {
    const delta = decodeTabsChangedPayload({ recent: { items: [] }, version: 6 });
    expect(delta.recent).toEqual({ items: [] });
  });

  it("refuses a list that is not an object of items", () => {
    expect(() => decodeTabsChangedPayload({ recent: [ALPHA], version: 7 })).toThrow(TypeError);
  });
});
