// The run's one-line summary: name, state word, progress, mini timeline and clock, or the alert
// that wants the reader in their place. One painter for the run dock's head and the chat's run bar
// rows, so the two never word a run differently.

import { el } from "@cplieger/reactive";
import { reconcile } from "../reconcile.js";
import { formatElapsed } from "../strings.js";
import {
  counters,
  currentWork,
  progressText,
  window as execWindow,
  type ExecNode,
  type ExecRun,
} from "./model.js";
import { STATE_WORD } from "./status.js";
import { buildAlertLine, wantsReader } from "./alert.js";

export interface ExecSummaryView {
  /** `.ev-sum`. */
  readonly root: HTMLElement;
  /** Paint from the model; text writes skip unchanged values, so the 1s tick is cheap. */
  paint(run: ExecRun): void;
  /** A run with no model yet: the name alone, every other part empty. */
  paintPending(label: string): void;
  /** "<name>, <state>, <progress>" or "<name>: <alert sentence>", for the consumer's accessible name. */
  describe(): string;
}

interface SummaryOpts {
  /** Receives `data-alert="<kind>"` while an alert wants the reader, so the consumer's container
   *  takes the wash. */
  readonly washHost: HTMLElement;
  /** Whether the alert line sits in a `role=status` / `role=alert` pair (the dock) or is plain text
   *  (a chat row, whose tab dot and notification already announce it). */
  readonly live: boolean;
}

export function buildExecSummary(opts: SummaryOpts): ExecSummaryView {
  const label = el("span", { className: "ev-sum-label" });
  const state = el("span", { className: "ev-sum-state" });
  const progress = el("span", { className: "ev-sum-progress" });
  const mini = el("span", { className: "ev-mini", "aria-hidden": "true" });
  const clock = el("span", { className: "ev-sum-clock" });
  // A failure interrupts and anything else waits its turn: two live hosts that stay rendered (a
  // region inserted together with its text is not announced), each with its own line, so a kind
  // change never writes the new sentence inside the old severity's region.
  const politeHost = el("span", { className: "ev-sum-live" });
  const urgentHost = el("span", { className: "ev-sum-live" });
  const politeLine = buildAlertLine("span", "ev-alert-line");
  const urgentLine = buildAlertLine("span", "ev-alert-line");
  if (opts.live) {
    politeHost.setAttribute("role", "status");
    urgentHost.setAttribute("role", "alert");
  }
  const root = el(
    "span",
    { className: "ev-sum" },
    label,
    state,
    progress,
    mini,
    clock,
    politeHost,
    urgentHost,
  );
  let described = "";

  const paintAlert = (run: ExecRun): void => {
    const alert = wantsReader(run.alert) ? run.alert : undefined;
    if (alert === undefined) {
      delete root.dataset["alerting"];
      delete opts.washHost.dataset["alert"];
      politeHost.replaceChildren();
      urgentHost.replaceChildren();
      return;
    }
    root.dataset["alerting"] = "";
    opts.washHost.dataset["alert"] = alert.kind;
    const [into, line, away] =
      alert.kind === "failed"
        ? [urgentHost, urgentLine, politeHost]
        : [politeHost, politeLine, urgentHost];
    away.replaceChildren();
    line.paint(alert);
    if (line.root.parentElement !== into) {
      into.replaceChildren(line.root);
    }
  };

  return {
    root,
    paint(run) {
      setText(label, run.label);
      const word = run.alert?.kind === "input" ? "needs input" : STATE_WORD[run.state];
      setText(state, word);
      // Withheld for a one-step run: "step 1 of 1" cannot progress, and the state word says how it ended.
      const steps = counters(run.nodes).total <= 1 ? "" : progressText(run.nodes);
      setText(progress, steps);
      const paintSeg = (seg: HTMLElement, n: ExecNode): void => {
        seg.dataset["state"] = n.state;
        const tip = `${n.label}, ${STATE_WORD[n.state]}`;
        if (seg.getAttribute("data-tooltip") !== tip) {
          seg.setAttribute("data-tooltip", tip);
        }
      };
      reconcile(mini, currentWork(run.nodes), {
        key: (n) => n.path,
        mount: (n) => {
          const seg = el("span", { className: "ev-mini-seg" });
          paintSeg(seg, n);
          return seg;
        },
        update: paintSeg,
      });
      const win = execWindow(run.nodes, run.live);
      setText(clock, win === undefined ? "" : formatElapsed(win.span));
      paintAlert(run);
      described = wantsReader(run.alert)
        ? `${run.label}: ${run.alert.text}`
        : [run.label, word, steps].filter((s) => s !== "").join(", ");
    },
    paintPending(name) {
      setText(label, name);
      for (const part of [state, progress, clock]) {
        setText(part, "");
      }
      mini.replaceChildren();
      delete root.dataset["alerting"];
      delete opts.washHost.dataset["alert"];
      politeHost.replaceChildren();
      urgentHost.replaceChildren();
      described = name;
    },
    describe() {
      return described;
    },
  };
}

/** A text write that skips an unchanged value, so a repaint announces nothing it did not change. */
function setText(target: HTMLElement, text: string): void {
  if (target.textContent !== text) {
    target.textContent = text;
  }
}
