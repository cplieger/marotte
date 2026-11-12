// The ordered mirror: which epoch it orders under across a reconcile, a boot bind and a restart
// seen first in a frame, and that a read it drops is always replaced.

import fc from "fast-check";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { BUS_RECONCILE, emitBus } from "./bus.js";
import { createMirror, installMirrors, type Mirror, type MirrorOptions } from "./mirror.js";
import { versionMap, _resetForTest as resetVersions } from "./subject-versions.js";
import type { SubjectStamp } from "./wire/types.gen.js";
import { EPOCH_A, EPOCH_B } from "./__test-helpers__/sse-fetch.js";
import { until } from "./__test-helpers__/until.js";

type Answer = { rows: ReadonlyMap<string, string>; stamp: SubjectStamp } | null;

interface Read {
  answer(rows: [string, string][], version: string, epoch?: string): void;
}

let uninstall: () => void = () => undefined;
let kinds = 0;

type Hooks = Pick<MirrorOptions<string, string>, "onChange" | "onFetched">;

/** A mirror over a kind no other case uses, whose every read waits for the case to answer it. */
function scripted(hooks: Hooks = {}): {
  mirror: Mirror<string, string>;
  reads: Read[];
  kind: string;
} {
  kinds++;
  const kind = `mirror_case_${String(kinds)}`;
  const reads: Read[] = [];
  const mirror = createMirror<string, string>({
    ...hooks,
    kind,
    fetch: () =>
      new Promise<Answer>((resolve) => {
        reads.push({
          answer: (rows, version, epoch = "") => {
            resolve({ rows: new Map(rows), stamp: { kind, ref: "", version, epoch } });
          },
        });
      }),
  });
  return { mirror, reads, kind };
}

function frameStamp(kind: string, version: string): SubjectStamp {
  return { kind, ref: "", version };
}

function reconcile(): void {
  emitBus(BUS_RECONCILE, { cause: "full:hello", signal: new AbortController().signal });
}

beforeEach(() => {
  resetVersions();
  uninstall = installMirrors();
});

afterEach(() => {
  uninstall();
});

describe("the epoch a mirror orders under", () => {
  it("a reconcile's read still applies after the new process's first frame", async () => {
    versionMap().bind(EPOCH_A);
    const { mirror, reads, kind } = scripted();
    const first = mirror.refetch();
    reads[0]?.answer([["old", "o"]], "3", EPOCH_A);
    await first;
    expect(mirror.get("old")).toBe("o");

    // The stream binds the restarted server's epoch and runs the reconcile before its first frame.
    versionMap().bind(EPOCH_B);
    reconcile();
    await until(() => reads.length === 2, "the reconcile's read");
    mirror.applyFrame("framed", "f", frameStamp(kind, "5"));
    expect(mirror.get("framed")).toBe("f");

    reads[1]?.answer([["kept", "k"]], "4", EPOCH_B);
    await until(() => mirror.get("kept") === "k", "the reconcile's read to apply");
    expect(mirror.get("old"), "a row the restarted server does not list").toBeUndefined();
    expect(mirror.get("framed"), "a frame newer than the read").toBe("f");
    expect(reads).toHaveLength(2);
  });

  it("a boot read pending across the bind and the first frame still applies", async () => {
    const { mirror, reads, kind } = scripted();
    const boot = mirror.refetch();
    versionMap().bind(EPOCH_B);
    mirror.applyFrame("framed", "f", frameStamp(kind, "5"));

    reads[0]?.answer([["listed", "l"]], "4", EPOCH_B);
    await boot;

    expect(mirror.get("listed")).toBe("l");
    expect(mirror.get("framed")).toBe("f");
    expect(reads, "nothing was reset, so nothing was re-read").toHaveLength(1);
  });

  it("a restart first seen in a frame replaces the read it drops", async () => {
    versionMap().bind(EPOCH_A);
    const { mirror, reads, kind } = scripted();
    const first = mirror.refetch();
    reads[0]?.answer([["old", "o"]], "3", EPOCH_A);
    await first;
    void mirror.refetch();
    await until(() => reads.length === 2, "the second read");

    versionMap().bind(EPOCH_B);
    mirror.applyFrame("framed", "f", frameStamp(kind, "2"));
    await until(() => reads.length === 3, "the read that replaces the dropped one");

    reads[1]?.answer([["stale", "s"]], "9");
    reads[2]?.answer([["fresh", "x"]], "1", EPOCH_B);
    await until(() => mirror.get("fresh") === "x", "the replacement read to apply");
    expect(mirror.get("stale"), "the read begun under the old process").toBeUndefined();
    expect(mirror.get("old")).toBeUndefined();
    expect(mirror.get("framed")).toBe("f");
  });
});

/** Answer the read at `index` and wait for the mirror to have handled it. */
async function answerRead(
  reads: Read[],
  index: number,
  pending: Promise<void>,
  rows: [string, string][],
  version: string,
  epoch?: string,
): Promise<void> {
  reads[index]?.answer(rows, version, epoch);
  await pending;
}

function heldVersion(kind: string): string | undefined {
  return versionMap()
    .snapshot()
    .held.find((h) => h.kind === kind)?.version;
}

describe("the order a mirror applies frames and snapshots in", () => {
  it("refuses a frame no newer than its key or the snapshot floor", async () => {
    const { mirror, reads, kind } = scripted();
    await answerRead(reads, 0, mirror.refetch(), [["a", "1"]], "5");

    expect(mirror.applyFrame("a", "old", frameStamp(kind, "4"))).toBeUndefined();
    expect(mirror.applyFrame("b", "old", frameStamp(kind, "5")), "at the floor").toBeUndefined();
    expect(mirror.applyFrame("a", "2", frameStamp(kind, "6"))).toEqual({
      key: "a",
      prev: "1",
      next: "2",
    });
    expect(
      mirror.applyFrame("a", "3", frameStamp(kind, "6")),
      "at the key's own version",
    ).toBeUndefined();
    expect(mirror.get("a")).toBe("2");
    expect(mirror.get("b")).toBeUndefined();
  });

  it("refuses a snapshot older than the floor whole", async () => {
    const fetched = vi.fn();
    const { mirror, reads } = scripted({ onFetched: fetched });
    await answerRead(reads, 0, mirror.refetch(), [["a", "x"]], "5");
    await answerRead(reads, 1, mirror.refetch(), [["b", "y"]], "4");

    expect(mirror.get("a")).toBe("x");
    expect(mirror.get("b")).toBeUndefined();
    expect(fetched).toHaveBeenCalledTimes(1);
  });

  it("a snapshot removes only keys held below its version", async () => {
    const { mirror, reads, kind } = scripted();
    await answerRead(
      reads,
      0,
      mirror.refetch(),
      [
        ["a", "1"],
        ["b", "2"],
      ],
      "3",
    );
    mirror.applyFrame("b", "2b", frameStamp(kind, "4"));
    mirror.applyFrame("c", "3", frameStamp(kind, "6"));

    await answerRead(reads, 1, mirror.refetch(), [["a", "1"]], "5");

    expect(mirror.get("a")).toBe("1");
    expect(mirror.get("b"), "its frame is older than the read that omits it").toBeUndefined();
    expect(mirror.get("c"), "added by a frame newer than the read").toBe("3");
  });

  it("a removal frame for a key never held stops an older read adding it", async () => {
    const { mirror, reads, kind } = scripted();
    const read = mirror.refetch();
    expect(mirror.applyFrame("gone", undefined, frameStamp(kind, "7"))).toBeUndefined();
    await answerRead(reads, 0, read, [["gone", "g"]], "6");
    expect(mirror.get("gone")).toBeUndefined();
  });

  it("adopt orders like a fetch", () => {
    const onChange = vi.fn();
    const fetched = vi.fn();
    const { mirror, kind } = scripted({ onChange, onFetched: fetched });
    mirror.adopt(new Map([["a", "1"]]), "5");
    mirror.adopt(new Map([["b", "2"]]), "4");
    expect(mirror.applyFrame("a", "old", frameStamp(kind, "5"))).toBeUndefined();

    expect(mirror.entries()).toEqual([["a", "1"]]);
    expect(onChange).toHaveBeenCalledExactlyOnceWith([{ key: "a", prev: undefined, next: "1" }]);
    expect(fetched).not.toHaveBeenCalled();
  });

  it("refuses a frame with no usable stamp and reads the inventory again", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const { mirror, reads, kind } = scripted();
    expect(mirror.applyFrame("a", "x", undefined)).toBeUndefined();
    expect(mirror.applyFrame("a", "x", frameStamp("another_kind", "3"))).toBeUndefined();
    expect(mirror.applyFrame("a", "x", frameStamp(kind, "v3"))).toBeUndefined();

    expect(mirror.get("a")).toBeUndefined();
    expect(reads, "each refusal reads again").toHaveLength(3);
    expect(warn).toHaveBeenCalledTimes(1);
    await answerRead(reads, 0, Promise.resolve(), [["a", "read"]], "1");
    await until(() => mirror.get("a") === "read", "the re-read to apply");
  });

  it.each([
    ["the older answers first", [0, 1]],
    ["the newer answers first", [1, 0]],
  ] as const)("a refetch while a read is out starts its own read: %s", async (_, order) => {
    const { mirror, reads } = scripted();
    const pending = [mirror.refetch(), mirror.refetch()];
    expect(reads, "the second refetch's own read").toHaveLength(2);

    const answers = [
      () => reads[0]?.answer([["a", "before"]], "4"),
      () => reads[1]?.answer([["a", "after"]], "6"),
    ];
    for (const index of order) {
      answers[index]?.();
    }
    await Promise.all(pending);
    expect(mirror.get("a"), "the newer read's row").toBe("after");
  });

  it("a reconcile forgets the versions, keeps the values and reads again", async () => {
    const { mirror, reads, kind } = scripted();
    await answerRead(reads, 0, mirror.refetch(), [["a", "1"]], "9");
    reconcile();

    expect(mirror.get("a"), "shown until the new read answers").toBe("1");
    await until(() => reads.length === 2, "the reconcile's read");
    expect(
      mirror.applyFrame("b", "2", frameStamp(kind, "2")),
      "below the forgotten floor",
    ).toBeDefined();
  });

  it("observes a stamp only above the highest it observed", async () => {
    const { mirror, reads, kind } = scripted();
    mirror.applyFrame("a", "1", frameStamp(kind, "5"));
    expect(heldVersion(kind)).toBe("5");

    await answerRead(reads, 0, mirror.refetch(), [["a", "1"]], "4");
    expect(heldVersion(kind), "an older read applied but recorded nothing").toBe("5");
    mirror.applyFrame("a", "2", frameStamp(kind, "8"));
    expect(heldVersion(kind)).toBe("8");
  });

  it("a watched key moves only with its own row", () => {
    const { mirror, kind } = scripted();
    const a = mirror.watch("a");
    mirror.applyFrame("b", "1", frameStamp(kind, "1"));
    expect(a.value).toBeUndefined();
    mirror.applyFrame("a", "2", frameStamp(kind, "2"));
    expect(a.value).toBe("2");
    mirror.applyFrame("a", undefined, frameStamp(kind, "3"));
    expect(a.value).toBeUndefined();
  });
});

describe("the epoch and the generation a read is applied under", () => {
  it("refuses a read stamped by another server process", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const onChange = vi.fn();
    const fetched = vi.fn();
    versionMap().bind(EPOCH_B);
    const { mirror, reads, kind } = scripted({ onChange, onFetched: fetched });
    mirror.applyFrame("a", "1", frameStamp(kind, "2"));
    onChange.mockClear();

    await answerRead(reads, 0, mirror.refetch(), [["b", "2"]], "9", EPOCH_A);

    expect(mirror.entries()).toEqual([["a", "1"]]);
    expect(onChange).not.toHaveBeenCalled();
    expect(fetched).not.toHaveBeenCalled();
    expect(heldVersion(kind)).toBe("2");
    expect(
      mirror.applyFrame("a", "3", frameStamp(kind, "3")),
      "the floor did not move",
    ).toBeDefined();
    expect(warn).toHaveBeenCalledTimes(1);
  });

  it("after a reconcile, a read below the old floor applies and is observed", async () => {
    versionMap().bind(EPOCH_A);
    const { mirror, reads, kind } = scripted();
    await answerRead(reads, 0, mirror.refetch(), [["a", "1"]], "9", EPOCH_A);

    versionMap().bind(EPOCH_B);
    reconcile();
    await until(() => reads.length === 2, "the reconcile's read");
    reads[1]?.answer([["b", "2"]], "2", EPOCH_B);
    await until(() => mirror.get("b") === "2", "the reconcile's read to apply");

    expect(mirror.get("a")).toBeUndefined();
    expect(heldVersion(kind)).toBe("2");
  });

  it("drops a read begun before a reconcile and applies the reconcile's own", async () => {
    versionMap().bind(EPOCH_B);
    const { mirror, reads } = scripted();
    const early = mirror.refetch();
    reconcile();
    await until(() => reads.length === 2, "the reconcile's own read");

    await answerRead(reads, 0, early, [["early", "e"]], "5");
    expect(mirror.get("early"), "an empty epoch").toBeUndefined();
    reads[1]?.answer([["late", "l"]], "3", EPOCH_B);
    await until(() => mirror.get("late") === "l", "the reconcile's read to apply");
    expect(mirror.get("early")).toBeUndefined();
  });

  it("a matching epoch does not save a read begun before a reconcile", async () => {
    versionMap().bind(EPOCH_B);
    const { mirror, reads } = scripted();
    const early = mirror.refetch();
    reconcile();
    await until(() => reads.length === 2, "the reconcile's read");
    await answerRead(reads, 0, early, [["early", "e"]], "5", EPOCH_B);
    expect(mirror.get("early")).toBeUndefined();
  });

  it("an adopted snapshot below the old floor applies once the map was rebound", () => {
    versionMap().bind(EPOCH_A);
    const { mirror } = scripted();
    mirror.adopt(new Map([["a", "1"]]), "9");

    versionMap().bind(EPOCH_B);
    mirror.adopt(new Map([["b", "2"]]), "2");

    expect(mirror.entries()).toEqual([["b", "2"]]);
  });
});

type Op = readonly [key: string, value: string | undefined];

describe("any interleaving converges", () => {
  it("in-order frames and snapshots read at any version end on the newest state", () => {
    const op = fc.tuple(
      fc.constantFrom("a", "b", "c", "d"),
      fc.option(fc.string({ maxLength: 2 }), { nil: undefined }),
    );
    const run = fc.array(op, { minLength: 1, maxLength: 12 }).chain((ops: Op[]) =>
      fc.tuple(
        fc.constant(ops),
        // Each snapshot: the frame count delivered before it, and the version it was read at.
        fc.array(fc.tuple(fc.nat({ max: ops.length }), fc.nat({ max: ops.length })), {
          maxLength: 6,
        }),
      ),
    );
    fc.assert(
      fc.property(run, ([ops, snaps]) => {
        const states: Map<string, string>[] = [new Map()];
        for (const [key, value] of ops) {
          const next = new Map(states.at(-1));
          if (value === undefined) {
            next.delete(key);
          } else {
            next.set(key, value);
          }
          states.push(next);
        }
        const { mirror, kind } = scripted();
        const adoptSnapshotsAt = (delivered: number): void => {
          for (const [at, readAt] of snaps) {
            if (at === delivered) {
              mirror.adopt(states[readAt] ?? new Map(), String(readAt));
            }
          }
        };
        ops.forEach(([key, value], delivered) => {
          adoptSnapshotsAt(delivered);
          mirror.applyFrame(key, value, frameStamp(kind, String(delivered + 1)));
        });
        adoptSnapshotsAt(ops.length);
        expect(new Map(mirror.entries())).toEqual(states.at(-1));
      }),
    );
  });
});

describe("a read that fails", () => {
  it("a fetch that throws at once is a failed read and the next refetch reads again", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    let calls = 0;
    const mirror = createMirror<string, string>({
      kind: "mirror_case_throws",
      fetch: () => {
        calls++;
        if (calls === 1) {
          throw new Error("refused");
        }
        return Promise.resolve({
          rows: new Map([["a", "1"]]),
          stamp: { kind: "mirror_case_throws", ref: "", version: "1" },
        });
      },
    });
    await mirror.refetch();
    expect(mirror.get("a")).toBeUndefined();
    expect(warn).toHaveBeenCalledTimes(1);

    await mirror.refetch();
    expect(mirror.get("a")).toBe("1");
  });
});
