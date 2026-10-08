// In Node because the subject is collection: only Node lets a test force a full GC (`--expose-gc` set at runtime),
// and a followed surface leaves the registry only once its target is collected.
import v8 from "node:v8";
import vm from "node:vm";
import { afterEach, describe, expect, it } from "vitest";

import { _followedCountForTest, _resetForTest, followRoot } from "./workspace.js";

v8.setFlagsFromString("--expose-gc");
const gc = vm.runInNewContext("gc") as () => void;

afterEach(() => {
  _resetForTest();
});

/** Registers `n` targets nothing else holds and returns a probe on the first, to prove the collection happened. */
function followCollectable(n: number): WeakRef<object> {
  const first = {};
  followRoot(first, () => true);
  for (let i = 1; i < n; i++) {
    followRoot({}, () => true);
  }
  return new WeakRef(first);
}

/** A WeakRef target stays alive until the job that made it ends, so the collection waits a task first. */
async function collect(probe: WeakRef<object>): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0));
  gc();
  expect(probe.deref(), "Setup: a full GC left a followed target nothing holds").toBeUndefined();
}

describe("the followRoot registry", () => {
  it("leaves collected surfaces in a registry below the floor, which no sweep is worth", async () => {
    const probe = followCollectable(8);
    await collect(probe);

    followRoot({ live: true }, () => true);

    expect(_followedCountForTest()).toBe(9);
  });

  it("drops collected surfaces once registrations reach twice the floor of 128", async () => {
    const probe = followCollectable(255);
    await collect(probe);
    const live = [{}, {}];

    followRoot(live[0] as object, () => true);
    expect(_followedCountForTest()).toBe(256);
    followRoot(live[1] as object, () => true);

    expect(_followedCountForTest()).toBe(2);
  });

  it("sweeps next at twice the live count the last sweep left", async () => {
    const held = Array.from({ length: 200 }, () => ({}));
    for (const target of held) {
      followRoot(target, () => true);
    }
    const probe = followCollectable(56);
    await collect(probe);
    followRoot(held[0] as object, () => true);
    expect(_followedCountForTest()).toBe(201);

    const later = followCollectable(198);
    await collect(later);
    followRoot(held[1] as object, () => true);
    expect(_followedCountForTest()).toBe(400);
    followRoot(held[2] as object, () => true);

    expect(_followedCountForTest()).toBe(203);
  });
});
