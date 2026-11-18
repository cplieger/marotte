// Per-instance scroll offsets for a scroller several subjects share in turn: a multi-instance view is ONE element
// re-pointed per tab, and a view with sub-tabs swaps their panels inside one scroller. One offset per subject is kept
// and restored; a subject not shown before starts at the top.

/** One shared scroller's per-subject offsets. `scroller` resolves lazily; `current` is the shown
 *  subject ("" for none). Recorded on scroll: a hidden box reads every offset as 0. */
export class SharedScroll {
  private readonly offsets = new Map<string, number>();
  private wired: HTMLElement | null = null;
  /** The subject a `restore` found the box unlaid-out for, "" for none. While set, a scroll is not
   *  recorded: the box's first layout can carry the previous subject's offset (`dom.ts` `unpark`
   *  puts back the one it parked with), and it is this subject's to replace. */
  private pending = "";

  constructor(
    private readonly scroller: () => HTMLElement | null,
    private readonly current: () => string,
  ) {}

  /** Start recording. Idempotent, and re-wires if the element was replaced. */
  track(): void {
    const box = this.scroller();
    if (box === null || box === this.wired) {
      return;
    }
    this.wired = box;
    box.addEventListener(
      "scroll",
      () => {
        const key = this.current();
        if (this.pending === "" && key !== "" && box.getClientRects().length > 0) {
          this.offsets.set(key, box.scrollTop);
        }
      },
      { passive: true },
    );
    // A box gains a size when it is laid out again, which is the first moment a deferred restore can land.
    new ResizeObserver(() => {
      if (this.pending !== "" && box === this.wired && box.getClientRects().length > 0) {
        const key = this.pending;
        this.pending = "";
        if (key === this.current()) {
          box.scrollTop = this.offsets.get(key) ?? 0;
        }
      }
    }).observe(box);
  }

  /** Put the box at `key`'s offset now, or at its next layout when it has none. */
  restore(key: string): void {
    const box = this.scroller();
    if (box === null || key === "") {
      return;
    }
    this.track();
    if (box.getClientRects().length === 0) {
      this.pending = key;
      return;
    }
    this.pending = "";
    box.scrollTop = this.offsets.get(key) ?? 0;
  }

  /** The offset recorded for `key`, or undefined when none is. */
  saved(key: string): number | undefined {
    return this.offsets.get(key);
  }

  forget(key: string): void {
    this.offsets.delete(key);
  }

  /** Forget every recorded key `match` accepts. */
  forgetWhere(match: (key: string) => boolean): void {
    for (const key of [...this.offsets.keys()]) {
      if (match(key)) {
        this.offsets.delete(key);
      }
    }
  }
}
