// The interaction dock: where the agent asks the user something (permission, MCP elicitation, structured question,
// workflow-step question). A bottom-bar region rather than a modal, so the transcript the decision is about stays
// readable; any bottom bar can host it (`mountDecisionDock(host)`), since the run tab has no composer. Focus is not
// trapped: the region is non-modal. The queue is per-chat, so a second ask never overwrites the first.

import { el, signal, effect, touch } from "@cplieger/reactive";
import { announce } from "@cplieger/ui-primitives/announce";
import { activeSession } from "./store.js";
import { BUS_USER_INPUT_ANSWERED, emitBus } from "./bus.js";
import { releaseClampsIn } from "./clamp-text.js";
import { forceReflow } from "./dom.js";
import { RUN_INPUT_FALLBACK } from "./dock-ask.js";
import { buildPermissionCard, type PermissionAnswer } from "./permission.js";
import { buildElicitationCard } from "./elicitation.js";
import { buildUserInputCard } from "./user-input.js";
import { buildRunInputCard } from "./run-input.js";
import { info } from "./toast.js";
import { named, noticeSubject } from "./notice-subject.js";
import { join } from "@cplieger/keyenc";
import type { RunAsks } from "./run-exec-source.js";
import type {
  PermissionNeededPayload,
  ElicitationNeededPayload,
  UserInputNeededPayload,
  RunInputNeededPayload,
  SettledBy,
} from "./types.js";

/** A permission ask, including the turn-approval variety (payload.files). */
interface PermissionDecision {
  kind: "permission";
  chatID: string;
  /** The workflow run this ask belongs to ("" otherwise); lets a run tab render an ask keyed to the launching chat. */
  runID?: string;
  requestID: number;
  payload: PermissionNeededPayload;
  submit: (answer: PermissionAnswer) => void;
}

interface ElicitationDecision {
  kind: "elicitation";
  chatID: string;
  runID?: string;
  requestID: number;
  payload: ElicitationNeededPayload;
  submit: (action: "accept" | "decline" | "cancel", content?: Record<string, unknown>) => void;
}

interface UserInputDecision {
  kind: "user_input";
  chatID: string;
  runID?: string;
  requestID: number;
  payload: UserInputNeededPayload;
  submit: (action: "answered" | "dismissed", answer?: string) => void;
}

/**
 * A workflow step's question: durable across bridge death and restart, answered by a fresh `session/prompt` to the
 * paused step's session, and carrying a string id (hence `decisionIdentity`). `submit(null)` continues without
 * answering, for the post-restart case where the question text is gone (the ask registry is in memory).
 */
interface RunInputDecision {
  kind: "run_input";
  chatID: string;
  runID?: string;
  /** The ask's identity within its run, server-composed. */
  askID: string;
  payload: RunInputNeededPayload;
  submit: (text: string | null) => void;
  /** Asks the launching agent to answer, on a chat-parented ask only; its presence puts the Defer button on the card. */
  defer?: () => void | Promise<void>;
}

/** The dock's input; the per-kind shapes are this module's business. */
export type Decision =
  PermissionDecision | ElicitationDecision | UserInputDecision | RunInputDecision;

/** The three request-shaped kinds (an open JSON-RPC request with an int64 id), which `decision_settled` can name. */
type RequestDecision = PermissionDecision | ElicitationDecision | UserInputDecision;

function isRequestDecision(d: Decision): d is RequestDecision {
  return d.kind !== "run_input";
}

/** Per-chat FIFO of unanswered decisions. The head is the one on screen. */
const queues = new Map<string, Decision[]>();

/**
 * The queue key a run owns, twin of `runChatPrefix` in `internal/agent/run_host.go`. Not a chat id: no tab or session
 * row answers to it.
 */
const RUN_CHAT_PREFIX = "run:";

/** Bumped on every queue mutation: a decision arriving for the chat on screen changes nothing the store can see. */
const queueVersion = signal(0);
function bump(): void {
  queueVersion.value = queueVersion.peek() + 1;
}

/** `data-dock-phase` carries the same word into `css/26-dock.css`, which owns every duration and keyframe. */
type Phase = "entering" | "leaving" | "advancing";

/**
 * Twin of the durations in `css/26-dock.css` (`--dur-standard`, `--dock-exit-dur`, `--dur-exit`): one timer per host is
 * the only cleanup (no transitionend listener to orphan), and `decision-dock.test.ts` asserts the two agree. Test-only export.
 */
export const DOCK_PHASE_MS: Readonly<Record<Phase, number>> = {
  entering: 200,
  leaving: 120,
  advancing: 150,
};

/** `""` hands the property back to the stylesheet (`height: auto`, `margin-block-end: var(--sp-2)`). */
interface BoxState {
  height: string;
  margin: string;
}

/**
 * One mounted dock. The composer's shows the active chat's queue; a run tab's shows one run's decisions wherever keyed
 * (launching chat id or `run:<id>`). Each host owns its own phase.
 */
interface DockHost {
  el: HTMLElement;
  match: (d: Decision) => boolean;
  renderedKey: string;
  /** The answered card, kept on screen (neutralised) for the phase's duration.
   *  At most one per host, ever. */
  outgoing: HTMLElement | null;
  /** The cleanup timer. `clearTimeout` is the optimisation; `gen` is the
   *  guarantee. */
  timer: ReturnType<typeof setTimeout> | null;
  /** Bumped by `endPhase`. A timer callback whose generation has moved on
   *  returns without touching the DOM. */
  gen: number;
  /**
   * This host's render subscription, released when a per-page host (`spec-view.ts`) drops; otherwise the effect renders
   * into a detached element for the tab's lifetime.
   */
  stop: () => void;
}

const hosts: DockHost[] = [];

/** Wire the composer's dock into the chat bottom bar. Idempotent per host. */
export function mountDecisionDock(hostEl: HTMLElement): void {
  addHost(hostEl, (d) => d.chatID === activeChatID());
}

/**
 * Wire the run view's dock: the current run's decisions, whether keyed to the launching chat or `run:<id>`. The run is
 * a getter because one view element serves every run tab. Idempotent per host element.
 */
export function mountRunDecisionDock(hostEl: HTMLElement, runID: () => string): void {
  addHost(hostEl, (d) => {
    const id = runID();
    return id !== "" && (d.runID === id || d.chatID === `${RUN_CHAT_PREFIX}${id}`);
  });
}

/**
 * Wire a dock showing one named chat's queue, for a page with no active chat (a spec tab). A getter because a
 * re-parent changes the id during the host's life. Idempotent per host element.
 */
export function mountChatDecisionDock(hostEl: HTMLElement, chatID: () => string): void {
  addHost(hostEl, (d) => {
    const id = chatID();
    return id !== "" && d.chatID === id;
  });
}

/** Re-render every dock; the run view calls it when its run changes, which the reactive graph cannot see. */
export function rerenderDocks(): void {
  bump();
}

/**
 * Release a host whose element is going away: stop its subscription, end its phase, forget it. Unknown elements
 * are ignored.
 */
export function unmountDecisionDock(hostEl: HTMLElement): void {
  const i = hosts.findIndex((h) => h.el === hostEl);
  const h = hosts[i];
  if (h === undefined) {
    return;
  }
  endPhase(h);
  h.stop();
  hosts.splice(i, 1);
}

function addHost(hostEl: HTMLElement, match: (d: Decision) => boolean): void {
  if (hosts.some((existing) => existing.el === hostEl)) {
    return;
  }
  const h: DockHost = {
    el: hostEl,
    match,
    renderedKey: "",
    outgoing: null,
    timer: null,
    gen: 0,
    stop: () => undefined,
  };
  hosts.push(h);
  h.stop = effect(() => {
    touch(activeSession, queueVersion);
    renderHost(h);
  });
}

function activeChatID(): string {
  return activeSession.peek()?.id ?? "";
}

/** Enqueue a decision and show it when its chat is the one on screen. */
export function pushDecision(d: Decision): void {
  const q = queues.get(d.chatID) ?? [];
  // SSE connect replays every unanswered permission and parked question, so a re-delivery must not stack. Per-kind
  // identity, so a string ask id and a request id are never compared.
  const id = decisionIdentity(d);
  if (q.some((existing) => existing.kind === d.kind && decisionIdentity(existing) === id)) {
    return;
  }
  q.push(d);
  queues.set(d.chatID, q);
  bump();
}

/**
 * Drop every decision keyed to this surface without answering (chat gone, or a transport gap). Never calls submit.
 * Unconditional: `close_chat` / `delete_chat` cancel the chat's runs server-side, so run asks keyed here are dead too.
 */
export function dropDecisions(chatID: string): void {
  if (!queues.delete(chatID)) {
    return;
  }
  bump();
}

/**
 * Drop every ask a run owns (`run:` prefix), which the per-chat sweep cannot reach; the replay re-offers what is still
 * open. Keyed on the prefix: chat-keyed run asks belong to `dropDecisions`.
 */
export function dropRunDecisions(): void {
  let dropped = false;
  for (const chatID of [...queues.keys()]) {
    if (chatID.startsWith(RUN_CHAT_PREFIX)) {
      queues.delete(chatID);
      dropped = true;
    }
  }
  if (dropped) {
    bump();
  }
}

/**
 * Drop every unanswered ask of this run, wherever filed, without answering. All four kinds carry `runID`
 * (handlers/turn.ts, handlers/run.ts), so a step's permission and elicitation have no other remover. Safe: a terminal
 * run has no step to consume an answer, and the server clears its own pending set on the same transition. Same join
 * as `runPendingAsks`. Refuses an empty id.
 */
export function dropRunAsks(workflowID: string): void {
  if (workflowID === "") {
    return;
  }
  const runKey = `${RUN_CHAT_PREFIX}${workflowID}`;
  // Includes an ask whose per-ask settle never arrived, which `collapseSettledRunInput` cannot cover.
  heldAnswers.delete(workflowID);
  let dropped = false;
  for (const [chatID, q] of queues) {
    const rest = q.filter((d) => d.runID !== workflowID && d.chatID !== runKey);
    if (rest.length === q.length) {
      continue;
    }
    dropped = true;
    if (rest.length === 0) {
      queues.delete(chatID);
    } else {
      queues.set(chatID, rest);
    }
  }
  if (dropped) {
    bump();
  }
}

/**
 * Drop the decisions a turn owned, leaving run asks alone. Triggers: `turn_ended`, and a run's terminal frame while
 * the launching chat is idle (a step's ask can arrive with empty `run_id`). A blocking ask cannot outlive its turn.
 * Run-scoped asks are exempt: an agent-launched run outlives the turn that launched it; `dropRunAsks` ends that.
 */
export function dropTurnDecisions(chatID: string): void {
  const q = queues.get(chatID);
  if (q === undefined) {
    return;
  }
  const rest = q.filter((d) => d.runID !== undefined && d.runID !== "");
  if (rest.length === q.length) {
    return;
  }
  if (rest.length === 0) {
    queues.delete(chatID);
  } else {
    queues.set(chatID, rest);
  }
  bump();
}

/**
 * Whether this chat holds an unanswered decision (the tab dot). Reads `queueVersion.value` so the calling effect
 * subscribes to arrivals and answers. A `run:<id>` key never matches.
 */
export function hasPendingDecision(chatID: string): boolean {
  touch(queueVersion);
  return (queues.get(chatID)?.length ?? 0) > 0;
}

/**
 * What one workflow run waits on a person for (the run card): KAS leaves the run `running` while a step is blocked.
 * Scans every queue (same join as `mountRunDecisionDock`), and reads `queueVersion.value` to subscribe the caller.
 */
export function runPendingAsks(workflowID: string): RunAsks {
  touch(queueVersion);
  const nodes = new Set<string>();
  if (workflowID === "") {
    return { count: 0, nodes, label: "" };
  }
  const runKey = `${RUN_CHAT_PREFIX}${workflowID}`;
  let count = 0;
  let label = "";
  for (const q of queues.values()) {
    for (const d of q) {
      if (d.runID !== workflowID && d.chatID !== runKey) {
        continue;
      }
      count++;
      // Absent when the step-session registry never saw the sub-session; the run is blocked either way.
      const node = d.payload.node_id ?? "";
      if (node !== "") {
        nodes.add(node);
      }
      if (label === "") {
        label = askLabel(d);
      }
    }
  }
  return { count, nodes, label };
}

/** Per-kind and private: the run card takes the sentence, not a payload. */
function askLabel(d: Decision): string {
  switch (d.kind) {
    case "permission":
      return d.payload.title ?? "";
    case "elicitation":
      return d.payload.message ?? "";
    case "user_input":
      return d.payload.question;
    case "run_input":
      // Empty after a restart: the ask registry is in memory, so the server reconstructs the ask without its question.
      return d.payload.question === "" ? RUN_INPUT_FALLBACK : d.payload.question;
  }
}

/**
 * Retire a decision another surface answered (`decision_settled`) without calling submit, and say who answered it
 * when the card was on screen (`renderedKey`). Returns the settled ask's run id ("" for a chat's own), or undefined
 * when none was queued here.
 */
export function collapseSettledDecision(
  chatID: string,
  // Request-shaped kinds only: `decision_settled` is keyed by an int64 id; run asks use `collapseSettledRunInput`.
  kind: RequestDecision["kind"],
  requestID: number,
  settledBy: SettledBy,
): string | undefined {
  const q = queues.get(chatID);
  const i =
    q?.findIndex((d) => d.kind === kind && isRequestDecision(d) && d.requestID === requestID) ?? -1;
  if (q === undefined || i < 0) {
    return undefined;
  }
  const [settled] = q.splice(i, 1);
  if (q.length === 0) {
    queues.delete(chatID);
  }
  const key = decisionKey(chatID, kind, String(requestID));
  if (hosts.some((h) => h.renderedKey === key)) {
    // The toast announces itself into the shared live region; a second announce() would read it twice.
    info(named(noticeSubject(chatID), settledMessage(kind, settledBy)));
  }
  bump();
  // The settle frame names the chat, the banner was tagged by the run; the queue is where that attribution survives.
  return settled?.runID ?? "";
}

/**
 * Three causes read differently: another window (a person), the unattended floor (a deadline), `moot` (nobody;
 * the subject moved on, so claiming an answer would be false).
 */
function settledMessage(kind: Decision["kind"], settledBy: SettledBy): string {
  const subject = settledSubject(kind);
  switch (settledBy) {
    case "unattended":
      return `${subject} was answered automatically because nobody was watching.`;
    case "moot":
      return `${subject} is no longer waiting for an answer.`;
    case "user":
      return `${subject} was answered in another window.`;
    default:
      settledBy satisfies never;
      return `${subject} is no longer waiting for an answer.`;
  }
}

function settledSubject(kind: Decision["kind"]): string {
  switch (kind) {
    case "permission":
      return "The permission request";
    case "elicitation":
      return "The input request";
    case "user_input":
      return "The agent's question";
    case "run_input":
      return "The workflow step's question";
    default:
      kind satisfies never;
      return "The request";
  }
}

/**
 * Retire a run ask answered or waived elsewhere (`run_input_settled`), keyed by string ask id. Scans every queue:
 * the event names only the run.
 */
export function collapseSettledRunInput(
  workflowID: string,
  askID: string,
  settledBy: SettledBy,
): void {
  if (workflowID === "" || askID === "") {
    return;
  }
  // Unconditional: the ask is over whether or not a card is still queued here.
  forgetHeldAnswer(workflowID, askID);
  for (const [chatID, q] of queues) {
    const i = q.findIndex(
      (d) => d.kind === "run_input" && d.askID === askID && d.payload.workflow_id === workflowID,
    );
    if (i < 0) {
      continue;
    }
    q.splice(i, 1);
    if (q.length === 0) {
      queues.delete(chatID);
    }
    if (hosts.some((h) => h.renderedKey === decisionKey(chatID, "run_input", askID))) {
      info(named(noticeSubject(chatID), settledMessage("run_input", settledBy)));
    }
    bump();
    return;
  }
}

/**
 * A run-input card's typed words, held per ask: `settle` splices before sending, so a retryable refusal
 * (`errRunNotParked`) re-offers the same ask and must return the text. Nested by run because `collapseSettledRunInput`
 * (per ask) and `dropRunAsks` (per run) are every exit, which bounds the map.
 */
const heldAnswers = new Map<string, Map<string, string>>();

/** The text this ask is holding, or "" — what a rebuilt card seeds its box with. */
function heldAnswer(d: RunInputDecision): string {
  return heldAnswers.get(d.payload.workflow_id)?.get(d.askID) ?? "";
}

/** Hold the words being sent; a `null` text continues without answering, so the hold is dropped instead. */
function holdAnswer(d: RunInputDecision, text: string | null): void {
  if (text === null) {
    forgetHeldAnswer(d.payload.workflow_id, d.askID);
    return;
  }
  const byAsk = heldAnswers.get(d.payload.workflow_id) ?? new Map<string, string>();
  byAsk.set(d.askID, text);
  heldAnswers.set(d.payload.workflow_id, byAsk);
}

function forgetHeldAnswer(workflowID: string, askID: string): void {
  const byAsk = heldAnswers.get(workflowID);
  if (byAsk === undefined) {
    return;
  }
  byAsk.delete(askID);
  if (byAsk.size === 0) {
    heldAnswers.delete(workflowID);
  }
}

/**
 * Answer a decision and retire it. The settle-once guard lives here because "answered" is the queue entry's property.
 * Membership, not head position: a run tab may answer an ask queued behind the chat's own, which is protocol-correct.
 */
function settle(d: Decision, run: () => void): void {
  const q = queues.get(d.chatID);
  const i = q?.indexOf(d) ?? -1;
  if (q === undefined || i < 0) {
    // A double answer on one request id is worse than a dropped click.
    return;
  }
  q.splice(i, 1);
  if (q.length === 0) {
    queues.delete(d.chatID);
  }
  run();
  bump();
}

/** Insertion order across queues is stable enough: a run's asks come from one bridge. */
function matching(h: DockHost): Decision[] {
  const out: Decision[] = [];
  for (const q of queues.values()) {
    for (const d of q) {
      if (h.match(d)) {
        out.push(d);
      }
    }
  }
  return out;
}

/**
 * Within-kind identity: int64 request id, or the run ask id. A synthetic number would share the per-bridge JSON-RPC
 * id space, where collisions are ordinary.
 */
function decisionIdentity(d: Decision): string {
  return d.kind === "run_input" ? d.askID : String(d.requestID);
}

/** keyenc rather than a template join: a run ask id is arbitrary text, and a separator inside it would merge two keys. */
function decisionKey(chatID: string, kind: Decision["kind"], identity: string): string {
  return join(chatID, kind, identity);
}

function renderHost(h: DockHost): void {
  const mine = matching(h);
  const head = mine[0];

  if (head === undefined) {
    // Cleared when `leaving` starts, so a second empty render does not restart the exit.
    if (h.renderedKey !== "") {
      swap(h, undefined, 0);
    }
    return;
  }

  // A rebuild would discard the user's typing, so the same decision is a no-op apart from the depth line. MUST NOT start a phase.
  const key = decisionKey(head.chatID, head.kind, decisionIdentity(head));
  const depth = mine.length;
  if (key === h.renderedKey) {
    updateDepth(h, depth);
    return;
  }

  swap(h, head, depth);
}

// The phase machine. The next head is observed, not detected: `settle` splices then `bump()`s, so the render sees the
// next decision or none, and every retirement path animates the same way. The dispatch is never gated on motion.

/**
 * Swap the host's content, animating unless motion is off: nothing->head enters, head->head advances, head->nothing
 * leaves (nothing->nothing returns early in `renderHost`).
 */
function swap(h: DockHost, head: Decision | undefined, depth: number): void {
  // Measured first, before `endPhase` or any write, so it reads the live height and rapid answers morph from it.
  const from = measure(h);
  const phase: Phase =
    head === undefined ? "leaving" : h.renderedKey === "" ? "entering" : "advancing";

  // Idempotent: at most one timer and one outgoing element per host.
  endPhase(h);

  // The one point a card leaves, so clamps are released here explicitly (`clamp-text.ts` `releaseClamp`). Before `show`,
  // or the incoming card's clamp would be swept.
  releaseClampsIn(h.el);

  // Reduced motion and a background tab take no phase: animations stall in a background tab while `setTimeout` runs, so
  // a stale outgoing card would remain at full opacity.
  if (motionOff()) {
    if (head === undefined) {
      h.el.replaceChildren();
      h.el.classList.add("hidden");
      h.renderedKey = "";
    } else {
      show(h, head, depth);
    }
    return;
  }

  // Nothing to take on enter: the host is empty or its content already detached.
  const outgoing = phase === "entering" ? null : takeOutgoing(h);

  if (head === undefined) {
    // `.hidden` lands in `finishPhase`, after the collapse; adding it here makes the exit unanimatable.
    h.el.replaceChildren();
    h.renderedKey = "";
  } else {
    show(h, head, depth);
  }
  if (outgoing !== null) {
    h.el.prepend(outgoing);
    h.outgoing = outgoing;
  }

  h.el.dataset["dockPhase"] = phase;
  pinAndRelease(
    h,
    from,
    phase === "leaving" ? { height: "0px", margin: "0px" } : { height: "", margin: "" },
  );

  const gen = h.gen;
  h.timer = setTimeout(() => {
    finishPhase(h, gen, phase);
  }, DOCK_PHASE_MS[phase]);
}

/** Identical on both paths, so `announce()` fires once per new head with the card in the DOM and never opacity-0. */
function show(h: DockHost, head: Decision, depth: number): void {
  h.el.replaceChildren(buildCard(head), depthRow(depth));
  h.el.classList.remove("hidden");
  h.renderedKey = decisionKey(head.chatID, head.kind, decisionIdentity(head));
  announce(announcementFor(head));
}

/** A collapsed host is `display: none`, so its margin is 0 whatever `getComputedStyle` reports. */
function measure(h: DockHost): BoxState {
  const height = `${String(h.el.getBoundingClientRect().height)}px`;
  if (h.el.classList.contains("hidden")) {
    return { height, margin: "0px" };
  }
  return { height, margin: getComputedStyle(h.el).marginBlockEnd };
}

/**
 * Pin the box at a measured geometry with transitions suppressed, then release. Measured in Chromium: a live
 * transition on the pin is cancelled by the same-task release and the box snaps; neither a reflow nor two frames fix it.
 */
function pinAndRelease(h: DockHost, from: BoxState, to: BoxState): void {
  const s = h.el.style;
  s.transition = "none";
  s.height = from.height;
  s.marginBlockEnd = from.margin;
  // Flush: the pin has to land in a COMPLETED style resolution.
  forceReflow(h.el);
  s.transition = "";
  s.height = to.height;
  s.marginBlockEnd = to.margin;
}

/**
 * The answered content in a neutralised wrapper: `aria-hidden` (no second reading) and `inert` (no tab or click;
 * `settle`'s membership guard is authoritative).
 */
function takeOutgoing(h: DockHost): HTMLElement | null {
  const kids = [...h.el.children];
  if (kids.length === 0) {
    return null;
  }
  const wrap = el("div", { className: "dock-outgoing", "aria-hidden": "true" });
  wrap.setAttribute("inert", "");
  wrap.append(...kids);
  return wrap;
}

/** Tear down this host's phase. Idempotent. */
function endPhase(h: DockHost): void {
  h.gen++;
  if (h.timer !== null) {
    clearTimeout(h.timer);
    h.timer = null;
  }
  h.outgoing?.remove();
  h.outgoing = null;
  delete h.el.dataset["dockPhase"];
  h.el.style.transition = "";
  h.el.style.height = "";
  h.el.style.marginBlockEnd = "";
}

/** The generation check makes a superseded timer harmless even if it escaped `clearTimeout`. */
function finishPhase(h: DockHost, gen: number, phase: Phase): void {
  if (h.gen !== gen) {
    return;
  }
  endPhase(h);
  if (phase === "leaving") {
    h.el.classList.add("hidden");
  }
}

/** Read live per transition, so the preference takes effect on the next swap. */
function motionOff(): boolean {
  if (document.hidden) {
    return true;
  }
  return (
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}

/** Scoped to a DIRECT child: a stale depth row inside `.dock-outgoing` sits
 *  earlier in document order and would win an unscoped lookup. */
function updateDepth(h: DockHost, depth: number): void {
  const row = h.el.querySelector(":scope > .dock-depth");
  if (row === null) {
    return;
  }
  row.textContent = depthText(depth);
  row.classList.toggle("hidden", depth < 2);
}

function depthRow(depth: number): HTMLElement {
  const row = el("p", { className: "dock-depth" }, depthText(depth));
  row.classList.toggle("hidden", depth < 2);
  return row;
}

function depthText(depth: number): string {
  return depth < 2 ? "" : `${String(depth - 1)} more waiting`;
}

function announcementFor(d: Decision): string {
  switch (d.kind) {
    case "permission":
      return d.payload.files !== undefined && d.payload.files.length > 0
        ? "Review this turn's changes"
        : "The agent needs permission";
    case "elicitation":
      return "A tool is requesting input";
    case "user_input":
      return "The agent has a question";
    case "run_input":
      return "A workflow step is waiting for your answer";
    default:
      d satisfies never;
      return "";
  }
}

function buildCard(d: Decision): HTMLElement {
  switch (d.kind) {
    case "permission":
      return buildPermissionCard(d.chatID, d.payload, (answer) => {
        settle(d, () => {
          d.submit(answer);
        });
      });
    case "elicitation":
      return buildElicitationCard(d.payload, (action, content) => {
        settle(d, () => {
          d.submit(action, content);
        });
      });
    case "user_input":
      return buildUserInputCard(d.payload, (action, answer) => {
        settle(d, () => {
          d.submit(action, answer);
          // After the answer goes out, inside settle's callback: never act on an ask another surface answered, or before the
          // agent has it.
          if (action === "answered" && answer !== undefined) {
            emitBus(BUS_USER_INPUT_ANSWERED, { chatID: d.chatID, answer });
          }
        });
      });
    case "run_input":
      // Inside settle's callback, so an already-answered ask leaves nothing held.
      return buildRunInputCard(
        d.payload,
        heldAnswer(d),
        (text) => {
          settle(d, () => {
            holdAnswer(d, text);
            d.submit(text);
          });
        },
        // Unwrapped: a deferral answers nothing, and splicing would remove the card while the run is still parked.
        d.defer,
      );
    default:
      d satisfies never;
      return el("div");
  }
}

/** Reset module state between tests. `endPhase` first: a pending timer would fire into the next test's DOM. */
export function _resetForTest(): void {
  for (const h of hosts) {
    endPhase(h);
    h.stop();
  }
  queues.clear();
  heldAnswers.clear();
  hosts.length = 0;
  queueVersion.value = 0;
}

/** @internal Number of mounted hosts: the one observable of a release. */
export function _hostCount(): number {
  return hosts.length;
}
