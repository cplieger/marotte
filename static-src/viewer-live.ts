// Live refresh: one machine per open file. It starts PAUSED with no button and checks the file
// every 5 s; the first change shows "Switch to live refresh", and live mode follows the file every
// second until paused. It is held while the buffer is dirty or the reader is composing or typing,
// so an adoption can never replace text under an IME composition or a keystroke. A file whose
// rules deny live refresh (over the cap) is never polled.

import { subscribe } from "@cplieger/reactive";
import { $ } from "./dom.js";
import { pollAction } from "./actions/index.js";
import { statFile, type StatAnswer } from "./actions/editor.js";
import {
  fileStates,
  getActiveFilePath,
  invalidate,
  isShown,
  issuedNow,
  repaint,
  type FileState,
  type LiveStatus,
} from "./editor-types.js";
import { getActiveTabId, tabIdFor } from "./tabs.js";
import { rulesFor } from "./viewer-rules.js";
import type { FileStat } from "./wire/types.gen.js";

const WATCH_MS = 5000;
const LIVE_MS = 1000;
/** An activation and the tab's refresh land together; one check answers both. */
const RESTART_DEBOUNCE_MS = 1000;

export const LIVE_SWITCH = "Switch to live refresh";
export const LIVE_PAUSE = "Pause live refresh";
export const LIVE_HELD = "Save or discard your changes to follow live updates";
export const GONE_SENTENCE = "This file is no longer on disk.";

/** The poller for one record; its status and generation live on the record. */
interface Machine {
  readonly path: string;
  readonly state: FileState;
  composing: boolean;
  typing: boolean;
  stop: (() => void) | null;
  startedAt: number;
  following: boolean;
  /** Disposes the effect that holds and releases the machine as the buffer turns dirty. */
  unwatch: () => void;
}

/** How the opener re-reads a file under the adoption guard. Injected by editor-openers, which
 *  owns adoption; `stillCurrent` says whether the answer may be adopted. */
export type LiveReload = (
  state: FileState,
  stat: FileStat,
  stillCurrent: () => boolean,
) => Promise<void>;

let reload: LiveReload | null = null;
const machines = new Map<string, Machine>();

export function setLiveReload(fn: LiveReload): void {
  reload = fn;
}

function machineFor(state: FileState): Machine {
  const existing = machines.get(state.path);
  if (existing !== undefined) {
    return existing;
  }
  const m: Machine = {
    path: state.path,
    state,
    composing: false,
    typing: false,
    stop: null,
    startedAt: 0,
    following: false,
    unwatch: () => undefined,
  };
  machines.set(state.path, m);
  state.live = "watching";
  m.unwatch = subscribe(state.dirty, () => {
    reconcileHold(m, state);
  });
  return m;
}

/** Whether the machine still serves its record. Closing the tab disposes the machine with the
 *  record it is bound to and a reopen makes new ones, so a delayed answer proves nothing by its
 *  path. */
function attached(m: Machine): boolean {
  return machines.get(m.path) === m;
}

function held(m: Machine, state: FileState): boolean {
  return state.dirty.value || m.composing || m.typing;
}

/** Whether the rules for the file as it stands allow live refresh at all. */
function allowed(state: FileState): boolean {
  const facts = state.facts.value;
  return facts !== null && rulesFor(facts, state.path, "read").live;
}

function shown(path: string): boolean {
  return (
    getActiveFilePath() === path &&
    getActiveTabId() !== "" &&
    getActiveTabId() === tabIdFor("editor", path)
  );
}

/** The file on screen moved away from what the answer reports. */
function differs(state: FileState, stat: FileStat): boolean {
  const facts = state.facts.value;
  if (facts === null) {
    return false;
  }
  if (stat.large !== (facts.kind === "large")) {
    return true;
  }
  return !stat.large && (stat.file_id ?? "") !== state.fileId;
}

function stopPolling(m: Machine): void {
  m.stop?.();
  m.stop = null;
}

function startPolling(m: Machine): void {
  stopPolling(m);
  const state = m.state;
  if ((state.live !== "watching" && state.live !== "live") || held(m, state) || !allowed(state)) {
    return;
  }
  m.startedAt = Date.now();
  m.stop = pollAction(
    statFile,
    { path: m.path, shown: () => shown(m.path) },
    {
      interval: state.live === "live" ? LIVE_MS : WATCH_MS,
      pauseWhenHidden: true,
      refreshOnFocus: false,
      onSuccess: (answer) => {
        onAnswer(m, answer);
      },
    },
  );
}

/** A poll's answer. A stopped poller delivers nothing (`pollAction`) and disposing the machine
 *  stops it, so an answer here is the attached machine's. */
function onAnswer(m: Machine, answer: StatAnswer): void {
  const state = m.state;
  if (answer.kind === "skipped" || answer.kind === "refused") {
    return;
  }
  if (answer.kind === "gone") {
    setStatus(m, "gone");
    return;
  }
  if (!differs(state, answer.stat)) {
    return;
  }
  if (state.live === "watching") {
    setStatus(m, "changed");
    return;
  }
  if (state.live === "live") {
    void follow(m, state, answer.stat);
  }
}

/** Re-read and adopt, unless a transition since `issued` was taken, a newer identity or a hold
 *  intervenes before the answer lands. */
async function follow(
  m: Machine,
  state: FileState,
  stat: FileStat,
  issued = issuedNow(state),
): Promise<void> {
  if (reload === null || m.following || held(m, state)) {
    return;
  }
  const fileId = state.fileId;
  m.following = true;
  try {
    await reload(state, stat, () => issued() && state.fileId === fileId && !held(m, state));
  } finally {
    m.following = false;
  }
  if (!allowed(state)) {
    stopPolling(m);
  }
}

/** Leaving `live` drops a re-read in flight, so nothing changes on screen after a Pause. The
 *  caller has checked the machine is attached. */
function setStatus(m: Machine, status: LiveStatus): void {
  const state = m.state;
  if (state.live === "live" && status !== "live") {
    invalidate(state);
  }
  state.live = status;
  if (status === "watching" || status === "live") {
    startPolling(m);
  } else {
    stopPolling(m);
  }
  repaint(state);
}

/** Activation, or the tab's refresh: one immediate check, then the machine's own cadence. */
export function liveActivate(path: string): void {
  for (const other of machines.values()) {
    if (other.path !== path) {
      // The textarea's holds are the file it shows; an IME that never ends its composition
      // must not strand the file left behind.
      other.composing = false;
      other.typing = false;
      stopPolling(other);
    }
  }
  const state = fileStates.get(path);
  if (state === undefined) {
    return;
  }
  const m = machineFor(state);
  if (m.stop !== null && Date.now() - m.startedAt < RESTART_DEBOUNCE_MS) {
    return;
  }
  startPolling(m);
  paintLiveButton(state);
}

/** A save wrote the file: one that had vanished is on disk again. */
export function liveSaved(path: string): void {
  const m = machines.get(path);
  if (m?.state.live === "gone") {
    setStatus(m, "watching");
  }
}

export function liveDispose(path: string): void {
  const m = machines.get(path);
  if (m !== undefined) {
    stopPolling(m);
    m.unwatch();
    machines.delete(path);
  }
}

/** A mutating `beforeinput` (`typing`) or an IME composition on `state`'s record: hold, and drop
 *  any answer in flight, before the edit can make the buffer dirty. The release frees this
 *  record's hold and nothing else: after a tab switch it still names the file that began, and
 *  once that tab has closed it does nothing, so it cannot clear a reopened record's hold. */
export function liveHold(state: FileState, reason: "typing" | "composing"): () => void {
  const m = machines.get(state.path);
  if (m?.state !== state) {
    return () => undefined;
  }
  invalidate(state);
  m[reason] = true;
  reconcileHold(m, state);
  return () => {
    if (attached(m)) {
      m[reason] = false;
      reconcileHold(m, state);
    }
  };
}

/** Poll exactly when the machine wants to and nothing holds it; a held `live` checks at once on
 *  the way back. A hold moves only the toggle, so it repaints only that. */
function reconcileHold(m: Machine, state: FileState): void {
  if (held(m, state)) {
    stopPolling(m);
  } else if (m.stop === null && (state.live === "watching" || state.live === "live")) {
    startPolling(m);
  }
  if (isShown(state)) {
    paintLiveButton(state);
  }
}

/** The click's check, then live. A close, a reopen or any transition since the click drops it,
 *  leaving the toggle as the reader now finds it. */
async function freshCheckThenLive(m: Machine): Promise<void> {
  const state = m.state;
  const gen = state.gen;
  const stillCurrent = (): boolean => attached(m) && state.gen === gen;
  const answer = await statFile.dispatch({ path: m.path, shown: () => true });
  if (!stillCurrent()) {
    return;
  }
  switch (answer?.kind) {
    case "gone":
      setStatus(m, "gone");
      return;
    case "stat":
      if (differs(state, answer.stat)) {
        await follow(m, state, answer.stat, stillCurrent);
        if (!stillCurrent()) {
          return;
        }
      }
      setStatus(m, "live");
      return;
    // Live mode's own check, every second, passes over these answers the same way.
    case "refused":
    case "skipped":
    case undefined:
      setStatus(m, "live");
      return;
  }
}

function onButton(): void {
  const path = getActiveFilePath();
  const state = fileStates.get(path);
  const m = machines.get(path);
  if (state === undefined || m === undefined || held(m, state)) {
    return;
  }
  if (state.live === "live") {
    setStatus(m, "paused");
    return;
  }
  if (state.live === "changed" || state.live === "paused") {
    void freshCheckThenLive(m);
  }
}

/** The toggle's one writer. Its accessible name stays "Live refresh"; `aria-pressed` carries the
 *  state and the visible label says what a click does. */
export function paintLiveButton(state: FileState): void {
  const btn = $.editorLiveBtn;
  const m = machines.get(state.path);
  const facts = state.facts.value;
  const visible =
    m !== undefined &&
    facts !== null &&
    facts.kind !== "large" &&
    (state.live === "changed" || state.live === "live" || state.live === "paused");
  btn.classList.toggle("hidden", !visible);
  if (m === undefined || !visible) {
    $.editorLiveReason.classList.add("hidden");
    return;
  }
  const isLive = state.live === "live";
  btn.textContent = isLive ? LIVE_PAUSE : LIVE_SWITCH;
  btn.setAttribute("aria-pressed", isLive ? "true" : "false");
  const isHeld = held(m, state);
  btn.disabled = isHeld;
  // The reason exists in the accessibility tree only while it describes a held, shown toggle.
  $.editorLiveReason.classList.toggle("hidden", !isHeld);
  if (isHeld) {
    btn.setAttribute("data-tooltip", LIVE_HELD);
    btn.setAttribute("aria-describedby", "editor-live-reason");
  } else {
    btn.removeAttribute("data-tooltip");
    btn.removeAttribute("aria-describedby");
  }
}

let wired = false;

export function initLive(): void {
  if (wired) {
    return;
  }
  wired = true;
  $.editorLiveBtn.addEventListener("click", onButton);
}

/** @internal Test seam. */
// deadset:ignore DS1004 -- test seam: resets the live-view machines and their polls
export function _resetLiveForTest(): void {
  for (const m of machines.values()) {
    stopPolling(m);
    m.unwatch();
  }
  machines.clear();
}
