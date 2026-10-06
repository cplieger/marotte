// Per-tab scroll offsets for a scroller every tab of one kind shares: a multi-instance view is ONE
// element re-pointed per tab, so one offset per instance is kept and restored; new ones start at top.

/** One shared scroller's per-instance offsets. `scroller` resolves lazily; `current` is the shown
 *  instance ("" for none). Recorded on scroll: a hidden box reads every offset as 0. */
export class SharedScroll {
  private readonly offsets = new Map<string, number>();
  private wired: HTMLElement | null = null;

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
        if (key !== "" && box.getClientRects().length > 0) {
          this.offsets.set(key, box.scrollTop);
        }
      },
      { passive: true },
    );
  }

  restore(key: string): void {
    const box = this.scroller();
    if (box === null || key === "") {
      return;
    }
    this.track();
    box.scrollTop = this.offsets.get(key) ?? 0;
  }

  forget(key: string): void {
    this.offsets.delete(key);
  }
}
