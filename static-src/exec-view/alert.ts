// The run's alert as a mark and a sentence: one painter for the page's alert and the dock's row,
// so the two never disagree on its kind, glyph or words.

import { el } from "@cplieger/reactive";
import { iconEl } from "../icon-el.js";
import { ICON_ALERT, ICON_WARN } from "../icons.js";
import type { ExecRun } from "./model.js";

type ExecAlert = NonNullable<ExecRun["alert"]>;

export function alertGlyph(kind: ExecAlert["kind"]): string {
  return kind === "failed" ? ICON_ALERT : ICON_WARN;
}

/** A kind that asks something of the reader. A `stopped` alert states what a person already did,
 *  so it never stands in for the run's summary. */
export function wantsReader(alert: ExecRun["alert"]): alert is ExecAlert {
  return alert !== undefined && alert.kind !== "stopped";
}

interface AlertLine {
  readonly root: HTMLElement;
  paint(alert: ExecAlert): void;
}

export function buildAlertLine(tag: "div" | "span", className: string): AlertLine {
  const icon = el("span", { className: "ev-alert-icon", "aria-hidden": "true" });
  const text = el("span", { className: "ev-alert-text" });
  const root = el(tag, { className }, icon, text);
  let glyph = "";
  return {
    root,
    paint(alert) {
      root.dataset["kind"] = alert.kind;
      const want = alertGlyph(alert.kind);
      if (glyph !== want) {
        glyph = want;
        icon.replaceChildren(iconEl(want));
      }
      if (text.textContent !== alert.text) {
        text.textContent = alert.text;
      }
    },
  };
}
