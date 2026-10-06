// The digest subjects' version map, read by the wake digest. Every version is a server stamp except
// `run_turn` (`run-store.ts`). A LEAF: `store.ts` and `store-load.ts` import it.
import { type Subject, type VersionMap, createVersionMap } from "@cplieger/sse";

import type { SubjectStamp } from "./wire/types.gen.js";

/** Where a recorded stamp is reported beyond this map. Under the worker host the tab
 *  forwards it, so the host's map holds every version the profile's tabs hold and one
 *  digest serves them all. */
type ObserveSink = (subject: Subject, version: string, epoch: string) => void;

let map: VersionMap = createVersionMap();
let sink: ObserveSink | null = null;

/** The live map, for the stream that binds it and the digest that reads it. */
export function versionMap(): VersionMap {
  return map;
}

/** Install (or with `null` remove) the reporter every recorded stamp reaches. */
export function setObserveSink(fn: ObserveSink | null): void {
  sink = fn;
}

/** Record a stamp AFTER the state it certifies is applied, never from a digest's answer. A REST
 *  stamp carries and binds the epoch; a frame stamp rides the stream's binding; a foreign one is
 *  ignored. */
export function observeStamp(stamp: SubjectStamp | undefined): void {
  if (stamp === undefined) {
    return;
  }
  const subject: Subject = { kind: stamp.kind, ref: stamp.ref };
  const epoch = stamp.epoch === undefined || stamp.epoch === "" ? undefined : stamp.epoch;
  map.observe(subject, stamp.version, epoch);
  // Only what the map recorded travels: a foreign-epoch stamp recorded nothing, and an
  // entry in an unbound map has no epoch to present.
  const bound = map.epoch();
  if (sink !== null && bound !== null && (epoch === undefined || epoch === bound)) {
    sink(subject, stamp.version, bound);
  }
}

export function hasSubject(kind: string, ref: string): boolean {
  return map.has({ kind, ref });
}

export function forgetSubject(kind: string, ref: string): void {
  map.forget({ kind, ref });
}

/** A fresh, unbound map with no reporter. Test isolation only; production never resets. */
export function _resetForTest(): void {
  map = createVersionMap();
  sink = null;
}
