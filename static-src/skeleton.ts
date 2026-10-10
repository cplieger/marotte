import { el } from "@cplieger/reactive";

import { setBusy } from "./dom.js";
import { KEY_ATTR } from "./reconcile.js";

// Skeleton loading placeholders for perceived performance.

/** Mount a placeholder into `host`, or refuse. THE ONE DOOR every container placeholder mounts
 *  through, so a surface whose own arm is wrong shows no placeholder rather than stacking one
 *  under the content. */
export function paintPlaceholder(
  host: Element | null,
  build: () => Element,
  opts?: { readonly content?: string; readonly mount?: "replace" | "append" },
): () => void {
  // Both refusals in one test: an absent host answers `undefined` and a populated one answers an
  // Element, so only an empty host reaches the paint.
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
  // The placeholder is `aria-hidden`, so the HOST is the only thing left that can say the region is
  // being updated. Written here rather than at each call site, and only on the ADMITTED path: a
  // refusal painted nothing, so nothing is busy.
  setBusy(host, true);
  return () => {
    node.remove();
    setBusy(host, false);
  };
}

/** TEXT BLOCK — a run of bars standing in for prose or code, one bar per real line box. `widths`
 *  is a DETERMINISTIC per-surface array (never random): the shape of the text is what makes a
 *  block read as prose rather than as a grey box, and a random shape means the placeholder for
 *  one surface looks different on every load. */
function skeletonText(opts: {
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

/** The transcript placeholder's element id. The renderer drops it by this id when real turns
 *  land, so the placeholder and the conversation can never share the container. */
export const CHAT_SKELETON_ID = "chat-skeleton";

/** One placeholder turn: a short request over a longer reply, as bar widths. */
interface SkeletonTurnShape {
  readonly prompt: readonly string[];
  readonly reply: readonly string[];
}

/** The transcript's placeholder shape: three cards, newest last, each a short request over a
 *  longer reply — the real distribution. */
const CHAT_SKELETON: readonly SkeletonTurnShape[] = [
  { prompt: ["68%"], reply: ["94%", "88%", "52%"] },
  { prompt: ["46%"], reply: ["91%", "73%"] },
  { prompt: ["82%", "37%"], reply: ["96%", "89%", "84%", "41%"] },
];

/** The transcript's own card shape, two cards deep: the reader sees the top edge of the incoming
 *  page and nothing more, and `scroll.ts` corrects the reader's drift again AFTER this comes down,
 *  so its height is not a term in that arithmetic. */
const LOAD_MORE_SKELETON: readonly SkeletonTurnShape[] = [
  { prompt: ["54%"], reply: ["92%", "61%"] },
  { prompt: ["73%"], reply: ["95%", "87%", "48%"] },
];

/** COMPOSITE — one placeholder turn card, assembled from TEXT BLOCK twice inside the real
 *  turn-card containers. The two chrome bars MUST state a width, because neither `.turn-n` nor
 *  `.turn-ts` declares a box: each is the reserved ADVANCE WIDTH of the string it stands in for,
 *  MEASURED on the real cells rather than derived from a character count — the two are in
 *  DIFFERENT faces, so no single per-character figure covers both. The ordinal's real width is
 *  data-dependent (`#7` through `#1024`), so past `#999` the placeholder is short by design; and
 *  both figures are font-dependent, so a platform face wider than the one they were measured on
 *  shortens them further rather than overshooting. */
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

/** The transcript's placeholder: N turn cards inside a `display: contents` wrap. */
export function chatSkeleton(): HTMLDivElement {
  const wrap = el("div", {
    className: "skeleton-rows",
    "aria-hidden": "true",
  }) as HTMLDivElement;
  // Carries its id from here rather than from the caller (unlike `load-more-skeleton`, which
  // scroll.ts stamps) because TWO modules address it: chat.ts mounts it and messages.ts drops it
  // the moment real turns land. A literal in both would be a coupling that can drift silently.
  wrap.id = CHAT_SKELETON_ID;
  for (const shape of CHAT_SKELETON) {
    wrap.appendChild(skeletonTurnCard(shape));
  }
  return wrap;
}

/** The placeholder for the page loading in at the top of the transcript. */
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

/** Placeholder per-repo sections for the git view's two tabs, which both paint into the same
 *  `.git-repo-section` shape afterwards, so its geometry has one definition. `aria-hidden`
 *  because both mounts are `aria-live="polite"` and announcing placeholder bars is noise. */
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

/** Placeholder for the editor's document pane: the file's opening declaration line, a blank
 *  line, then body lines. */
export function editorDocSkeleton(): HTMLDivElement {
  const wrap = el("div", {
    className: "editor-skeleton",
    "aria-hidden": "true",
  }) as HTMLDivElement;
  wrap.appendChild(skelBar("editor-skel-title", "38%"));
  // Widths only: a source file's shape is what makes this read as a document rather than a block,
  // and an empty string is the blank line between the declaration and the body.
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

/** Placeholder rows for a file listing: a name plus the size and date bars, one row per entry;
 *  `meta: false` drops the bars for a list whose rows carry no meta column. */
export function fileRowsSkeleton(opts?: { readonly meta?: boolean }): HTMLDivElement {
  const wrap = el("div", {
    className: "fb-skeleton",
    "aria-hidden": "true",
  }) as HTMLDivElement;
  for (const width of ["62%", "38%", "71%", "45%", "56%", "33%", "68%", "49%"]) {
    const row = el("div", { className: "fb-row fb-row-skel" });
    const name = el("div", { className: "fb-skel-name" });
    name.appendChild(skelBar("skeleton-line", width));
    row.append(el("div", { className: "fb-skel-check" }), skelBar("fb-skel-icon", "1.25rem"), name);
    if (opts?.meta !== false) {
      row.append(
        skelBar("skeleton-line fb-skel-meta", "3rem"),
        skelBar("skeleton-line fb-skel-meta", "9rem"),
      );
    }
    wrap.appendChild(row);
  }
  return wrap;
}

/** ONE bar: `.skeleton` plus `className`, at `width`. The primitive every shape above is built
 *  from. Module-private: a COMPOSITE that the flat `skeletonRows` cell model cannot describe — a
 *  row three levels deep, or one holding two bars stacked inside a single cell — writes its
 *  painter HERE, beside the shapes it composes, rather than reaching in from a feature module. */
function skelBar(className: string, width: string): HTMLElement {
  const bar = el("div", { className: `skeleton ${className}` });
  bar.style.width = width;
  return bar;
}
