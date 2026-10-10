// The title bar's heading: one writer for every view's title and subtitle. The subtitle is
// REMEMBERED per view kind here rather than passed, because a view switch and a sub-tab switch are
// separate events with no fixed order.

import { byId } from "./dom.js";

/** Keyed by `TabKind` as a plain string so this module stays a leaf; every caller already holds one. */
const subtitles = new Map<string, string>();

/** The kind whose title is currently painted, so a sub-tab setter running for a view that is NOT
 *  on screen records and writes nothing; every view switch repaints. */
let shownKind = "";

/** The class a heading wears when its title will not fit: the screen-reader-only utility
 *  (40-a11y.css), so only the pixels go. `display: none` would take the page's one heading out
 *  of the accessibility tree on the devices where this bar is the only thing naming the view. */
const CLIPPED = "sr-only";

/** Whether the bar's ACTIONS are on more than one row, read off the rendered rows rather than
 *  computed from widths. */
function actionsWrapped(heading: HTMLElement): boolean {
  const bar = heading.parentElement;
  if (bar === null) {
    return false;
  }
  const rows = new Set<number>();
  for (const btn of bar.querySelectorAll<HTMLElement>(":scope > .icon-btn")) {
    const r = btn.getBoundingClientRect();
    if (r.width === 0 && r.height === 0) {
      continue;
    }
    rows.add(Math.round(r.top));
  }
  return rows.size > 1;
}

/** Clip the heading when it does not fit: either its own title would render truncated, or the
 *  ACTIONS have wrapped. MEASURED, never keyed to a width, for `tab-bar-fit.ts`'s reason. The
 *  read is taken with the heading SHOWN, or a clipped heading measures 1px, reports no overflow
 *  and unclips itself forever. */
function fitHeading(): void {
  // Null-tolerant, unlike the WRITE below: a title with no element to land in is a bug and `byId`
  // should say so, while a fit over a bar that is not there is not an error.
  const heading = document.getElementById("titlebar-heading");
  const title = document.getElementById("titlebar-title");
  if (heading === null || title === null) {
    return;
  }
  heading.classList.remove(CLIPPED);
  // A hidden view and an empty title both report 0/0, so no overflow and shown; the observer fires
  // again once the bar has real geometry.
  const truncated = title.scrollWidth > title.clientWidth;
  heading.classList.toggle(CLIPPED, truncated || actionsWrapped(heading));
}

/** Watch the heading AND the bar, and keep the title's presence fitted to the room the actions
 *  leave. Call once at init; both are static singletons, so nothing to release. BOTH, because
 *  each sees a change the other cannot. */
export function initPageTitleFit(): void {
  const ro = new ResizeObserver(() => {
    // Deferred a frame: a class write inside the RO cycle logs a benign loop error.
    requestAnimationFrame(fitHeading);
  });
  const heading = byId<HTMLElement>("titlebar-heading");
  ro.observe(heading);
  const bar = heading.parentElement;
  if (bar !== null) {
    ro.observe(bar);
  }
  fitHeading();
}

function paint(title: string, subtitle: string): void {
  const titleEl = byId<HTMLElement>("titlebar-title");
  const subtitleEl = byId<HTMLElement>("titlebar-subtitle");
  // Guarded because this runs from the view effect, which re-runs on every projection mutation: a
  // rename, a dot change and a background tab's close all repaint the same two strings, and
  // `fitHeading` forces layout to read `scrollWidth`.
  const moved = titleEl.textContent !== title || subtitleEl.textContent !== subtitle;
  titleEl.textContent = title;
  subtitleEl.textContent = subtitle;
  if (moved) {
    fitHeading();
  }
}

/** Show `title`, with whatever subtitle `kind` last recorded. */
export function setPageTitle(title: string, kind = ""): void {
  shownKind = kind;
  paint(title, subtitles.get(kind) ?? "");
}

/** Record this view's section name, and show it when that view is the one on screen. The title
 *  is left exactly as it is, which is what lets a sub-tab switch repaint half the heading
 *  without knowing what the other half says. */
export function setPageSubtitle(kind: string, subtitle: string): void {
  subtitles.set(kind, subtitle);
  if (kind !== shownKind) {
    return;
  }
  const el = byId<HTMLElement>("titlebar-subtitle");
  if (el.textContent === subtitle) {
    return;
  }
  el.textContent = subtitle;
  fitHeading();
}

/** Clear the heading. Used when no tab is open, where a stale title would name a view that is no
 *  longer on screen. */
export function clearPageTitle(): void {
  shownKind = "";
  paint("", "");
}
