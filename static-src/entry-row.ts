// ---------------------------------------------------------------------------
// The list row both page lists build, and the container it sits in. ONE builder
// for History and the configuration browser, so a row's height, inset, truncation
// and hit target are decided once; what varies between the pages is which SLOTS a
// row fills, and that is what the spec is. The rules it enforces are
// `marotte-ui.md`'s: one row height per list (CSS's, from the tier the list
// sets), a row with one destination puts the box inside the control, at most two
// badges on the title line, a relative time whose absolute twin is the tooltip.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { relativeTime, absoluteTime } from "./relative-time.js";

/** The subtitle region. Three shapes, because a list carries either one
 *  ellipsised line, a two-line description, or a pair of single-line facts. */
export type EntrySub =
  | { readonly kind: "line"; readonly text: string }
  | { readonly kind: "clamp"; readonly text: string }
  | { readonly kind: "lines"; readonly lines: readonly EntrySubLine[] };

interface EntrySubLine {
  readonly text: string;
  /** A path, a matcher or a command: an identifier rather than prose. */
  readonly mono?: boolean;
}

/** A timestamp, or a count the time slot borrows. `{ms}` gets the relative
 *  vocabulary plus its absolute tooltip; `{text}` is rendered verbatim and gets
 *  NO tooltip, because a hit count has no absolute form. */
type EntryTime = { readonly ms: number } | { readonly text: string };

export interface EntryRowSpec {
  /** The row's identity, written as `data-key`. ONE spelling for every page on
   *  this builder, so a delegated listener's `closest("[data-key]")` is the same
   *  read everywhere; `data` below carries a page's own extra attributes. */
  readonly key: string;
  readonly title: string;
  readonly lead?: HTMLElement | undefined;
  /** A chip that belongs to the NAME rather than to the row's facts (a git
   *  status letter): seated in the name group against the title's last glyph,
   *  outside the badge cap. */
  readonly mark?: HTMLElement | undefined;
  readonly badges?: readonly HTMLElement[] | undefined;
  readonly time?: EntryTime | undefined;
  /** Absent still renders an EMPTY subtitle line: the body centres in the row,
   *  so a row without one would lift its title off the column its neighbours
   *  share. */
  readonly sub?: EntrySub | undefined;
  readonly actions?: readonly HTMLElement[] | undefined;
  /** Present when the row is a DOOR: the body becomes the open control and its
   *  accessible name is `Open <name>`. Absent leaves an inert body with no role,
   *  no tabindex and no listener. */
  readonly open?: { readonly name: string; readonly onOpen: () => void } | undefined;
  /** Extra attributes, by full attribute name (`data-hook-id`). */
  readonly data?: Readonly<Record<string, string>> | undefined;
}

/** At most two, and a third is DROPPED rather than wrapped: the title line is
 *  one line, and a third badge is what turns it into two. */
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
 * One row. Every descendant of the body is a `span` or a `time`, never a `div`:
 * the body is a `<button>` when the row is a door and a button takes phrasing
 * content only. The CSS blockifies them (they are flex items), so the ellipsis
 * and the clamp behave as they would on a div. The name group (title plus mark)
 * is the title line's grower; a title that grew put the mark beside the badges.
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
    spec.badges !== undefined && spec.badges.length > 0
      ? el("span", { className: "entry-badges" }, ...spec.badges.slice(0, MAX_BADGES))
      : null,
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
    spec.actions !== undefined && spec.actions.length > 0
      ? el("span", { className: "entry-actions" }, ...spec.actions)
      : null,
  );
}

/** The card the rows sit in. `role="list"` with `role="listitem"` rows, which is
 *  what a `div`-based list owes a screen reader; the seams are the container's
 *  own fill showing through its `--hairline` gap. */
export function entryList(): HTMLElement {
  return el("div", { className: "list-container", role: "list" });
}

/**
 * The region a row may grow BELOW itself, mounted as the row's next sibling so
 * the row never moves. A list may own nothing but list items, so the region is
 * one: without the role, axe reports the list owning the form's own controls.
 */
export function entryDetail(...children: HTMLElement[]): HTMLElement {
  return el("div", { className: "entry-detail", role: "listitem" }, ...children);
}

/**
 * N placeholder rows at the list's own tier, each holding a title bar and a
 * subtitle bar, so the swap to real rows moves nothing (`marotte-ui.md` "Stable
 * skeletons"). `aria-hidden`, because a placeholder names nothing.
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
