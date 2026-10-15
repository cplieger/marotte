// The run's one-line summary: name, state word, progress, mini timeline and clock, or the alert
// that wants the reader in their place. One painter for the run dock's head and the chat's run bar
// rows, so the two never word a run differently.

import { el } from "@cplieger/reactive";
import { reconcile } from "../reconcile.js";
import { formatElapsed } from "../strings.js";
import {
  currentWork,
  summaryProgress,
  window as execWindow,
  type ExecNode,
  type ExecRun,
} from "./model.js";
import { STATE_WORD } from "./status.js";
import { buildAlertLine, wantsReader } from "./alert.js";
import { kindGlyph } from "./tree.js";

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
  // Two forms, the narrow container showing one (31-exec-view.css): the long one stays in the
  // accessibility tree at every width, the glyph form never enters it.
  const progressLong = el("span", { className: "ev-sum-long" });
  const progressShort = el("span", { className: "ev-sum-short-text" });
  const progress = el(
    "span",
    { className: "ev-sum-progress" },
    progressLong,
    el(
      "span",
      { className: "ev-sum-short", "aria-hidden": "true" },
      kindGlyph("repeat"),
      progressShort,
    ),
  );
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
      const phrase = summaryProgress(run);
      setText(progressLong, phrase.long);
      setText(progressShort, phrase.short);
      progress.toggleAttribute("data-loop", phrase.short !== "");
      const paintSeg = (seg: HTMLElement, n: ExecNode): void => {
        if (seg.dataset["state"] !== n.state) {
          seg.dataset["state"] = n.state;
        }
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
        : [run.label, word, phrase.pass, phrase.steps].filter((s) => s !== "").join(", ");
    },
    paintPending(name) {
      setText(label, name);
      for (const part of [state, progressLong, progressShort, clock]) {
        setText(part, "");
      }
      progress.removeAttribute("data-loop");
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
