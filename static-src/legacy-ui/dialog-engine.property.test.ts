import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import fc from "fast-check";

import { closeDialog, createDialog, openDialog, type DialogController } from "./dialog.js";
import { createModal, _resetForTest, type ModalController } from "./modal.js";
import { topmostOpenDialog } from "./modal-host.js";
import { nextTask } from "./__test-helpers__/reentry.js";

// The differential property of a dialog's engine: an open or a close has the
// same effects whether the library or the page makes it. Each edge sequence runs
// twice on fresh dialogs, every edge through the library and then each by whoever
// the case names, and after each realized edge both runs must show the same view.
// `a` is a createModal; `b` is the caller's dialog, whose createDialog is made
// before the edge at `createAt`, so its earlier edges reach a dialog with no engine.

type Who = "a" | "b";
type By = "engine" | "page";
type PageClose = "close" | "removeOpen" | "form";

interface EdgeSpec {
  readonly who: Who;
  readonly by: By;
  readonly via: PageClose;
  /** For a close: a closeDialog callback is given first, starting the fade the close ends. */
  readonly cb: boolean;
}

interface Case {
  readonly edges: readonly EdgeSpec[];
  readonly createAt: number;
}

const caseArb: fc.Arbitrary<Case> = fc
  .array(
    fc.record({
      who: fc.constantFrom<Who>("a", "b"),
      by: fc.constantFrom<By>("engine", "page"),
      via: fc.constantFrom<PageClose>("close", "removeOpen", "form"),
      cb: fc.boolean(),
    }),
    { minLength: 1, maxLength: 8 },
  )
  .chain((edges) =>
    fc.record({ edges: fc.constant(edges), createAt: fc.nat({ max: edges.length }) }),
  );

function labelOf(el: Element | null): string {
  return el?.getAttribute("data-label") ?? el?.tagName.toLowerCase() ?? "null";
}

function bodyLock(): string {
  const st = document.body.style;
  return `${st.position}|${st.overflow}|${st.width}`;
}

/** Content with a dialog form and two buttons; the second is the initial focus. */
function content(who: Who): { root: HTMLElement; form: HTMLFormElement; focus: HTMLElement } {
  const root = document.createElement("div");
  const form = document.createElement("form");
  form.method = "dialog";
  const buttons = [0, 1].map((i) => {
    const btn = document.createElement("button");
    btn.setAttribute("data-label", `${who}${i.toString()}`);
    return btn;
  });
  form.append(...buttons);
  root.append(form);
  return { root, form, focus: buttons[1] ?? root };
}

/** Whether the second run makes edge `i` from the page. */
function pageMade(c: Case, i: number): boolean {
  const spec = c.edges[i];
  return spec !== undefined && !(spec.by === "engine" && (spec.who === "a" || i >= c.createAt));
}

/** The first close of a modal dialog the second run makes by removing `open`.
 *  HTML ("the dialog element") leaves that dialog in the top layer, blocking the
 *  document, so from there focus is the platform's to refuse: it is not compared. */
function strandedFrom(c: Case): number {
  const open: Record<Who, boolean> = { a: false, b: false };
  for (const [i, spec] of c.edges.entries()) {
    if (open[spec.who] && spec.via === "removeOpen" && pageMade(c, i)) {
      return i;
    }
    open[spec.who] = !open[spec.who];
  }
  return c.edges.length;
}

/** Run `c`'s edges, every one through the library when `allEngine`; returns the
 *  view after each edge from `createAt` on. */
async function run(c: Case, allEngine: boolean): Promise<string[]> {
  const stranded = strandedFrom(c);
  const opener = document.createElement("button");
  opener.setAttribute("data-label", "opener");
  const bDialog = document.createElement("dialog");
  bDialog.setAttribute("data-label", "b");
  const bParts = content("b");
  bDialog.append(bParts.root);
  document.body.append(opener, bDialog);
  opener.focus();

  const closes = { a: 0, b: 0 };
  const cbCalls: number[] = [];
  const aParts = content("a");
  const a: ModalController = createModal(aParts.root, {
    initialFocus: aParts.focus,
    onClose: () => {
      closes.a++;
    },
  });
  a.el.setAttribute("data-label", "a");
  let b: DialogController | null = null;
  const els: Record<Who, HTMLDialogElement> = { a: a.el, b: bDialog };
  const forms: Record<Who, HTMLFormElement> = { a: aParts.form, b: bParts.form };
  const open: Record<Who, boolean> = { a: false, b: false };

  const view = (): string => {
    const parts = (["a", "b"] as const).map((who) => {
      const el = els[who];
      return [
        who,
        `open=${String(el.open)}`,
        // Read only while open: Chromium leaves a modal dialog `:modal`, and the page
        // inert, after the page removes its `open` attribute, which no library call can undo.
        `modal=${String(el.open && el.matches(":modal"))}`,
        `leaving=${String(el.classList.contains("is-leaving"))}`,
        `regions=${el.querySelectorAll(":scope > .uip-visually-hidden").length.toString()}`,
        `onClose=${closes[who].toString()}`,
      ].join(" ");
    });
    return [
      ...parts,
      `top=${labelOf(topmostOpenDialog())}`,
      `lock=${bodyLock()}`,
      `timers=${vi.getTimerCount().toString()}`,
      `callbacks=${cbCalls.join(",")}`,
    ].join("; ");
  };
  const realized = async (): Promise<void> => {
    await nextTask();
    await nextTask();
  };

  const views: string[] = [];
  const create = async (): Promise<DialogController> => {
    const made = createDialog(bDialog, {
      onClose: () => {
        closes.b++;
      },
    });
    await realized();
    views.push(`create: ${view()}`);
    return made;
  };
  try {
    for (const [i, spec] of c.edges.entries()) {
      if (i === c.createAt) {
        b = await create();
      }
      const { who } = spec;
      const el = els[who];
      const managed = who === "a" || b !== null;
      const byEngine = allEngine || !pageMade(c, i);
      let what: string;
      if (!open[who]) {
        what = `open ${who}`;
        if (byEngine) {
          if (who === "a") {
            a.open();
          } else if (b !== null) {
            b.open();
          } else {
            openDialog(el);
          }
        } else {
          el.showModal();
        }
        await realized();
        // Initial focus is the library's on every open; focus after a close is the platform's.
        if (i < stranded) {
          what += ` focus=${labelOf(document.activeElement)}`;
        }
      } else {
        const withCb = spec.cb && managed;
        what = `close ${who}${withCb ? " with a callback" : ""}`;
        if (withCb) {
          const id = i;
          closeDialog(el, () => {
            cbCalls.push(id);
          });
        } else if (byEngine) {
          if (who === "a") {
            a.close();
          } else if (b !== null) {
            b.close();
          } else {
            closeDialog(el);
          }
        }
        if (byEngine) {
          vi.advanceTimersByTime(400);
        } else if (spec.via === "close") {
          el.close();
        } else if (spec.via === "removeOpen") {
          el.removeAttribute("open");
        } else {
          forms[who].requestSubmit();
        }
        await realized();
      }
      open[who] = !open[who];
      // Before b's controller exists only the rank is the library's in both runs.
      views.push(
        i >= c.createAt
          ? `${what}: ${view()}`
          : `${open[who] ? "open" : "close"} ${who}: top=${labelOf(topmostOpenDialog())}`,
      );
    }
    if (c.createAt === c.edges.length) {
      b = await create();
    }
    return views;
  } finally {
    b?.dispose();
    a.dispose();
    if (bDialog.open) {
      bDialog.close();
    }
    await realized();
    vi.clearAllTimers();
    document.body.replaceChildren();
    _resetForTest();
  }
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  document.body.replaceChildren();
  _resetForTest();
});

describe("dialog engine: differential property", () => {
  it("an open or a close has the same effects whether the library or the page makes it", async () => {
    await fc.assert(
      fc.asyncProperty(caseArb, async (c) => {
        const reference = await run(c, true);
        const mixed = await run(c, false);
        expect(mixed).toEqual(reference);
      }),
      {
        numRuns: 100,
        examples: [
          // The page opens b before its controller exists, then the library opens a over
          // it: a, the dialog the platform opened last, stays on top once b is adopted.
          [
            {
              edges: [
                { who: "b", by: "page", via: "close", cb: false },
                { who: "a", by: "engine", via: "close", cb: false },
                { who: "a", by: "engine", via: "close", cb: false },
              ],
              createAt: 2,
            },
          ],
          // A page open of a takes its initial focus as the library's open does.
          [{ edges: [{ who: "a", by: "page", via: "close", cb: false }], createAt: 1 }],
          // A page close during a closeDialog fade calls the callback and onClose once.
          [
            {
              edges: [
                { who: "a", by: "engine", via: "close", cb: false },
                { who: "a", by: "page", via: "removeOpen", cb: true },
              ],
              createAt: 0,
            },
          ],
        ],
      },
    );
  }, 60000);
});
