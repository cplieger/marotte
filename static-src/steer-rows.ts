// The one fold for the chat's dock (store.ts) and a run step's (run-step-steers.ts): two stores, one set
// of rules.

import { payloadOf } from "./turns.js";
import type { Entry, PendingSteer } from "./types.js";
import type { SteerOrigin, SteerRowState } from "./wire/types.gen.js";

export interface SteerFrame {
  id: string;
  text: string;
  origin: SteerOrigin;
  replaces?: readonly string[] | undefined;
  /** Absent reads as `queued`, the plain row a direct POST reply describes. */
  state?: SteerRowState | undefined;
}

/** A fold's outcome: the new rows, and whether the change moves what the dock draws (a batch only
 *  re-labels the id its rows will be read under, which repaints nothing). */
interface SteerFold {
  rows: readonly PendingSteer[];
  repaint: boolean;
}

/** The row a device draws on Send, before any frame: `user` is a fact, it is this device's POST. */
export function pendingSteerRow(id: string, text: string): PendingSteer {
  return { id, text, origin: "user", pending: true };
}

/** Upsert a row by id: a retry under the same message id refreshes the row rather than adding one. */
export function upsertSteerRow(
  rows: readonly PendingSteer[],
  row: PendingSteer,
): readonly PendingSteer[] {
  const at = rows.findIndex((e) => e.id === row.id);
  return at >= 0 ? rows.map((e, i) => (i === at ? row : e)) : [...rows, row];
}

/** Whether a `steer` entry settles the row: its own id, the id KAS held a batch under, or a resend
 *  naming it. */
export function steerEntrySettles(entry: Entry, rowID: string, kas: string | undefined): boolean {
  if (entry.id === rowID || (kas !== undefined && entry.id === kas)) {
    return true;
  }
  return payloadOf(entry, "steer")?.resends?.includes(rowID) === true;
}

/** The rows a `steer` entry leaves, or null when it settles none. */
export function retireSettledSteers(
  rows: readonly PendingSteer[],
  entry: Entry,
): readonly PendingSteer[] | null {
  const rest = rows.filter((e) => !steerEntrySettles(entry, e.id, e.kas));
  return rest.length === rows.length ? null : rest;
}

/** Fold one `steer_queued` frame. A ROW frame upserts the row its id names: the id matches (adopt the
 *  text and state, clear `pending`); no id match but the OLDEST pending row carries the same text
 *  (adopt the server's id); neither (append it). A BATCH frame (`replaces`) records the id KAS holds
 *  its rows under. `settled` reports whether the log already holds that id's `steer` entry, which
 *  retires the row instead: a reconnect replays frames for steers the agent has since read. Null when
 *  nothing moves. */
export function foldSteerFrame(
  rows: readonly PendingSteer[],
  frame: SteerFrame,
  settled: (steerID: string) => boolean,
): SteerFold | null {
  if (frame.replaces !== undefined && frame.replaces.length > 0) {
    return foldSteerBatch(rows, frame.id, frame.replaces, settled);
  }
  if (frame.state === "removed" || settled(frame.id)) {
    const rest = rows.filter((e) => e.id !== frame.id);
    return rest.length === rows.length ? null : { rows: rest, repaint: true };
  }
  const at = rows.findIndex((e) => e.id === frame.id);
  const adoptAt = at >= 0 ? at : rows.findIndex((e) => e.pending === true && e.text === frame.text);
  const prev = adoptAt >= 0 ? rows[adoptAt] : undefined;
  // The frame's origin wins in every branch: the server resolved it against the ledger of what it
  // sent, where the optimistic row's `user` was this device's claim.
  const row: PendingSteer = {
    id: frame.id,
    text: frame.text,
    origin: frame.origin,
    ...(prev?.kas !== undefined && { kas: prev.kas }),
    ...(prev?.compacted === true && { compacted: true as const }),
    ...(frame.state === "unsent" && { unsent: true as const }),
  };
  const next = adoptAt >= 0 ? rows.map((e, i) => (i === adoptAt ? row : e)) : [...rows, row];
  return { rows: next, repaint: true };
}

/** A batch the log already shows read retires its members; otherwise only the id their read will
 *  arrive under moves, and the rows keep their element and their words. */
function foldSteerBatch(
  rows: readonly PendingSteer[],
  batchID: string,
  keys: readonly string[],
  settled: (steerID: string) => boolean,
): SteerFold | null {
  const members = new Set(keys);
  if (settled(batchID)) {
    const rest = rows.filter((e) => !members.has(e.id));
    return rest.length === rows.length ? null : { rows: rest, repaint: true };
  }
  if (!rows.some((e) => members.has(e.id) && e.kas !== batchID)) {
    return null;
  }
  return {
    rows: rows.map((e) => (members.has(e.id) ? { ...e, kas: batchID } : e)),
    repaint: false,
  };
}
