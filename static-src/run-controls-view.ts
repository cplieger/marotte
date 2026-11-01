// The run's verbs as one row of buttons, drawn from the server's `GET /api/runs/{id}/controls`
// answer (`run-store.ts` `runControls`) and rebuilt only when that answer moves. The run dock is
// its one surface.

import { createRoot, el } from "@cplieger/reactive";
import { bindLoadingState } from "./actions/index.js";
import {
  cancelRun,
  extendRunRepeat,
  finishRunRepeat,
  pauseRun,
  resumeRun,
  retryRun,
} from "./actions/runs.js";
import { iconEl } from "./icon-el.js";
import {
  ICON_CANCEL,
  ICON_PAUSE,
  ICON_PLAY,
  ICON_PLUS_UI,
  ICON_REFRESH,
  ICON_SAVE_OK,
} from "./icons.js";
import { CONTROL_LABEL, offeredVerbs, refusalSentences, type RunVerb } from "./run-controls.js";
import { runControls } from "./run-store.js";
import type { RunControlsResponse } from "./wire/types.gen.js";

export interface RunControlsView {
  /** The row for the current answer, or null with no verb to draw; the SAME element while the
   *  answer holds, so a repaint seats nothing new and focus inside it survives. Reads the
   *  answer's signal, so a caller's effect follows it. */
  current(): HTMLElement | null;
  /** The server's sentences for a run it offers no verb on, in the order they show. */
  refusals(): readonly string[];
  /** Release every pending binding and forget the row. */
  dispose(): void;
}

/** Largest count one extend may add; the server refuses anything past it. */
const MAX_EXTEND = 1000;

type RepeatVerb = "extend" | "finish_loop";

const RUN_ACTION: Record<
  Exclude<RunVerb, RepeatVerb>,
  { readonly name: string; dispatch: (id: string) => Promise<unknown> }
> = {
  pause: pauseRun,
  resume: resumeRun,
  cancel: cancelRun,
  retry: retryRun,
};

const ICON: Record<RunVerb, string> = {
  pause: ICON_PAUSE,
  resume: ICON_PLAY,
  extend: ICON_PLUS_UI,
  finish_loop: ICON_SAVE_OK,
  cancel: ICON_CANCEL,
  retry: ICON_REFRESH,
};

/** What each verb says on hover; a button shows its icon alone, its name in `aria-label`. */
const TIP: Record<RunVerb, string> = {
  pause: "Pause the run at the end of its current step",
  resume: "Resume the run",
  extend: "Add passes to the paused loop",
  finish_loop: "End the paused loop and go on",
  cancel: "Cancel the run",
  retry: "Retry the failed steps",
};

export function buildRunControlsView(workflowID: string): RunControlsView {
  let sig = "";
  let row: HTMLElement | null = null;
  // `bindLoadingState` self-disposes only for an element attached when its effect last ran, and a
  // row is built before the dock seats it, so a row replaced first would leak its bindings. Each
  // row's bindings are made under its own root, which `drop` releases.
  let release: (() => void) | null = null;

  const drop = (): void => {
    release?.();
    release = null;
    row = null;
  };

  return {
    current() {
      const answer = runControls(workflowID);
      const next = answer === undefined ? "" : signature(answer);
      if (next !== sig) {
        drop();
        sig = next;
        row =
          answer === undefined
            ? null
            : createRoot((dispose) => {
                release = dispose;
                return render(workflowID, answer);
              });
      }
      return row;
    },
    refusals() {
      const answer = runControls(workflowID);
      if (answer === undefined || offeredVerbs(answer.verbs).length > 0) {
        return [];
      }
      return refusalSentences(answer.refused);
    },
    dispose() {
      drop();
      sig = "";
    },
  };
}

/** What the row is a function of: the verbs it draws, the refusals and the node a repeat verb
 *  addresses. Keyed, not positional, since `refused` is a map the server may serialize in any order. */
function signature(answer: RunControlsResponse): string {
  const refused = Object.entries(answer.refused ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([verb, text]) => `${verb}\u0001${text}`)
    .join("\u0002");
  return `${answer.verbs.join("\u0001")}\u0000${refused}\u0000${answer.pause_node_id ?? ""}`;
}

function render(workflowID: string, answer: RunControlsResponse): HTMLElement | null {
  const nodeID = answer.pause_node_id ?? "";
  const out = el("span", { className: "run-controls" });
  for (const verb of offeredVerbs(answer.verbs)) {
    if (verb === "extend" || verb === "finish_loop") {
      // The repeat verbs address the node the server reported as paused, never one derived here.
      if (nodeID === "") {
        continue;
      }
      if (verb === "finish_loop") {
        const btn = button(verb, () => void finishRunRepeat.dispatch({ workflowID, nodeID }));
        bindLoadingState(finishRunRepeat.name, btn);
        out.append(btn);
        continue;
      }
      const count = el("input", {
        type: "number",
        className: "tool-form-input run-extend-count",
        min: "1",
        max: String(MAX_EXTEND),
        step: "1",
        value: "1",
        "aria-label": "Passes to add",
      }) as HTMLInputElement;
      const btn = button(verb, () => {
        if (!count.reportValidity()) {
          return;
        }
        void extendRunRepeat.dispatch({ workflowID, nodeID, iterations: count.valueAsNumber });
      });
      bindLoadingState(extendRunRepeat.name, btn);
      out.append(el("span", { className: "run-extend" }, count, btn));
      continue;
    }
    const action = RUN_ACTION[verb];
    // No optimistic flip: every verb settles at a node boundary, so the run still reads `running`
    // when the reply lands, and the `run_progress` invalidation repaints the row.
    const btn = button(verb, () => void action.dispatch(workflowID));
    // A retry loads the run's session and can take tens of seconds; unbound, the button looks dead
    // and can be clicked again meanwhile.
    bindLoadingState(action.name, btn);
    out.append(btn);
  }
  return out.childElementCount === 0 ? null : out;
}

function button(verb: RunVerb, onclick: () => void): HTMLButtonElement {
  return el(
    "button",
    {
      type: "button",
      className: verb === "cancel" ? "icon-btn run-icon-btn is-danger" : "icon-btn run-icon-btn",
      "aria-label": CONTROL_LABEL[verb],
      "data-tooltip": TIP[verb],
      onclick,
    },
    iconEl(ICON[verb]),
  ) as HTMLButtonElement;
}
