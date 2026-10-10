// Line endings between a file's exact bytes and a textarea, which reads every CRLF and lone CR
// back as LF. The editor holds LF-normalized text plus a record of each break's ending, and a save
// serializes the two back. A file whose breaks all share one ending (or has none) needs nothing
// more. A MIXED file shadows every edit, because the final text cannot say which of two identical
// lines was deleted; an edit it cannot attribute exactly marks the record uncertain, and an
// uncertain record refuses to save rather than guess an ending for an untouched line.

type Eol = "lf" | "crlf" | "cr";

const EOLS: readonly Eol[] = ["lf", "crlf", "cr"];
const ENDING: Readonly<Record<Eol, string>> = { lf: "\n", crlf: "\r\n", cr: "\r" };
const TAG: Readonly<Record<Eol, number>> = { lf: 0, crlf: 1, cr: 2 };

/** The endings of a normalized text's breaks. */
export interface EolRecord {
  /** The ending every break carries, or null when they differ. */
  readonly uniform: Eol | null;
  /** One tag per LF of the normalized text (an index into `lf, crlf, cr`), when mixed. */
  readonly tags: Uint8Array | null;
}

const LF = 10;
const CR = 13;

/** Split exact text into LF-normalized text and its record. */
export function normalize(raw: string): { text: string; record: EolRecord } {
  const parts: string[] = [];
  const tags: number[] = [];
  let start = 0;
  for (let i = 0; i < raw.length; i++) {
    const c = raw.charCodeAt(i);
    if (c === LF) {
      parts.push(raw.slice(start, i));
      tags.push(TAG.lf);
      start = i + 1;
    } else if (c === CR) {
      parts.push(raw.slice(start, i));
      if (raw.charCodeAt(i + 1) === LF) {
        tags.push(TAG.crlf);
        i++;
      } else {
        tags.push(TAG.cr);
      }
      start = i + 1;
    }
  }
  parts.push(raw.slice(start));
  return { text: parts.join("\n"), record: recordOf(Uint8Array.from(tags)) };
}

function recordOf(tags: Uint8Array): EolRecord {
  const first = tags[0];
  if (first === undefined) {
    return { uniform: "lf", tags: null };
  }
  if (tags.every((t) => t === first)) {
    return { uniform: EOLS[first] ?? "lf", tags: null };
  }
  return { uniform: null, tags };
}

/** The exact text for normalized `text` under `record`. */
export function serialize(text: string, record: EolRecord): string {
  return serializeRange(text, record, 0, text.length);
}

/** The exact text of `text.slice(from, to)`: what a copy of that range writes. */
export function serializeRange(text: string, record: EolRecord, from: number, to: number): string {
  const slice = text.slice(from, to);
  if (record.uniform !== null) {
    return record.uniform === "lf" ? slice : slice.replaceAll("\n", ENDING[record.uniform]);
  }
  const tags = record.tags ?? new Uint8Array();
  let k = countLF(text, 0, from);
  const lines = slice.split("\n");
  let out = lines[0] ?? "";
  for (let i = 1; i < lines.length; i++) {
    out += ENDING[EOLS[tags[k] ?? 0] ?? "lf"] + (lines[i] ?? "");
    k++;
  }
  return out;
}

function countLF(s: string, from: number, to: number): number {
  let n = 0;
  for (let i = s.indexOf("\n", from); i !== -1 && i < to; i = s.indexOf("\n", i + 1)) {
    n++;
  }
  return n;
}

/** One applied change, kept so a native undo or redo can restore its breaks' own tags. */
interface Change {
  readonly a: number;
  readonly removed: string;
  readonly inserted: string;
  readonly removedTags: Uint8Array;
  readonly insertedTags: Uint8Array;
}

/** What `beforeinput` saw, for the `input` that follows. */
interface Pending {
  readonly start: number;
  readonly end: number;
  readonly inputType: string;
}

/** The change between `old` and `next`: `[a, a + removed)` of `old` became `inserted`. */
interface Edit {
  readonly a: number;
  readonly removedLen: number;
  readonly inserted: string;
}

const LOG_ENTRIES = 1000;
const LOG_CHARS = 8 << 20;
/** How many logged changes one native undo step may span. */
const UNDO_SPAN = 64;

/** Shadows a textarea's edits to keep a mixed record exact. A uniform record needs no tracking:
 *  every break, old or typed, takes its one ending. */
export class EolTracker {
  private value: string;
  private uniform: Eol | null;
  private tags: Uint8Array;
  private pending: Pending | null = null;
  private log: Change[] = [];
  private redo: Change[] = [];
  private logChars = 0;
  /** The text and tags a drag removed, for the drop that re-inserts them. */
  private dragged: { text: string; tags: Uint8Array } | null = null;
  /** An edit could not be attributed exactly; the record may hold a guessed ending. */
  uncertain = false;

  constructor(text: string, record: EolRecord) {
    this.value = text;
    this.uniform = record.uniform;
    this.tags = record.tags ?? new Uint8Array();
  }

  /** The record for the current text. */
  snapshot(): EolRecord {
    return this.uniform !== null
      ? { uniform: this.uniform, tags: null }
      : { uniform: null, tags: this.tags.slice() };
  }

  /** Start over from a known text and record: an adoption, a discard or a save's baseline. */
  reset(text: string, record: EolRecord): void {
    this.value = text;
    this.uniform = record.uniform;
    this.tags = record.tags ?? new Uint8Array();
    this.pending = null;
    this.log = [];
    this.redo = [];
    this.logChars = 0;
    this.dragged = null;
    this.uncertain = false;
  }

  /** From `beforeinput`: the selection the edit will replace and its kind. */
  noteBeforeInput(start: number, end: number, inputType: string): void {
    this.pending = { start, end, inputType };
  }

  /** From `input`: the textarea's value after the edit. */
  noteInput(next: string): void {
    const pending = this.pending;
    this.pending = null;
    const old = this.value;
    this.value = next;
    if (this.uniform !== null || old === next) {
      return;
    }
    const type = pending?.inputType ?? "";
    if (type === "historyUndo" || type === "historyRedo") {
      if (!this.replay(old, next, type === "historyUndo")) {
        this.markUncertain(old, next);
      }
      return;
    }
    const exact = pending === null ? null : attribute(old, next, pending);
    const edit = exact ?? diffEdit(old, next);
    this.applyEdit(old, edit, exact !== null, type);
  }

  /** A programmatic write of the whole value. `moved` text re-arranges existing lines (a conflict
   *  resolution), so its breaks keep their own tags or the record turns uncertain; `new` text is
   *  the user's own new bytes, which take the neighbouring ending. */
  replace(next: string, kind: "moved" | "new"): void {
    const old = this.value;
    this.value = next;
    if (this.uniform !== null || old === next) {
      return;
    }
    if (kind === "new") {
      this.applyEdit(old, diffEdit(old, next), true, "insertText");
      return;
    }
    const tags = movedTags(old, next, this.tags);
    if (tags === null) {
      this.markUncertain(old, next);
      return;
    }
    this.tags = tags;
    this.log = [];
    this.redo = [];
    this.logChars = 0;
  }

  private applyEdit(old: string, edit: Edit, exact: boolean, inputType: string): void {
    const removed = old.slice(edit.a, edit.a + edit.removedLen);
    const insertedBreaks = countLF(edit.inserted, 0, edit.inserted.length);
    const bi = countLF(old, 0, edit.a);
    const removedBreaks = countLF(removed, 0, removed.length);
    if (inputType === "deleteByDrag") {
      this.dragged = { text: removed, tags: this.tags.slice(bi, bi + removedBreaks) };
    }
    if (!exact && removedBreaks > 0 && slideIsAmbiguous(old, edit, this.tags, bi)) {
      this.markUncertain(
        old,
        old.slice(0, edit.a) + edit.inserted + old.slice(edit.a + edit.removedLen),
      );
      return;
    }
    let insertedTags: Uint8Array;
    if (inputType === "insertFromDrop" && this.dragged?.text === edit.inserted) {
      insertedTags = this.dragged.tags;
    } else if (!exact && insertedBreaks > 0) {
      this.markUncertain(
        old,
        old.slice(0, edit.a) + edit.inserted + old.slice(edit.a + edit.removedLen),
      );
      return;
    } else {
      insertedTags = new Uint8Array(insertedBreaks).fill(this.neighbourTag(bi, removedBreaks));
    }
    if (inputType !== "deleteByDrag") {
      this.dragged = null;
    }
    this.commit(old, edit, insertedTags);
  }

  private commit(old: string, edit: Edit, insertedTags: Uint8Array): void {
    const removed = old.slice(edit.a, edit.a + edit.removedLen);
    const bi = countLF(old, 0, edit.a);
    const removedTags = this.tags.slice(bi, bi + countLF(removed, 0, removed.length));
    this.splice(bi, removedTags.length, insertedTags);
    this.redo = [];
    this.push({ a: edit.a, removed, inserted: edit.inserted, removedTags, insertedTags });
  }

  /** Pair a native undo or redo with the newest logged changes whose inverse (or replay) turns
   *  `old` into exactly `next`, and restore their tags. */
  private replay(old: string, next: string, undo: boolean): boolean {
    const stack = undo ? this.log : this.redo;
    let s = old;
    for (let k = 1; k <= Math.min(stack.length, UNDO_SPAN); k++) {
      const e = stack[stack.length - k];
      if (e === undefined) {
        return false;
      }
      s = undo
        ? s.slice(0, e.a) + e.removed + s.slice(e.a + e.inserted.length)
        : s.slice(0, e.a) + e.inserted + s.slice(e.a + e.removed.length);
      if (s !== next) {
        continue;
      }
      let t = old;
      for (let i = 0; i < k; i++) {
        const c = stack.pop();
        if (c === undefined) {
          return false;
        }
        const bi = countLF(t, 0, c.a);
        if (undo) {
          this.splice(bi, c.insertedTags.length, c.removedTags);
          t = t.slice(0, c.a) + c.removed + t.slice(c.a + c.inserted.length);
          this.logChars -= sizeOf(c);
          this.redo.push(c);
        } else {
          this.splice(bi, c.removedTags.length, c.insertedTags);
          t = t.slice(0, c.a) + c.inserted + t.slice(c.a + c.removed.length);
          this.log.push(c);
          this.logChars += sizeOf(c);
        }
      }
      this.trim();
      return true;
    }
    return false;
  }

  /** Keep the tag count true to the text, by the neighbour rule, and refuse to vouch for it. */
  private markUncertain(old: string, next: string): void {
    this.uncertain = true;
    const edit = diffEdit(old, next);
    const removed = old.slice(edit.a, edit.a + edit.removedLen);
    const bi = countLF(old, 0, edit.a);
    const removedBreaks = countLF(removed, 0, removed.length);
    const insertedBreaks = countLF(edit.inserted, 0, edit.inserted.length);
    this.splice(
      bi,
      removedBreaks,
      new Uint8Array(insertedBreaks).fill(this.neighbourTag(bi, removedBreaks)),
    );
    this.log = [];
    this.redo = [];
    this.logChars = 0;
  }

  /** The ending a newly typed break takes: the break before the edit, else the one after it,
   *  else the file's majority, LF on a tie. */
  private neighbourTag(bi: number, removedBreaks: number): number {
    const before = this.tags[bi - 1];
    if (before !== undefined) {
      return before;
    }
    const after = this.tags[bi + removedBreaks];
    if (after !== undefined) {
      return after;
    }
    const counts = [0, 0, 0];
    for (const t of this.tags) {
      counts[t] = (counts[t] ?? 0) + 1;
    }
    let best = 0;
    for (let t = 1; t < counts.length; t++) {
      if ((counts[t] ?? 0) > (counts[best] ?? 0)) {
        best = t;
      }
    }
    return best;
  }

  private splice(at: number, removeCount: number, insert: Uint8Array): void {
    const out = new Uint8Array(this.tags.length - removeCount + insert.length);
    out.set(this.tags.subarray(0, at), 0);
    out.set(insert, at);
    out.set(this.tags.subarray(at + removeCount), at + insert.length);
    this.tags = out;
  }

  private push(c: Change): void {
    this.log.push(c);
    this.logChars += sizeOf(c);
    this.trim();
  }

  /** Drop the oldest logged changes past the budget. `logChars` counts `log` alone, so a change
   *  an undo moved to `redo` stops counting until a redo moves it back. */
  private trim(): void {
    while (this.log.length > LOG_ENTRIES || this.logChars > LOG_CHARS) {
      const dropped = this.log.shift();
      if (dropped === undefined) {
        break;
      }
      this.logChars -= sizeOf(dropped);
    }
  }
}

function sizeOf(c: Change): number {
  return c.removed.length + c.inserted.length;
}

/** The tags for `next` when its lines are a subsequence of `old`'s, each keeping the ending its
 *  line had; null when they are not, or when matching from either end would pick a line with a
 *  different ending (two identical lines). */
function movedTags(old: string, next: string, tags: Uint8Array): Uint8Array | null {
  const oldLines = old.split("\n");
  const newLines = next.split("\n");
  const forward = matchLines(oldLines, newLines, tags, false);
  const backward = matchLines(oldLines, newLines, tags, true);
  if (forward === null || backward === null || forward.some((t, i) => t !== backward[i])) {
    return null;
  }
  return forward;
}

function matchLines(
  oldLines: readonly string[],
  newLines: readonly string[],
  tags: Uint8Array,
  fromEnd: boolean,
): Uint8Array | null {
  const out = new Uint8Array(newLines.length - 1);
  const n = newLines.length;
  let j = fromEnd ? oldLines.length - 1 : 0;
  for (let k = 0; k < n; k++) {
    const i = fromEnd ? n - 1 - k : k;
    while (j >= 0 && j < oldLines.length && oldLines[j] !== newLines[i]) {
      j += fromEnd ? -1 : 1;
    }
    if (j < 0 || j >= oldLines.length) {
      return null;
    }
    if (i < n - 1) {
      const tag = tags[j];
      if (tag === undefined) {
        return null;
      }
      out[i] = tag;
    }
    j += fromEnd ? -1 : 1;
  }
  return out;
}

/** The edit a `beforeinput` selection and kind imply, or null when the observed value
 *  contradicts it (a composition, autocorrect, a drop). */
function attribute(old: string, next: string, p: Pending): Edit | null {
  const delta = next.length - old.length;
  let a: number;
  let removedLen: number;
  if (p.start !== p.end) {
    a = p.start;
    removedLen = p.end - p.start;
  } else if (p.inputType.startsWith("delete") && p.inputType.endsWith("Backward")) {
    removedLen = -delta;
    a = p.start - removedLen;
  } else if (p.inputType.startsWith("delete") && p.inputType.endsWith("Forward")) {
    a = p.start;
    removedLen = -delta;
  } else if (
    p.inputType.startsWith("insert") &&
    p.inputType !== "insertFromDrop" &&
    p.inputType !== "insertCompositionText"
  ) {
    a = p.start;
    removedLen = 0;
  } else {
    return null;
  }
  const insertedLen = delta + removedLen;
  if (a < 0 || removedLen < 0 || insertedLen < 0 || a + removedLen > old.length) {
    return null;
  }
  if (
    old.slice(0, a) !== next.slice(0, a) ||
    old.slice(a + removedLen) !== next.slice(a + insertedLen)
  ) {
    return null;
  }
  return { a, removedLen, inserted: next.slice(a, a + insertedLen) };
}

/** The common-prefix, common-suffix edit between two values. */
function diffEdit(old: string, next: string): Edit {
  const max = Math.min(old.length, next.length);
  let p = 0;
  while (p < max && old.charCodeAt(p) === next.charCodeAt(p)) {
    p++;
  }
  let s = 0;
  while (
    s < max - p &&
    old.charCodeAt(old.length - 1 - s) === next.charCodeAt(next.length - 1 - s)
  ) {
    s++;
  }
  return { a: p, removedLen: old.length - s - p, inserted: next.slice(p, next.length - s) };
}

/** Whether a pure deletion found by the prefix/suffix diff could equally have removed breaks
 *  with other endings: the deleted run can slide left while the character before it equals its
 *  last character, so every break in the slide window must share one tag. */
function slideIsAmbiguous(old: string, edit: Edit, tags: Uint8Array, bi: number): boolean {
  if (edit.inserted !== "") {
    return false;
  }
  let a = edit.a;
  let b = edit.a + edit.removedLen;
  while (a > 0 && old.charCodeAt(a - 1) === old.charCodeAt(b - 1)) {
    a--;
    b--;
  }
  const from = bi - countLF(old, a, edit.a);
  const to = bi + countLF(old, edit.a, edit.a + edit.removedLen);
  const first = tags[from];
  for (let i = from; i < to; i++) {
    if (tags[i] !== first) {
      return true;
    }
  }
  return false;
}
