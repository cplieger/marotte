// The reader's gestures on one scroller: which input would move it, which way, and when the motion it started is
// over. It reports to its owner (scroll-controller.ts) and never decides a reading state itself.

import { shellTakesBodyKeys } from "./shell-keys.js";

/** Which way an input moves the content: -1 up, 1 down, 0 no stated direction. */
export type Aim = -1 | 0 | 1;

/** The tolerance of a position at the scroller's end, in CSS px, shared by every at-the-end decision and the follow
 *  write's skip. Never below 1: `scrollHeight` and `clientHeight` are integers rounded apart from the fractional
 *  layout, Firefox rounds `scrollTop` and WebKit floors it. Below DPR 1 one device pixel, because Firefox aligns its
 *  async scroll positions to device pixels. Read at use: zoom changes `devicePixelRatio`. */
export function edgeSlack(): number {
  return Math.max(1, 1 / window.devicePixelRatio);
}

/** How long an open gesture survives with no input and no scroll event of its own, for motion the platform answers
 *  with no `scrollend`: a wheel latched to a nested scroller at its end, an engine with no async scrolling, an owner
 *  write that cancelled the platform's animation. A scroll event refreshes it, so it never ends live motion. */
export const GESTURE_IDLE_MS = 250;
/** The same bound in 60Hz frames, also required: an animation's slow tail lands a whole pixel only every few frames,
 *  and a long frame or a held clock can pass the time alone before the next step's scroll event. */
const GESTURE_IDLE_FRAMES = 15;

/** How far a touch travels before it is a scroll rather than a tap: Android's touch slop (8dp,
 *  `ViewConfiguration.getScaledTouchSlop`), where the platform starts a drag. */
const TAP_SLOP_PX = 8;

/** Shift+Space scrolls up, which no key spelling distinguishes. `End` is the owner's: in Reading it resumes. */
const SCROLL_UP_KEYS = new Set(["ArrowUp", "PageUp", "Home"]);
const SCROLL_DOWN_KEYS = new Set(["ArrowDown", "PageDown", " "]);
/** What Space activates rather than scrolls; a link scrolls on Space. */
const SPACE_PRESSES = 'button, summary, [role="button"]';

/** What the tracker reports. Positions are the owner's: `endTop` is the scroller's end as the owner measures it. */
export interface GestureHost {
  /** A gesture opened, aimed `aim` (0 for a thumb press or an aimless scroll). */
  opened(aim: Aim): void;
  /** A later input of the open gesture carried `aim`. */
  aimed(aim: Aim): void;
  /** The gesture's motion is over. Not called by `forget`. */
  ended(): void;
  /** The reader aimed down while the scroller sits on its end, and `rejoinable()` said yes. */
  atEndDown(): void;
  rejoinable(): boolean;
  /** A press in the view: a pointer down off the scrollbar from a mouse, a click, or Enter or Space on a control in
   *  it. */
  pressed(): void;
  /** `End` with no modifier. */
  endKey(): void;
  endTop(): number;
}

export class GestureTracker {
  private open = false;
  /** Scroll events the owner attributed to the open gesture. */
  private seen = 0;
  /** `Date.now()` at the open gesture's last input or scroll event. */
  private lastActivity = 0;
  /** Animation frames since the open gesture's last input or scroll event. */
  private stillFrames = 0;
  private idleFrame = 0;
  private finger = false;
  private thumb = false;
  private touchStart: { x: number; y: number } | null = null;
  private lastTouchY: number | null = null;
  private touchMoved = false;
  private touchAim: Aim = 0;
  private lastBarY: number | null = null;
  /** Bumped by every owner write; the write mark lasts two frames, the life of the owner's own-write record. */
  private writeGen = 0;
  private writeMark = false;

  constructor(
    private readonly scrollEl: HTMLElement,
    private readonly viewOf: () => HTMLElement,
    private readonly host: GestureHost,
  ) {}

  /** Attach the listeners. Called once. */
  listen(): void {
    const el = this.scrollEl;
    el.addEventListener(
      "wheel",
      (e) => {
        if (e.deltaY === 0 || e.ctrlKey) {
          return;
        }
        this.input(e.deltaY < 0 ? -1 : 1, e.target);
      },
      { passive: true },
    );
    el.addEventListener(
      "touchstart",
      (e) => {
        const t = e.touches[0];
        this.touchStart = t === undefined ? null : { x: t.clientX, y: t.clientY };
        this.lastTouchY = t?.clientY ?? null;
        this.touchMoved = false;
        this.touchAim = 0;
      },
      { passive: true },
    );
    el.addEventListener(
      "touchmove",
      (e) => {
        this.touchMove(e);
      },
      { passive: true },
    );
    for (const type of ["touchend", "touchcancel"] as const) {
      el.addEventListener(
        type,
        () => {
          if (this.finger) {
            this.finger = false;
            this.touch();
          }
          this.touchStart = null;
        },
        { passive: true },
      );
    }
    el.addEventListener(
      "pointerdown",
      (e) => {
        if (!this.inScrollbarGutter(e)) {
          // A touch or pen is a press only at its click: until then it may be a fling's touchdown, and a press drops
          // the anchor through the growth a fling's last steps bring in.
          if (e.pointerType !== "touch" && e.pointerType !== "pen") {
            this.host.pressed();
          }
          return;
        }
        this.lastBarY = e.clientY;
        if (this.input(0, null)) {
          this.thumb = true;
        }
      },
      { passive: true },
    );
    // Capture, so the press is open before the control's own handler changes the layout. Every activation ends in a
    // click (mouse, touch, Enter or Space on a button, a script's `click()`), with or without a pointerdown first.
    el.addEventListener(
      "click",
      (e) => {
        if (e.target instanceof Node && this.viewOf().contains(e.target)) {
          this.host.pressed();
        }
      },
      { capture: true, passive: true },
    );
    // On the document: a drag that leaves the scroller still owns the bar. The thumb moves with the content.
    document.addEventListener(
      "pointermove",
      (e) => {
        if (!this.thumb || this.lastBarY === null || e.clientY === this.lastBarY) {
          return;
        }
        const aim: Aim = e.clientY < this.lastBarY ? -1 : 1;
        this.lastBarY = e.clientY;
        this.input(aim, null);
      },
      { passive: true },
    );
    for (const type of ["pointerup", "pointercancel"] as const) {
      document.addEventListener(
        type,
        () => {
          if (this.thumb) {
            this.thumb = false;
            this.touch();
          }
        },
        { passive: true },
      );
    }
    el.addEventListener(
      "scrollend",
      () => {
        if (this.open && !this.finger && !this.thumb && this.seen > 0 && !this.writeMark) {
          this.end();
        }
      },
      { passive: true },
    );
    // On the document: until something focuses the transcript, Chromium scrolls it with `activeElement` on `body`, so
    // the keydown never passes through the scroller. Bubble phase, so a control's own `preventDefault` runs first.
    document.addEventListener("keydown", (e) => {
      this.key(e);
    });
  }

  isOpen(): boolean {
    return this.open;
  }

  /** Whether the owner wrote within the last two frames: a `scrollend` then may be the write's own, which an engine
   *  fires in the write's frame while a wheel tick's animation carries on. */
  ownWriteRecent(): boolean {
    return this.writeMark;
  }

  noteOwnWrite(): void {
    const gen = ++this.writeGen;
    this.writeMark = true;
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        if (gen === this.writeGen) {
          this.writeMark = false;
        }
      });
    });
  }

  /** A scroll event the owner attributed to the open gesture. */
  noteScroll(): void {
    this.seen++;
    this.touch();
  }

  private touch(): void {
    this.lastActivity = Date.now();
    this.stillFrames = 0;
  }

  /** Open a gesture with no input behind it, for a reader move the owner saw only as a scroll event. */
  openAimless(): void {
    this.extend(0);
  }

  /** Drop the gesture and every input in progress without reporting an end. */
  forget(): void {
    this.open = false;
    this.seen = 0;
    this.finger = false;
    this.thumb = false;
    this.touchStart = null;
    this.lastTouchY = null;
    this.touchMoved = false;
    this.touchAim = 0;
    this.lastBarY = null;
    this.cancelIdle();
  }

  /** An input aimed `aim` from `target`; true when it opened or extended a gesture. */
  private input(aim: Aim, target: EventTarget | null, rejoin = true): boolean {
    if (aim !== 0 && target instanceof Element && this.nestedTakes(target, aim)) {
      return false;
    }
    if (!this.movable(aim)) {
      if (aim > 0 && rejoin && this.host.rejoinable()) {
        this.host.atEndDown();
      }
      return false;
    }
    this.extend(aim);
    return true;
  }

  private extend(aim: Aim): void {
    this.touch();
    if (this.open) {
      if (aim !== 0) {
        this.host.aimed(aim);
      }
      return;
    }
    this.open = true;
    this.seen = 0;
    this.host.opened(aim);
    this.queueIdle();
  }

  private end(): void {
    this.open = false;
    this.seen = 0;
    this.cancelIdle();
    this.host.ended();
  }

  /** Can an input aimed `aim` move the scroller? An input at a clamp the scroller already sits on moves nothing. */
  private movable(aim: Aim): boolean {
    const top = this.scrollEl.scrollTop;
    const end = this.host.endTop();
    const slack = edgeSlack();
    if (aim < 0) {
      return top > slack;
    }
    if (aim > 0) {
      return top < end - slack;
    }
    return end > slack;
  }

  /** Whether a scroller between `target` and this one takes the input, as the platform's scroll chaining does. */
  private nestedTakes(target: Element, aim: Aim): boolean {
    if (!this.scrollEl.contains(target)) {
      return false;
    }
    for (
      let el: Element | null = target;
      el !== null && el !== this.scrollEl;
      el = el.parentElement
    ) {
      if (el.scrollHeight <= el.clientHeight) {
        continue;
      }
      const oy = getComputedStyle(el).overflowY;
      if (oy !== "auto" && oy !== "scroll") {
        continue;
      }
      const canMove =
        aim < 0 ? el.scrollTop > 0 : Math.ceil(el.scrollTop) + el.clientHeight < el.scrollHeight;
      if (canMove) {
        return true;
      }
    }
    return false;
  }

  /** A finger moving down the screen scrolls the content up, so the aim is the inverse of this move's own step. */
  private touchMove(e: TouchEvent): void {
    const t = e.touches[0];
    const start = this.touchStart;
    // A move with no position, or no start to measure from, cannot prove a tap.
    this.touchMoved ||=
      t === undefined ||
      start === null ||
      Math.hypot(t.clientX - start.x, t.clientY - start.y) > TAP_SLOP_PX;
    const y = t?.clientY ?? null;
    const step = y === null || this.lastTouchY === null ? 0 : y - this.lastTouchY;
    this.lastTouchY = y;
    if (!this.touchMoved) {
      return;
    }
    if (step !== 0) {
      this.touchAim = step > 0 ? -1 : 1;
    }
    if (this.touchAim === 0) {
      return;
    }
    // The at-end rejoin needs the finger's net move up the screen past the slop, so a sideways drift never rejoins.
    const netUp = start === null || y === null ? 0 : start.y - y;
    if (this.input(this.touchAim, e.target, netUp > TAP_SLOP_PX)) {
      this.finger = true;
    }
  }

  private key(e: KeyboardEvent): void {
    if (this.scrollEl.getClientRects().length === 0 || e.defaultPrevented) {
      return;
    }
    const t = e.target instanceof HTMLElement ? e.target : null;
    if (
      (t !== null && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName))) ||
      shellTakesBodyKeys()
    ) {
      return;
    }
    if ((e.key === "Enter" || e.key === " ") && t !== null && this.viewOf().contains(t)) {
      this.host.pressed();
      if (e.key === " " && t.closest(SPACE_PRESSES) !== null) {
        return;
      }
    }
    // Under a modifier too: Ctrl+Home scrolls this box.
    if (SCROLL_UP_KEYS.has(e.key) || (e.key === " " && e.shiftKey)) {
      this.input(-1, t);
      return;
    }
    if (SCROLL_DOWN_KEYS.has(e.key)) {
      this.input(1, t);
      return;
    }
    if (e.key === "End" && !e.ctrlKey && !e.metaKey && !e.altKey) {
      this.host.endKey();
    }
  }

  private inScrollbarGutter(e: PointerEvent): boolean {
    const gutter = this.scrollEl.offsetWidth - this.scrollEl.clientWidth;
    return gutter > 0 && e.clientX >= this.scrollEl.getBoundingClientRect().right - gutter;
  }

  private queueIdle(): void {
    if (this.idleFrame !== 0) {
      return;
    }
    this.idleFrame = requestAnimationFrame(() => {
      this.idleFrame = 0;
      if (!this.open) {
        return;
      }
      this.stillFrames++;
      if (
        !this.finger &&
        !this.thumb &&
        this.stillFrames >= GESTURE_IDLE_FRAMES &&
        Date.now() - this.lastActivity >= GESTURE_IDLE_MS
      ) {
        this.end();
        return;
      }
      this.queueIdle();
    });
  }

  private cancelIdle(): void {
    if (this.idleFrame !== 0) {
      cancelAnimationFrame(this.idleFrame);
      this.idleFrame = 0;
    }
  }
}
