// The exec view's detail pane: one node's facts, failure, and the transcript host it hands OUT
// (`bodyFor`). No capture: the page's per-step results region renders that.

import { el } from "@cplieger/reactive";
import { formatElapsed } from "../strings.js";
import { elapsed, type ExecNode } from "./model.js";
import { STATE_WORD } from "./status.js";

export interface ExecDetailView {
  readonly root: HTMLElement;
  /** Show one node. `undefined` renders the empty state. */
  render(node: ExecNode | undefined): void;
  /** The element this node's live transcript renders into, created on demand and kept per PATH for
   *  the pane's life: frames are live-only and cannot be replayed. */
  bodyFor(path: string): HTMLElement;
  /** Advance the duration of a node still running. */
  tick(): void;
}

/** What a node with no transcript host says; injected so the consumer owns the wording. */
export type EmptyNote = (node: ExecNode) => string;

/** An affordance BESIDE that note, or null: a run whose steps are in the launching chat can offer
 *  to open it; a delegate's page has nowhere new to send anyone. */
export type EmptyAction = (node: ExecNode) => HTMLElement | null;

export function buildExecDetail(emptyNote: EmptyNote, emptyAction?: EmptyAction): ExecDetailView {
  const title = el("span", { className: "ev-d-title" });
  const state = el("span", { className: "ev-d-state" });
  const dur = el("span", { className: "ev-d-dur" });
  const head = el(
    "div",
    { className: "ev-d-head" },
    title,
    el("span", { className: "ev-d-meta" }, state, dur),
  );
  const facts = el("dl", { className: "ev-d-facts" });
  const failure = el("div", { className: "ev-d-fail", role: "status" });
  const bodies = el("div", { className: "ev-d-bodies" });
  const empty = el("div", { className: "ev-d-empty" });
  const emptyAct = el("div", { className: "ev-d-empty-action", hidden: true });
  const root = el(
    "div",
    { className: "ev-detail", "aria-live": "polite" },
    head,
    facts,
    failure,
    bodies,
    empty,
    emptyAct,
  );

  const hosts = new Map<string, HTMLElement>();
  let shown: ExecNode | undefined;

  function render(node: ExecNode | undefined): void {
    shown = node;
    if (node === undefined) {
      root.dataset["state"] = "pending";
      title.textContent = "No step selected";
      state.textContent = "";
      dur.textContent = "";
      facts.replaceChildren();
      facts.hidden = true;
      failure.hidden = true;
      bodies.hidden = true;
      empty.hidden = false;
      empty.textContent = "Pick a step to see what it did.";
      emptyAct.replaceChildren();
      emptyAct.hidden = true;
      return;
    }
    root.dataset["state"] = node.state;
    title.textContent = node.label;
    state.textContent = STATE_WORD[node.state];
    const ms = elapsed(node.start, node.end);
    dur.textContent = ms > 0 ? formatElapsed(ms) : "";

    const list = node.facts ?? [];
    facts.hidden = list.length === 0;
    facts.replaceChildren(
      ...list.flatMap((f) => [
        el("dt", { className: "ev-d-k" }, f.label),
        el("dd", { className: f.mono === true ? "ev-d-v ev-mono" : "ev-d-v" }, f.value),
      ]),
    );

    failure.hidden = node.failure === undefined;
    failure.textContent = node.failure ?? "";

    // Only ONE node's transcript is on screen; the rest stay in the DOM since
    // their live content cannot be replayed.
    let anyShown = false;
    for (const [path, host] of hosts) {
      const isShown = path === node.path;
      host.hidden = !isShown;
      anyShown = anyShown || (isShown && host.childElementCount > 0);
    }
    bodies.hidden = false;
    // A container hosts nothing; saying so beats an empty region.
    const hostable = node.transcript === true;
    empty.hidden = anyShown;
    empty.textContent = hostable ? emptyNote(node) : "";
    if (!hostable) {
      empty.hidden = true;
    }

    const action = hostable ? (emptyAction?.(node) ?? null) : null;
    // IDENTITY-guarded: `render` runs on every invalidation and re-seating BLURS the link; the
    // consumer returns a cached element.
    if (action !== emptyAct.firstElementChild) {
      emptyAct.replaceChildren(...(action === null ? [] : [action]));
    }
    emptyAct.hidden = action === null || empty.hidden;
  }

  return {
    root,
    render,
    bodyFor(path) {
      let host = hosts.get(path);
      if (host === undefined) {
        host = el("div", { className: "ev-d-body", "data-path": path });
        host.hidden = shown?.path !== path;
        hosts.set(path, host);
        bodies.appendChild(host);
      }
      // A first frame for the shown node retires the note and its action in the same pass.
      if (shown?.path === path) {
        empty.hidden = true;
        emptyAct.hidden = true;
      }
      return host;
    },
    tick() {
      if (shown === undefined || shown.end !== undefined) {
        return;
      }
      const ms = elapsed(shown.start, undefined);
      if (ms > 0) {
        dur.textContent = formatElapsed(ms);
      }
    },
  };
}
