// The ordered client copy of a server inventory that both row frames and a whole-inventory read
// write. A mirror never applies information older than what it holds: each key and the snapshot
// floor carry the subject version they were set at, and the server mints that version from a
// counter, so within an epoch the versions compare as numbers. A LEAF: the stores that own an
// inventory import it.
import { batch, signal, touch, type ReadonlySignal, type Signal } from "@cplieger/reactive";

import { BUS_RECONCILE, onBus } from "./bus.js";
import { observeStamp, versionMap } from "./subject-versions.js";
import type { SubjectStamp } from "./wire/types.gen.js";

/** One read of the whole inventory and the stamp it was read at. */
export interface MirrorSnapshot<K, V> {
  readonly rows: ReadonlyMap<K, V>;
  readonly stamp: SubjectStamp;
}

/** One key the mirror moved; `undefined` is absent. */
export interface MirrorChange<K, V> {
  readonly key: K;
  readonly prev: V | undefined;
  readonly next: V | undefined;
}

export interface MirrorOptions<K, V> {
  /** The digest subject kind, workspace-wide (empty ref). One mirror per kind: the last one
   *  created for a kind is the one the reconcile reaches. */
  readonly kind: string;
  /** One read of the whole inventory; null when the read failed. */
  readonly fetch: (signal?: AbortSignal) => Promise<MirrorSnapshot<K, V> | null>;
  /** Every change the mirror applied, in order, before its stamp is observed. */
  readonly onChange?: (changes: readonly MirrorChange<K, V>[]) => void;
  /** Once after a fetched snapshot applied, after its `onChange` and its stamp; never for a frame
   *  or `adopt`, nor for a read that failed, was refused or was dropped. */
  readonly onFetched?: () => void;
}

export interface Mirror<K, V> {
  /** Apply one row frame (`undefined` value: the key left the inventory). Answers the change, or
   *  `undefined` when the frame is not newer than what the mirror holds for its key. */
  applyFrame(
    key: K,
    value: V | undefined,
    stamp: SubjectStamp | undefined,
  ): MirrorChange<K, V> | undefined;
  /** Apply a snapshot another reader already read, ordered like a fetched one. */
  adopt(rows: ReadonlyMap<K, V>, version: string): void;
  /** Start a read of the inventory and apply its answer; a read already out is not joined, so
   *  the answer was read after the call. Never rejects for a refused or dropped answer. */
  refetch(signal?: AbortSignal): Promise<void>;
  /** Untracked. */
  get(key: K): V | undefined;
  /** Tracked per key: a reader re-runs only when this key moves. */
  watch(key: K): ReadonlySignal<V | undefined>;
  /** Tracked on the whole inventory, in insertion order. */
  entries(): readonly (readonly [K, V])[];
}

interface Resettable {
  reconcile(signal: AbortSignal): void;
}

const registry = new Map<string, Resettable>();

/** Whether `kind` is a mirror's subject, whose stamps only the mirror observes. */
export function isMirrorKind(kind: string): boolean {
  return registry.has(kind);
}

/** Subscribe every mirror to the transport reconcile: a gap or a new server process may have
 *  moved any row, so each forgets its versions and reads again. Returns the unsubscribe. */
export function installMirrors(): () => void {
  return onBus(BUS_RECONCILE, ({ signal: abort }) => {
    for (const m of registry.values()) {
      m.reconcile(abort);
    }
  });
}

/** A subject version as a number, or null for anything the server's counter cannot mint. */
function parseVersion(version: string): bigint | null {
  return /^\d{1,20}$/u.test(version) ? BigInt(version) : null;
}

export function createMirror<K, V>(o: MirrorOptions<K, V>): Mirror<K, V> {
  const rows = new Map<K, V>();
  const keyVersions = new Map<K, bigint>();
  const keySignals = new Map<K, Signal<V | undefined>>();
  const changed = signal(0);
  // null is below every version, so a snapshot at "0" applies to an empty mirror.
  let floor: bigint | null = null;
  let observed: bigint | null = null;
  // The map epoch the held versions were set under, and whether anything was applied under it:
  // a mirror that applied nothing since its creation or reset adopts the current epoch instead
  // of resetting, so a reconcile's read is not dropped by the new process's first frame.
  let epoch: string | null = versionMap().epoch();
  let applied = false;
  let generation = 0;
  // The newest read still out, compared by identity.
  let inflight: object | null = null;
  let warned = false;

  function warnOnce(what: string): void {
    if (!warned) {
      warned = true;
      console.warn(`mirror ${o.kind}: ${what}`);
    }
  }

  function reset(): void {
    floor = null;
    observed = null;
    keyVersions.clear();
    generation++;
    epoch = versionMap().epoch();
    applied = false;
  }

  /** Reset when the map was rebound since this mirror last applied. A read the reset retires
   *  would be dropped with nothing replacing it, so one is started in its place. */
  function followEpoch(): void {
    const now = versionMap().epoch();
    if (!applied) {
      epoch = now;
      return;
    }
    if (epoch === now) {
      return;
    }
    reset();
    if (inflight !== null) {
      void refetch();
    }
  }

  function heldVersion(key: K): bigint | null {
    const v = keyVersions.get(key);
    if (v === undefined) {
      return floor;
    }
    return floor !== null && floor > v ? floor : v;
  }

  function write(key: K, next: V | undefined): MirrorChange<K, V> {
    const prev = rows.get(key);
    if (next === undefined) {
      rows.delete(key);
    } else {
      rows.set(key, next);
    }
    const sig = keySignals.get(key);
    if (sig !== undefined) {
      sig.value = next;
    }
    return { key, prev, next };
  }

  /** After the state is applied: record the stamp (only above the highest recorded, so the digest
   *  map never moves back) and the epoch the map now holds. */
  function settle(version: bigint, stamp: SubjectStamp): void {
    if (observed === null || version > observed) {
      observed = version;
      observeStamp(stamp);
    }
    epoch = versionMap().epoch();
    applied = true;
  }

  function bump(): void {
    changed.value = changed.peek() + 1;
  }

  function applyFrame(
    key: K,
    value: V | undefined,
    stamp: SubjectStamp | undefined,
  ): MirrorChange<K, V> | undefined {
    const version = stamp?.kind === o.kind ? parseVersion(stamp.version) : null;
    if (stamp === undefined || version === null) {
      warnOnce("a frame with no usable stamp; reading the inventory again");
      void refetch();
      return undefined;
    }
    followEpoch();
    const held = heldVersion(key);
    if (held !== null && version <= held) {
      return undefined;
    }
    const changes: MirrorChange<K, V>[] = [];
    batch(() => {
      // The version is recorded even when the row does not move: a removal frame for a key the
      // mirror never held still has to stop an older read from adding it.
      keyVersions.set(key, version);
      if (rows.get(key) !== value) {
        changes.push(write(key, value));
        bump();
      }
    });
    if (changes.length > 0) {
      o.onChange?.(changes);
    }
    settle(version, stamp);
    return changes[0];
  }

  /** Answers whether the snapshot applied. A snapshot AT the floor still applies, so a read of an
   *  unchanged inventory reaches `onFetched`, and rewrites each key without its own version whose
   *  value is a different reference. */
  function applySnapshot(answer: ReadonlyMap<K, V>, version: bigint, stamp: SubjectStamp): boolean {
    followEpoch();
    if (floor !== null && version < floor) {
      return false;
    }
    const changes: MirrorChange<K, V>[] = [];
    batch(() => {
      const keys = new Set<K>([...rows.keys(), ...answer.keys()]);
      for (const key of keys) {
        const kv = keyVersions.get(key);
        if (kv !== undefined && kv >= version) {
          continue;
        }
        keyVersions.delete(key);
        const prev = rows.get(key);
        const next = answer.get(key);
        if (prev !== next) {
          changes.push(write(key, next));
        }
      }
      floor = version;
      if (changes.length > 0) {
        bump();
      }
    });
    if (changes.length > 0) {
      o.onChange?.(changes);
    }
    settle(version, stamp);
    return true;
  }

  function applyFetched(answer: MirrorSnapshot<K, V> | null, readGeneration: number): void {
    if (readGeneration !== generation || answer === null) {
      return;
    }
    const stampEpoch = answer.stamp.epoch ?? "";
    const bound = versionMap().epoch();
    if (stampEpoch !== "" && bound !== null && stampEpoch !== bound) {
      warnOnce("refused a read from another server process");
      return;
    }
    const version = answer.stamp.kind === o.kind ? parseVersion(answer.stamp.version) : null;
    if (version === null) {
      warnOnce("a read with no usable stamp");
      return;
    }
    if (applySnapshot(answer.rows, version, answer.stamp)) {
      o.onFetched?.();
    }
  }

  function refetch(abort?: AbortSignal): Promise<void> {
    const readGeneration = generation;
    // Held before the fetch starts: a fetch that throws synchronously reaches `finally` at once.
    const read = {};
    inflight = read;
    return (async (): Promise<void> => {
      let answer: MirrorSnapshot<K, V> | null = null;
      try {
        answer = await o.fetch(abort);
      } catch (e) {
        // Callers fire and forget, so a throwing fetch is reported here as a failed read.
        warnOnce(`a read threw: ${String(e)}`);
      } finally {
        if (inflight === read) {
          inflight = null;
        }
      }
      applyFetched(answer, readGeneration);
    })();
  }

  function adopt(answer: ReadonlyMap<K, V>, version: string): void {
    const v = parseVersion(version);
    if (v === null) {
      warnOnce("an adopted snapshot with no usable version; reading the inventory again");
      void refetch();
      return;
    }
    applySnapshot(answer, v, { kind: o.kind, ref: "", version });
  }

  registry.set(o.kind, {
    reconcile(abort) {
      reset();
      void refetch(abort);
    },
  });

  return {
    applyFrame,
    adopt,
    refetch,
    get: (key) => rows.get(key),
    watch(key) {
      let sig = keySignals.get(key);
      if (sig === undefined) {
        sig = signal<V | undefined>(rows.get(key));
        keySignals.set(key, sig);
      }
      return sig;
    },
    entries() {
      touch(changed);
      return [...rows.entries()];
    },
  };
}
