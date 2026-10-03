// ---------------------------------------------------------------------------
// Per-tab scroll offsets for a scroller every tab of one kind shares.
//
// A multi-instance view (run, subagent, spec) is ONE element re-pointed at each
// tab's subject, so the scroller's offset belongs to whichever instance was
// shown last. This keeps one offset per instance and puts it back when that
// instance is shown again; a never-seen instance lands at the top.
// ---------------------------------------------------------------------------

/** One shared scroller's per-instance offsets.
 *
 *  `scroller` is resolved lazily (the view's element may not exist at import),
 *  and `current` names the instance the scroller is showing right now, "" for
 *  none. Offsets are recorded on scroll rather than at departure, because leaving
 *  a tab runs none of the view's code and a hidden box reads every offset as 0. */
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
