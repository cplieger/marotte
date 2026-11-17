// The list row both page lists build (History, the configuration browser), and its container: height, inset,
// truncation and hit target are decided once; pages vary only in which slots they fill.

import { el } from "@cplieger/reactive";
import { relativeTime, absoluteTime } from "./relative-time.js";

/** The subtitle: one ellipsised line, a two-line description, or a pair of single-line facts. */
export type EntrySub =
  | { readonly kind: "line"; readonly text: string }
  | { readonly kind: "clamp"; readonly text: string }
  | { readonly kind: "lines"; readonly lines: readonly EntrySubLine[] };

interface EntrySubLine {
  readonly text: string;
  /** Written as `data-line`, so a repaint finds the line by name: a line with no text is not drawn. */
  readonly key?: string;
  /** A path, a matcher or a command: an identifier rather than prose. */
  readonly mono?: boolean;
  /** Why the row's subject cannot do its job, in the failure ink. */
  readonly error?: boolean;
}

/** A timestamp (relative text plus absolute tooltip) or verbatim text with no tooltip (a count has no absolute form). */
type EntryTime = { readonly ms: number } | { readonly text: string };

export interface EntryRowSpec {
  /** The row's identity as `data-key`, one spelling so a delegated `closest("[data-key]")` reads the same everywhere. */
  readonly key: string;
  readonly title: string;
  readonly lead?: HTMLElement | undefined;
  /** A chip belonging to the name (a git letter), seated against the title's last glyph, outside the badge cap. */
  readonly mark?: HTMLElement | undefined;
  readonly badges?: readonly HTMLElement[] | undefined;
  readonly time?: EntryTime | undefined;
  /** Absent or empty: no subtitle; the row fits its title. */
  readonly sub?: EntrySub | undefined;
  readonly actions?: readonly HTMLElement[] | undefined;
  /** Present for a door: the body becomes the open control named `Open <name>`. Absent: inert, no role or listener. */
  readonly open?: { readonly name: string; readonly onOpen: () => void } | undefined;
  /** Extra attributes, by full attribute name (`data-hook-id`). */
  readonly data?: Readonly<Record<string, string>> | undefined;
}

/** A third badge is dropped, not wrapped: the badge column beside the actions is one line. */
const MAX_BADGES = 2;

/** Null when nothing is left to draw: a line with no text is dropped, never drawn as a blank band. */
function subNode(sub: EntrySub | undefined): HTMLElement | null {
  if (sub === undefined) {
    return null;
  }
  if (sub.kind === "lines") {
    const lines = sub.lines.filter((l) => l.text !== "");
    if (lines.length === 0) {
      return null;
    }
    return el(
      "span",
      { className: "entry-lines" },
      ...lines.map((l) => {
        const cls = ["entry-sub"];
        if (l.mono === true) {
          cls.push("entry-sub-mono");
        }
        if (l.error === true) {
          cls.push("entry-sub-error");
        }
        return el(
          "span",
          { className: cls.join(" "), ...(l.key === undefined ? {} : { "data-line": l.key }) },
          l.text,
        );
      }),
    );
  }
  if (sub.text === "") {
    return null;
  }
  const cls = sub.kind === "clamp" ? "entry-sub entry-sub-clamp" : "entry-sub";
  return el("span", { className: cls }, sub.text);
}

/** Repaint a kept row's subtitle in place: replaced, added after the title line, or removed. */
export function replaceEntrySub(row: HTMLElement, sub: EntrySub | undefined): void {
  const body = row.querySelector<HTMLElement>(":scope > .entry-open, :scope > .entry-body");
  if (body === null) {
    return;
  }
  const old = body.querySelector(":scope > .entry-sub, :scope > .entry-lines");
  const next = subNode(sub);
  if (next === null) {
    old?.remove();
  } else if (old === null) {
    body.querySelector(":scope > .entry-line")?.after(next);
  } else {
    old.replaceWith(next);
  }
}

function timeNode(time: EntryTime): HTMLElement {
  if (!("ms" in time)) {
    // Not a `<time>`: the slot is carrying something that is not a timestamp.
    return el("span", { className: "entry-time" }, time.text);
  }
  const text = relativeTime(time.ms);
  return el(
    "time",
    {
      className: "entry-time",
      datetime: new Date(time.ms).toISOString(),
      "data-tooltip": absoluteTime(time.ms),
      "data-clock-tip": "full",
    },
    text,
  );
}

/**
 * One row. The body's descendants are only `span`/`time`: a door's body is a `<button>`, which takes phrasing
 * content only (the CSS blockifies them).
 */
export function entryRow(spec: EntryRowSpec): HTMLElement {
  const line = el(
    "span",
    { className: "entry-line" },
    el(
      "span",
      { className: "entry-name" },
      el("span", { className: "entry-title" }, spec.title),
      spec.mark ?? null,
    ),
    spec.time !== undefined ? timeNode(spec.time) : null,
  );
  const sub = subNode(spec.sub);

  const body =
    spec.open !== undefined
      ? el(
          "button",
          {
            type: "button",
            className: "entry-open",
            "aria-label": `Open ${spec.open.name}`,
            onclick: spec.open.onOpen,
          },
          line,
          sub,
        )
      : el("div", { className: "entry-body" }, line, sub);

  return el(
    "div",
    {
      className: "entry",
      role: "listitem",
      "data-key": spec.key,
      ...spec.data,
    },
    spec.lead ?? null,
    body,
    spec.badges !== undefined && spec.badges.length > 0
      ? el("span", { className: "entry-badges" }, ...spec.badges.slice(0, MAX_BADGES))
      : null,
    spec.actions !== undefined && spec.actions.length > 0
      ? el("span", { className: "entry-actions" }, ...spec.actions)
      : null,
  );
}

/** The rows' card: `role="list"` with `role="listitem"` rows; seams are the container's fill through its `--hairline` gap. */
export function entryList(): HTMLElement {
  return el("div", { className: "list-container", role: "list" });
}

/**
 * A region a row grows below itself as its next sibling, so the row never moves. It takes a list-item role, since a
 * list may own only list items.
 */
export function entryDetail(...children: HTMLElement[]): HTMLElement {
  return el("div", { className: "entry-detail", role: "listitem" }, ...children);
}

/**
 * N placeholder rows, each the height of a title-plus-one-line row.
 * `aria-hidden`: a placeholder names nothing.
 */
export function entrySkeleton(n: number): HTMLElement[] {
  const rows: HTMLElement[] = [];
  for (let i = 0; i < n; i++) {
    rows.push(
      el(
        "div",
        { className: "entry entry-skel", "aria-hidden": "true" },
        el(
          "div",
          { className: "entry-body" },
          el("span", { className: "skeleton entry-skel-title" }),
          el("span", { className: "skeleton entry-skel-sub" }),
        ),
      ),
    );
  }
  return rows;
}
