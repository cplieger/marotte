// The ordered mirror: which epoch it orders under across a reconcile, a boot bind and a restart
// seen first in a frame, and that a read it drops is always replaced.

import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { BUS_RECONCILE, emitBus } from "./bus.js";
import { createMirror, installMirrors, type Mirror } from "./mirror.js";
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

/** A mirror over a kind no other case uses, whose every read waits for the case to answer it. */
function scripted(): { mirror: Mirror<string, string>; reads: Read[]; kind: string } {
  kinds++;
  const kind = `mirror_case_${String(kinds)}`;
  const reads: Read[] = [];
  const mirror = createMirror<string, string>({
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
