// The SYNC half of the tab projection: which frames reach `tabs.ts`, in what order, and when to
// re-list. No rows, no DOM, no SSE binding (the composition root feeds it).
//  1. VERSION RULES over the collection's one monotonic version (internal/tabs.Store.list): at or
//     below local ignore, local+1 apply, beyond re-list. ONLY AN EVENT advances the watermark; a
//     response version is consumed by the pending-op machine, never adopted.
//  2. ONE SERIALIZED QUEUE in arrival order; frames arriving during a re-list are re-tested after.
//  3. STALE-SNAPSHOT GUARD: a snapshot BELOW local is discarded. 4. PENDING-OP MACHINE by `op_id`:
//     reconciles response, echo and snapshot in any order; sole `takeLocalOp` consumer.

import { apiGetTyped } from "./api-client.js";
import { observeStamp } from "./subject-versions.js";
import type { TabSubject, TabsChangedPayload } from "./types.js";
import { decodeTabList } from "./wire/decoders.gen.js";

/** How long a dispatched mutation stays correlatable: bounds the set for frames that never come
 *  (an idempotent open, a refusal, a dropped connection). */
const OP_TTL_MS = 60_000;

/** Re-list backoff for a `verifying` remove, capped at the last entry. */
const VERIFY_BACKOFF_MS = [1_000, 2_000, 4_000, 8_000, 16_000, 30_000] as const;

/** What the sync layer needs from the row holder: two verbs, declared at the consumer. */
export interface TabsTarget {
  /** Replace the projection from a version-checked, overlaid snapshot (boot list and re-lists only). */
  reset: (tabs: readonly TabSubject[]) => void;
  /** Apply ONE version-checked mutation; `local` marks this device's own echo (mechanism 4). */
  apply: (delta: TabsChangedPayload, local: boolean) => void;
}

let target: TabsTarget | null = null;

/** The version the projection reflects. Advanced by an applied event and by an
 *  adopted snapshot, and by nothing else. */
let localVersion = 0;

/** Frames waiting to be applied, in ARRIVAL order. */
const queue: TabsChangedPayload[] = [];

/** One loop at a time is what makes the version rules well-defined; see mechanism 2. */
let draining = false;

/** The re-list in flight, so a gap detected while one is already running joins it
 *  rather than issuing a second GET. Boot's own list shares this slot, which is
 *  what stops an event that arrives mid-boot from fetching the collection twice. */
let listInFlight: Promise<boolean> | null = null;

/** op_ids this device minted, with the time they were minted. */
const localOps = new Map<string, number>();

/** Register the projection. Called once, from the composition root. Last
 *  registration wins, like `setReorderCallback`. */
export function registerTabsTarget(next: TabsTarget): void {
  target = next;
}

/** The version the projection reflects. Nothing in the app branches on it: every rule that
 *  consumes it is in this file. */
// deadset:ignore DS1004 -- test seam: observes the local tab list version
export function tabsVersion(): number {
  return localVersion;
}

/** Record a dispatched `opID` at the DISPATCH SITE: `run()` re-runs per retry, so an id minted there
 *  correlates nothing. */
export function markLocalOp(opID: string): void {
  sweepOps();
  localOps.set(opID, Date.now());
}

/** Whether `opID` names a mutation this device dispatched, CONSUMING it (one frame per committed
 *  mutation). The pending-op machine is the one caller. */
function takeLocalOp(opID: string | undefined): boolean {
  if (opID === undefined) {
    return false;
  }
  return localOps.delete(opID);
}

function sweepOps(): void {
  const cutoff = Date.now() - OP_TTL_MS;
  for (const [id, at] of localOps) {
    if (at < cutoff) {
      localOps.delete(id);
    }
  }
}

// The pending-op machine: one record per optimistic adopt/remove/reorder, keyed by opID. States:
// awaiting-response → confirmed-awaiting-frame, or (REMOVE-ONLY, on timeout) verifying: the removal
// stays applied and re-lists on backoff. A matching frame confirms in any state; a response retires
// at once when the watermark covers it or nothing committed (empty `closed`, `created: false`); a
// covering frame or snapshot absorbs; a definitive error rolls back; verifying settles on the first
// matching frame or authoritative list (present → rollback, absent → confirm). A pending reorder is
// an overlay. A RETIRED OP IGNORES LATER SIGNALS. `onConfirm` is client-local and runs AFTER the
// settling frame is applied, so its reopen re-check sees the current projection.

/** What the machine needs from a remove: its tab ids; the captured subtree rides the
 *  `onConfirm`/`rollback` closures. */
interface PendingRemoveSpec {
  /** The tab the close names. Presence of THIS id in an authoritative list is
   *  what settles a verifying op. */
  id: string;
  /** Every tab id the reversible visual removal took out (the row and its
   *  descendants). Keys the changed-upsert suppression and the snapshot filter. */
  capturedTabIDs: readonly string[];
  /** The deferred client-local teardown. Runs exactly once, on confirmation. */
  onConfirm: () => void;
  /** Restore the captured subtree. Runs exactly once, on definitive failure or
   *  on authoritative presence. */
  rollback: () => void;
}

interface PendingAdopt {
  kind: "adopt";
  opID: string;
  state: "awaiting-response" | "confirmed-awaiting-frame";
  /** Gesture order, for the suppression override below: only an open gestured
   *  AFTER a close may resurrect a row that close captured. */
  seq: number;
  /** For snapshot merge-back. Set at response. */
  subject?: TabSubject;
  committedVersion?: number;
}

interface PendingRemove {
  kind: "remove";
  opID: string;
  state: "awaiting-response" | "confirmed-awaiting-frame" | "verifying";
  /** Gesture order; see PendingAdopt.seq. */
  seq: number;
  id: string;
  capturedTabIDs: readonly string[];
  onConfirm: () => void;
  rollback: () => void;
  committedVersion?: number;
  verifyAttempts: number;
  verifyTimer: ReturnType<typeof setTimeout> | null;
}

interface PendingReorder {
  kind: "reorder";
  opID: string;
  state: "awaiting-response" | "confirmed-awaiting-frame";
  order: readonly string[];
  /** Runs exactly once, on definitive failure. */
  rollback: () => void;
  committedVersion?: number;
}

type PendingOp = PendingAdopt | PendingRemove | PendingReorder;

const pendingOps = new Map<string, PendingOp>();

/** Monotonic gesture order across ops of both kinds. Which of two ops the USER
 *  performed second is a fact the op set itself cannot answer once both are
 *  pending, and the suppression override turns on it. */
let opSeq = 0;

/** Notified whenever the LAST pending remove settles. One slot, one consumer:
 *  tabs.ts, whose deferred empty-state respawn re-arms on it. */
let onRemovesSettled: (() => void) | null = null;

/** Transition 1 for an open: record the dispatch. The op has no id yet — the
 *  server mints it — so the opID is the whole correlation. */
export function beginAdopt(opID: string): void {
  pendingOps.set(opID, { kind: "adopt", opID, state: "awaiting-response", seq: ++opSeq });
}

/** Transition 1 for a close: record the dispatch and the reversible removal the
 *  caller has already applied. The capture itself stays with the caller (see
 *  PendingRemoveSpec); the machine keeps the ids and the two callbacks. */
export function beginRemove(opID: string, spec: PendingRemoveSpec): void {
  pendingOps.set(opID, {
    kind: "remove",
    opID,
    state: "awaiting-response",
    seq: ++opSeq,
    id: spec.id,
    capturedTabIDs: spec.capturedTabIDs,
    onConfirm: spec.onConfirm,
    rollback: spec.rollback,
    verifyAttempts: 0,
    verifyTimer: null,
  });
}

/** Transition 1 for a reorder: record the dispatch. The caller has already
 *  applied `order` to the projection, as a remove's caller has already removed
 *  the row. */
export function beginReorder(
  opID: string,
  spec: { order: readonly string[]; rollback: () => void },
): void {
  pendingOps.set(opID, {
    kind: "reorder",
    opID,
    state: "awaiting-response",
    order: [...spec.order],
    rollback: spec.rollback,
  });
}

/** Transition 3 for a reorder: the response committed at `committedVersion`. It
 *  retires once the watermark covers that version and waits for the frame
 *  otherwise; it never writes the watermark itself. */
export function reorderCommitted(opID: string, committedVersion: number): void {
  const op = pendingOps.get(opID);
  if (op?.kind !== "reorder") {
    return;
  }
  op.committedVersion = committedVersion;
  if (localVersion >= committedVersion) {
    confirmOp(op);
    return;
  }
  op.state = "confirmed-awaiting-frame";
}

/** `ids` permuted by every pending reorder that may postdate `version` (all of them
 *  when `version` is omitted), in gesture order. An id a pending order does not
 *  name sorts last and is never dropped. */
export function overlayOrder(ids: readonly string[], version?: number): string[] {
  let out = [...ids];
  for (const op of pendingOps.values()) {
    if (
      op.kind === "reorder" &&
      (version === undefined || op.committedVersion === undefined || op.committedVersion > version)
    ) {
      out = permute(out, (id) => id, op.order);
    }
  }
  return out;
}

/** Transition 3 for an open: the response committed `subject` at
 *  `committedVersion`. `created: false` means the mutation committed NOTHING —
 *  no frame is coming, so the op retires on the spot. */
export function adoptCommitted(
  opID: string,
  subject: TabSubject,
  committedVersion: number,
  created: boolean,
): void {
  const op = pendingOps.get(opID);
  if (op?.kind !== "adopt") {
    return;
  }
  op.subject = subject;
  op.committedVersion = committedVersion;
  if (!created || localVersion >= committedVersion) {
    confirmOp(op);
    return;
  }
  op.state = "confirmed-awaiting-frame";
}

/** Transition 3 for a close. An EMPTY `closed` list confirms absence regardless of version (another
 *  device closed it). */
export function removeCommitted(
  opID: string,
  closed: readonly string[],
  committedVersion: number,
): void {
  const op = pendingOps.get(opID);
  if (op?.kind !== "remove") {
    return;
  }
  cancelVerify(op);
  op.committedVersion = committedVersion;
  if (closed.length === 0 || localVersion >= committedVersion) {
    confirmOp(op);
    return;
  }
  op.state = "confirmed-awaiting-frame";
}

/** Transition 5: the server answered an error, so the removal is undone; a retired op ignores it. */
export function opFailed(opID: string): void {
  const op = pendingOps.get(opID);
  if (op === undefined) {
    return;
  }
  failOp(op);
}

/** Transition 6: no answer, so NO restore and NO retire; the op verifies by re-listing. Remove-only. */
export function opTimedOut(opID: string): void {
  const op = pendingOps.get(opID);
  if (op?.kind !== "remove" || op.state === "verifying") {
    return;
  }
  op.state = "verifying";
  armVerify(op);
}

/** Whether any remove is pending, in ANY state — `verifying` included, because
 *  network unavailability can hold one there indefinitely and an empty strip
 *  mid-outage must not auto-respawn a chat. Read by `scheduleEmpty`'s deferral. */
export function removesPending(): boolean {
  for (const op of pendingOps.values()) {
    if (op.kind === "remove") {
      return true;
    }
  }
  return false;
}

/** Register the removes-settled notification. Called once, from the composition
 *  root (tabs.ts). Last registration wins, like `registerTabsTarget`. */
export function setOnRemovesSettled(fn: (() => void) | null): void {
  onRemovesSettled = fn;
}

/** Retire: delete the record and cancel its timer. Every later signal for this
 *  opID now finds nothing, which is what retired-op immunity IS. */
function retire(op: PendingOp): void {
  if (op.kind === "remove") {
    cancelVerify(op);
  }
  pendingOps.delete(op.opID);
}

function confirmOp(op: PendingOp): void {
  retire(op);
  if (op.kind === "remove") {
    op.onConfirm();
    noteRemoveSettled();
  }
}

function failOp(op: PendingOp): void {
  retire(op);
  if (op.kind === "remove") {
    op.rollback();
    noteRemoveSettled();
  } else if (op.kind === "reorder") {
    op.rollback();
  }
}

function noteRemoveSettled(): void {
  if (!removesPending()) {
    onRemovesSettled?.();
  }
}

function cancelVerify(op: PendingRemove): void {
  if (op.verifyTimer !== null) {
    clearTimeout(op.verifyTimer);
    op.verifyTimer = null;
  }
}

/** One timer per verifying remove, re-armed per attempt, cleared on retire; each tick re-lists once. */
function armVerify(op: PendingRemove): void {
  const delay = VERIFY_BACKOFF_MS[Math.min(op.verifyAttempts, VERIFY_BACKOFF_MS.length - 1)] ?? 0;
  op.verifyTimer = setTimeout(() => {
    op.verifyTimer = null;
    op.verifyAttempts++;
    void relist().finally(() => {
      if (pendingOps.get(op.opID) === op && op.state === "verifying") {
        armVerify(op);
      }
    });
  }, delay);
}

/** Whether this frame echoes a mutation this device dispatched: the marked-op
 *  set (consumed — a duplicate frame must not claim authorship twice) or a
 *  pending op, which by construction was dispatched here. */
function frameIsLocal(opID: string | undefined): boolean {
  const marked = takeLocalOp(opID);
  return marked || (opID !== undefined && pendingOps.has(opID));
}

/** Suppress a frame re-upserting a row a pending remove captured, keyed by TAB ID (a remote reopen
 *  mints a new id). Overridden only by a local adopt gestured AFTER that remove and carrying the
 *  same subject id, so an earlier open's late echo stays suppressed. */
function suppressChanged(changed: TabSubject): boolean {
  let capturedBy = -1;
  for (const op of pendingOps.values()) {
    if (op.kind === "remove" && op.capturedTabIDs.includes(changed.id)) {
      capturedBy = Math.max(capturedBy, op.seq);
    }
  }
  if (capturedBy < 0) {
    return false;
  }
  for (const op of pendingOps.values()) {
    if (
      op.kind === "adopt" &&
      op.seq > capturedBy &&
      op.subject?.kind === changed.kind &&
      op.subject.ref === changed.ref &&
      op.subject.id === changed.id
    ) {
      return false;
    }
  }
  return true;
}

/** The delta as the projection should see it: `changed` suppressed by a pending remove, `order`
 *  permuted by pending reorders it may predate; `removed_ids` always passes. */
function overlayFrame(delta: TabsChangedPayload): TabsChangedPayload {
  let out = delta;
  if (out.order !== undefined) {
    out = { ...out, order: overlayOrder(out.order, out.version) };
  }
  if (out.changed === undefined || !suppressChanged(out.changed)) {
    return out;
  }
  const { changed: _suppressed, ...rest } = out;
  return rest;
}

/** Transitions 2 and 4, run AFTER the frame reached the projection so a
 *  confirmation callback observes the post-apply state (the reopen re-check
 *  reads the CURRENT projection). */
function settleFrame(opID: string | undefined): void {
  if (opID !== undefined) {
    const op = pendingOps.get(opID);
    if (op !== undefined) {
      confirmOp(op);
    }
  }
  absorbCommitted();
}

/** Transition 4: every confirmed-awaiting-frame op the watermark now covers is
 *  absorbed — the mutation is part of what the projection holds, whether or not
 *  its own echo was ever correlated. */
function absorbCommitted(): void {
  for (const op of [...pendingOps.values()]) {
    if (
      op.state === "confirmed-awaiting-frame" &&
      op.committedVersion !== undefined &&
      localVersion >= op.committedVersion
    ) {
      confirmOp(op);
    }
  }
}

/** A snapshot with the pending overlay: removed rows filtered, a pending adopt merged back,
 *  reorders permuted, each only while the list can predate the mutation. */
function overlayList(tabs: readonly TabSubject[], version: number): TabSubject[] {
  let out = [...tabs];
  for (const op of pendingOps.values()) {
    if (
      op.kind === "remove" &&
      (op.committedVersion === undefined || op.committedVersion > version)
    ) {
      out = out.filter((t) => !op.capturedTabIDs.includes(t.id));
    }
  }
  for (const op of pendingOps.values()) {
    const subject = op.kind === "adopt" ? op.subject : undefined;
    if (
      subject !== undefined &&
      (op.committedVersion === undefined || op.committedVersion > version) &&
      !out.some((t) => t.id === subject.id)
    ) {
      out.push(subject);
    }
  }
  return permute(
    out,
    (t) => t.id,
    overlayOrder(
      out.map((t) => t.id),
      version,
    ),
  );
}

/** Hand one `tabs_changed` frame to the queue; never throws. The rules run in the drain, preserving
 *  arrival order across a re-list. */
export function ingestTabsChanged(delta: TabsChangedPayload): void {
  queue.push(delta);
  void drain();
}

async function drain(): Promise<void> {
  if (draining) {
    return;
  }
  draining = true;
  try {
    while (queue.length > 0) {
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- guarded by the loop condition
      const delta = queue.shift()!;
      if (delta.version <= localVersion) {
        // RULE 1: a duplicate or a pre-snapshot frame. The coordinator's emit-anyway removal (version + 1)
        // lands here only for a client already past it.
        continue;
      }
      if (delta.version > localVersion + 1) {
        // RULE 3: a frame was missed, so re-list. The queue is NOT cleared: queued frames re-test against
        // the new version.
        await relist();
        continue;
      }
      // RULE 2. Exactly one past local: this frame IS the next mutation.
      const local = frameIsLocal(delta.op_id);
      localVersion = delta.version;
      target?.apply(overlayFrame(delta), local);
      settleFrame(delta.op_id);
    }
  } finally {
    draining = false;
  }
}

/** Read the collection and adopt it: the boot read, a gap, a `reorder_tabs` 409 (re-list, NEVER
 *  re-send), each verify tick. Answers whether a snapshot was ADOPTED; a discarded stale one is false
 *  with nothing wrong, which only boot reads. */
export function listTabs(signal?: AbortSignal): Promise<boolean> {
  return relist(signal);
}

/** One read at a time; a caller arriving while one is out shares its answer, so the
 *  first caller's signal is the one the shared read carries. */
function relist(signal?: AbortSignal): Promise<boolean> {
  listInFlight ??= readList(signal).finally(() => {
    listInFlight = null;
  });
  return listInFlight;
}

async function readList(signal?: AbortSignal): Promise<boolean> {
  const list = await apiGetTyped("/api/tabs", decodeTabList, signal);
  if (list === null) {
    // Unreachable or undecodable: keep the projection; a failed list settles NO pending op.
    return false;
  }
  if (list.version < localVersion) {
    // MECHANISM 3: we are ahead of this snapshot, so discard it; it settles no verifying op.
    return false;
  }
  localVersion = list.version;

  // Authoritative: settle and retire BEFORE the reset (a settled op must not overlay it), run the
  // callbacks AFTER it.
  const confirms: PendingRemove[] = [];
  const rollbacks: PendingRemove[] = [];
  for (const op of [...pendingOps.values()]) {
    if (op.kind !== "remove") {
      continue;
    }
    if (op.state === "verifying") {
      // Transition 6's settlement: membership decides, PRESENT → restore,
      // ABSENT → confirm. A verifying op has no committedVersion (no answer ever
      // came), so a version comparison cannot answer this one.
      retire(op);
      (list.tabs.some((t) => t.id === op.id) ? rollbacks : confirms).push(op);
    }
  }
  target?.reset(overlayList(list.tabs, list.version));
  for (const op of confirms) {
    op.onConfirm();
  }
  for (const op of rollbacks) {
    op.rollback();
  }
  if (confirms.length > 0 || rollbacks.length > 0) {
    noteRemoveSettled();
  }
  // Transition 4 over the snapshot: an op whose committed version the adopted
  // list covers is absorbed, correlation or not.
  absorbCommitted();
  // AFTER the adoption: the `tabs` digest stamp certifies the set now held (frames carry no stamp).
  observeStamp(list.subject);
  return true;
}

/** Reorder `items` to match `order`, a PERMUTATION, never membership: an unnamed id keeps its
 *  relative position and sorts LAST, never closed and never first. Pure and generic. */
export function permute<T>(
  items: readonly T[],
  idOf: (item: T) => string,
  order: readonly string[],
): T[] {
  const remaining = new Map<string, T>();
  for (const item of items) {
    remaining.set(idOf(item), item);
  }
  const next: T[] = [];
  for (const id of order) {
    const item = remaining.get(id);
    if (item !== undefined) {
      next.push(item);
      remaining.delete(id);
    }
  }
  // Everything the order did not name, in the order it already had.
  for (const item of items) {
    if (remaining.has(idOf(item))) {
      next.push(item);
    }
  }
  return next;
}

/** @internal Test seam: drop the target, the version, the queue and every
 *  pending correlation and op. */
// deadset:ignore DS1004 -- test seam: resets the target, version, queue and pending ops
export function _resetTabsSyncForTest(): void {
  target = null;
  localVersion = 0;
  queue.length = 0;
  draining = false;
  listInFlight = null;
  localOps.clear();
  for (const op of pendingOps.values()) {
    if (op.kind === "remove") {
      cancelVerify(op);
    }
  }
  pendingOps.clear();
  opSeq = 0;
  onRemovesSettled = null;
}
