import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import fc from "fast-check";

import { closeDialog, createDialog, openDialog, type DialogController } from "./dialog.js";
import { topmostOpenDialog } from "./modal-host.js";
import { nextTask } from "./__test-helpers__/reentry.js";

// topmostOpenDialog against a reference model of the top layer: four dialogs in three
// trees (the document, two shadow roots) that the library and the page open, close and
// move. The model stamps, in order, every opening of a driven dialog wherever it is,
// and any other opening only in a reached tree: the document, or a shadow root where
// the library made an engine, opened a dialog or stamped an opening. Of the known open
// dialogs (driven ones, others in a reached tree) the newest-stamped modal wins, else
// the newest-stamped open one, else any unstamped one. The model's top layer is
// checked against the browser's hit test.

type Kind = "libOpen" | "libClose" | "pageModal" | "pageShow" | "pageClose" | "create" | "move";

interface Step {
  readonly kind: Kind;
  readonly who: number;
  /** The tree a `move` takes the dialog to: 0 the document, 1 and 2 the shadow roots. */
  readonly to: number;
}

const stepArb: fc.Arbitrary<Step> = fc.record({
  kind: fc.constantFrom<Kind>(
    "libOpen",
    "libClose",
    "pageModal",
    "pageShow",
    "pageClose",
    "create",
    "move",
  ),
  who: fc.nat({ max: 3 }),
  to: fc.nat({ max: 2 }),
});

interface Slot {
  readonly el: HTMLDialogElement;
  tree: number;
  driven: boolean;
  modal: boolean;
  stamp: number | null;
  controller: DialogController | null;
}

const realized = async (): Promise<void> => {
  await nextTask();
  await nextTask();
};

async function run(steps: readonly Step[]): Promise<void> {
  const make = (label: string): HTMLDialogElement => {
    const d = document.createElement("dialog");
    d.dataset["label"] = label;
    return d;
  };
  const hosts = [document.createElement("div"), document.createElement("div")];
  document.body.append(...hosts);
  const trees: ParentNode[] = [
    document.body,
    ...hosts.map((h) => h.attachShadow({ mode: "open" })),
  ];
  const slots: Slot[] = (
    [
      ["d0", 0],
      ["d1", 0],
      ["s0", 1],
      ["s1", 1],
    ] as const
  ).map(([label, tree]) => {
    const el = make(label);
    trees[tree]?.append(el);
    return { el, tree, driven: false, modal: false, stamp: null, controller: null };
  });
  const reached = new Set<number>([0]);
  let clock = 0;
  const topLayer: Slot[] = [];
  // The engine is made on the library's first call on the dialog and reaches the tree
  // it is in then; the library's every opening reaches the tree it is in at that moment.
  const drive = (s: Slot): void => {
    if (!s.driven) {
      s.driven = true;
      reached.add(s.tree);
    }
  };
  const opened = (s: Slot, modal: boolean): void => {
    s.modal = modal;
    s.stamp = s.driven || reached.has(s.tree) ? ++clock : null;
    if (s.stamp !== null) {
      reached.add(s.tree);
    }
    if (modal) {
      topLayer.push(s);
    }
  };
  const closed = (s: Slot): void => {
    topLayer.splice(topLayer.indexOf(s), 1);
  };
  const isOpen = (s: Slot): boolean => s.el.open;
  const label = (el: Element | null): string =>
    (el as HTMLElement | null)?.dataset["label"] ?? "null";

  const expected = (): readonly HTMLDialogElement[] => {
    const known = slots.filter((s) => isOpen(s) && (s.driven || reached.has(s.tree)));
    const modals = known.filter((s) => s.modal);
    const pool = modals.length > 0 ? modals : known;
    const stamped = pool.filter((s) => s.stamp !== null);
    if (stamped.length === 0) {
      return pool.map((s) => s.el);
    }
    const newest = stamped.reduce((x, y) => ((y.stamp ?? 0) > (x.stamp ?? 0) ? y : x));
    return [newest.el];
  };

  const trail: string[] = [];
  try {
    for (const step of steps) {
      const s = slots[step.who];
      if (s === undefined) {
        throw new Error("no slot");
      }
      const name = label(s.el);
      switch (step.kind) {
        case "libOpen":
          if (isOpen(s)) {
            continue;
          }
          drive(s);
          reached.add(s.tree);
          openDialog(s.el);
          opened(s, true);
          break;
        case "libClose":
          if (!isOpen(s)) {
            continue;
          }
          drive(s);
          closeDialog(s.el);
          vi.advanceTimersByTime(400);
          if (s.modal) {
            closed(s);
          }
          break;
        case "pageModal":
          if (isOpen(s)) {
            continue;
          }
          s.el.showModal();
          opened(s, true);
          break;
        case "pageShow":
          if (isOpen(s)) {
            continue;
          }
          s.el.show();
          opened(s, false);
          break;
        case "pageClose":
          if (!isOpen(s)) {
            continue;
          }
          s.el.close();
          if (s.modal) {
            closed(s);
          }
          break;
        case "create":
          if (s.controller !== null) {
            continue;
          }
          drive(s);
          s.controller = createDialog(s.el);
          break;
        case "move":
          // An open dialog leaves the top layer when removed; only closed ones move.
          if (isOpen(s) || s.tree === step.to) {
            continue;
          }
          trees[step.to]?.append(s.el);
          s.tree = step.to;
          break;
        default: {
          const unknown: never = step.kind;
          throw new Error(String(unknown));
        }
      }
      await realized();
      trail.push(
        step.kind === "move" ? `move ${name} to ${String(step.to)}` : `${step.kind} ${name}`,
      );
      const platformTop = topLayer.at(-1);
      if (platformTop !== undefined) {
        const hit = document.elementFromPoint(1, 1);
        expect(hit, `the model's top layer after: ${trail.join(", ")}`).toBe(
          platformTop.tree === 0 ? platformTop.el : hosts[platformTop.tree - 1],
        );
      }
      const want = expected();
      const got = topmostOpenDialog();
      expect(
        want.length === 0 ? got === null : want.includes(got as HTMLDialogElement),
        `after ${trail.join(", ")}: got ${label(got)}, want one of [${want.map(label).join(",")}]`,
      ).toBe(true);
    }
  } finally {
    for (const s of slots) {
      s.controller?.dispose();
      if (s.el.open) {
        s.el.close();
      }
    }
    await realized();
    vi.clearAllTimers();
    document.body.replaceChildren();
  }
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  document.body.replaceChildren();
});

describe("topmostOpenDialog: top-layer property", () => {
  it("returns the open modal the platform put on top last, of the dialogs the library observed", async () => {
    await fc.assert(fc.asyncProperty(fc.array(stepArb, { minLength: 1, maxLength: 12 }), run), {
      numRuns: 100,
      examples: [
        // The page's showModal() over a dialog the library holds puts the page's on top.
        [
          [
            { kind: "libOpen", who: 0, to: 0 },
            { kind: "pageModal", who: 1, to: 0 },
          ],
        ],
        // A page modal in a shadow root a controller reached, over a held document dialog.
        [
          [
            { kind: "create", who: 2, to: 0 },
            { kind: "libOpen", who: 0, to: 0 },
            { kind: "pageModal", who: 3, to: 0 },
          ],
        ],
        // A shadow-root opening no observer saw ranks below the observed one.
        [
          [
            { kind: "libOpen", who: 0, to: 0 },
            { kind: "pageModal", who: 2, to: 0 },
            { kind: "create", who: 2, to: 0 },
          ],
        ],
        // A page show() does not outrank an open modal.
        [
          [
            { kind: "pageModal", who: 0, to: 0 },
            { kind: "pageShow", who: 1, to: 0 },
          ],
        ],
        // The library opens a driven dialog moved into a shadow root over an older modal.
        [
          [
            { kind: "create", who: 1, to: 0 },
            { kind: "move", who: 1, to: 2 },
            { kind: "libOpen", who: 0, to: 0 },
            { kind: "libOpen", who: 1, to: 0 },
          ],
        ],
        // The page opens a driven dialog moved between shadow roots over an older modal.
        [
          [
            { kind: "create", who: 2, to: 0 },
            { kind: "move", who: 2, to: 2 },
            { kind: "libOpen", who: 0, to: 0 },
            { kind: "pageModal", who: 2, to: 0 },
          ],
        ],
      ],
    });
  }, 60000);
});
