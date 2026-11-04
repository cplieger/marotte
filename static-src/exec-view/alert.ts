// The run's alert as a mark and a sentence, painted by the run summary (`summary.ts`) wherever it
// shows.

import { el } from "@cplieger/ui";
import { iconEl } from "../icon-el.js";
import { ICON_ALERT } from "../icons.js";
import type { ExecRun } from "./model.js";

export type ExecAlert = NonNullable<ExecRun["alert"]>;

/** A kind that asks something of the reader. A `stopped` alert states what a person already did,
 *  so it never stands in for the run's summary. */
export function wantsReader(alert: ExecRun["alert"]): alert is ExecAlert {
  return alert !== undefined && alert.kind !== "stopped";
}

interface AlertLine {
  readonly root: HTMLElement;
  paint(alert: ExecAlert): void;
}

/** The sentence, led by a red mark on a failure only: a run bar row's glyph marks a question or a
 *  pause, and nothing else marks a failed run. */
export function buildAlertLine(tag: "div" | "span", className: string): AlertLine {
  const icon = el(
    "span",
    { className: "ev-alert-icon", "aria-hidden": "true" },
    iconEl(ICON_ALERT),
  );
  const text = el("span", { className: "ev-alert-text" });
  const root = el(tag, { className }, text);
  return {
    root,
    paint(alert) {
      root.dataset["kind"] = alert.kind;
      if (alert.kind !== "failed") {
        icon.remove();
      } else if (icon.parentElement !== root) {
        root.prepend(icon);
      }
      if (text.textContent !== alert.text) {
        text.textContent = alert.text;
      }
    },
  };
}
