// Transcript disclosures: a card header is its disclosure's hit target, forwarding to the one control that keeps
// `aria-expanded`, the Tab stop and the chevron rotation, and every box opens and shuts with one slide.

/**
 * Anything in a header row that owns its click, by kind: `summary` and `label` activate something else,
 * `[contenteditable]` places a caret.
 */
const OWNS_ITS_CLICK =
  "a[href],area[href],button,input,select,textarea,summary,label," +
  "[role=button],[role=link],[role=checkbox],[role=menuitem],[contenteditable]";

/** Make every part of `row` activate `control`, except a nested control and a click that ends a selection in the row. */
export function wireRowToggle(row: HTMLElement, control: HTMLElement): void {
  row.addEventListener("click", (e: Event) => {
    const target = e.target;
    if (!(target instanceof Element)) {
      return;
    }
    const owner = target.closest(OWNS_ITS_CLICK);
    // Bounded to the row: `closest` walks past it, and the row may itself be a control.
    if (owner !== null && owner !== row && row.contains(owner)) {
      return;
    }
    if (hasSelectionIn(row)) {
      return;
    }
    // A detached control keeps its listeners (a tool card removes its chevron), so an unrefused forward would fire it.
    if (!control.isConnected) {
      return;
    }
    control.click();
  });
}

const slides = new WeakMap<HTMLElement, { anim: Animation; open: boolean }>();

/**
 * Slide `box` between its heights on either side of `apply(open)`: the transcript's one disclosure motion, at the
 * `createDisclosure` tween's tokens, in every engine. A CSS `block-size: auto` transition needs `interpolate-size`,
 * which only Chromium ships (https://webkit.org/b/295132, https://bugzil.la/1945962). A close keeps the content
 * applied until the slide ends; reduced motion, an unrendered box and `motion: false` flip at once, cancelling a
 * slide still running.
 */
export function slideBox(
  box: HTMLElement,
  open: boolean,
  apply: (open: boolean) => void,
  motion = true,
): void {
  const from = box.getBoundingClientRect().height;
  slides.get(box)?.anim.cancel();
  slides.delete(box);
  if (
    !motion ||
    box.getClientRects().length === 0 ||
    matchMedia("(prefers-reduced-motion: reduce)").matches
  ) {
    apply(open);
    return;
  }
  apply(open);
  const to = box.getBoundingClientRect().height;
  if (from === to) {
    return;
  }
  if (!open) {
    apply(true);
  }
  const style = getComputedStyle(box);
  let anim: Animation;
  try {
    anim = box.animate(
      { blockSize: [`${String(from)}px`, `${String(to)}px`], overflow: ["clip", "clip"] },
      {
        duration: durationMs(style.getPropertyValue("--uip-disclosure-duration")),
        easing: style.getPropertyValue("--uip-disclosure-easing").trim() || "ease",
      },
    );
  } catch {
    // WAAPI throws a TypeError on an easing the engine cannot parse; the state still flips.
    apply(open);
    return;
  }
  slides.set(box, { anim, open });
  // A cancelled slide rejects `finished`: the slide that replaced it owns the state.
  anim.finished.then(
    () => {
      slides.delete(box);
      if (!open) {
        apply(false);
      }
    },
    () => undefined,
  );
}

/** Whether a `slideBox` slide is running on `box`. */
export function isSliding(box: HTMLElement): boolean {
  return slides.has(box);
}

/** Where `box` is heading: a slide's target while one runs (a closing box still renders open), else `settled`. */
export function targetOpen(box: HTMLElement, settled: boolean): boolean {
  return slides.get(box)?.open ?? settled;
}

const folds = new WeakMap<HTMLElement, Animation[]>();

/** Set on a folding body while it fades and slides shut: 29-turns.css withholds the folded body's rules under it. */
const FOLD_CLOSING_ATTR = "data-fold-closing";

/** One turn body's fold: `apply` is the card's state flip. */
export interface TurnFold {
  readonly body: HTMLElement;
  readonly folded: boolean;
  readonly apply: () => void;
}

/**
 * Fold or unfold each turn `body` on either side of its `apply()`, the card's state flip, in the fold's two beats
 * (29-turns.css `--fold-slide`): a close fades the content out, slides shut once the fade is nearly done, and only then
 * lets the folded rules apply; an open slides first with the content transparent, then fades it in. Script-driven for
 * the same reason as `slideBox`. Reduced motion and an unrendered body flip at once, with no second measurement. Every
 * body is measured, then every flip runs, then each animated body is measured again, so a bulk fold forces a constant
 * number of layouts rather than two per body.
 */
export function slideTurnFolds(items: readonly TurnFold[]): void {
  const froms = items.map(({ body }) => {
    const before = getComputedStyle(body);
    return {
      h: body.getBoundingClientRect().height,
      start: before.paddingBlockStart,
      end: before.paddingBlockEnd,
      opacity: before.opacity,
    };
  });
  for (const { body, apply } of items) {
    for (const a of folds.get(body) ?? []) {
      a.cancel();
    }
    folds.delete(body);
    body.removeAttribute(FOLD_CLOSING_ATTR);
    apply();
  }
  if (matchMedia("(prefers-reduced-motion: reduce)").matches) {
    return;
  }
  const moving = items.flatMap((item, i) => {
    const from = froms[i];
    return from === undefined || item.body.getClientRects().length === 0 ? [] : [{ ...item, from }];
  });
  for (const { body, folded } of moving) {
    if (folded) {
      body.setAttribute(FOLD_CLOSING_ATTR, "");
    }
  }
  const planned = moving.map(({ body, folded, from }) => ({
    body,
    beats: foldBeats(body, folded, from),
  }));
  for (const { body, beats } of planned) {
    animateFold(body, beats);
  }
}

type FoldBeat = [Keyframe[] | PropertyIndexedKeyframes, KeyframeAnimationOptions];

/** The two beats of one body's fold from `from`, measuring its flipped layout. */
function foldBeats(
  body: HTMLElement,
  folded: boolean,
  from: { h: number; start: string; end: string; opacity: string },
): FoldBeat[] {
  const style = getComputedStyle(body);
  const time = (name: string): number => durationMs(style.getPropertyValue(name));
  const ease = (name: string): string => style.getPropertyValue(name).trim() || "ease";
  const slide = time("--fold-slide");
  const to = folded
    ? { h: 0, start: "0px", end: "0px" }
    : {
        h: body.getBoundingClientRect().height,
        start: style.paddingBlockStart,
        end: style.paddingBlockEnd,
      };
  const box = {
    blockSize: [`${String(from.h)}px`, `${String(to.h)}px`],
    paddingBlockStart: [from.start, to.start],
    paddingBlockEnd: [from.end, to.end],
  };
  const exit = time("--dur-exit");
  return folded
    ? [
        [
          { opacity: [from.opacity, "0"] },
          { duration: exit, easing: ease("--ease-exit"), fill: "forwards" },
        ],
        [
          box,
          { duration: slide, delay: exit * 0.8, easing: ease("--ease-standard"), fill: "both" },
        ],
      ]
    : [
        [box, { duration: slide, easing: ease("--ease-standard") }],
        [
          { opacity: [from.opacity, "1"] },
          {
            duration: time("--dur-standard"),
            delay: slide,
            easing: ease("--ease-enter"),
            fill: "backwards",
          },
        ],
      ];
}

function animateFold(body: HTMLElement, beats: readonly FoldBeat[]): void {
  const anims: Animation[] = [];
  try {
    for (const [frames, timing] of beats) {
      anims.push(body.animate(frames, timing));
    }
  } catch {
    // WAAPI throws a TypeError on an easing the engine cannot parse; the state has already flipped.
    for (const a of anims) {
      a.cancel();
    }
    body.removeAttribute(FOLD_CLOSING_ATTR);
    return;
  }
  folds.set(body, anims);
  // A cancelled beat rejects `finished`: the fold that replaced it owns the body.
  Promise.all(anims.map((a) => a.finished)).then(
    () => {
      folds.delete(body);
      body.removeAttribute(FOLD_CLOSING_ATTR);
      for (const a of anims) {
        a.cancel();
      }
    },
    () => undefined,
  );
}

/** A CSS `<time>` token in ms: `200ms` or `0.2s`. */
function durationMs(token: string): number {
  const t = token.trim();
  const n = parseFloat(t);
  if (!Number.isFinite(n)) {
    return 0;
  }
  return t.endsWith("ms") ? n : n * 1000;
}

/**
 * Give a native `<details>` the slide: its summary's activation (click, Enter, Space) toggles through `slideBox`.
 * A programmatic `open` write still snaps, which find-in-chat's reveal relies on.
 */
export function wireDetailsSlide(details: HTMLDetailsElement): void {
  const summary = details.querySelector<HTMLElement>(":scope > summary");
  summary?.addEventListener("click", (e) => {
    if (e.defaultPrevented) {
      return;
    }
    e.preventDefault();
    slideDetails(details, !targetOpen(details, details.open));
  });
}

/** Open or close `details` through the slide. */
export function slideDetails(details: HTMLDetailsElement, open: boolean): void {
  slideBox(details, open, (o) => {
    details.open = o;
  });
}

/**
 * Checked at click time: a drag leaves the selection standing through `mouseup`, while a selection elsewhere was
 * collapsed by this click's mousedown.
 */
function hasSelectionIn(row: HTMLElement): boolean {
  const sel = row.ownerDocument.defaultView?.getSelection() ?? null;
  if (sel === null || sel.isCollapsed || sel.rangeCount === 0) {
    return false;
  }
  const anchor = sel.getRangeAt(0).commonAncestorContainer;
  return row.contains(anchor) || anchor.contains(row);
}
