// The run's one-line summary: name, state word, progress, mini bar and clock. One painter for the
// run dock's head and the chat's run bar rows, so the two never word a run differently. A question
// or a pause takes the progress's place (`inPlace`); a run bar row puts a failure on a line of its
// own, and the dock gives every alert it does not show here a region of its own.

import { el } from "@cplieger/reactive";
import { reconcile } from "../reconcile.js";
import { formatElapsed } from "../strings.js";
import {
  miniBar,
  summaryProgress,
  window as execWindow,
  type ExecRun,
  type MiniSeg,
  type ProgressPhrase,
} from "./model.js";
import { STATE_WORD } from "./status.js";
import { buildAlertLine, inPlace, wantsReader, type ExecAlert } from "./alert.js";
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
  /** Where the alert shows, taking `data-alert="<kind>"` for its mark's colour and
   *  `data-alert-in-place` when it takes the progress's place (`inPlace`); absent where the
   *  consumer shows every alert itself. */
  readonly alertHost?: HTMLElement;
  /** Which alerts the summary shows, every one that wants the reader by default; the consumer
   *  shows the rest. */
  readonly shows?: (alert: ExecAlert) => boolean;
}

export function buildExecSummary(opts: SummaryOpts = {}): ExecSummaryView {
  const label = el("span", { className: "ev-sum-label" });
  const state = el("span", { className: "ev-sum-state" });
  const progress = buildProgress();
  const mini = el("span", { className: "ev-mini", "aria-hidden": "true" });
  const clock = el("span", { className: "ev-sum-clock" });
  // Plain text: a run bar row's tab dot and notification already announce it.
  const line = buildAlertLine("span", "ev-alert-line");
  const root = el("span", { className: "ev-sum" }, label, state, progress.root, mini, clock);
  let described = "";
  const shows = opts.shows ?? (() => true);
  const shown = (alert: ExecRun["alert"]): alert is ExecAlert => wantsReader(alert) && shows(alert);

  const paintAlert = (run: ExecRun | undefined): void => {
    const host = opts.alertHost;
    if (host === undefined) {
      return;
    }
    const alert = run !== undefined && shown(run.alert) ? run.alert : undefined;
    if (alert === undefined) {
      delete host.dataset["alert"];
      host.removeAttribute("data-alert-in-place");
      line.root.remove();
      return;
    }
    host.dataset["alert"] = alert.kind;
    const placed = inPlace(alert);
    host.toggleAttribute("data-alert-in-place", placed);
    line.paint(alert);
    // In the progress's place the line is clipped, so the tooltip carries it whole.
    if (!placed) {
      line.root.removeAttribute("data-tooltip");
    } else if (line.root.getAttribute("data-tooltip") !== alert.text) {
      line.root.setAttribute("data-tooltip", alert.text);
    }
    // Before the clock: the dock head lays the summary out in source order.
    if (line.root.nextSibling !== clock) {
      root.insertBefore(line.root, clock);
    }
  };

  return {
    root,
    paint(run) {
      setLabel(label, run.label);
      const word = run.alert?.kind === "input" ? "needs input" : STATE_WORD[run.state];
      setText(state, word);
      const phrase = summaryProgress(run);
      progress.paint(phrase);
      paintMini(mini, miniBar(run));
      const win = execWindow(run.nodes, run.live);
      setText(clock, win === undefined ? "" : formatElapsed(win.span));
      paintAlert(run);
      described =
        opts.alertHost !== undefined && shown(run.alert)
          ? `${run.label}: ${run.alert.text}`
          : [run.label, word, phrase.words].filter((s) => s !== "").join(", ");
    },
    paintPending(name) {
      setLabel(label, name);
      for (const part of [state, clock]) {
        setText(part, "");
      }
      progress.paint(undefined);
      mini.replaceChildren();
      paintAlert(undefined);
      described = name;
    },
    describe() {
      return described;
    },
  };
}

interface ProgressView {
  readonly root: HTMLElement;
  /** Paint a phrase, or clear it with none. */
  paint(phrase: ProgressPhrase | undefined): void;
}

/** The progress phrase as every run surface draws it: the compact form ("↻15/50 › 2/4 · 1/4", the
 *  repeat glyph standing for "pass") on screen, the words in the accessibility tree, the phrase's
 *  tip in the tooltip. The compact form never enters the accessibility tree. */
export function buildProgress(): ProgressView {
  const pass = el("span", { className: "ev-sum-pass" });
  const passText = el("span", { className: "ev-sum-frac" });
  pass.append(kindGlyph("repeat"), passText);
  const inner = el("span", { className: "ev-sum-inner" });
  const dot = el("span", { className: "ev-sum-dot" });
  const steps = el("span", { className: "ev-sum-steps" });
  const words = el("span", { className: "sr-only" });
  const root = el(
    "span",
    { className: "ev-sum-progress" },
    el("span", { className: "ev-sum-compact", "aria-hidden": "true" }, pass, inner, dot, steps),
    words,
  );
  return {
    root,
    paint(phrase) {
      const p = phrase ?? { pass: "", inner: "", steps: "", words: "", tip: "" };
      setText(passText, p.pass);
      setHidden(pass, p.pass === "");
      setText(inner, p.inner === "" ? "" : `\u203a ${p.inner}`);
      setHidden(inner, p.inner === "");
      const joined = p.pass !== "" && p.steps !== "";
      setText(dot, joined ? "\u00b7" : "");
      setHidden(dot, !joined);
      setText(steps, p.steps);
      setHidden(steps, p.steps === "");
      setText(words, p.words);
      setHidden(root, p.words === "");
      if (p.tip === "") {
        root.removeAttribute("data-tooltip");
      } else if (root.getAttribute("data-tooltip") !== p.tip) {
        root.setAttribute("data-tooltip", p.tip);
      }
    },
  };
}

function paintMini(mini: HTMLElement, segs: readonly MiniSeg[]): void {
  const n = String(Math.max(1, segs.length + segs.filter((s) => s.branch).length));
  if (mini.style.getPropertyValue("--mini-n") !== n) {
    mini.style.setProperty("--mini-n", n);
  }
  const paintSeg = (seg: HTMLElement, s: MiniSeg): void => {
    if (seg.dataset["state"] !== s.state) {
      seg.dataset["state"] = s.state;
    }
    if (seg.getAttribute("data-tooltip") !== s.tip) {
      seg.setAttribute("data-tooltip", s.tip);
    }
    seg.toggleAttribute("data-branch", s.branch);
  };
  reconcile(mini, segs, {
    key: (s) => s.key,
    mount: (s) => {
      const seg = el("span", { className: "ev-mini-seg" });
      paintSeg(seg, s);
      return seg;
    },
    update: paintSeg,
  });
}

/** A `hidden` write that skips an unchanged value: a same-value write still records a mutation. */
function setHidden(target: HTMLElement, hidden: boolean): void {
  if (target.hidden !== hidden) {
    target.hidden = hidden;
  }
}

/** The run's name, whose column ellipsizes a long one: the full name is its tooltip too. */
function setLabel(label: HTMLElement, name: string): void {
  setText(label, name);
  if (label.getAttribute("data-tooltip") !== name) {
    label.setAttribute("data-tooltip", name);
  }
}

/** A text write that skips an unchanged value, so a repaint announces nothing it did not change. */
function setText(target: HTMLElement, text: string): void {
  if (target.textContent !== text) {
    target.textContent = text;
  }
}
