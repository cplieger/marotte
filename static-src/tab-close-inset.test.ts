// The ×'s inset from the row's trailing edge is not the row's text padding (`--sp-3`): `.tab-close`
// paints a 24px box, so its four gaps must match (8px). A LAYOUT measurement: the gaps come from
// three mechanisms and the box size is tier-dependent. The flex name's width is not asserted.

import { describe, it, expect, beforeAll, afterAll } from "vitest";
import { page } from "vitest/browser";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;
let host: HTMLElement;
/** `page.viewport` has no getter, so the entry size is read off the frame rather
 *  than copied from `vitest.config.ts`, which would silently leave every later file
 *  measuring at the old size if that config moved. */
let entry: { readonly width: number; readonly height: number };

beforeAll(() => {
  entry = { width: window.innerWidth, height: window.innerHeight };
  style = mountAppCSS();
  host = document.createElement("div");
  // A sidebar-width column, so the name is long enough to be the row's flexible
  // child rather than letting the row shrink-wrap its content.
  host.style.cssText = "inline-size:260px;";
  document.body.appendChild(host);
});

afterAll(async () => {
  style.remove();
  host.remove();
  await page.viewport(entry.width, entry.height);
});

interface Row {
  readonly row: HTMLElement;
  readonly name: HTMLElement;
  readonly close: HTMLElement;
}

/** A chat tab row in the shape `createTabEl` appends: dot, run dot, name, the two
 *  screen-reader spans, pin, ×. */
function tabRow(): Row {
  const list = document.createElement("div");
  list.id = "tab-list";
  const row = document.createElement("div");
  row.className = "tab active";
  row.setAttribute("role", "tab");
  const span = (cls: string, text = ""): HTMLElement => {
    const el = document.createElement("span");
    el.className = cls;
    el.textContent = text;
    return el;
  };
  const name = span("tab-name", "a chat title long enough to be clipped by the row");
  const close = span("tab-close");
  // The glyph, SIZED like `iconEl`: an unsized <svg> takes the UA's 150px height and overflows the
  // box, so probes would answer for the glyph instead of the expander.
  const glyph = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  glyph.setAttribute("width", "16");
  glyph.setAttribute("height", "16");
  close.appendChild(glyph);
  row.append(
    span("tab-status-dot"),
    span("tab-run-dot"),
    name,
    span("tab-status-dot-sr sr-only"),
    span("tab-run-dot-sr sr-only"),
    span("tab-pin"),
    close,
  );
  list.appendChild(row);
  host.replaceChildren(list);
  return { row, name, close };
}

/** The four distances, named. A `Record<string, number>` would be a weaker type than
 *  the truth — the keys are fixed — and under `noPropertyAccessFromIndexSignature` it
 *  also forces every reader to index with brackets. */
interface Insets {
  readonly top: number;
  readonly bottom: number;
  readonly trailing: number;
  readonly toName: number;
}

/** The four distances that make the × look centred in its corner, in CSS px. The
 *  row's reserved 1px border is subtracted from the three that cross it, so all four
 *  are padding-or-gap and therefore comparable. */
function insets({ row, name, close }: Row): Insets {
  const r = row.getBoundingClientRect();
  const c = close.getBoundingClientRect();
  const n = name.getBoundingClientRect();
  const border = parseFloat(getComputedStyle(row).borderTopWidth);
  return {
    top: c.top - r.top - border,
    bottom: r.bottom - c.bottom - border,
    trailing: r.right - c.right - border,
    toName: c.left - n.right,
  };
}

describe("the × is inset equally on all four sides", () => {
  it("measures one value at the row's own gap, on a fine pointer", () => {
    const row = tabRow();
    const gap = parseFloat(getComputedStyle(row.row).columnGap);
    const i = insets(row);
    expect(gap, "the row's gap is --sp-2").toBe(8);
    expect(i, "trailing was --sp-3 (12px) against 8px on the other three").toEqual({
      top: gap,
      bottom: gap,
      trailing: gap,
      toName: gap,
    });
  });

  it("keeps the trailing rung shorter than the leading one, which is deliberate", () => {
    // The leading dot or arrow paints no box, so its inset stays the row padding: asymmetric on purpose.
    const cs = getComputedStyle(tabRow().row);
    expect(parseFloat(cs.paddingInlineStart)).toBeGreaterThan(parseFloat(cs.paddingInlineEnd));
  });

  // ON A PHONE THE TARGET GROWS AND THE PAINTED BOX DOES NOT: a 44px × stacked on the row's chrome
  // made every row 62px, so it takes the `--hit-floor` `::after` expander (`.tab-close` is a SPAN,
  // since `role="tab"` is Children Presentational, so `61-mcp-tools.css`'s floor misses it). The
  // target half is a real hit test, never a style read.
  describe("on a phone", () => {
    /** Each case sets its own viewport: 390x844 trips `01-tokens.css`'s `width <= 48rem` fallback,
     *  moving `--hit-floor` to 2.75rem with no `data-pointer`. */
    async function phoneRow(): Promise<Row> {
      await page.viewport(390, 844);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        390, 844,
      ]);
      expect(
        getComputedStyle(document.documentElement).getPropertyValue("--hit-floor").trim(),
        "the width fallback moved the floor, so this is the coarse tier",
      ).toBe("2.75rem");
      return tabRow();
    }

    /** `--hit-floor` in CSS px, measured through a consuming element (`getPropertyValue` returns the
     *  authored `2.75rem`). */
    function resolvedFloor(): number {
      const probe = document.createElement("div");
      probe.style.cssText = "position:absolute;visibility:hidden;block-size:var(--hit-floor);";
      host.appendChild(probe);
      const h = probe.getBoundingClientRect().height;
      probe.remove();
      expect(h, "the floor resolved to a real length").toBeGreaterThan(0);
      return h;
    }

    it("keeps the painted box at its 24px desktop size", async () => {
      // The whole point of the expander: growing this box is what made the row 62px.
      const row = await phoneRow();
      const box = row.close.getBoundingClientRect();
      expect(box.width, "the box must NOT take the touch floor").toBe(24);
      expect(box.height).toBe(24);
    });

    it("leaves the row sitting on the floor rather than outgrowing it", async () => {
      // What the expander prevents, asserted at the row rather than at the ×:
      // a 44px child inside 8px padding and a 1px border is a 62px row.
      const row = await phoneRow();
      expect(row.row.getBoundingClientRect().height, "the row is the floor, not more").toBe(
        resolvedFloor(),
      );
    });

    it("still lands the finger on the × ten pixels outside the paint", async () => {
      // The 44px expander reaches 10px past each edge of the 24px box; probed 5px out on all four sides
      // and asserted by IDENTITY (a hit on a descendant would be the glyph overflowing).
      const row = await phoneRow();
      const box = row.close.getBoundingClientRect();
      const cx = box.left + box.width / 2;
      const cy = box.top + box.height / 2;
      const out = box.width / 2 + 5;
      for (const [name, x, y] of [
        ["above", cx, cy - out],
        ["below", cx, cy + out],
        ["leading", cx - out, cy],
        ["trailing", cx + out, cy],
      ] as const) {
        expect(document.elementFromPoint(x, y), `the × owns the point 5px ${name} its paint`).toBe(
          row.close,
        );
      }
    });

    it("keeps the inline clearance at the row's own gap", async () => {
      // The two numbers the expander does not move: it is absolutely positioned, so
      // it takes no layout space and the × sits where it sat.
      const row = await phoneRow();
      const gap = parseFloat(getComputedStyle(row.row).columnGap);
      const i = insets(row);
      expect(gap, "the row's gap is --sp-2").toBe(8);
      expect({ trailing: i.trailing, toName: i.toName }).toEqual({ trailing: gap, toName: gap });
    });

    it("centres the × in a row the floor made taller than its content", async () => {
      // Under the floor the row is 44px with a 26px content box, so the 24px box centres with 1px slack:
      // asserted as symmetry plus a derived figure.
      const row = await phoneRow();
      const cs = getComputedStyle(row.row);
      const r = row.row.getBoundingClientRect();
      const box = row.close.getBoundingClientRect();
      const i = insets(row);
      expect(i.top, "symmetric about the row's centre").toBe(i.bottom);
      expect(box.top + box.height / 2, "the × 's centre IS the row's centre").toBeCloseTo(
        r.top + r.height / 2,
        5,
      );
      const contentH =
        r.height - 2 * parseFloat(cs.paddingBlockStart) - 2 * parseFloat(cs.borderTopWidth);
      const slack = (contentH - box.height) / 2;
      expect(slack, "the floor left 1px on each side").toBe(1);
      expect(i.top).toBe(parseFloat(cs.paddingBlockStart) + slack);
    });
  });
});
