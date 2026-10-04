import { el } from "@cplieger/reactive";

import { setBusy } from "./dom.js";
import { KEY_ATTR } from "./reconcile.js";

// ---------------------------------------------------------------------------
// Skeleton loading placeholders for perceived performance.
// ---------------------------------------------------------------------------

/** Mount a placeholder into `host`, or refuse. THE ONE DOOR every container
 *  placeholder mounts through, so a surface whose own arm is wrong shows no
 *  placeholder rather than stacking one under the content.
 *
 *  `content` names what counts as content IN THIS HOST — the surfaces disagree.
 *  `mount: "replace"` also clears what the guard cannot see (an error row, a
 *  banner). Returns the teardown, or a no-op on a refusal, so a caller hands it
 *  to `skeletonTiming` either way. */
export function paintPlaceholder(
  host: Element | null,
  build: () => Element,
  opts?: { readonly content?: string; readonly mount?: "replace" | "append" },
): () => void {
  // Both refusals in one test: an absent host answers `undefined` and a populated
  // one answers an Element, so only an empty host reaches the paint.
  if (host?.querySelector(opts?.content ?? `[${KEY_ATTR}]`) !== null) {
    return () => {
      /* no host, or one already holding content */
    };
  }
  const node = build();
  if ((opts?.mount ?? "replace") === "append") {
    host.appendChild(node);
  } else {
    host.replaceChildren(node);
  }
  // The placeholder is `aria-hidden`, so the HOST is the only thing left that can
  // say the region is being updated. Written here rather than at each call site,
  // and only on the ADMITTED path: a refusal painted nothing, so nothing is busy.
  setBusy(host, true);
  return () => {
    node.remove();
    setBusy(host, false);
  };
}

/** TEXT BLOCK — a run of bars standing in for prose or code, one bar per real
 *  line box.
 *
 *  `widths` is a DETERMINISTIC per-surface array (never random): the shape of the
 *  text is what makes a block read as prose rather than as a grey box, and a
 *  random shape means the placeholder for one surface looks different on every
 *  load. An EMPTY STRING is a deliberate blank line — it reserves the line box and
 *  paints nothing.
 *
 *  `lineClass` decides the UNIT: `skeleton-line-text` for a bar inside prose
 *  (sized in `em`/`lh` against the container's own type), a site's own `em`-sized
 *  class where the pane has its own measured metrics (`editor-skel-line`), or
 *  `skeleton-line` for a chrome slot sized in `rem`. */
export function skeletonText(opts: {
  readonly widths: readonly string[];
  readonly lineClass?: string;
}): HTMLDivElement {
  const wrap = el("div", { className: "skeleton-text" }) as HTMLDivElement;
  const lineClass = opts.lineClass ?? "skeleton-line-text";
  for (const width of opts.widths) {
    if (width === "") {
      wrap.appendChild(el("div", { className: lineClass }));
      continue;
    }
    wrap.appendChild(skelBar(lineClass, width));
  }
  return wrap;
}

/** One cell of a ROW LIST row. `w` absent RESERVES the cell empty — a placeholder
 *  control says nothing, and dropping the column instead moves every cell after it
 *  when the real row lands. */
export interface SkelCell {
  /** Class on the CELL WRAPPER. Wear the real cell's class when it is layout-only;
   *  declare a `-skel-` class beside it when the real one carries content
   *  styling (`fileRowsSkeleton` is the reference for the second case). Omit for a
   *  bare cell — a grid track the template already sized, a reserved control
   *  column, or a row whose single bar is a direct child. */
  readonly cls?: string;
  /** Class on the BAR ITSELF, beside `.skeleton`. This is what a site passes whose
   *  existing class IS the bar's own height (`docs-skel-name` is `height:
   *  0.875rem`); omitted, the bar takes `.skeleton-line`'s `0.75rem`. Without this
   *  field, adopting the builder silently RESIZES such a site's bar and parks its
   *  class on an unsized wrapper — which is the defect it exists to make
   *  unrepresentable. */
  readonly bar?: string;
  /** The bar's width. Absent = a reserved empty cell, no bar, no shimmer. */
  readonly w?: string;
}

/** ROW LIST — N rows wearing `rowClass`, one entry per row.
 *
 *  Cells are POSITIONAL and FLAT, which is what makes the table variant work: a
 *  row whose own class declares `grid-template-columns` gets its columns from that
 *  template, so the Nth cell lands in the Nth track and nothing here restates a
 *  width. A row needing NESTING (two bars stacked inside one cell, an intermediate
 *  layout element) is a COMPOSITE and writes its own painter in this module with
 *  `skelBar`. This builder deliberately takes no `children`.
 *
 *  `rowClass` carries the REAL row's class plus a `-skel` modifier for the
 *  placeholder-only concerns (`pointer-events: none`, and nothing else).
 *
 *  A cell resolves to one of three shapes and the rule is one sentence: a `cls`
 *  wraps, a `bar` names the bar, absence of both is the default. Independently,
 *  the bar takes `skeleton` plus `bar ?? "skeleton-line"`, so a site whose class
 *  carries its own height passes `bar` and one that wants the chrome default
 *  passes nothing. */
export function skeletonRows(
  rowClass: string,
  rows: readonly (readonly SkelCell[])[],
): HTMLDivElement {
  const wrap = el("div", {
    className: "skeleton-rows",
    "aria-hidden": "true",
  }) as HTMLDivElement;
  for (const cells of rows) {
    const row = el("div", { className: rowClass });
    for (const cell of cells) {
      if (cell.cls !== undefined) {
        const wrapper = el("div", { className: cell.cls });
        if (cell.w !== undefined) {
          wrapper.appendChild(skelBar(cell.bar ?? "skeleton-line", cell.w));
        }
        row.appendChild(wrapper);
        continue;
      }
      if (cell.w !== undefined) {
        row.appendChild(skelBar(cell.bar ?? "skeleton-line", cell.w));
        continue;
      }
      row.appendChild(el("div"));
    }
    wrap.appendChild(row);
  }
  return wrap;
}

/** INLINE FIELD — one bar standing in for a single value, `aria-hidden` so the
 *  HOST carries the accessible name and the busy state. Insert it INTO the element
 *  whose width it is holding open; never class that element itself.
 *
 *  It takes no arguments and never will. The width is a per-site CUSTOM PROPERTY
 *  declared on the HOST, which is what makes it argument-free — the bar inherits
 *  `--skel-field-w` from whatever it is inserted into, so a site with a
 *  non-default width declares one CSS rule and passes nothing. A width parameter
 *  here would be the same value in two places with no way to tell which a site
 *  meant, and it would put a per-site number in TypeScript where the cascade
 *  argument in 30-utilities.css needs it in CSS. */
export function skeletonField(): HTMLSpanElement {
  return el("span", {
    className: "skeleton skeleton-field",
    "aria-hidden": "true",
  });
}

/** The transcript placeholder's element id. The renderer drops it by this id
 *  when real turns land, so the placeholder and the conversation can never share
 *  the container. */
export const CHAT_SKELETON_ID = "chat-skeleton";

/** One placeholder turn: a short request over a longer reply, as bar widths. */
interface SkeletonTurnShape {
  readonly prompt: readonly string[];
  readonly reply: readonly string[];
}

/** The transcript's placeholder shape: three cards, newest last, each a short
 *  request over a longer reply — the real distribution.
 *
 *  Interior lines of a wrapped paragraph run 85-97% and the last line is short,
 *  which is what makes a run of bars read as prose.
 *
 *  Three cards rather than five: three fills the first screen of a transcript at
 *  every width this view is shown at, and the count is a claim about how much
 *  conversation is arriving. The tallest is LAST, nearest the live edge, because
 *  `.transcript-view` is bottom-anchored and that is where the reader is looking. */
const CHAT_SKELETON: readonly SkeletonTurnShape[] = [
  { prompt: ["68%"], reply: ["94%", "88%", "52%"] },
  { prompt: ["46%"], reply: ["91%", "73%"] },
  { prompt: ["82%", "37%"], reply: ["96%", "89%", "84%", "41%"] },
];

/** The top-of-transcript placeholder while an older page loads. The transcript's
 *  own card shape, two cards deep: the reader sees the top edge of the incoming
 *  page and nothing more, and `scroll.ts` measures the reading-position
 *  compensation AFTER this comes down, so its height is not a term in that
 *  arithmetic. */
const LOAD_MORE_SKELETON: readonly SkeletonTurnShape[] = [
  { prompt: ["54%"], reply: ["92%", "61%"] },
  { prompt: ["73%"], reply: ["95%", "87%", "48%"] },
];

/** COMPOSITE — one placeholder turn card, assembled from TEXT BLOCK twice inside
 *  the real turn-card containers.
 *
 *  Every class here is a real element's, so the card's border, radius, fill, its
 *  header band, both gaps and the reply's own type metrics are the real card's
 *  rather than a copy of them, and this painter declares no geometry at all. What
 *  it does NOT wear is as load-bearing: no `id` (the real card answers to the
 *  rail's `#turn-N` anchor, and a placeholder must not), no `data-*` state, no
 *  dot, no footer, no tool card — each of those is a claim about a turn the load
 *  has not described yet.
 *
 *  The leading `.turn-fold-toggle` is RESERVED and empty: that class's own square is
 *  what stops the `#N` bar sitting where no real `#N` sits, and drawing a chevron into
 *  it would say something. An empty div matches no hit-floor selector, so the reserved
 *  cell is SHORT of the real button under a finger — the tolerated direction. The trailing copy button is not
 *  reserved — it sits at `margin-inline-start: auto` with nothing after it, so its
 *  absence moves no cell.
 *
 *  The two chrome bars MUST state a width, because neither `.turn-n` nor
 *  `.turn-ts` declares a box: each is the reserved ADVANCE WIDTH of the string it
 *  stands in for, MEASURED on the real cells rather than derived from a character
 *  count — the two are in DIFFERENT faces, so no single per-character figure
 *  covers both. `.turn-n` sets `font-family: var(--font-mono)` and `.turn-ts` sets
 *  only `font-variant-numeric`, so the timestamp inherits the head row's own
 *  `system-ui` and its colon is narrower than its digits. Rendered at
 *  `--fs-xs`: `#14` is 16.5px against `1rem`, and `09:41` is 29.7px against
 *  `1.875rem`. `rem` because a chrome bar is chrome.
 *
 *  Both are approximations, admissibly, and the tolerated direction is SHORT: the
 *  head row's only auto margin sits on its trailing slot, so a bar a few pixels
 *  short moves no other cell and changes no card's height, while one wider than
 *  the string it replaces claims more content than is arriving. The ordinal's real
 *  width is data-dependent (`#7` through `#1024`), so past `#999` the placeholder
 *  is short by design; and both figures are font-dependent, so a platform face
 *  wider than the one they were measured on shortens them further rather than
 *  overshooting. */
function skeletonTurnCard(shape: SkeletonTurnShape): HTMLDivElement {
  const card = el("div", { className: "turn turn-skel" }) as HTMLDivElement;
  const headRow = el("div", { className: "turn-head-row" });
  headRow.append(
    el("div", { className: "turn-fold-toggle" }),
    skelBar("skeleton-line", "1rem"),
    skelBar("skeleton-line", "1.875rem"),
  );
  const reqText = el("div", { className: "turn-req-text" });
  reqText.appendChild(skeletonText({ widths: shape.prompt }));
  const req = el("div", { className: "turn-req" });
  req.appendChild(reqText);
  const header = el("div", { className: "turn-header" });
  header.append(headRow, req);
  const message = el("div", { className: "message assistant" });
  message.appendChild(skeletonText({ widths: shape.reply }));
  const row = el("div", { className: "msg-row" });
  row.appendChild(message);
  const body = el("div", { className: "turn-body" });
  body.appendChild(row);
  card.append(header, body);
  return card;
}

/** The transcript's placeholder: N turn cards inside a `display: contents` wrap.
 *
 *  The wrap is what keeps `turnCards()` clean. `messages.ts` collects every DIRECT
 *  child of the view carrying `.turn`, and the wrap carries none — so the timeline
 *  rail's observed set and the fold pass never see a placeholder card, whatever
 *  `display: contents` does, because `root.children` is a DOM query.
 *
 *  Zero arguments, and it stays that way: two suites mock this as a zero-argument
 *  factory, so a widened signature would silently pass a mock that ignores it. */
export function chatSkeleton(): HTMLDivElement {
  const wrap = el("div", {
    className: "skeleton-rows",
    "aria-hidden": "true",
  }) as HTMLDivElement;
  // Carries its id from here rather than from the caller (unlike
  // `load-more-skeleton`, which scroll.ts stamps) because TWO modules address it:
  // chat.ts mounts it and messages.ts drops it the moment real turns land. A
  // literal in both would be a coupling that can drift silently.
  wrap.id = CHAT_SKELETON_ID;
  for (const shape of CHAT_SKELETON) {
    wrap.appendChild(skeletonTurnCard(shape));
  }
  return wrap;
}

/** The placeholder for the page loading in at the top of the transcript.
 *
 *  scroll.ts stamps `load-more-skeleton` on the returned wrap and removes it by
 *  that id; removing a `display: contents` wrap takes its subtree with it. That
 *  wrap generates no box, so it carries no padding of its own either — the view's
 *  own gap separates these cards from the first real turn. */
export function loadMoreSkeleton(): HTMLDivElement {
  const wrap = el("div", {
    className: "skeleton-rows",
    "aria-hidden": "true",
  }) as HTMLDivElement;
  for (const shape of LOAD_MORE_SKELETON) {
    wrap.appendChild(skeletonTurnCard(shape));
  }
  return wrap;
}

/** Placeholder per-repo sections for the git view's two tabs, which both paint
 *  into the same `.git-repo-section` shape afterwards, so its geometry has one
 *  definition. `aria-hidden` because both mounts are `aria-live="polite"` and
 *  announcing placeholder bars is noise. `label` names what the PR tab is
 *  waiting on (the inventory, or one connection); the Changes tab passes none. */
export function gitRepoSkeleton(opts: {
  readonly label?: string;
  readonly widths: string[];
}): HTMLDivElement {
  const wrap = el("div", {
    className: "git-repo-skeleton",
    "aria-hidden": "true",
  }) as HTMLDivElement;
  if (opts.label !== undefined) {
    wrap.appendChild(el("div", { className: "git-repo-skel-label" }, opts.label));
  }
  for (const width of opts.widths) {
    const section = el("div", { className: "git-repo-section git-repo-skel-section" });
    section.append(
      el("div", { className: "skeleton git-repo-skel-icon" }),
      skelBar("git-repo-skel-name", width),
      skelBar("git-repo-skel-meta", "4rem"),
    );
    wrap.appendChild(section);
  }
  return wrap;
}

/** Placeholder for the editor's document pane: the file's opening declaration
 *  line, a blank line, then body lines.
 *
 *  Every bar occupies exactly ONE of the pane's line boxes, so N bars stand on the
 *  first N real lines and the file landing moves nothing. The geometry is the
 *  pane's own (`.editor-skeleton` in 30-utilities.css measures in `em`, and the
 *  mount sits inside `#editor-code`, which inherits the mono metrics), so a change
 *  to that font-size carries the placeholder with it. */
export function editorDocSkeleton(): HTMLDivElement {
  const wrap = el("div", {
    className: "editor-skeleton",
    "aria-hidden": "true",
  }) as HTMLDivElement;
  wrap.appendChild(skelBar("editor-skel-title", "38%"));
  // Widths only: a source file's shape is what makes this read as a document
  // rather than a block, and an empty string is the blank line between the
  // declaration and the body.
  for (const width of ["", "72%", "54%", "83%", "41%", "", "66%", "78%", "49%", "60%"]) {
    const line = el("div", { className: "editor-skel-line" });
    if (width !== "") {
      line.classList.add("skeleton");
      line.style.width = width;
    }
    wrap.appendChild(line);
  }
  return wrap;
}

/** Placeholder rows for the file browser's listing: a name plus the size and date
 *  the meta column carries, one row per entry.
 *
 *  The row wears `.fb-row` itself, so its height, padding, gap and bottom rule are
 *  a real row's rather than a copy of them, and adopting the listing moves nothing.
 *  The `.fb-check` column is RESERVED without a bar: a placeholder checkbox says
 *  nothing, and dropping the column instead would step the icon and every name
 *  left by 1.5rem the moment the listing lands. */
export function fileRowsSkeleton(): HTMLDivElement {
  const wrap = el("div", {
    className: "fb-skeleton",
    "aria-hidden": "true",
  }) as HTMLDivElement;
  for (const width of ["62%", "38%", "71%", "45%", "56%", "33%", "68%", "49%"]) {
    const row = el("div", { className: "fb-row fb-row-skel" });
    const name = el("div", { className: "fb-skel-name" });
    name.appendChild(skelBar("skeleton-line", width));
    row.append(
      el("div", { className: "fb-skel-check" }),
      skelBar("fb-skel-icon", "1.25rem"),
      name,
      skelBar("skeleton-line fb-skel-meta", "3rem"),
      skelBar("skeleton-line fb-skel-meta", "9rem"),
    );
    wrap.appendChild(row);
  }
  return wrap;
}

/** ONE bar: `.skeleton` plus `className`, at `width`. The primitive every shape
 *  above is built from. Module-private: a COMPOSITE that the flat `skeletonRows`
 *  cell model cannot describe — a row three levels deep, or one holding two bars
 *  stacked inside a single cell — writes its painter HERE, beside the shapes it
 *  composes, rather than reaching in from a feature module. */
function skelBar(className: string, width: string): HTMLElement {
  const bar = el("div", { className: `skeleton ${className}` });
  bar.style.width = width;
  return bar;
}
