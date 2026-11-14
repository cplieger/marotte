// ---------------------------------------------------------------------------
// Editor: Conflict-mode rendering and per-hunk resolution.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { el } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { reconcile } from "./reconcile.js";
import { sigChanged } from "./paint-sig.js";
import {
  parseConflicts,
  resolveHunk,
  type ConflictFile,
  type ConflictHunk,
  type Resolution,
} from "./conflict.js";
import type { FileState } from "./editor-types.js";
import { enterView, repaint } from "./editor-types.js";
import { paintCommonControls, showEditSurface } from "./editor-ui.js";

export function renderConflictModeUI(state: FileState): void {
  if (state.mode.value.kind !== "conflict") {
    state.mode.value = {
      kind: "conflict",
      conflict: parseConflicts(state.current.value),
      editing: true,
    };
  }
  showEditSurface(state);
  $.editorEditBtn.classList.add("hidden");
  $.editorCancelBtn.classList.remove("hidden");
  $.editorSaveBtn.classList.remove("hidden");
  $.editorDiffBtn.classList.add("hidden");
  paintCommonControls(state);
}

/** One element of the overlay: the live status line or one hunk's control row. */
type OverlayEntry =
  | { readonly kind: "status"; readonly text: string }
  | { readonly kind: "hunk"; readonly index: number; readonly hunk: ConflictHunk };

function overlayEntries(conflict: ConflictFile): OverlayEntry[] {
  const n = conflict.hunks.length;
  return [
    { kind: "status", text: `${String(n)} unresolved conflict${n === 1 ? "" : "s"}` },
    ...conflict.hunks.map((hunk, index): OverlayEntry => ({ kind: "hunk", index, hunk })),
  ];
}

/** Repaint the per-hunk control overlay.
 *
 *  Keyed on a stable identity with the content repainted in `update`, never on the
 *  rendered state: every row is a set of real buttons, and the status line is an
 *  `aria-live` region a replacement is not reliably re-announced from. */
export function renderConflictOverlay(state: FileState): void {
  const overlay = $.editorConflictOverlay;
  if (
    state.error.value !== "" ||
    state.mode.value.kind !== "conflict" ||
    state.mode.value.conflict.hunks.length === 0
  ) {
    overlay.replaceChildren();
    overlay.classList.add("hidden");
    return;
  }
  const conflict = state.mode.value.conflict;
  overlay.classList.remove("hidden");
  reconcile(overlay, overlayEntries(conflict), {
    key: (e: OverlayEntry) => entryKey(e),
    mount: (e: OverlayEntry) => mountEntry(e, state),
    update: (node: HTMLElement, e: OverlayEntry) => {
      updateEntry(node, e, state);
    },
  });
}

function entryKey(e: OverlayEntry): string {
  switch (e.kind) {
    // STABLE across every count, so the live region survives and re-announces.
    case "status":
      return "status";
    // The hunk's POSITION IN THE FILE, which is what makes it the same hunk.
    case "hunk":
      return join("hunk", String(e.hunk.startLine));
  }
}

/** Repaint a kept element. Guarded per element, so a repaint that changes nothing
 *  touches nothing: the hunk rows are the ones that matter, since replacing a row's
 *  children is what takes `:hover` and the keyboard's place off its buttons. */
function updateEntry(node: HTMLElement, e: OverlayEntry, state: FileState): void {
  switch (e.kind) {
    case "status":
      node.textContent = e.text;
      return;
    case "hunk": {
      if (!sigChanged(node, hunkSignature(e))) {
        return;
      }
      const fresh = mountHunkRow(e, state);
      node.setAttribute("aria-label", fresh.getAttribute("aria-label") ?? "");
      node.replaceChildren(...Array.from(fresh.childNodes));
      return;
    }
  }
}

function mountEntry(e: OverlayEntry, state: FileState): HTMLElement {
  switch (e.kind) {
    case "status":
      return el(
        "div",
        {
          className: "conflict-status",
          // Live region: this repaints after each resolution, so the changed count
          // ("2 unresolved conflicts" → "1 …" → mode drops to edit) is announced to
          // screen readers.
          role: "status",
          "aria-live": "polite",
          "aria-atomic": "true",
        },
        e.text,
      );
    case "hunk": {
      const row = mountHunkRow(e, state);
      // Record it here, or the FIRST update repaints a row that has not moved.
      sigChanged(row, hunkSignature(e));
      return row;
    }
  }
}

/** The signature of everything a hunk row renders BELOW its identity. Read at mount
 *  and at every update from one place, so a first repaint cannot be spent rebuilding a
 *  row that has not moved. */
function hunkSignature(e: Extract<OverlayEntry, { kind: "hunk" }>): string[] {
  return [e.hunk.ourLabel, e.hunk.theirLabel];
}

function mountHunkRow(e: Extract<OverlayEntry, { kind: "hunk" }>, state: FileState): HTMLElement {
  const { hunk, index: i } = e;
  const lineNo = String(hunk.startLine + 1);
  const ours = hunk.ourLabel || "HEAD";
  const theirs = hunk.theirLabel || "incoming";
  // role=group + aria-label so a screen reader announces the per-hunk button
  // set with its line + side context (the visible title serves sighted users).
  const row = el("div", {
    className: "conflict-hunk-row",
    role: "group",
    "aria-label": `Conflict at line ${lineNo}: ${ours} vs ${theirs}`,
  });
  row.appendChild(
    el("span", { className: "conflict-hunk-title" }, `Line ${lineNo}: ${ours} vs ${theirs}`),
  );
  row.appendChild(
    resolveBtn(
      "Ours",
      () => {
        applyResolution(state, i, "ours");
      },
      `Accept ours (${ours}) for the conflict at line ${lineNo}`,
    ),
  );
  row.appendChild(
    resolveBtn(
      "Theirs",
      () => {
        applyResolution(state, i, "theirs");
      },
      `Accept theirs (${theirs}) for the conflict at line ${lineNo}`,
    ),
  );
  row.appendChild(
    resolveBtn(
      "Both",
      () => {
        applyResolution(state, i, "both");
      },
      `Keep both sides for the conflict at line ${lineNo}`,
    ),
  );
  return row;
}

function resolveBtn(label: string, onClick: () => void, ariaLabel?: string): HTMLElement {
  const b = el("button", { className: "btn-small conflict-btn" }, label);
  if (ariaLabel !== undefined) {
    b.setAttribute("aria-label", ariaLabel);
  }
  b.addEventListener("click", onClick);
  return b;
}

function applyResolution(state: FileState, hunkIndex: number, resolution: Resolution): void {
  if (state.mode.value.kind !== "conflict") {
    return;
  }
  const newContent = resolveHunk(state.mode.value.conflict, hunkIndex, resolution);
  writeBuffer(state, newContent);
  const parsed = parseConflicts(newContent);
  if (parsed.hunks.length === 0) {
    enterView(state, { kind: "text", editing: false });
    repaint(state);
    return;
  }
  state.mode.value = { kind: "conflict", conflict: parsed, editing: true };
  renderConflictOverlay(state);
}

/** A programmatic write to the edit buffer: the textarea, the line-ending record and `current`
 *  move together. */
function writeBuffer(state: FileState, text: string): void {
  state.eol?.tracker.replace(text);
  state.current.value = text;
  $.editorContent.value = text;
  showEditSurface(state);
}
