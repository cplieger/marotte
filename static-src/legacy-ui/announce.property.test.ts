import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import fc from "fast-check";

import { announce, mountLiveRegions, _resetForTest } from "./announce.js";
import { closeDialog, openDialog } from "./dialog.js";
import { nextTask } from "./__test-helpers__/reentry.js";

// Announcements against the browser's own top layer: three driven dialogs (two in the
// document, one in a shadow root) and one the page alone opens, which the library and
// the page open, close, move, remove and put back while messages are announced. The
// regions exposed are the browser's: with a modal open, those inside the modal the hit
// test finds on top; with none, the root's and every open dialog's.

type Kind =
  | "libOpen"
  | "libClose"
  | "pageModal"
  | "pageShow"
  | "pageClose"
  | "remove"
  | "restore"
  | "move"
  | "announce";

/** How a move inserts the node: moveBefore() keeps an open modal in the top layer, the
 *  other two take it out. */
type Method = "moveBefore" | "insertBefore" | "append";
/** Where a move puts the node: the body, a fresh element in it, the registered shadow
 *  root, or a fresh element in that root. */
type Place = "body" | "wrapper" | "shadow" | "shadowWrapper";
/** What follows a move: the moved node's removal, its new parent's, or the dialog's close. */
type Then = "remove" | "removeParent" | "close";

interface Step {
  readonly kind: Kind;
  readonly who: number;
  readonly assertive: boolean;
  readonly method: Method;
  readonly place: Place;
  readonly then: Then;
}

const stepArb: fc.Arbitrary<Step> = fc.record({
  kind: fc.constantFrom<Kind>(
    "libOpen",
    "libClose",
    "pageModal",
    "pageShow",
    "pageClose",
    "remove",
    "restore",
    "move",
    "announce",
  ),
  who: fc.nat({ max: 3 }),
  assertive: fc.boolean(),
  method: fc.constantFrom<Method>("moveBefore", "insertBefore", "append"),
  place: fc.constantFrom<Place>("body", "wrapper", "shadow", "shadowWrapper"),
  then: fc.constantFrom<Then>("remove", "removeParent", "close"),
});

/** A seed step; a move defaults to moveBefore() into a fresh element, then removal. */
function ex(kind: Kind, who: number, more: Partial<Step> = {}): Step {
  return {
    kind,
    who,
    assertive: false,
    method: "moveBefore",
    place: "wrapper",
    then: "remove",
    ...more,
  };
}

interface Heard {
  readonly message: string;
  readonly politeness: "polite" | "assertive";
}

interface Slot {
  readonly el: HTMLDialogElement;
  /** What the page moves, removes and puts back: the dialog, or its shadow host. */
  readonly node: Element;
  readonly driven: boolean;
}

const realized = async (): Promise<void> => {
  await nextTask();
  await nextTask();
};

function regionsOf(host: Element): HTMLElement[] {
  return [...host.children].filter(
    (c): c is HTMLElement => c instanceof HTMLElement && c.hasAttribute("aria-live"),
  );
}

/** The element at (1, 1), through every open shadow root on the way down. */
function deepHit(): Element | null {
  let hit = document.elementFromPoint(1, 1);
  while (hit?.shadowRoot) {
    const inner = hit.shadowRoot.elementFromPoint(1, 1);
    if (inner === null || inner === hit) {
      break;
    }
    hit = inner;
  }
  return hit;
}

async function run(steps: readonly Step[]): Promise<void> {
  const make = (label: string): HTMLDialogElement => {
    const d = document.createElement("dialog");
    d.dataset["label"] = label;
    return d;
  };
  const shadowHost = document.createElement("div");
  const a = make("a");
  const b = make("b");
  const s = make("s");
  const u = make("u");
  document.body.append(a, b, shadowHost, u);
  const shadow = shadowHost.attachShadow({ mode: "open" });
  shadow.append(s);
  mountLiveRegions(document.body);
  const slots: Slot[] = [
    { el: a, node: a, driven: true },
    { el: b, node: b, driven: true },
    { el: s, node: shadowHost, driven: true },
    { el: u, node: u, driven: false },
  ];
  for (const slot of slots) {
    if (slot.driven) {
      // Gives the dialog its engine, and so its pair, without opening it.
      closeDialog(slot.el);
    }
  }
  const all = [...regionsOf(document.body), ...slots.flatMap((x) => regionsOf(x.el))];

  /** The regions the browser exposes now, and of them the pair an announcement is for:
   *  the top modal's, or the root's with no modal open. */
  const exposed = (): { regions: Set<HTMLElement>; hearers: HTMLElement[] } => {
    const modals = slots.filter((x) => x.el.matches(":modal"));
    if (modals.length === 0) {
      const open = slots.filter((x) => x.el.open && x.el.isConnected);
      const root = regionsOf(document.body);
      return {
        regions: new Set([...root, ...open.flatMap((x) => regionsOf(x.el))]),
        hearers: root,
      };
    }
    const hit = deepHit();
    const top = modals.find((x) => x.el === hit);
    expect(top, "the hit test finds an open modal on top").toBeDefined();
    const pair = top === undefined ? [] : regionsOf(top.el);
    return { regions: new Set(pair), hearers: pair };
  };

  /** Where a move of `node` to `place` goes, or `null` when it cannot go there. */
  const destination = (node: Element, place: Place): ParentNode | null => {
    const inShadow = place === "shadow" || place === "shadowWrapper";
    if (inShadow && (node === shadowHost || !shadowHost.isConnected)) {
      return null;
    }
    const base = inShadow ? shadow : document.body;
    if (place === "body" || place === "shadow") {
      return base;
    }
    const wrapper = document.createElement("section");
    base.append(wrapper);
    return wrapper;
  };

  const trail: string[] = [];
  let said = 0;
  let before = exposed().regions;

  /** Record `label`, then hold the invariants against the exposure before the change. */
  const check = (label: string, wasModal: readonly Slot[], heard: Heard | null): void => {
    trail.push(label);
    const { regions: after, hearers } = exposed();
    const at = `after: ${trail.join(", ")}`;
    for (const region of after) {
      if (!before.has(region)) {
        expect(region.textContent, `a region the step exposed holds no text, ${at}`).toBe("");
      }
    }
    if (wasModal.some((x) => !x.el.matches(":modal"))) {
      for (const region of all) {
        if (!after.has(region)) {
          expect(region.textContent, `a region a modal's leaving hid holds no text, ${at}`).toBe(
            "",
          );
        }
      }
    }
    if (heard !== null) {
      const hearer = hearers.find((r) => r.getAttribute("aria-live") === heard.politeness);
      if (hearer !== undefined) {
        expect(hearer.textContent, `the exposed region hears it, ${at}`).toBe(heard.message);
      }
    }
    before = after;
  };
  const modalsNow = (): Slot[] => slots.filter((x) => x.el.matches(":modal"));

  try {
    for (const step of steps) {
      const slot = slots[step.who];
      if (slot === undefined) {
        throw new Error("no slot");
      }
      const { el, node } = slot;
      const label = el.dataset["label"] ?? "";
      const wasModal = modalsNow();
      let heard: Heard | null = null;
      switch (step.kind) {
        case "libOpen":
          if (!slot.driven || el.open || !el.isConnected) {
            continue;
          }
          openDialog(el);
          break;
        case "libClose":
          if (!slot.driven || !el.open) {
            continue;
          }
          closeDialog(el);
          vi.advanceTimersByTime(400);
          break;
        case "pageModal":
        case "pageShow":
          if (el.open || !el.isConnected) {
            continue;
          }
          if (step.kind === "pageModal") {
            el.showModal();
          } else {
            el.show();
          }
          break;
        case "pageClose":
          if (!el.open) {
            continue;
          }
          el.close();
          break;
        case "remove":
          if (!node.isConnected) {
            continue;
          }
          node.remove();
          break;
        case "restore":
          if (node.isConnected) {
            continue;
          }
          document.body.append(node);
          break;
        case "move": {
          if (!node.isConnected) {
            continue;
          }
          const into = destination(node, step.place);
          if (into === null) {
            continue;
          }
          // Chromium segfaults the renderer when moveBefore() puts a dialog open
          // by show() straight into a ShadowRoot (crbug.com/583431475); no
          // announce or dialog code path takes it. Remove once that is fixed.
          if (step.method === "moveBefore" && into === shadow && el.open && !el.matches(":modal")) {
            continue;
          }
          if (step.method === "append") {
            into.append(node);
          } else {
            into[step.method](node, null);
          }
          break;
        }
        case "announce":
          heard = {
            message: `m${String(++said)}`,
            politeness: step.assertive ? "assertive" : "polite",
          };
          announce(heard.message, heard.politeness);
          vi.advanceTimersByTime(100);
          break;
        default: {
          const unknown: never = step.kind;
          throw new Error(String(unknown));
        }
      }
      await realized();
      if (heard !== null) {
        check(`announce ${heard.politeness}`, wasModal, heard);
        continue;
      }
      if (step.kind !== "move") {
        check(`${step.kind} ${label}`, wasModal, null);
        continue;
      }
      check(`${step.method} ${label} to ${step.place}`, wasModal, null);
      const movedModal = modalsNow();
      const parent = node.parentElement;
      if (step.then === "close") {
        if (!el.open) {
          continue;
        }
        el.close();
      } else if (step.then === "removeParent" && parent !== null && parent !== document.body) {
        parent.remove();
      } else {
        node.remove();
      }
      await realized();
      check(`then ${step.then}`, movedModal, null);
    }
  } finally {
    for (const slot of slots) {
      if (!slot.node.isConnected) {
        document.body.append(slot.node);
      }
      if (slot.el.open) {
        slot.el.close();
      }
    }
    await realized();
    _resetForTest();
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

describe("announce: uncovered regions property", () => {
  it("exposes no region holding text a modal's close, move or removal uncovered, and the exposed one hears each message", async () => {
    await fc.assert(fc.asyncProperty(fc.array(stepArb, { minLength: 1, maxLength: 14 }), run), {
      numRuns: 100,
      examples: [
        // An assertive message in the top modal, then the modal closes over the one below.
        [
          [
            ex("libOpen", 0),
            ex("libOpen", 1),
            ex("announce", 0, { assertive: true }),
            ex("libClose", 1),
          ],
        ],
        // A root message, then a modal opens and the page closes it.
        [[ex("announce", 0), ex("libOpen", 0), ex("pageClose", 0)]],
        // The page removes the shadow host of the modal on top.
        [
          [
            ex("libOpen", 0),
            ex("libOpen", 2),
            ex("announce", 0, { assertive: true }),
            ex("remove", 2),
          ],
        ],
        // A modal the page alone opened closes over a driven one.
        [[ex("libOpen", 0), ex("pageModal", 3), ex("announce", 0), ex("pageClose", 3)]],
        // A closed modal's own pair when it opens again.
        [[ex("libOpen", 1), ex("announce", 0), ex("libClose", 1), ex("libOpen", 1)]],
        // The modal on top moved with moveBefore(), then removed from its new parent.
        [
          [
            ex("libOpen", 0),
            ex("libOpen", 1),
            ex("announce", 0, { assertive: true }),
            ex("move", 1),
          ],
        ],
        // The modal on top moved into the shadow root, then its new parent removed.
        [
          [
            ex("libOpen", 0),
            ex("libOpen", 1),
            ex("announce", 0),
            ex("move", 1, { place: "shadowWrapper", then: "removeParent" }),
          ],
        ],
        // The shadow host of the modal on top moved, then removed.
        [[ex("libOpen", 2), ex("announce", 0, { assertive: true }), ex("move", 2)]],
      ],
    });
  }, 60000);
});
