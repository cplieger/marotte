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
  /** A path, a matcher or a command: an identifier rather than prose. */
  readonly mono?: boolean;
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
  /** Absent still renders an empty subtitle line, so the title stays on the column. */
  readonly sub?: EntrySub | undefined;
  readonly actions?: readonly HTMLElement[] | undefined;
  /** Present for a door: the body becomes the open control named `Open <name>`. Absent: inert, no role or listener. */
  readonly open?: { readonly name: string; readonly onOpen: () => void } | undefined;
  /** Extra attributes, by full attribute name (`data-hook-id`). */
  readonly data?: Readonly<Record<string, string>> | undefined;
}

/** A third badge is dropped, not wrapped: the badge column beside the actions is one line. */
const MAX_BADGES = 2;

function subNode(sub: EntrySub): HTMLElement {
  if (sub.kind === "lines") {
    return el(
      "span",
      { className: "entry-lines" },
      ...sub.lines.map((l) =>
        el(
          "span",
          { className: l.mono === true ? "entry-sub entry-sub-mono" : "entry-sub" },
          l.text,
        ),
      ),
    );
  }
  const cls = sub.kind === "clamp" ? "entry-sub entry-sub-clamp" : "entry-sub";
  return el("span", { className: cls }, sub.text);
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
  const sub = subNode(spec.sub ?? { kind: "line", text: "" });

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
 * N placeholder rows at the list's tier, so the swap to real rows moves nothing.
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
