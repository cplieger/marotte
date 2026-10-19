// A bottom-bar region rather than a modal, so the transcript a decision is about stays readable; focus is not
// trapped. The queue is per-chat, so a second ask never overwrites the first.

import { el, signal, effect, touch } from "@cplieger/reactive";
import { announce } from "@cplieger/ui-primitives/announce";
import { activeSession } from "./store.js";
import { BUS_USER_INPUT_ANSWERED, emitBus } from "./bus.js";
import { releaseClampsIn } from "./clamp-text.js";
import { forceReflow } from "./dom.js";
import { buildPermissionCard, type PermissionAnswer } from "./permission.js";
import { buildElicitationCard } from "./elicitation.js";
import { buildUserInputCard } from "./user-input.js";
import { followBand } from "./dock-ask.js";
import { info } from "./toast.js";
import { named, noticeSubject } from "./notice-subject.js";
import { join } from "@cplieger/keyenc";
import { nodePathKey } from "./run-node-key.js";
import { allRunAsks, type RunAskAddress, type RunAsks } from "./run-asks.js";
import type {
  PermissionNeededPayload,
  ElicitationNeededPayload,
  UserInputNeededPayload,
  SettledBy,
} from "./types.js";
import type { RunAsk } from "./wire/types.gen.js";

/** A permission ask, including the turn-approval variety (payload.files). */
interface PermissionDecision {
  kind: "permission";
  chatID: string;
  /** The workflow run this ask belongs to ("" otherwise); lets a run tab render an ask keyed to the launching chat. */
  runID?: string;
  requestID: number;
  payload: PermissionNeededPayload;
  submit: (answer: PermissionAnswer) => Promise<AnswerOutcome>;
}

interface ElicitationDecision {
  kind: "elicitation";
  chatID: string;
  runID?: string;
  requestID: number;
  payload: ElicitationNeededPayload;
  submit: (
    action: "accept" | "decline" | "cancel",
    content?: Record<string, unknown>,
  ) => Promise<AnswerOutcome>;
}

interface UserInputDecision {
  kind: "user_input";
  chatID: string;
  runID?: string;
  requestID: number;
  payload: UserInputNeededPayload;
  submit: (action: "answered" | "dismissed", answer?: string) => Promise<AnswerOutcome>;
}

/** What a dock renders: an open JSON-RPC request with an int64 id, which `decision_settled` can name. A workflow
 *  step's question takes no card: kiro-cli hands the questions a step sends to the agent that launched the run, and
 *  the run tab's composer answers any of them. */
type Decision = PermissionDecision | ElicitationDecision | UserInputDecision;

/**
 * What the server made of an answer: `answered` (its `decision_settled` retires the card), `superseded` (another
 * surface holds the ask, so this card goes now), `withdrawn` (the ask settled before the answer reached the agent; that
 * settlement's `decision_settled` or the turn's or run's end retires the card), or `failed` (the ask is still open and
 * the card is offered again).
 */
export type AnswerOutcome = "answered" | "superseded" | "withdrawn" | "failed";

/**
 * Request decisions this surface answered and the server has not yet settled: their cards stay on screen, inert,
 * because only the server knows whether the answer reached the agent.
 */
const answering = new WeakSet<Decision>();

/** Per-chat FIFO of unanswered decisions. */
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

/** The composer's shows the active chat's queue; a run tab's shows one run's decisions wherever
 *  keyed (launching chat id or `run:<id>`). Each host owns its own phase. */
interface DockHost {
  el: HTMLElement;
  match: (d: Decision) => boolean;
  renderedKey: string;
  /** The card `renderedKey` names, which an answer in flight makes inert. */
  card: HTMLElement | null;
  /** Whether the bar's composer rendered at the last `focusAnswerIfAside`. */
  composerShown: boolean;
  /** Whether the question card outgrew its floors at the last `paintFloor`. */
  cardScrolls: boolean;
  /** The answered card, kept on screen (neutralised) for the phase's duration.
   *  At most one per host, ever. */
  outgoing: HTMLElement | null;
  /** `clearTimeout` is the optimisation; `gen` is the guarantee. */
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
let viewportFollowed = false;

/** The tray's floor under a question card, which `css/26-dock.css` gives the tray as its `min-block-size`. */
const FLOOR_PROP = "--dock-floor";

/** A question card's options region's floor (`paintOptionsFloor`). */
const OPTIONS_FLOOR_PROP = "--options-floor";

/** A question card's question region's floor and the bar's steer stack's, each written by `paintFloor` only while
 *  it is lowered under its own. */
const ASK_FLOOR_PROP = "--ask-floor";
const STEER_FLOOR_PROP = "--steer-floor";

/** Wire the composer's dock into the chat bottom bar. Idempotent per host. */
export function mountDecisionDock(hostEl: HTMLElement): void {
  addHost(hostEl, (d) => d.chatID === activeChatID());
}

/**
 * Wire the run view's dock: the current run's decisions, whether keyed to the launching chat or `run:<id>`. The run is
 * a getter because one view element serves every run tab. Idempotent per host element. A step's question is answered
 * in the run composer, whose placeholder names the step and whose Send reads "Send answer" (`run-composer.ts`).
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
  if (!viewportFollowed) {
    viewportFollowed = true;
    window.addEventListener("resize", followViewport);
    followBand(followViewport);
  }
  const h: DockHost = {
    el: hostEl,
    match,
    renderedKey: "",
    card: null,
    composerShown: true,
    cardScrolls: false,
    outgoing: null,
    timer: null,
    gen: 0,
    stop: () => undefined,
  };
  hosts.push(h);
  // The floor follows the bar's content: a new card, a stage switch inside it, the depth row, a member
  // stepping aside or back, the box taking the typing, and the clamp's review of a card's question once
  // a hidden view shows it.
  const content = new MutationObserver(() => {
    paintFloor(h);
  });
  content.observe(hostEl.parentElement ?? hostEl, {
    childList: true,
    subtree: true,
    attributeFilter: ["class", TYPING_ATTR],
  });
  const stopTyping = followTyping(hostEl);
  const stopRender = effect(() => {
    touch(activeSession, queueVersion);
    renderHost(h);
  });
  h.stop = () => {
    stopRender();
    content.disconnect();
    stopTyping();
  };
}

/** On the bar: which of its text boxes last took the typing, `card` (the tray's) or `composer`, absent for another. */
const TYPING_ATTR = "data-typing";

/**
 * Keep the tray's bar's `data-typing` (`css/26-dock.css` reads it under a soft keyboard and on a tight band). Written
 * only when a text box takes focus, never when it loses it: a pressed button takes focus at pointerdown, and a bar that
 * reflowed then would move the button out from under the pointer before the click.
 */
function followTyping(tray: HTMLElement): () => void {
  const bar = tray.parentElement;
  if (bar === null) {
    return () => undefined;
  }
  const take = (box: EventTarget | null): void => {
    if (!(box instanceof HTMLTextAreaElement) || !bar.contains(box)) {
      return;
    }
    if (tray.contains(box)) {
      bar.setAttribute(TYPING_ATTR, "card");
    } else if (box.matches('[id="prompt-input"], [id="run-composer-input"]')) {
      bar.setAttribute(TYPING_ATTR, "composer");
    } else {
      bar.removeAttribute(TYPING_ATTR);
    }
  };
  const onFocus = (e: FocusEvent): void => {
    take(e.target);
  };
  take(document.activeElement);
  bar.addEventListener("focusin", onFocus);
  return () => {
    bar.removeEventListener("focusin", onFocus);
  };
}

function activeChatID(): string {
  return activeSession.peek()?.id ?? "";
}

/** Enqueue a decision and show it when its chat is the one on screen. */
export function pushDecision(d: Decision): void {
  const q = queues.get(d.chatID) ?? [];
  // SSE connect replays every unanswered request, so a re-delivery must not stack.
  if (q.some((existing) => existing.kind === d.kind && existing.requestID === d.requestID)) {
    return;
  }
  q.push(d);
  queues.set(d.chatID, q);
  bump();
}

/**
 * Drop every decision keyed to this surface without answering (chat gone, or a transport gap). Never calls submit.
 * Unconditional: `close_chat` / `delete_chat` cancel the chat's runs server-side, so a run's requests keyed here are dead too.
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
 * Drop every request decision of this run, wherever filed, without answering. All three kinds carry `runID`
 * (handlers/turn.ts), so a step's permission and elicitation have no other remover. Safe: a terminal run has no step to
 * consume an answer. Same join as `runPendingAsks`. Refuses an empty id.
 */
export function dropRunRequests(workflowID: string): void {
  if (workflowID === "") {
    return;
  }
  const runKey = `${RUN_CHAT_PREFIX}${workflowID}`;
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
 * Run-scoped asks are exempt: an agent-launched run outlives the turn that launched it; `dropRunRequests` ends that.
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
 * Whether this chat holds an unanswered decision (the tab dot's `input`). A step question of a run it launched is not
 * one: the run's own mark carries it (`chat-run-dots.ts`). Subscribes the calling effect to the queue. A `run:<id>`
 * key never matches a tab.
 */
export function hasPendingDecision(chatID: string): boolean {
  touch(queueVersion);
  return (queues.get(chatID)?.length ?? 0) > 0;
}

/**
 * What one workflow run waits on a person for (the run card, the exec view, the dots): its steps' questions from the
 * run-asks store, then its request decisions from every queue (same join as `mountRunDecisionDock`). Subscribes the
 * caller to both.
 */
export function runPendingAsks(workflowID: string): RunAsks {
  touch(queueVersion);
  const asked: RunAskAddress[] = [];
  if (workflowID === "") {
    return { count: 0, asked, label: "" };
  }
  let label = "";
  for (const r of allRunAsks()) {
    if (r.workflow_id !== workflowID) {
      continue;
    }
    for (const a of r.asks) {
      asked.push({
        nodeID: a.node_id,
        sessionID: a.step_session_id,
        answer: true,
        path: nodePathKey(a.node_path),
        askID: a.ask_id,
        text: runAskLabel(a),
      });
      if (label === "") {
        label = runAskLabel(a);
      }
    }
  }
  const runKey = `${RUN_CHAT_PREFIX}${workflowID}`;
  for (const q of queues.values()) {
    for (const d of q) {
      if (d.runID !== workflowID && d.chatID !== runKey) {
        continue;
      }
      asked.push({
        nodeID: d.payload.node_id ?? "",
        sessionID: "",
        answer: false,
        text: requestLabel(d),
      });
      if (label === "") {
        label = requestLabel(d);
      }
    }
  }
  return { count: asked.length, asked, label };
}

/** What a step's question reads as when marotte has no text for it. */
const RUN_ASK_FALLBACK = "A step is waiting for an answer";

function runAskLabel(a: RunAsk): string {
  return a.text === "" ? RUN_ASK_FALLBACK : a.text;
}

/** Per-kind and private: the run card takes the sentence, not a payload. */
function requestLabel(d: Decision): string {
  switch (d.kind) {
    case "permission":
      return d.payload.title ?? "";
    case "elicitation":
      return d.payload.message ?? "";
    case "user_input":
      return d.payload.question;
  }
}

/**
 * Retire a decision another surface answered (`decision_settled`) without calling submit, and say who answered it
 * when the card was on screen (`renderedKey`). Returns the settled ask's run id ("" for a chat's own), or undefined
 * when none was queued here.
 */
export function collapseSettledDecision(
  chatID: string,
  kind: Decision["kind"],
  requestID: number,
  settledBy: SettledBy,
): string | undefined {
  const q = queues.get(chatID);
  const i = q?.findIndex((d) => d.kind === kind && d.requestID === requestID) ?? -1;
  if (q === undefined || i < 0) {
    return undefined;
  }
  const [settled] = q.splice(i, 1);
  if (q.length === 0) {
    queues.delete(chatID);
  }
  const key = decisionKey(chatID, kind, String(requestID));
  // This surface's own answer, delivered: nothing to explain. An end or a withdrawal still says why.
  const ownAnswer = settled !== undefined && answering.has(settled) && settledBy === "user";
  if (!ownAnswer && hosts.some((h) => h.renderedKey === key)) {
    // The toast announces itself into the shared live region; a second announce() would read it twice.
    info(named(noticeSubject(chatID), settledMessage(kind, settledBy)));
  }
  bump();
  // The settle frame names the chat, the banner was tagged by the run; the queue is where that attribution survives.
  return settled?.runID ?? "";
}

/**
 * Four causes read differently: another window (a person), the unattended floor (a deadline), `moot` (nobody;
 * the subject moved on, so claiming an answer would be false), `ended` (nobody can: the agent session that asked
 * is gone).
 */
function settledMessage(kind: Decision["kind"], settledBy: SettledBy): string {
  const subject = settledSubject(kind);
  switch (settledBy) {
    case "unattended":
      return `${subject} was answered automatically because nobody was watching.`;
    case "moot":
      return `${subject} is no longer waiting for an answer.`;
    case "ended":
      return `${subject} was closed because the agent session that asked it ended, so nothing is listening for an answer.`;
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
  }
}

/** Whether d is still queued; membership, not head position: a run tab may answer an ask queued behind the chat's
 *  own, which is protocol-correct. */
function isQueued(d: Decision): boolean {
  return queues.get(d.chatID)?.includes(d) ?? false;
}

function unqueue(d: Decision): void {
  const q = queues.get(d.chatID);
  const i = q?.indexOf(d) ?? -1;
  if (q === undefined || i < 0) {
    return;
  }
  q.splice(i, 1);
  if (q.length === 0) {
    queues.delete(d.chatID);
  }
}

/**
 * Send a request decision's answer, holding its card inert until the server settles it: `decision_settled`
 * retires it, a failure hands it back, and `onAnswered` runs only once the agent has the answer.
 */
function answerRequest(
  d: Decision,
  send: () => Promise<AnswerOutcome>,
  onAnswered?: () => void,
): void {
  if (!isQueued(d) || answering.has(d)) {
    // A double answer on one request id is worse than a dropped click.
    return;
  }
  answering.add(d);
  paintAnswering(d);
  void send().then((outcome) => {
    switch (outcome) {
      case "answered":
        onAnswered?.();
        return;
      case "superseded":
        unqueue(d);
        bump();
        return;
      case "withdrawn":
        return;
      case "failed":
        answering.delete(d);
        paintAnswering(d);
        return;
      default:
        outcome satisfies never;
    }
  });
}

/** Every host showing d's card makes it inert while its answer is in flight, and live again after. */
function paintAnswering(d: Decision): void {
  const key = decisionKey(d.chatID, d.kind, decisionIdentity(d));
  for (const h of hosts) {
    if (h.renderedKey === key && h.card !== null) {
      markAnswering(h.card, answering.has(d));
    }
  }
}

function markAnswering(card: HTMLElement, on: boolean): void {
  card.inert = on;
  card.toggleAttribute("aria-busy", on);
}

/** In insertion order across queues (a run's asks come from one host session). */
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

/** Within-kind identity: the server's int64 ask id (`request_id`). */
function decisionIdentity(d: Decision): string {
  return String(d.requestID);
}

/** keyenc rather than a template join: a chat id is arbitrary text, and a separator inside it would merge two keys. */
function decisionKey(chatID: string, kind: Decision["kind"], identity: string): string {
  return join(chatID, kind, identity);
}

function renderHost(h: DockHost): void {
  const mine = matching(h);
  const head = mine[0];

  if (head === undefined) {
    // Cleared when `leaving` starts, so a second empty render does not restart the exit.
    if (h.renderedKey !== "") {
      swap(h, undefined, []);
    }
    return;
  }

  // A rebuild would discard the user's typing, so the same decision only re-states what can move in place. MUST NOT
  // start a phase.
  const key = decisionKey(head.chatID, head.kind, decisionIdentity(head));
  const rest = mine.slice(1);
  if (key === h.renderedKey) {
    paintRest(h, rest);
    return;
  }

  swap(h, head, rest);
}

// The phase machine. The next head is observed, not detected: every retirement unqueues then `bump()`s, so the render
// sees the next decision or none, and every retirement path animates the same way. The dispatch is never gated on motion.

/**
 * Swap the host's content, animating unless motion is off: nothing->head enters, head->head advances, head->nothing
 * leaves (nothing->nothing returns early in `renderHost`).
 */
function swap(h: DockHost, head: Decision | undefined, rest: readonly Decision[]): void {
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
      h.card = null;
    } else {
      show(h, head, rest);
    }
    return;
  }

  // Nothing to take on enter: the host is empty or its content already detached.
  const outgoing = phase === "entering" ? null : takeOutgoing(h);

  if (head === undefined) {
    // `.hidden` lands in `finishPhase`, after the collapse; adding it here makes the exit unanimatable.
    h.el.replaceChildren();
    h.renderedKey = "";
    h.card = null;
  } else {
    show(h, head, rest);
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
function show(h: DockHost, head: Decision, rest: readonly Decision[]): void {
  const card = buildCard(head);
  if (answering.has(head)) {
    markAnswering(card, true);
  }
  h.card = card;
  h.cardScrolls = false;
  h.el.replaceChildren(card, depthRow());
  h.el.classList.remove("hidden");
  h.renderedKey = decisionKey(head.chatID, head.kind, decisionIdentity(head));
  paintRest(h, rest);
  announce(announcementFor(head));
  h.composerShown = true;
  focusAnswerIfAside(h);
}

/**
 * When the stylesheet has hidden the bar's composer under a question card (`css/26-dock.css`), focus left in it or on
 * body moves to the card's own answer box. Only on the composer's going from rendered to hidden, so a later call never
 * pulls focus back; keyed on its rendering, so the stylesheet alone owns the condition.
 */
function focusAnswerIfAside(h: DockHost): void {
  const composer = h.el.parentElement?.querySelector(
    ':scope > :is([id="prompt-box"], [id="run-composer"])',
  );
  if (h.card === null || composer == null) {
    return;
  }
  const shown = composer.checkVisibility();
  const hidden = h.composerShown && !shown;
  h.composerShown = shown;
  const typing = document.activeElement;
  if (hidden && (typing === document.body || composer.contains(typing))) {
    h.card.querySelector<HTMLElement>(".dock-ask-text")?.focus({ preventScroll: true });
  }
}

/** A rotation can hide the composer under a card already up, and a new width or band moves the card's floor. */
function followViewport(): void {
  for (const h of hosts) {
    focusAnswerIfAside(h);
    paintFloor(h);
  }
}

/**
 * Publish the tray's floor while it holds a question card: the height of its card with each scroller in it at its own
 * `min-block-size` and every other region whole, plus the tray's other rows. Measured because the bar's flex column
 * sees only the tray's content size: a scroller's floor nested in it is invisible to the bar. Where the bar's ceiling
 * cannot hold every member's floor, they are lowered in the reverse of their order (`css/26-dock.css` "ONE
 * ALLOCATION"): the steer stack's, then the question's second line. The card's controls never give: past that, the
 * floor is the room and the card scrolls, opened at its controls.
 */
function paintFloor(h: DockHost): void {
  const card = h.card;
  if (card === null || !card.classList.contains("dock-user-input") || !card.isConnected) {
    h.el.style.removeProperty(FLOOR_PROP);
    h.cardScrolls = false;
    return;
  }
  const options = card.querySelector<HTMLElement>(":scope > .user-input-options");
  if (options !== null) {
    paintOptionsFloor(options);
  }
  const atEnd = card.scrollTop + card.clientHeight >= card.scrollHeight - 1;
  const ask = card.querySelector<HTMLElement>(":scope > .dock-ask-body");
  const stack = h.el.parentElement?.querySelector<HTMLElement>(":scope > .steer-stack") ?? null;
  ask?.style.removeProperty(ASK_FLOOR_PROP);
  stack?.style.removeProperty(STEER_FLOOR_PROP);
  const rows = flexItems(h.el).filter((row) => row !== h.outgoing);
  const px = rows.reduce(
    (sum, row) => sum + (row === card ? floorOf(card) : row.getBoundingClientRect().height),
    gapsAndChrome(h.el, rows.length),
  );
  const over = lower(stack, STEER_FLOOR_PROP, px - roomFor(h), 1);
  const left = lower(ask, ASK_FLOOR_PROP, over, 1 / 2);
  // Where the floors outgrow the room, `px - over` is that room plus the steer stack's give; the card scrolls `left`.
  const floor = px - Math.max(0, over);
  h.el.style.setProperty(
    FLOOR_PROP,
    `${String(left > 0 ? Math.max(0, Math.floor(floor)) : Math.ceil(floor))}px`,
  );
  // A card that scrolls opens at its controls, which outrank its question, and stays there unless the reader scrolled.
  if (left > 0 && (!h.cardScrolls || atEnd)) {
    card.scrollTop = card.scrollHeight;
  }
  h.cardScrolls = left > 0;
}

/** Lower `box`'s floor through `prop` by `over`, at most `share` of it, if `box` renders; what is still over. */
function lower(box: HTMLElement | null, prop: string, over: number, share: number): number {
  if (box === null || over <= 0 || !box.checkVisibility()) {
    return over;
  }
  const floor = Number.parseFloat(getComputedStyle(box).minBlockSize) || 0;
  const give = Math.min(over, floor * share);
  box.style.setProperty(prop, `${String(floor - give)}px`);
  return over - give;
}

/**
 * What the bar's ceiling leaves the tray with every other member at its floor (a shrinking item's `min-block-size`,
 * else its height); `Infinity` under no ceiling.
 */
function roomFor(h: DockHost): number {
  const bar = h.el.parentElement;
  const s = bar === null ? null : getComputedStyle(bar);
  const ceiling = s === null ? Number.NaN : Number.parseFloat(s.maxBlockSize);
  if (bar === null || s === null || Number.isNaN(ceiling)) {
    return Number.POSITIVE_INFINITY;
  }
  const px = (v: string): number => Number.parseFloat(v) || 0;
  const items = flexItems(bar);
  const chrome =
    s.boxSizing === "border-box"
      ? gapsAndChrome(bar, items.length)
      : Math.max(0, items.length - 1) * px(s.rowGap);
  return items.reduce((room, item) => {
    const is = getComputedStyle(item);
    const margins = px(is.marginBlockStart) + px(is.marginBlockEnd);
    if (item === h.el) {
      return room - margins;
    }
    const shrinks = px(is.flexShrink) > 0 && is.minBlockSize !== "auto";
    return room - margins - (shrinks ? px(is.minBlockSize) : item.getBoundingClientRect().height);
  }, ceiling - chrome);
}

/**
 * Publish the options region's floor, which `css/26-dock.css` gives it as its `min-block-size`: what stands before its
 * first control and its tallest control, so each option is whole once scrolled to, however its description wraps.
 * Never above the region's own ceiling: a minimum beats a maximum, so a taller option would push the answer controls
 * off the band; it scrolls inside the region instead.
 */
function paintOptionsFloor(region: HTMLElement): void {
  const controls = [...region.children].filter(
    (c) => c.matches(".user-input-option, .user-input-sub-option") && c.checkVisibility(),
  );
  const [first] = controls;
  if (first === undefined || region.firstElementChild === null) {
    region.style.removeProperty(OPTIONS_FLOOR_PROP);
    return;
  }
  const lead =
    first.getBoundingClientRect().top - region.firstElementChild.getBoundingClientRect().top;
  const tallest = Math.max(...controls.map((c) => c.getBoundingClientRect().height));
  const px = Math.ceil(lead + tallest + gapsAndChrome(region, 1));
  const ceiling = Number.parseFloat(getComputedStyle(region).maxBlockSize);
  const floor = Number.isNaN(ceiling) ? px : Math.min(px, Math.floor(ceiling));
  region.style.setProperty(OPTIONS_FLOOR_PROP, `${String(floor)}px`);
}

function floorOf(box: Element): number {
  const items = flexItems(box);
  return items.reduce(
    (sum, item) => {
      const s = getComputedStyle(item);
      const scrolls = item instanceof HTMLTextAreaElement || s.overflowY !== "visible";
      return (
        sum +
        (scrolls ? Number.parseFloat(s.minBlockSize) || 0 : item.getBoundingClientRect().height)
      );
    },
    gapsAndChrome(box, items.length),
  );
}

/** A column's rendered flex items: a `display: contents` child's children stand in for it. */
function flexItems(box: Element): Element[] {
  return [...box.children]
    .flatMap((c) => (getComputedStyle(c).display === "contents" ? [...c.children] : [c]))
    .filter((c) => c.checkVisibility());
}

function gapsAndChrome(box: Element, items: number): number {
  const s = getComputedStyle(box);
  const px = (v: string): number => Number.parseFloat(v) || 0;
  return (
    px(s.paddingBlockStart) +
    px(s.paddingBlockEnd) +
    px(s.borderBlockStartWidth) +
    px(s.borderBlockEndWidth) +
    Math.max(0, items - 1) * px(s.rowGap)
  );
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
 * the answer paths' membership guards are authoritative).
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

/** The queue behind the head, as a count. Scoped to a DIRECT child: a stale row inside `.dock-outgoing` sits earlier
 *  in document order. */
function paintRest(h: DockHost, rest: readonly Decision[]): void {
  const row = h.el.querySelector(":scope > .dock-depth");
  if (row !== null) {
    row.textContent = rest.length === 0 ? "" : `${String(rest.length)} more waiting`;
    row.classList.toggle("hidden", rest.length === 0);
  }
}

function depthRow(): HTMLElement {
  return el("p", { className: "dock-depth hidden" });
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
    default:
      d satisfies never;
      return "";
  }
}

function buildCard(d: Decision): HTMLElement {
  switch (d.kind) {
    case "permission":
      return buildPermissionCard(d.chatID, d.payload, (answer) => {
        answerRequest(d, () => d.submit(answer));
      });
    case "elicitation":
      return buildElicitationCard(d.payload, (action, content) => {
        answerRequest(d, () => d.submit(action, content));
      });
    case "user_input":
      return buildUserInputCard(d.payload, (action, answer) => {
        answerRequest(
          d,
          () => d.submit(action, answer),
          // Never act on an ask another surface answered, or before the agent has it.
          action === "answered" && answer !== undefined
            ? () => {
                emitBus(BUS_USER_INPUT_ANSWERED, { chatID: d.chatID, answer });
              }
            : undefined,
        );
      });
    default:
      d satisfies never;
      return el("div");
  }
}

/** Reset module state between tests. `endPhase` first: a pending timer would fire into the next test's DOM. */
// deadset:ignore DS1004 -- test seam: resets the mounted hosts, queues and held answers
export function _resetForTest(): void {
  for (const h of hosts) {
    endPhase(h);
    h.stop();
  }
  queues.clear();
  hosts.length = 0;
  queueVersion.value = 0;
}

/** @internal Number of mounted hosts: the one observable of a release. */
// deadset:ignore DS1004 -- test seam: observes the mounted host list
export function _hostCount(): number {
  return hosts.length;
}
