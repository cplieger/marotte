// engine.ts — Pure timer/queue state machine for toasts. No DOM: it drives an
// injected `ToastView` port, so it is fully testable headless. Owns the queue,
// the visible set, promotion, per-toast timers, and pause/resume math.

/** Severity of a toast. It selects the skin modifier class, the politeness of
 *  the screen-reader announcement, and the default duration — `"error"` is
 *  sticky, the other two auto-dismiss. */
export type ToastLevel = "info" | "success" | "error";

/** A retry affordance rendered inside the toast: a button the user can press to
 *  re-attempt whatever failed. */
export interface ToastRetry {
  /** Button text. Default `"Retry"`. */
  readonly label?: string;
  /** Runs on press, after the toast has been dismissed. The result is not
   *  awaited: a throw or a rejection is logged and swallowed, so reporting the
   *  retry's own outcome is the caller's job. */
  readonly onClick: () => void | Promise<void>;
}

/** Per-toast overrides, all optional: the level defaults to `"info"` and the
 *  duration to that level's default, so `show(message)` alone is a complete
 *  call. */
export interface ToastOptions {
  /** Severity. Default `"info"`. */
  level?: ToastLevel;
  /** Auto-dismiss after this many ms. `0` = sticky (manual dismiss only). */
  duration?: number;
  retry?: ToastRetry;
}

const DEFAULT_MAX_VISIBLE = 3;
const DEFAULT_MAX_QUEUE = 20;
const DEFAULT_DURATION_MS = 4000;
const ERROR_DURATION_MS = 0;

/** The per-level default duration: sticky (0) for errors, `fallback` otherwise. */
function defaultDurationFor(level: ToastLevel, fallback: number): number {
  return level === "error" ? ERROR_DURATION_MS : fallback;
}

/** Immutable data the view needs to render one toast. */
export interface ToastRenderData {
  readonly message: string;
  readonly level: ToastLevel;
  readonly duration: number;
  readonly retry?: ToastRetry;
}

/** Per-toast callbacks the view wires to DOM events. */
export interface ToastCallbacks {
  dismiss(): void;
  pause(): void;
  resume(): void;
}

/** DOM port. The engine holds handles of type `H` opaquely. */
export interface ToastView<H> {
  mount(data: ToastRenderData, ctx: ToastCallbacks): H;
  /** Animate out, then call `done()` once the node is gone. */
  scheduleLeave(handle: H, done: () => void): void;
  /** Remove immediately, no animation. */
  remove(handle: H): void;
  pauseProgress(handle: H): void;
  resumeProgress(handle: H): void;
  /** Tear down any shared container. */
  dispose(): void;
}

/** Construction options for {@link ToastEngine}: the view port it drives, plus
 *  the capacity, duration and mode knobs a `Toaster` forwards from its own
 *  options. `view` is the only required field. */
interface ToastEngineOptions<H> {
  view: ToastView<H>;
  maxVisible?: number;
  maxQueue?: number;
  defaultDuration?: number;
  /** `"stack"` (default): up to `maxVisible` toasts show at once, the rest
   *  queue. `"replace"`: single-slot latest-wins — a new toast instantly
   *  replaces the visible one (no leave animation, no queue), the embeddable-
   *  widget semantics where a stale "Copied" queue would be wrong. */
  mode?: "stack" | "replace";
}

interface ActiveToast<H> {
  readonly id: number;
  readonly duration: number;
  remaining: number;
  startedAt: number;
  timer: ReturnType<typeof setTimeout> | null;
  handle: H;
  dismissed: boolean;
}

interface QueuedToast {
  readonly id: number;
  readonly message: string;
  readonly level: ToastLevel;
  readonly duration: number;
  readonly retry?: ToastRetry;
  bindDismiss(dismiss: () => void): void;
}

/** The toast state machine, holding the queue, the visible set, per-toast
 *  timers and the pause/resume math, and driving an injected {@link ToastView}.
 *  Touches no DOM itself: handles of type `H` come from the view and are held
 *  opaquely. */
export class ToastEngine<H> {
  private readonly view: ToastView<H>;
  private readonly maxVisible: number;
  private readonly maxQueue: number;
  private readonly defaultDuration: number;
  private readonly replace: boolean;
  private readonly visible: ActiveToast<H>[] = [];
  private readonly queue: QueuedToast[] = [];
  private idSeq = 0;

  constructor(opts: ToastEngineOptions<H>) {
    this.view = opts.view;
    this.replace = opts.mode === "replace";
    this.maxVisible = this.replace ? 1 : Math.max(1, opts.maxVisible ?? DEFAULT_MAX_VISIBLE);
    this.maxQueue = Math.max(0, opts.maxQueue ?? DEFAULT_MAX_QUEUE);
    this.defaultDuration = opts.defaultDuration ?? DEFAULT_DURATION_MS;
  }

  /** Show a toast (or queue it if the visible set is full). Returns a dismiss fn. */
  show(message: string, opts?: ToastOptions): () => void {
    const level = opts?.level ?? "info";
    const duration = opts?.duration ?? defaultDurationFor(level, this.defaultDuration);
    const retry = opts?.retry;
    const id = ++this.idSeq;

    // No leave animation — the new message must not coexist with or wait for the old one.
    if (this.replace && this.visible.length > 0) {
      for (const t of [...this.visible]) {
        if (t.timer !== null) {
          clearTimeout(t.timer);
          t.timer = null;
        }
        t.dismissed = true;
        this.view.remove(t.handle);
      }
      this.visible.length = 0;
    }

    if (this.visible.length < this.maxVisible) {
      this.mountToast(id, message, level, duration, retry);
      return () => {
        this.dismiss(id);
      };
    }

    let mountedDismiss: (() => void) | null = null;
    let cancelled = false;
    const queued: QueuedToast = {
      id,
      message,
      level,
      duration,
      ...(retry !== undefined ? { retry } : {}),
      bindDismiss: (dismiss) => {
        if (cancelled) {
          dismiss();
        } else {
          mountedDismiss = dismiss;
        }
      },
    };
    this.enqueue(queued);
    return () => {
      if (mountedDismiss !== null) {
        mountedDismiss();
        return;
      }
      cancelled = true;
      const i = this.queue.indexOf(queued);
      if (i !== -1) {
        this.queue.splice(i, 1);
      }
    };
  }

  /** Dismiss the newest visible toast (Escape handler target). */
  dismissNewest(): void {
    const newest = this.visible[this.visible.length - 1];
    if (newest !== undefined) {
      this.dismiss(newest.id);
    }
  }

  /** Immediately remove all visible toasts and clear the queue (test-friendly). */
  clear(): void {
    for (const t of [...this.visible]) {
      if (t.timer !== null) {
        clearTimeout(t.timer);
        t.timer = null;
      }
      t.dismissed = true;
      this.view.remove(t.handle);
    }
    this.visible.length = 0;
    this.queue.length = 0;
  }

  private mountToast(
    id: number,
    message: string,
    level: ToastLevel,
    duration: number,
    retry: ToastRetry | undefined,
  ): void {
    const data: ToastRenderData = {
      message,
      level,
      duration,
      ...(retry !== undefined ? { retry } : {}),
    };
    const ctx: ToastCallbacks = {
      dismiss: () => {
        this.dismiss(id);
      },
      pause: () => {
        this.pause(id);
      },
      resume: () => {
        this.resume(id);
      },
    };
    const handle = this.view.mount(data, ctx);
    const record: ActiveToast<H> = {
      id,
      duration,
      remaining: duration,
      startedAt: Date.now(),
      timer: null,
      handle,
      dismissed: false,
    };
    this.visible.push(record);
    this.startTimer(record);
  }

  private enqueue(entry: QueuedToast): void {
    if (this.maxQueue <= 0) {
      return;
    }
    while (this.queue.length >= this.maxQueue) {
      this.queue.shift();
    }
    this.queue.push(entry);
  }

  private promote(): void {
    while (this.visible.length < this.maxVisible) {
      const next = this.queue.shift();
      if (next === undefined) {
        return;
      }
      this.mountToast(next.id, next.message, next.level, next.duration, next.retry);
      next.bindDismiss(() => {
        this.dismiss(next.id);
      });
    }
  }

  private startTimer(t: ActiveToast<H>): void {
    if (t.duration <= 0 || t.remaining <= 0) {
      return;
    }
    t.startedAt = Date.now();
    t.timer = setTimeout(() => {
      this.dismiss(t.id);
    }, t.remaining);
  }

  private pause(id: number): void {
    const t = this.find(id);
    if (t === undefined || t.duration <= 0 || t.timer === null) {
      return;
    }
    clearTimeout(t.timer);
    t.timer = null;
    const elapsed = Date.now() - t.startedAt;
    t.remaining = Math.max(0, t.remaining - elapsed);
    this.view.pauseProgress(t.handle);
  }

  private resume(id: number): void {
    const t = this.find(id);
    if (t === undefined || t.duration <= 0 || t.timer !== null) {
      return;
    }
    if (t.remaining <= 0) {
      // A backgrounded tab can let the wall clock pass the deadline before the
      // pending timeout runs, draining `remaining` to 0 with no timer left to
      // fire. Releasing the hold is the last chance to run the promised
      // auto-dismiss, or it stays on screen until clicked/Escaped/cleared.
      this.dismiss(id);
      return;
    }
    this.startTimer(t);
    this.view.resumeProgress(t.handle);
  }

  private dismiss(id: number): void {
    const record = this.visible.find((t) => t.id === id);
    if (record === undefined || record.dismissed) {
      return;
    }
    record.dismissed = true;
    if (record.timer !== null) {
      clearTimeout(record.timer);
      record.timer = null;
    }
    this.view.scheduleLeave(record.handle, () => {
      const i = this.visible.indexOf(record);
      if (i !== -1) {
        this.visible.splice(i, 1);
      }
      this.promote();
    });
  }

  private find(id: number): ActiveToast<H> | undefined {
    return this.visible.find((t) => t.id === id);
  }
}
