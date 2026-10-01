// ---------------------------------------------------------------------------
// The spec tab's page: one Kiro spec's documents, tasks.md as a live checklist.
//
// The run view's shape. Page state is PER OPEN REF (the tab's spec directory),
// released when the ref leaves the open set, and it holds render and request
// lifecycle only: the selected document, the collapse set, the poll timer, the
// in-flight controller, the validator and the ids this page dispatched. The
// target chat is never in it. It is the tab's PARENT, read from the tab store at
// click time, so a re-parent through the picker changes what Run does with no
// page state to migrate.
//
// tasks.md is the only source of truth: the tree is what the server parsed with
// Kiro's own grammar, and this module writes nothing back. Running a task is an
// ordinary prompt into the parent chat through `sendPromptTo`.
// ---------------------------------------------------------------------------

import { effect, el, signal, touch } from "@cplieger/reactive";
import { createDisclosure } from "@cplieger/ui-primitives/disclosure";
import { apiGetConditional } from "./api-client.js";
import { BUS_USER_INPUT_ANSWERED, onBus, onSSE } from "./bus.js";
import { sendPromptTo, type SendPromptResult } from "./chat-commands.js";
import { CHROME_ATTR } from "./chrome-attr.js";
import { mountChatDecisionDock, rerenderDocks, unmountDecisionDock } from "./decision-dock.js";
import { approveSpecPhase } from "./actions/spec.js";
import { renderMarkdownDoc } from "./editor-markdown.js";
import { openFile } from "./editor-openers.js";
import { paintStatus } from "./fundamentals/work-status.js";
import { renderMarkdownInto } from "./markdown.js";
import { paintIfChanged, sigChanged } from "./paint-sig.js";
import { initSegmentedBar } from "./segmented-bar.js";
import { editorDocSkeleton } from "./skeleton.js";
import { get, isThinking, watchSession } from "./store.js";
import { openChatRefs, openSpecRefs, parentChatRef, setTabParent, tabIdFor } from "./tabs.js";
import { swapViews } from "./view-swap.js";
import { decodeSpec } from "./wire/decoders.gen.js";
import type { Spec, SpecDoc, SpecDocRole, SpecTaskNode } from "./wire/types.gen.js";
import { absPath } from "./workspace.js";

/** Tree depth, read by css/32-spec.css to indent a row, its note and its detail. */
const DEPTH_PROP = "--depth";

/** The server's prompt cap: `maxPromptBytes` in internal/command/validate.go
 *  (512 KiB, enforced over the text's bytes). A deliberate duplicate, because
 *  the constant is unexported and this check only decides whether Run is
 *  offered; the server still answers 413 either way. */
export const MAX_PROMPT_BYTES = 512 * 1024;

/** The poll cadence while the file was moving or this page just dispatched. */
export const POLL_FAST_MS = 2500;
/** The resting poll cadence. */
export const POLL_SLOW_MS = 15000;
/** How long a dispatch or a tasks hash change keeps the fast cadence. */
export const FAST_WINDOW_MS = 20000;

/** Which tasks a Run all covers. KAS's after-tasks.md checkpoint offers both. */
export type RunScope = "required" | "all";

/** The two answers of KAS's after-tasks.md phase checkpoint that the CLIENT has
 *  to carry out, verbatim from the spec-mode prompt (read on the pinned 2.21.4
 *  bundle). That prompt tells the agent "the client carries these out … Then end
 *  the turn", so a page that does not dispatch leaves the reader with a button
 *  that ended the turn and ran nothing. The third answer ("Not now") and
 *  anything the reader typed instead are not ours and fall through untouched.
 *
 *  A Map rather than an object literal because the key is text off the wire:
 *  `Map.get` has no prototype chain, where `table[answer]` answers
 *  `Object.prototype`'s member for `constructor` and friends. */
const CHECKPOINT_ANSWERS = new Map<string, RunScope>([
  ["Run required tasks", "required"],
  ["Run required and optional tasks", "all"],
]);

/** The three documents every spec is expected to have, in Kiro's order. */
const EXPECTED: readonly { role: Exclude<SpecDocRole, "other">; file: string; label: string }[] = [
  { role: "requirements", file: "requirements.md", label: "Requirements" },
  { role: "design", file: "design.md", label: "Design" },
  { role: "tasks", file: "tasks.md", label: "Tasks" },
];

interface SpecPageState {
  /** The selected segment's file, or null until a reply picks one. */
  doc: string | null;
  /** Collapsed task ids, kept across repaints and across a re-parent. */
  readonly collapse: Set<string>;
  pollTimer: ReturnType<typeof setTimeout> | null;
  /** Until when the poll runs at the fast cadence. */
  fastUntil: number;
  controller: AbortController | null;
  inflight: Promise<void> | null;
  trailing: boolean;
  trailingWaiters: (() => void)[];
  generation: number;
  etag: string;
  lastTasksHash: string;
  /** Task ids this page dispatched whose row pulses until the file moves or
   *  the target's turn ends. */
  readonly dispatched: Set<string>;
  /** Rows (or "all") whose dispatch answered queued or starting. */
  readonly busy: Set<string>;
  /** Task ids this page dispatched whose box the target's turn ended without
   *  moving, so the row says so. */
  readonly unmoved: Set<string>;
  /** Whether a dispatch from this page is in flight in the target chat. */
  dispatching: boolean;
  /** Phases whose approval is in flight, so the control says so and a second
   *  click cannot dispatch a second command. */
  readonly approving: Set<string>;
  /** A Run all this page owes the reader because they answered KAS's phase
   *  checkpoint with one of its two Run options, fired at the target chat's next
   *  turn end (`carryCheckpointAnswer`). Undefined when none. */
  armedRunAll: RunScope | undefined;
  lastThinking: boolean;
  failure: string;
  spec: Spec | null;
  /** The directory answered 404. */
  gone: boolean;
  page: PageEls | null;
}

interface PageEls {
  readonly root: HTMLElement;
  readonly head: HTMLElement;
  readonly barHost: HTMLElement;
  readonly pane: HTMLElement;
  /** This page's interaction dock: the fourth host, one per open spec, showing
   *  the TARGET CHAT's queue so a phase checkpoint raised while the reader is on
   *  this tab is answerable beside the document it is about. */
  readonly dockHost: HTMLElement;
  /** The segment list the bar was built from, so a changed list rebuilds it. */
  barKey: string;
  paintBar: ((active: string) => void) | null;
}

const states = new Map<string, SpecPageState>();

/** The ref on screen, or "". */
let shown = "";

/** Bumped whenever `shown` moves, and TOUCHED at the top of the one effect below
 *  before its early returns, so pointing the page at a ref re-runs that effect.
 *  It has to: the effect subscribes to the TARGET CHAT's session, which is a
 *  different signal per ref and none at all while nothing is shown, so without a
 *  re-run an activation that followed a pass with no page on screen left the page
 *  subscribed to nothing and the target's turn end reached nobody. `run-view.ts`'s
 *  `touch(stepTranscriptVersion)` is this same discipline, and its reason is the
 *  same one: read the deps BEFORE the returns.
 *
 *  A version rather than a signal HOLDING the ref, because `release` clears
 *  `shown` from inside that effect: a signal written mid-run re-enters the effect,
 *  and a re-entrant pass leaves `@cplieger/reactive` 2.1.1 rolling a source's
 *  scratch pointer back onto the node it is already on, after which the next pass
 *  that drops that source can never resubscribe to it (measured: the page went
 *  permanently deaf to the target chat's session). */
const shownVersion = signal(0);

function stateFor(ref: string): SpecPageState {
  let st = states.get(ref);
  if (st === undefined) {
    st = {
      doc: null,
      collapse: new Set(),
      pollTimer: null,
      fastUntil: 0,
      controller: null,
      inflight: null,
      trailing: false,
      trailingWaiters: [],
      generation: 0,
      etag: "",
      lastTasksHash: "",
      dispatched: new Set(),
      busy: new Set(),
      unmoved: new Set(),
      dispatching: false,
      approving: new Set(),
      armedRunAll: undefined,
      lastThinking: false,
      failure: "",
      spec: null,
      gone: false,
      page: null,
    };
    states.set(ref, st);
  }
  return st;
}

function release(ref: string): void {
  const st = states.get(ref);
  if (st === undefined) {
    return;
  }
  st.controller?.abort();
  st.controller = null;
  if (st.pollTimer !== null) {
    clearTimeout(st.pollTimer);
  }
  if (st.page !== null) {
    // The dock host is this page's, so it goes with the page: a host left
    // registered keeps rendering into a detached element for the tab's life.
    unmountDecisionDock(st.page.dockHost);
  }
  st.page?.root.remove();
  states.delete(ref);
  if (shown === ref) {
    shown = "";
  }
}

// --- Public: the tab factory's two halves ---

/** Point the page at one spec directory. A spec tab's `onShow`. */
export function showSpec(dir: string): void {
  const st = stateFor(dir);
  installOnce();
  const body = document.getElementById("spec-body");
  if (body !== null) {
    const page = pageFor(dir, st);
    if (page.root.parentElement !== body) {
      body.replaceChildren(page.root);
    }
  }
  // EXPLICITLY, and this is the whole reason the dock needs a nudge here:
  // `addHost`'s effect touches `activeSession` and `queueVersion` only, so a host
  // whose own match closure has changed answer — this page's target chat, read
  // through the tab store — repaints for neither. The run view makes the same
  // call for the same reason.
  rerenderDocks();
  // AFTER the state exists and the effect is installed: the bump re-runs that
  // effect against this ref, which seeds `lastThinking` from the target chat, so a
  // page opened over a chat that is already working repaints when that turn ends.
  shown = dir;
  shownVersion.value++;
  paint(dir);
  void refetch(dir);
}

/** Refetch one spec. A spec tab's `refresh`. */
export function refreshSpec(dir: string): void {
  if (states.has(dir)) {
    void refetch(dir);
  }
}

// --- The one effect and the one SSE subscription ---

let installed = false;
function installOnce(): void {
  if (installed) {
    return;
  }
  installed = true;
  onSSE("spec_changed", (_chatID, p) => {
    if (states.has(p.dir)) {
      void refetch(p.dir);
    }
  });
  // Pure invalidation, exactly like `spec_changed`: the frame names the spec and
  // nothing else, so the record's new state — and the `stale` the server derives
  // against the live document — arrives by re-reading rather than by patching a
  // badge from an event. Workspace-global, so another device's approval lands
  // here too.
  onSSE("spec_approved", (_chatID, p) => {
    if (states.has(p.dir)) {
      void refetch(p.dir);
    }
  });
  onBus(BUS_USER_INPUT_ANSWERED, ({ chatID, answer }) => {
    carryCheckpointAnswer(chatID, answer);
  });
  effect(() => {
    // Before the returns below, so a pass with no page on screen keeps this
    // subscription and `showSpec` can re-point the effect at another ref.
    touch(shownVersion);
    const open = openSpecRefs();
    for (const ref of [...states.keys()]) {
      if (!open.includes(ref)) {
        release(ref);
      }
    }
    const ref = shown;
    if (ref === "") {
      return;
    }
    const chat = targetChat(ref);
    const thinking = chat === "" ? false : (watchSession(chat)?.thinking ?? false);
    onThinking(ref, thinking);
  });
}

/** The target chat's thinking edge. The FALLING edge after a dispatch from this
 *  page is a refetch trigger (the agent's write landed); either edge repaints,
 *  because Run's disabled state reads it. */
function onThinking(ref: string, thinking: boolean): void {
  const st = states.get(ref);
  if (st === undefined || st.lastThinking === thinking) {
    return;
  }
  st.lastThinking = thinking;
  if (!thinking && (st.dispatching || st.dispatched.size > 0 || st.busy.size > 0)) {
    const open = [...st.dispatched];
    st.dispatching = false;
    st.dispatched.clear();
    st.busy.clear();
    void refetch(ref).then(() => {
      noteUnmoved(ref, open);
    });
  }
  if (!thinking && st.armedRunAll !== undefined) {
    void fireArmedRunAll(ref);
  }
  paint(ref);
}

/** Mark every task whose box the turn ended without moving, so a turn that gave
 *  up says so on the row instead of the pulse simply stopping.
 *
 *  Read against the fetch that FOLLOWS the turn end rather than against the set
 *  at the falling edge, because the falling edge itself paints: a write landing
 *  in the same instant as the turn's close leaves its id in `dispatched` until
 *  that fetch adopts it, so marking from the edge paints a note the next fetch
 *  retracts. `adopt` is the second half — it retires a mark whose task has since
 *  moved, which is what covers a box ticked by hand later. */
function noteUnmoved(ref: string, ids: readonly string[]): void {
  const st = states.get(ref);
  if (!st?.spec || ids.length === 0) {
    return;
  }
  const tasks = tasksDoc(st.spec)?.tasks ?? [];
  let marked = false;
  for (const id of ids) {
    const node = findNode(tasks, id);
    // "all" is not a task id, so a Run all that produced nothing marks no row:
    // the header's own failure line is where that belongs.
    if (node?.status === "pending") {
      st.unmoved.add(id);
      marked = true;
    }
  }
  if (marked) {
    paint(ref);
  }
}

// --- Phase checkpoints ---

/** Carry out an answer to KAS's spec phase checkpoint, if it is one of ours.
 *
 *  Exported for the test, which drives it rather than the dock: the dock is one
 *  bus emit away and mocking a card's click to reach two lines of this module
 *  would test the dock. */
export function carryCheckpointAnswer(chatID: string, answer: string): void {
  const scope = CHECKPOINT_ANSWERS.get(answer);
  if (scope === undefined) {
    return;
  }
  const ref = checkpointTarget(chatID);
  if (ref === "") {
    return;
  }
  armRunAll(ref, scope);
}

/** Which open spec the checkpoint answered in `chatID` is about.
 *
 *  The ask names no spec — `_kiro/userInput` carries a question and options and
 *  nothing else — so the PARENT link is the whole join, exactly as it is for the
 *  Run buttons. One spec tab under that chat is the answer; where several are,
 *  the page ON SCREEN is, because that is the document the reader was reading
 *  when they answered. Where neither resolves, nothing is dispatched: running the
 *  wrong spec's tasks is worse than running none. */
function checkpointTarget(chatID: string): string {
  const candidates = [...states.keys()].filter((ref) => targetChat(ref) === chatID);
  if (candidates.length === 1) {
    return candidates[0] ?? "";
  }
  return candidates.includes(shown) ? shown : "";
}

/** Owe `ref` a Run all, and fire it at the moment the target can take a prompt.
 *
 *  Not immediately: the agent answers a Run checkpoint by saying the spec is
 *  ready and ENDING the turn, so the turn is still open at the click and a prompt
 *  sent now takes the server's 409, which `sendPromptTo` reports as `queued` and
 *  which dispatches nothing at all. So the run is armed and fired at the target's
 *  own turn end (`onThinking`), and at once when the target is already idle. */
function armRunAll(ref: string, scope: RunScope): void {
  const st = states.get(ref);
  if (st === undefined) {
    return;
  }
  st.armedRunAll = scope;
  if (!isThinking(targetChat(ref))) {
    void fireArmedRunAll(ref);
  }
}

/** Spend `ref`'s armed Run all. A no-op when nothing is armed. */
async function fireArmedRunAll(ref: string): Promise<void> {
  const st = states.get(ref);
  const scope = st?.armedRunAll;
  if (st === undefined || scope === undefined) {
    return;
  }
  st.armedRunAll = undefined;
  if (st.spec === null) {
    // The prompt names the spec, so a page whose first fetch has not landed
    // fetches before it composes one rather than dropping the answer.
    await refetch(ref);
  }
  await runAll(ref, scope);
}

// --- Target chat ---

/** The chat a Run goes to: the tab's parent chat, read now. "" when parentless. */
export function targetChat(ref: string): string {
  const id = tabIdFor("spec", ref);
  return id === "" ? "" : parentChatRef(id);
}

// --- Fetch ---

function specPath(ref: string): string {
  return `/api/specs/${encodeURIComponent(ref)}`;
}

/** Fetch `ref` once, with one in-flight request and one trailing refetch
 *  remembered. Resolves when a fetch issued at or after this call has landed. */
export function refetch(ref: string): Promise<void> {
  const st = states.get(ref);
  if (st === undefined) {
    return Promise.resolve();
  }
  if (st.inflight !== null) {
    st.trailing = true;
    return new Promise((resolve) => {
      st.trailingWaiters.push(resolve);
    });
  }
  const run = doFetch(ref, st).finally(() => {
    st.inflight = null;
    if (st.trailing) {
      st.trailing = false;
      const waiters = st.trailingWaiters;
      st.trailingWaiters = [];
      void refetch(ref).then(() => {
        for (const w of waiters) {
          w();
        }
      });
    }
  });
  st.inflight = run;
  return run;
}

async function doFetch(ref: string, st: SpecPageState): Promise<void> {
  const ctl = new AbortController();
  st.controller = ctl;
  const gen = ++st.generation;
  const r = await apiGetConditional(specPath(ref), st.etag, decodeSpec, ctl.signal);
  if (st.generation !== gen || !states.has(ref)) {
    return;
  }
  st.controller = null;
  if (r.status === 304) {
    // The last reply stands.
  } else if (r.status === 404) {
    st.gone = true;
    st.spec = null;
    st.etag = "";
  } else if (r.data !== null) {
    adopt(st, r.data, r.etag);
  }
  paint(ref);
  armPoll(ref);
}

function adopt(st: SpecPageState, spec: Spec, etag: string): void {
  st.gone = false;
  st.spec = spec;
  st.etag = etag;
  const tasks = tasksDoc(spec);
  const hash = tasks?.hash ?? "";
  if (hash !== st.lastTasksHash) {
    if (st.lastTasksHash !== "") {
      st.fastUntil = Date.now() + FAST_WINDOW_MS;
    }
    st.lastTasksHash = hash;
  }
  // A task that moved (or left the file) is neither still in flight nor still
  // unmoved, so one pass retires both marks.
  for (const set of [st.dispatched, st.unmoved]) {
    for (const id of [...set]) {
      const node = tasks === undefined ? undefined : findNode(tasks.tasks ?? [], id);
      if (node?.status !== "pending") {
        set.delete(id);
      }
    }
  }
  const files = segmentsFor(spec).map((s) => s.file);
  if (st.doc === null || !files.includes(st.doc)) {
    st.doc = defaultDoc(spec);
  }
}

/** Which document opens first: the phase's own, else the first listed. */
function defaultDoc(spec: Spec): string | null {
  const phase = phaseOf(spec);
  const doc = phase === null ? undefined : spec.docs.find((d) => d.role === phase.role);
  return doc?.file ?? segmentsFor(spec)[0]?.file ?? null;
}

// --- Poll ---

function armPoll(ref: string): void {
  const st = states.get(ref);
  if (st === undefined) {
    return;
  }
  if (st.pollTimer !== null) {
    clearTimeout(st.pollTimer);
  }
  st.pollTimer = setTimeout(() => {
    st.pollTimer = null;
    if (!states.has(ref)) {
      return;
    }
    if (pageVisible(ref)) {
      void refetch(ref);
    } else {
      armPoll(ref);
    }
  }, pollInterval(st));
}

/** The next poll's delay: fast inside the window after a dispatch or a tasks
 *  hash change, slow otherwise. Exported for the cadence test. */
export function pollInterval(st: { readonly fastUntil: number }, now = Date.now()): number {
  return now < st.fastUntil ? POLL_FAST_MS : POLL_SLOW_MS;
}

function pageVisible(ref: string): boolean {
  if (shown !== ref) {
    return false;
  }
  const view = document.getElementById("spec-view");
  return view !== null && view.offsetParent !== null;
}

// --- Derivations ---

export function tasksDoc(spec: Spec): SpecDoc | undefined {
  return spec.docs.find((d) => d.role === "tasks");
}

export function findNode(nodes: readonly SpecTaskNode[], id: string): SpecTaskNode | undefined {
  for (const n of nodes) {
    if (n.id === id) {
      return n;
    }
    const inner = findNode(n.children, id);
    if (inner !== undefined) {
      return inner;
    }
  }
  return undefined;
}

/** The phase pill's word, from the known roles only. Null when no expected
 *  document exists (an `other` document never names a phase). */
export function phaseOf(spec: Spec): { role: Exclude<SpecDocRole, "other">; label: string } | null {
  for (const e of [...EXPECTED].reverse()) {
    if (spec.docs.some((d) => d.role === e.role)) {
      return { role: e.role, label: e.label };
    }
  }
  return null;
}

interface Segment {
  /** The segment's id: the document's file, or the expected file when missing. */
  readonly file: string;
  readonly label: string;
  readonly doc: SpecDoc | undefined;
  readonly role: SpecDocRole;
}

/** One segment per expected role (present or missing) then every `other`
 *  document, in the reply's order. */
export function segmentsFor(spec: Spec): Segment[] {
  const out: Segment[] = [];
  for (const e of EXPECTED) {
    const doc = spec.docs.find((d) => d.role === e.role);
    out.push({ file: doc?.file ?? e.file, label: e.label, doc, role: e.role });
  }
  for (const d of spec.docs) {
    if (d.role === "other") {
      out.push({ file: d.file, label: d.file.replace(/\.md$/, ""), doc: d, role: "other" });
    }
  }
  return out;
}

/** The unreadable-lines line, or "" when the count is zero. */
export function unreadableLine(n: number): string {
  if (n <= 0) {
    return "";
  }
  return n === 1
    ? "1 line looks like a task but Kiro cannot read it"
    : `${String(n)} lines look like tasks but Kiro cannot read them`;
}

export function progressText(p: {
  readonly completed: number;
  readonly total: number;
  readonly in_progress: number;
  readonly queued: number;
}): string {
  return `${String(p.completed)} of ${String(p.total)} done, ${String(p.in_progress)} in progress, ${String(p.queued)} queued`;
}

// --- Prompts ---

const TASK_TRAILER =
  "Before implementing, read requirements.md (or bugfix.md) and design.md in that directory.\n" +
  "Work only on this task. Mark its checkbox [x] in tasks.md when it is genuinely done, run\n" +
  "the relevant build or tests to verify, then end the turn with a summary. Do not continue\n" +
  "to other tasks.";

const INTERRUPTED_SENTENCE =
  "Its box is `[-]` from an interrupted run, so treat the work as not started and redo it.";

/** The single-task prompt. */
export function taskPrompt(spec: { name: string; dir: string }, node: SpecTaskNode): string {
  const stem =
    node.number === ""
      ? `Execute the following task from spec '${spec.name}' (${spec.dir}/tasks.md):`
      : `Execute task ${node.number} from spec '${spec.name}' (${spec.dir}/tasks.md):`;
  const body = node.detail === "" ? node.text : `${node.text}\n${node.detail}`;
  const parts = [stem, body, TASK_TRAILER];
  if (node.status === "in_progress") {
    parts.push(INTERRUPTED_SENTENCE);
  }
  return parts.join("\n\n");
}

/** The Run all prompt. Both scopes open on the same stem, which is what KAS's
 *  local classifier matches (`/\brun\s+all\s+tasks?\b/`).
 *
 *  `all` is the checkpoint's second Run answer: the not-marked-optional clause is
 *  dropped, and the finish line drops "required" with it — left standing it would
 *  tell the agent to include the optional tasks and then to stop once the required
 *  ones were checked. */
export function runAllPrompt(
  spec: { name: string; dir: string },
  scope: RunScope = "required",
): string {
  const stem = `Run all tasks in spec '${spec.name}' (${spec.dir}/tasks.md). Before implementing, read requirements.md\n`;
  if (scope === "all") {
    return (
      stem +
      "(or bugfix.md) and design.md. Work through every unchecked task, the ones marked optional\n" +
      "(a `*` after the checkbox) included, respecting the ## Task Dependency Graph section if the\n" +
      "file has one. After each task mark its checkbox [x], run the relevant build or tests to\n" +
      "verify, then continue. Finish when every task is checked or you hit a blocker that needs me,\n" +
      "and end the turn with a summary of what was done and what remains."
    );
  }
  return (
    stem +
    "(or bugfix.md) and design.md. Work through every unchecked task that is not marked optional\n" +
    "(a `*` after the checkbox), respecting the ## Task Dependency Graph section if the file has\n" +
    "one. After each task mark its checkbox [x], run the relevant build or tests to verify, then\n" +
    "continue. Finish when every required task is checked or you hit a blocker that needs me,\n" +
    "and end the turn with a summary of what was done and what remains."
  );
}

export function promptFits(prompt: string): boolean {
  return new TextEncoder().encode(prompt).byteLength <= MAX_PROMPT_BYTES;
}

// --- Eligibility ---

export type Eligibility =
  | { readonly ok: true; readonly node: SpecTaskNode }
  | { readonly ok: false; readonly reason: string };

/** Whether `id` may run now, against the tree of a FRESH fetch. `hash` is the
 *  hash the row was rendered with; `targetThinking` is the target chat's state. */
export function eligibility(
  tasks: readonly SpecTaskNode[],
  id: string,
  hash: string,
  targetThinking: boolean,
): Eligibility {
  const node = findNode(tasks, id);
  if (node?.hash !== hash || node.children.length > 0) {
    return { ok: false, reason: "the list changed, pick again" };
  }
  if (node.status === "completed") {
    return { ok: false, reason: "already checked off" };
  }
  if (node.status === "in_progress" && targetThinking) {
    return { ok: false, reason: "the target chat is still working" };
  }
  return { ok: true, node };
}

/** Why a row's Run is withheld at render time, or "" when it is offered. */
function runBlocker(
  spec: Spec,
  node: SpecTaskNode,
  chat: string,
  thinking: boolean,
  truncated: boolean,
): string {
  if (chat === "") {
    return "Pick a chat to run in first";
  }
  if (thinking) {
    return "The chat is still working";
  }
  if (truncated) {
    return "The task list was cut at the node cap, so this list is not the whole file";
  }
  if (!promptFits(taskPrompt(spec, node))) {
    return "This task's text is too large to send as one prompt";
  }
  return "";
}

// --- Dispatch ---

async function runTask(ref: string, id: string, hash: string): Promise<void> {
  const st = states.get(ref);
  if (st === undefined) {
    return;
  }
  const chat = targetChat(ref);
  if (chat === "") {
    st.failure = "Pick a chat to run in first";
    paint(ref);
    return;
  }
  await refetch(ref);
  if (!states.has(ref) || st.spec === null) {
    return;
  }
  const tasks = tasksDoc(st.spec)?.tasks ?? [];
  const verdict = eligibility(tasks, id, hash, isThinking(chat));
  if (!verdict.ok) {
    st.failure = verdict.reason;
    paint(ref);
    return;
  }
  const prompt = taskPrompt(st.spec, verdict.node);
  if (!promptFits(prompt)) {
    st.failure = "This task's text is too large to send as one prompt";
    paint(ref);
    return;
  }
  applyResult(ref, id, await sendPromptTo(chat, prompt));
}

async function runAll(ref: string, scope: RunScope = "required"): Promise<void> {
  const st = states.get(ref);
  if (!st?.spec) {
    return;
  }
  const chat = targetChat(ref);
  if (chat === "") {
    st.failure = "Pick a chat to run in first";
    paint(ref);
    return;
  }
  const prompt = runAllPrompt(st.spec, scope);
  applyResult(ref, "all", await sendPromptTo(chat, prompt));
}

/** Fold a send result into the page. Exported for the mapping test. */
export function applyResult(ref: string, id: string, result: SendPromptResult): void {
  const st = states.get(ref);
  if (st === undefined) {
    return;
  }
  st.failure = "";
  switch (result) {
    case "sent":
      st.unmoved.delete(id);
      st.dispatched.add(id);
      st.dispatching = true;
      st.fastUntil = Date.now() + FAST_WINDOW_MS;
      armPoll(ref);
      break;
    case "queued":
    case "starting":
      st.busy.add(id);
      break;
    case "gone":
      st.gone = true;
      st.spec = null;
      break;
    case "failed":
      st.failure = "The prompt could not be sent";
      break;
  }
  paint(ref);
}

// --- Approvals ---

/** What the approval control on one phase segment says, decided once so the
 *  words and the button's presence are testable without a DOM.
 *
 *  It RECORDS, it does not ENFORCE: no state here disables Run, withholds a
 *  document or gates a phase behind an earlier one. What it states is which
 *  version was signed off and whether the file has moved since. */
export interface ApprovalView {
  /** The badge's words, or "" when there is nothing to state. */
  readonly badge: string;
  /** The badge's tooltip: when it was recorded, or "". */
  readonly detail: string;
  /** Whether an Approve control is offered. */
  readonly offer: boolean;
  /** The offered control's words. */
  readonly label: string;
  /** Whether that control is in flight, so it reads as busy and cannot fire
   *  twice. */
  readonly busy: boolean;
}

const NO_APPROVAL: ApprovalView = { badge: "", detail: "", offer: false, label: "", busy: false };

/** Resolve one segment's approval state.
 *
 *  A missing document and an `other` document are both unapprovable, for the
 *  reason `specapproval.Phases()` gives: `other` is the residual bucket, and one
 *  spec can hold several, so a per-phase key could not name which was approved.
 *  A document over the read cap carries an EMPTY hash, so there is no version to
 *  swap against and no control is offered — its badge still states an approval
 *  recorded when the file was smaller, because the record has not stopped being
 *  true. */
export function approvalView(
  seg: { readonly role: SpecDocRole; readonly doc: SpecDoc | undefined },
  spec: Spec,
  approving: ReadonlySet<string>,
): ApprovalView {
  if (seg.role === "other" || seg.doc === undefined) {
    return NO_APPROVAL;
  }
  const busy = approving.has(seg.role);
  // A key off the wire, so `Object.hasOwn` is the membership question: a bare
  // read of `approvals["constructor"]` answers `Object.prototype`'s member
  // (typescript.md "A record indexed by untrusted text").
  const record = spec.approvals;
  const a = record !== undefined && Object.hasOwn(record, seg.role) ? record[seg.role] : undefined;
  const approvable = seg.doc.hash !== "";
  if (a === undefined) {
    return approvable
      ? { badge: "", detail: "", offer: true, label: busy ? "Approving\u2026" : "Approve", busy }
      : NO_APPROVAL;
  }
  const detail = approvedAt(a.at);
  if (!a.stale) {
    return { badge: "Approved", detail, offer: false, label: "", busy: false };
  }
  return {
    badge: "Changed since you approved it",
    detail,
    offer: approvable,
    label: busy ? "Approving\u2026" : "Approve again",
    busy,
  };
}

/** The pane signature's approval terms, DERIVED from the view rather than listed
 *  beside it: a field added to `ApprovalView` and forgotten here would leave the
 *  badge frozen at whatever it last painted, which is the failure mode
 *  `paint-sig.ts`'s totality rule exists to make unrepresentable. */
function approvalParts(seg: Segment, spec: Spec, approving: ReadonlySet<string>): string[] {
  const v = approvalView(seg, spec, approving);
  const parts: Record<keyof ApprovalView, string> = {
    badge: v.badge,
    detail: v.detail,
    offer: String(v.offer),
    label: v.label,
    busy: String(v.busy),
  };
  return Object.values(parts);
}

/** The badge's tooltip, or "" for a stamp that cannot be read. Local time,
 *  because the reader is the person who approved it. */
function approvedAt(at: string): string {
  const ms = Date.parse(at);
  return Number.isNaN(ms) ? "" : `Approved ${new Date(ms).toLocaleString()}`;
}

/** Record a sign-off on one phase, then RE-READ rather than patch the badge: the
 *  record's own state and the `stale` derived against the live document are both
 *  the server's, and a refused claim means the file moved under the reader, which
 *  only a fetch can show them. */
async function approvePhase(ref: string, phase: string, hash: string): Promise<void> {
  const st = states.get(ref);
  if (st === undefined || st.approving.has(phase)) {
    return;
  }
  st.approving.add(phase);
  st.failure = "";
  paint(ref);
  const out = await approveSpecPhase.dispatch({ dir: ref, phase, hash }).outcome;
  // The tab can close while the command is in flight, and `release` deletes the
  // state; writing to the captured object would then paint nothing and leak.
  const live = states.get(ref);
  if (live === undefined) {
    return;
  }
  live.approving.delete(phase);
  if (out.status === "error") {
    live.failure =
      out.error.code === "doc_changed"
        ? `${phase} changed since you read it \u2014 reload before approving`
        : `The approval could not be recorded: ${out.error.message}`;
  } else if (out.status === "cancelled") {
    live.failure = "The approval was cancelled before it ran";
  }
  await refetch(ref);
  paint(ref);
}

// --- Render ---

function pageFor(ref: string, st: SpecPageState): PageEls {
  if (st.page !== null) {
    return st.page;
  }
  const head = el("div", { className: "spec-head" });
  const barHost = el("div", { className: "spec-bar-host" });
  const pane = el("div", { className: "spec-pane" });
  // The dock's own classes and roles, byte-for-byte the two hosts in
  // static/index.html: `css/26-dock.css` owns every rule and the module owns the
  // `hidden` class, which it lands at the end of the leaving phase.
  const dockHost = el("div", {
    className: "decision-dock hidden",
    role: "group",
    "aria-label": "Waiting for your decision",
  });
  const root = el("div", { className: "spec-page" }, head, barHost, pane, dockHost);
  st.page = { root, head, barHost, pane, dockHost, barKey: "", paintBar: null };
  // The TARGET chat rather than the active one: with a spec tab on screen there
  // is no active chat tab, and a re-parent moves the answer, so it is a getter.
  mountChatDecisionDock(dockHost, () => targetChat(ref));
  return st.page;
}

/** Repaint `ref`'s page from its state. Head and bar rebuild on a change of what
 *  they show; the pane rebuilds on a document switch or a content change. */
export function paint(ref: string): void {
  const st = states.get(ref);
  if (!st?.page) {
    return;
  }
  const page = st.page;
  const chat = targetChat(ref);
  const thinking = chat !== "" && isThinking(chat);

  // Everything the head renders, so an unchanged head is not re-seated on every
  // poll tick (web.md "RE-INSERTING AN ATTACHED NODE").
  const headParts = [
    String(st.gone),
    st.etag,
    st.spec === null ? "" : (tasksDoc(st.spec)?.hash ?? ""),
    chat,
    String(thinking),
    String(st.dispatching),
    String(st.busy.has("all")),
    st.failure,
    chat === ""
      ? openChatRefs()
          .map((id) => `${id}=${get(id)?.name ?? ""}`)
          .join(",")
      : "",
  ];
  paintIfChanged(page.head, headParts, () => buildHead(ref, st, chat, thinking));

  if (st.gone || st.spec === null) {
    page.barHost.replaceChildren();
    page.barKey = "";
    page.paintBar = null;
    const parts = [String(st.gone)];
    paintIfChanged(page.pane, parts, () => [
      st.gone
        ? el("div", { className: "spec-empty list-empty" }, `Nothing is at ${ref} any more`)
        : el("div", { className: "list-empty" }, "Loading spec\u2026"),
    ]);
    return;
  }

  const spec = st.spec;
  const segments = segmentsFor(spec);
  const key = segments
    .map((s) => `${s.file}|${s.doc === undefined ? "missing" : "present"}`)
    .join("\n");
  if (key !== page.barKey) {
    page.barKey = key;
    const bar = buildBar(ref, segments);
    page.barHost.replaceChildren(bar);
    page.paintBar = initSegmentedBar(bar, {
      attr: "data-spec-doc",
      idPrefix: "spec",
      tabs: segments.map((s) => ({ id: s.file, label: s.label })),
      onSelect: (file) => {
        if (st.doc === file) {
          return;
        }
        st.doc = file;
        paint(ref);
      },
    });
  }
  paintDots(page.barHost, segments, st, thinking);
  const active = st.doc ?? segments[0]?.file ?? "";
  page.paintBar?.(active);

  const seg = segments.find((s) => s.file === active);
  const paneParts = [
    active,
    seg?.doc?.hash ?? "missing",
    String(seg?.doc?.too_large === true),
    chat,
    String(thinking),
    [...st.dispatched].join(","),
    [...st.busy].join(","),
    [...st.unmoved].join(","),
    // The approval state of the SHOWN phase, so a record that moved repaints its
    // badge: neither the etag nor the doc's hash states it, because an approval
    // changes without the file changing.
    ...(seg === undefined ? [] : approvalParts(seg, spec, st.approving)),
  ];
  if (!sigChanged(page.pane, paneParts)) {
    return;
  }
  const switched = page.pane.dataset["activeDoc"] !== active;
  page.pane.dataset["activeDoc"] = active;
  const content = seg === undefined ? [] : buildPane(ref, st, spec, seg, chat, thinking);
  if (switched) {
    swapViews(() => {
      page.pane.replaceChildren(...content);
      return page.pane;
    });
  } else {
    page.pane.replaceChildren(...content);
  }
}

function buildHead(ref: string, st: SpecPageState, chat: string, thinking: boolean): HTMLElement[] {
  const spec = st.spec;
  const name = spec?.name ?? ref.split("/").pop() ?? ref;
  const row = el("div", { className: "spec-head-row" }, el("h2", { className: "spec-name" }, name));
  const out: HTMLElement[] = [row];
  const disabledAll = st.gone || spec === null;

  if (spec !== null && !st.gone) {
    const phase = phaseOf(spec);
    const building = st.dispatching && thinking;
    if (building || phase !== null) {
      row.appendChild(
        el(
          "span",
          { className: `spec-phase${building ? " spec-phase-building" : ""}` },
          building ? "Building" : (phase?.label ?? ""),
        ),
      );
    }
    const tasks = tasksDoc(spec);
    const progress = tasks?.progress;
    if (progress !== undefined) {
      row.appendChild(
        el(
          "span",
          { className: "spec-progress-count" },
          `${String(progress.completed)} of ${String(progress.total)} done`,
        ),
      );
      if (progress.total > 0) {
        out.push(progressBar(progress));
      }
    }
    const unreadable = unreadableLine(tasks?.unreadable_lines ?? 0);
    if (unreadable !== "") {
      out.push(el("div", { className: "spec-unreadable" }, unreadable));
    }
  }

  const actions = el("div", { className: "spec-actions" });
  const tasks = spec === null ? undefined : tasksDoc(spec);
  if (chat === "") {
    actions.appendChild(runInPicker(ref, disabledAll));
  } else if (tasks !== undefined) {
    let blocker = "";
    if (thinking) {
      blocker = "The chat is still working";
    } else if (tasks.truncated !== undefined) {
      blocker = "The task list was cut at the node cap, so Run all cannot see every task";
    } else if (spec !== null && !promptFits(runAllPrompt(spec))) {
      blocker = "The prompt is too large to send";
    }
    const busy = st.busy.has("all");
    const btn = el(
      "button",
      {
        type: "button",
        className: `btn-small spec-run-all${busy ? " spec-busy" : ""}`,
        disabled: blocker !== "" || disabledAll || busy,
        [CHROME_ATTR]: "",
      },
      "Run all",
    ) as HTMLButtonElement;
    if (blocker !== "") {
      btn.setAttribute("data-tooltip", blocker);
    }
    btn.addEventListener("click", () => {
      void runAll(ref);
    });
    actions.appendChild(btn);
  }
  if (st.failure !== "") {
    actions.appendChild(el("span", { className: "spec-failure", role: "status" }, st.failure));
  }
  out.push(actions);
  return out;
}

/** The "Run in" picker a parentless page shows over the open chats. Picking one
 *  re-parents the tab, after which the ordinary Run rule applies. */
function runInPicker(ref: string, disabled: boolean): HTMLElement {
  const select = el("select", {
    className: "spec-run-in-select",
    "aria-label": "Chat to run tasks in",
    disabled,
  }) as HTMLSelectElement;
  const chats = openChatRefs();
  for (const id of chats) {
    select.appendChild(el("option", { value: id }, get(id)?.name ?? "Chat"));
  }
  const btn = el(
    "button",
    {
      type: "button",
      className: "btn-small spec-run-in",
      disabled: disabled || chats.length === 0,
      [CHROME_ATTR]: "",
      "data-tooltip":
        chats.length === 0
          ? "Open a chat first, then pick it here"
          : "Nest this spec under the chosen chat so Run sends to it",
    },
    "Run in",
  ) as HTMLButtonElement;
  btn.addEventListener("click", () => {
    const chat = select.value;
    const tab = tabIdFor("spec", ref);
    const parent = tabIdFor("chat", chat);
    if (tab === "" || parent === "") {
      return;
    }
    void setTabParent(tab, parent).then(() => {
      paint(ref);
      // The dock host matches on this page's target chat, which the re-parent
      // just changed, and no signal the effect reads moved.
      rerenderDocks();
    });
  });
  return el("div", { className: "spec-run-in-row" }, select, btn);
}

function progressBar(p: {
  readonly completed: number;
  readonly in_progress: number;
  readonly queued: number;
  readonly total: number;
}): HTMLElement {
  const bar = el("div", {
    className: "spec-progressbar",
    role: "progressbar",
    "aria-valuenow": String(p.completed),
    "aria-valuemin": "0",
    "aria-valuemax": String(p.total),
    "aria-valuetext": progressText(p),
  });
  const pct = (n: number): string => `${String((n / p.total) * 100)}%`;
  bar.style.setProperty("--done", pct(p.completed));
  bar.style.setProperty("--active", pct(p.in_progress));
  bar.appendChild(el("span", { className: "spec-progress-done" }));
  bar.appendChild(el("span", { className: "spec-progress-active" }));
  return bar;
}

function buildBar(ref: string, segments: readonly Segment[]): HTMLElement {
  const bar = el("nav", { className: "seg-bar spec-bar", "aria-label": `Documents of ${ref}` });
  for (const s of segments) {
    bar.appendChild(
      el(
        "button",
        { type: "button", className: "seg spec-seg", "data-spec-doc": s.file },
        el("span", { className: "seg-icon spec-seg-dot", "aria-hidden": "true" }),
        el("span", { className: "seg-label" }, s.label),
      ),
    );
  }
  return bar;
}

/** Each segment's dot: present or missing, and the FIRST missing expected
 *  document pulses only while this page dispatched and the target is thinking. */
function paintDots(
  host: HTMLElement,
  segments: readonly Segment[],
  st: SpecPageState,
  thinking: boolean,
): void {
  let pulsed = false;
  for (const s of segments) {
    // Escaped for the reason segmented-bar.ts states at its own selector: a
    // segment's id here is a filename off the directory listing.
    const dot = host.querySelector<HTMLElement>(
      `[data-spec-doc="${CSS.escape(s.file)}"] .spec-seg-dot`,
    );
    if (dot === null) {
      continue;
    }
    const present = s.doc !== undefined;
    dot.classList.toggle("spec-seg-present", present);
    dot.classList.toggle("spec-seg-missing", !present);
    const pulse = !present && !pulsed && st.dispatching && thinking;
    dot.classList.toggle("spec-pulse", pulse);
    if (pulse) {
      pulsed = true;
    }
  }
}

function buildPane(
  ref: string,
  st: SpecPageState,
  spec: Spec,
  seg: Segment,
  chat: string,
  thinking: boolean,
): HTMLElement[] {
  const doc = seg.doc;
  if (doc === undefined) {
    return [editorDocSkeleton()];
  }
  if (doc.too_large === true) {
    return [
      el(
        "div",
        { className: "spec-notice" },
        el("span", {}, `${doc.file} is too large to show here`),
        ...docActions(ref, st, spec, seg, doc),
      ),
    ];
  }
  if (doc.role !== "tasks") {
    const host = el("div", { className: "spec-prose" });
    renderMarkdownDoc(host, doc.content);
    return [
      el("div", { className: "spec-doc-actions" }, ...docActions(ref, st, spec, seg, doc)),
      host,
    ];
  }
  return [
    el("div", { className: "spec-doc-actions" }, ...docActions(ref, st, spec, seg, doc)),
    buildTasks(ref, st, spec, doc, chat, thinking),
  ];
}

/** One document's actions, in reading order: what the record says about this
 *  phase, then the control that changes it, then Edit. Edit is last on every arm
 *  so its position does not move as an approval lands. */
function docActions(
  ref: string,
  st: SpecPageState,
  spec: Spec,
  seg: Segment,
  doc: SpecDoc,
): HTMLElement[] {
  const view = approvalView(seg, spec, st.approving);
  const out: HTMLElement[] = [];
  if (view.badge !== "") {
    const badge = el(
      "span",
      { className: "spec-approval", "data-approval": view.offer ? "stale" : "current" },
      view.badge,
    );
    if (view.detail !== "") {
      badge.dataset["tooltip"] = view.detail;
    }
    out.push(badge);
  }
  if (view.offer) {
    out.push(approveButton(ref, seg.role, doc.hash, view));
  }
  out.push(editButton(ref, doc.file));
  return out;
}

function approveButton(
  ref: string,
  role: SpecDocRole,
  hash: string,
  view: ApprovalView,
): HTMLElement {
  const btn = el(
    "button",
    { type: "button", className: "btn-small spec-approve", [CHROME_ATTR]: "" },
    view.label,
  ) as HTMLButtonElement;
  if (view.busy) {
    btn.disabled = true;
    btn.setAttribute("aria-busy", "true");
  }
  btn.addEventListener("click", () => {
    void approvePhase(ref, role, hash);
  });
  return btn;
}

function editButton(ref: string, file: string): HTMLElement {
  const btn = el(
    "button",
    { type: "button", className: "btn-small spec-edit", [CHROME_ATTR]: "" },
    "Edit",
  );
  btn.addEventListener("click", () => {
    openFile(absPath(`${ref}/${file}`));
  });
  return btn;
}

function buildTasks(
  ref: string,
  st: SpecPageState,
  spec: Spec,
  doc: SpecDoc,
  chat: string,
  thinking: boolean,
): HTMLElement {
  const tasks = doc.tasks ?? [];
  const wrap = el("div", { className: "spec-tasks" });
  if (doc.truncated !== undefined) {
    wrap.appendChild(
      el(
        "div",
        { className: "spec-truncated" },
        `Showing ${String(doc.truncated.returned)} of ${String(doc.truncated.total)} tasks`,
      ),
    );
  }
  if (tasks.length === 0) {
    wrap.appendChild(el("div", { className: "spec-empty-tasks" }, "tasks.md has no tasks yet."));
    return wrap;
  }
  const tree = el("div", { className: "spec-tree" });
  const ctx: NodeCtx = { ref, st, spec, chat, thinking, truncated: doc.truncated !== undefined };
  for (const n of tasks) {
    tree.appendChild(renderTaskNode(n, 0, ctx));
  }
  wrap.appendChild(tree);
  return wrap;
}

interface NodeCtx {
  readonly ref: string;
  readonly st: SpecPageState;
  readonly spec: Spec;
  readonly chat: string;
  readonly thinking: boolean;
  readonly truncated: boolean;
}

/** One task row and its disclosure (detail as markdown, then children). */
function renderTaskNode(node: SpecTaskNode, depth: number, ctx: NodeCtx): HTMLElement {
  const hasKids = node.children.length > 0;
  const hasBody = hasKids || node.detail !== "";
  const isCollapsed = hasBody && ctx.st.collapse.has(node.id);
  const dispatched = ctx.st.dispatched.has(node.id);
  const busy = ctx.st.busy.has(node.id);

  const wrap = el("div", {
    className: `spec-node${isCollapsed ? " collapsed" : ""}${dispatched ? " spec-dispatched" : ""}`,
    "data-task-id": node.id,
  });
  const row = el("div", { className: "spec-node-row", style: `${DEPTH_PROP}:${String(depth)}` });

  let toggle: HTMLButtonElement | null = null;
  if (hasBody) {
    toggle = el("button", {
      type: "button",
      className: "spec-toggle",
      "aria-label": isCollapsed ? "Expand" : "Collapse",
    }) as HTMLButtonElement;
    row.appendChild(toggle);
  } else {
    row.appendChild(el("span", { className: "spec-toggle-spacer" }));
  }

  const status = el("span", { className: "spec-status" });
  paintStatus(status, node.status, { queued: node.queued });
  row.appendChild(status);
  row.appendChild(el("span", { className: "spec-task-text" }, node.text));
  if (node.optional) {
    row.appendChild(el("span", { className: "spec-badge spec-badge-optional" }, "Optional"));
  }
  if (node.wave !== undefined) {
    row.appendChild(
      el(
        "span",
        {
          className: "spec-badge spec-badge-wave",
          "data-tooltip": "Runs in parallel with the rest of this batch",
        },
        `Wave ${String(node.wave)}`,
      ),
    );
  }

  const leaf = !hasKids && node.truncated_children !== true;
  if (leaf && node.status !== "completed") {
    const blocker = runBlocker(ctx.spec, node, ctx.chat, ctx.thinking, ctx.truncated);
    const run = el(
      "button",
      {
        type: "button",
        className: `btn-small spec-run${busy ? " spec-busy" : ""}`,
        disabled: blocker !== "" || busy,
        "aria-label": `Run task ${node.text}`,
        [CHROME_ATTR]: "",
      },
      "Run",
    ) as HTMLButtonElement;
    if (blocker !== "") {
      run.setAttribute("data-tooltip", blocker);
    }
    run.addEventListener("click", () => {
      void runTask(ctx.ref, node.id, node.hash);
    });
    row.appendChild(run);
  }
  wrap.appendChild(row);

  if (ctx.st.unmoved.has(node.id)) {
    wrap.appendChild(
      el(
        "div",
        { className: "spec-node-note", style: `${DEPTH_PROP}:${String(depth)}` },
        "The turn ended without ticking this box. Run it again when you are ready.",
      ),
    );
  }

  if (hasBody && toggle !== null) {
    const body = el("div", { className: "spec-node-body" });
    if (node.detail !== "") {
      const detail = el("div", {
        className: "spec-node-detail message assistant",
        style: `${DEPTH_PROP}:${String(depth)}`,
      });
      renderMarkdownInto(detail, node.detail);
      body.appendChild(detail);
    }
    if (hasKids) {
      const kids = el("div", { className: "spec-node-children" });
      for (const c of node.children) {
        kids.appendChild(renderTaskNode(c, depth + 1, ctx));
      }
      body.appendChild(kids);
    }
    wrap.appendChild(body);
    createDisclosure(toggle, body, {
      open: !isCollapsed,
      onToggle: (open) => {
        wrap.classList.toggle("collapsed", !open);
        toggle.setAttribute("aria-label", open ? "Collapse" : "Expand");
        if (open) {
          ctx.st.collapse.delete(node.id);
        } else {
          ctx.st.collapse.add(node.id);
        }
      },
    });
  }
  return wrap;
}

// --- Test seams ---

/** @internal The state map's keys, for the lifetime test. */
export function _openStates(): string[] {
  return [...states.keys()];
}

/** @internal One ref's state, for the tests. */
export function _stateOf(ref: string): SpecPageState | undefined {
  return states.get(ref);
}

/** @internal Drop every state and the shown ref between cases. */
export function _resetForTest(): void {
  for (const ref of [...states.keys()]) {
    release(ref);
  }
  shown = "";
}
