// Every text control sits on the type scale. The reset's zero-specificity size keeps a control no
// class sized OFF the UA default (Chromium: Arial 13.3333px). No control outsizes the body rung, and
// the pointer tier moves no text: touch grows targets, never type. Sizes are read off `:root`, so a
// token retune moves the assertions with it.

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
// The viewport control. `vitest/browser` is the Vitest 5 spelling;
// `@vitest/browser/context` is a stub that throws.
import { page } from "vitest/browser";
import indexHtml from "../static/index.html?raw";

import { ICON_SEND } from "./icons.js";
import { iconEl } from "./icon-el.js";

import { allRules, loadCSS, manifestSheets, mountAppCSS } from "./__test-helpers__/css-rules.js";

const CONTROLS = "input, textarea, select";

/** The type scale's own token names, in rung order. */
const RUNGS = ["--fs-xs", "--fs-sm", "--fs-base", "--fs-md", "--fs-lg", "--fs-xl"] as const;

/** The rung a field may not exceed: the size the transcript reads. */
const BODY_RUNG = "--fs-md";

/** The rows measured for overflow, all present in the shipped markup. */
const TIGHT_ROWS = [".prompt-pills", ".bottom-bar", ".mcp-modal-tabs", ".rule-form", ".seg-bar"];

describe("the declarations, read from source", () => {
  it("declares no font size on a text control keyed on the pointer tier", () => {
    const tiered: string[] = [];
    for (const { name, css } of manifestSheets()) {
      for (const rule of allRules(css)) {
        if (
          /data-pointer/.test(rule.selector) &&
          /\b(input|textarea|select)\b/.test(rule.selector) &&
          /font-size:/.test(rule.body)
        ) {
          tiered.push(`${name} ${rule.selector}`);
        }
      }
    }
    expect(tiered).toEqual([]);
  });

  it("sizes an unstyled text control from the reset, at zero specificity", () => {
    // Zero specificity is the whole design: it beats the UA sheet and loses to
    // every component class, so it reaches exactly the controls nothing else has sized.
    const rule = allRules(loadCSS("02-reset.css")).find(
      (r) => r.selector === ":where(input, textarea, select)",
    );
    expect(rule, "02-reset.css has no :where(input, textarea, select) rule").toBeDefined();
    expect(rule?.body).toMatch(new RegExp(`font-size:\\s*var\\(${BODY_RUNG}\\)`));
  });

  it("keeps `button` out of that selector", () => {
    // Several icon buttons are sized against the UA font-size, so a size here moves
    // them — the same reason the family rule one line above it is family-only.
    const rule = allRules(loadCSS("02-reset.css")).find(
      (r) => r.selector === ":where(input, textarea, select)",
    );
    expect(rule?.selector).not.toContain("button");
  });

  it("leaves no hardcoded 16px on a text control", () => {
    // Literal mobile-breakpoint `16px`/`1rem` sizes, swept as a population so the next one fails too.
    const offenders: string[] = [];
    for (const name of ["24-find.css", "20-editor.css", "18-pages.css"]) {
      for (const rule of allRules(loadCSS(name))) {
        if (
          /-input|-find-input/.test(rule.selector) &&
          /font-size:\s*(16px|1rem)\b/.test(rule.body)
        ) {
          offenders.push(`${name} ${rule.selector}`);
        }
      }
    }
    expect(offenders).toEqual([]);
  });
});

describe("the controls, measured over the shipped markup", () => {
  // The block sits LAST in the file and restores the size it found in `afterAll`.
  // `page.viewport` has no getter, so the entry size is READ off the frame rather
  // than copied from `vitest.config.ts`.
  let entry: { readonly width: number; readonly height: number } | null = null;
  let styleEl: HTMLStyleElement | null = null;

  beforeAll(() => {
    entry = { width: window.innerWidth, height: window.innerHeight };
    styleEl = mountAppCSS();
  });

  afterEach(() => {
    document.body.innerHTML = "";
    document.documentElement.removeAttribute("data-pointer");
  });

  afterAll(async () => {
    styleEl?.remove();
    if (entry !== null) {
      await page.viewport(entry.width, entry.height);
    }
  });

  /** The app's own page at one viewport size and pointer tier: the whole body with hidden containers
   *  revealed and dialogs shown, since most controls live behind one. The resize is asserted. */
  async function mountPage(
    width: number,
    height: number,
    pointer: "coarse" | "fine" | null,
  ): Promise<void> {
    await page.viewport(width, height);
    expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
      width,
      height,
    ]);
    if (pointer === null) {
      document.documentElement.removeAttribute("data-pointer");
    } else {
      document.documentElement.setAttribute("data-pointer", pointer);
    }
    const body = indexHtml.slice(indexHtml.indexOf("<body"), indexHtml.indexOf("</body>"));
    document.body.innerHTML = body.slice(body.indexOf(">") + 1);
    for (const el of document.querySelectorAll<HTMLElement>("[hidden], .hidden")) {
      el.removeAttribute("hidden");
      el.classList.remove("hidden");
    }
    for (const dlg of document.querySelectorAll("dialog")) {
      dlg.show();
    }
    // The send button's glyph, which `prompt-input.ts` injects at boot: without it the button is 26px
    // wide and its `--hit-floor` expander overflows `.prompt-pills` by 5px. The production glyph, so
    // an upstream path change follows.
    const send = document.getElementById("send-btn");
    if (send !== null) {
      send.replaceChildren(iconEl(ICON_SEND));
    }
  }

  /** The type scale as this page resolves it, in px. Read rather than restated, so
   *  a token retune moves every assertion below with it. */
  function scalePx(): Map<number, string> {
    const root = getComputedStyle(document.documentElement);
    const out = new Map<number, string>();
    for (const rung of RUNGS) {
      const raw = root.getPropertyValue(rung).trim();
      const rem = Number.parseFloat(raw);
      expect(Number.isNaN(rem), `${rung} is not declared on :root`).toBe(false);
      out.set(rem * Number.parseFloat(root.fontSize), rung);
    }
    return out;
  }

  /** Every control whose size is on no rung of the scale, named so a failure says
   *  WHICH one drifted rather than how many. The UA default (Arial 13.3333px) is
   *  the shape this catches. */
  function offScale(): string[] {
    const scale = scalePx();
    const out: string[] = [];
    for (const el of document.querySelectorAll(CONTROLS)) {
      const px = Number.parseFloat(getComputedStyle(el).fontSize);
      if (!scale.has(px)) {
        const type = el instanceof HTMLInputElement ? `[${el.type}]` : "";
        out.push(`${el.tagName.toLowerCase()}${type}#${el.id || "?"} = ${px}px`);
      }
    }
    return out;
  }

  /** Every control that computes above the body rung. */
  function aboveBody(): string[] {
    const root = getComputedStyle(document.documentElement);
    const bodyPx =
      Number.parseFloat(root.getPropertyValue(BODY_RUNG)) * Number.parseFloat(root.fontSize);
    const out: string[] = [];
    for (const el of document.querySelectorAll(CONTROLS)) {
      const px = Number.parseFloat(getComputedStyle(el).fontSize);
      if (px > bodyPx) {
        const type = el instanceof HTMLInputElement ? `[${el.type}]` : "";
        out.push(`${el.tagName.toLowerCase()}${type}#${el.id || "?"} = ${px}px`);
      }
    }
    return out;
  }

  const TIERS: readonly [string, number, number, "coarse" | "fine" | null][] = [
    ["a phone with a coarse pointer", 390, 844, "coarse"],
    // An iPad in landscape: a finger past 48rem, where the width fallback does not
    // reach and only the pointer tier can carry a rule.
    ["a WIDE coarse viewport", 1024, 768, "coarse"],
    ["a phone with no pointer tier resolved yet", 390, 844, null],
    ["a desktop with a fine pointer", 1280, 800, "fine"],
    ["a desktop with no pointer tier resolved yet", 1280, 800, null],
  ];

  it.each(TIERS)("puts every control on a rung of the scale on %s", async (_l, w, h, pointer) => {
    await mountPage(w, h, pointer);
    // Enumerated, never listed: the count is asserted only as a floor, so a control
    // added to the page joins this case without an edit here — and a markup change
    // that emptied the page could not make it pass vacuously.
    expect(document.querySelectorAll(CONTROLS).length).toBeGreaterThan(40);
    expect(offScale()).toEqual([]);
  });

  it.each(TIERS)("outsizes the body rung with no control on %s", async (_l, w, h, pointer) => {
    await mountPage(w, h, pointer);
    expect(document.querySelectorAll(CONTROLS).length).toBeGreaterThan(40);
    expect(aboveBody()).toEqual([]);
  });

  /** Every element's computed font size and line height, in document order. */
  function typeOf(): string[] {
    return [...document.body.querySelectorAll<HTMLElement>("*")].map((el) => {
      const cs = getComputedStyle(el);
      const id = el.id === "" ? el.tagName.toLowerCase() : `#${el.id}`;
      return `${id} ${cs.fontSize}/${cs.lineHeight}`;
    });
  }

  it.each([
    ["an iPad", 1024, 768],
    ["a phone", 390, 844],
  ])("sizes every element's text the same on a finger as on a mouse, on %s", async (_l, w, h) => {
    // The whole shipped page, fields included: iOS's focus zoom is stopped by focus-zoom.ts's
    // viewport clause, so no text grows to 16px to avoid it.
    await mountPage(w, h, "fine");
    const fine = typeOf();
    expect(fine.length).toBeGreaterThan(500);
    await mountPage(w, h, "coarse");
    const coarse = typeOf();
    expect(coarse.filter((line, i) => line !== fine[i])).toEqual([]);
  });

  it("keeps the edit gutter's line numbers on their lines on a finger", async () => {
    await mountPage(1024, 768, "coarse");
    const field = document.getElementById("editor-content");
    const gutter = document.getElementById("editor-edit-gutter");
    expect(field, "#editor-content is not in the shipped markup").not.toBeNull();
    expect(gutter, "#editor-edit-gutter is not in the shipped markup").not.toBeNull();
    if (field !== null && gutter !== null) {
      expect(getComputedStyle(field).lineHeight).toBe(getComputedStyle(gutter).lineHeight);
    }
  });

  // Consumers are READ off the manifest, so a new reader joins and a dropped one fails the source case.
  const formTierSelectors = (token: "--fs-form-label" | "--fs-form-peer"): string[] => {
    const out: string[] = [];
    for (const { name, css } of manifestSheets()) {
      if (name === "01-tokens.css") {
        continue;
      }
      for (const rule of allRules(css)) {
        if (new RegExp(`font-size:\\s*var\\(${token}\\)`).test(rule.body)) {
          out.push(rule.selector);
        }
      }
    }
    return out;
  };

  it("declares each form rung once, on every tier", () => {
    const tokens = loadCSS("01-tokens.css");
    for (const token of ["--fs-form-label", "--fs-form-peer"] as const) {
      const arms = allRules(tokens).filter((r) => new RegExp(`${token}:`).test(r.body));
      expect(
        arms.map((r) => r.selector),
        `${token} tier arms`,
      ).toEqual([":root"]);
      expect(formTierSelectors(token).length, `${token} has consumers`).toBeGreaterThan(0);
    }

    // The PEER consumers are a closed set: two of the three rows are built by JS, so the measured case
    // cannot reach them and this source contract is the only guard.
    expect(formTierSelectors("--fs-form-peer").sort()).toEqual([
      ".filepicker-footer .btn-save",
      ".knowledge-add-form .btn-small",
      ".native-rule-effect",
      ".rule-form .rf-submit",
    ]);
  });

  /** Every rendered text-entry control that outsizes the largest string beside it by more than one
   *  rung. Derived from the PAGE, never from the selectors that read the rungs: a consumer that stops
   *  reading a rung would drop out of a selector sweep and take its coverage with it. */
  function overNeighbours(gap: number): string[] {
    const out: string[] = [];
    for (const el of document.querySelectorAll<HTMLElement>(CONTROLS)) {
      if (el instanceof HTMLInputElement && (el.type === "checkbox" || el.type === "radio")) {
        continue;
      }
      if (getComputedStyle(el).display === "none") {
        continue;
      }
      const px = Number.parseFloat(getComputedStyle(el).fontSize);
      // The control's own field wrapper when it has one (`<label class="rf-field">`
      // holds the label span and the select), else its nearest box.
      const box = el.closest("label, fieldset, li, tr, div") ?? el.parentElement;
      if (box === null) {
        continue;
      }
      let worst: { px: number; sel: string } | null = null;
      for (const n of box.querySelectorAll<HTMLElement>("*")) {
        if (n === el || n.contains(el) || el.contains(n)) {
          continue;
        }
        if (/^(INPUT|TEXTAREA|SELECT)$/.test(n.tagName)) {
          continue;
        }
        const bears = [...n.childNodes].some(
          (k) => k.nodeType === Node.TEXT_NODE && (k.textContent ?? "").trim().length > 1,
        );
        if (!bears) {
          continue;
        }
        const ns = getComputedStyle(n);
        if (ns.display === "none" || ns.visibility === "hidden") {
          continue;
        }
        const npx = Number.parseFloat(ns.fontSize);
        if (worst === null || npx > worst.px) {
          worst = {
            px: npx,
            sel: n.className === "" ? n.tagName.toLowerCase() : `.${n.className}`,
          };
        }
      }
      if (worst !== null && px - worst.px > gap) {
        out.push(`#${el.id || "?"} ${String(px)}px over ${worst.sel} ${String(worst.px)}px`);
      }
    }
    return out;
  }

  it.each([
    ["a phone with a coarse pointer", 390, 844, "coarse" as const],
    ["a WIDE coarse viewport", 1024, 768, "coarse" as const],
    ["a phone with no pointer tier resolved yet", 390, 844, null],
    ["a desktop with a fine pointer", 1280, 800, "fine" as const],
  ])("outsizes the strings beside it by at most one rung on %s", async (_l, w, h, pointer) => {
    await mountPage(w, h, pointer);
    const root = getComputedStyle(document.documentElement);
    const rem = Number.parseFloat(root.fontSize);
    const rung = (name: string): number => Number.parseFloat(root.getPropertyValue(name)) * rem;

    // A form's label and its row-sharing peer read the field's own rung on every tier.
    expect(rung("--fs-form-label")).toBe(rung("--fs-sm"));
    expect(rung("--fs-form-peer")).toBe(rung("--fs-sm"));

    expect(document.querySelectorAll(CONTROLS).length).toBeGreaterThan(40);
    // One rung of the body scale: the composer reads `--fs-md` over its `--fs-sm` pills.
    expect(overNeighbours(rung("--fs-md") - rung("--fs-sm"))).toEqual([]);
  });

  it.each([
    ["a phone with a coarse pointer", 390, 844, "coarse" as const],
    ["a WIDE coarse viewport", 1024, 768, "coarse" as const],
    ["a desktop with a fine pointer", 1280, 800, "fine" as const],
  ])("matches a row-sharing button to the field it sits beside, on %s", async (_l, w, h, p) => {
    // The PEER half, which the one-rung tolerance cannot see, asserted as equality on NAMED containers.
    // Only those the shipped markup renders; `.knowledge-add-form` is guarded by the closed set above.
    await mountPage(w, h, p);
    const mismatched: string[] = [];
    for (const sel of [".editor-goto", ".filepicker-footer", ".rule-form"]) {
      const rows = [...document.querySelectorAll<HTMLElement>(sel)];
      expect(rows.length, `${sel} is not in the shipped markup`).toBeGreaterThan(0);
      for (const row of rows) {
        const size = (el: Element): number => Number.parseFloat(getComputedStyle(el).fontSize);
        const fields = [...row.querySelectorAll(CONTROLS)].filter(
          (el) =>
            !(el instanceof HTMLInputElement && (el.type === "checkbox" || el.type === "radio")),
        );
        const buttons = [...row.querySelectorAll("button")];
        if (fields.length === 0 || buttons.length === 0) {
          continue;
        }
        const field = Math.max(...fields.map(size));
        for (const b of buttons) {
          // An ICON-only button carries no label to size, so it is not a peer in
          // this sense; the ones this rule is about all render text.
          if ((b.textContent ?? "").trim() === "") {
            continue;
          }
          if (size(b) !== field) {
            mismatched.push(`${sel} button ${String(size(b))}px vs field ${String(field)}px`);
          }
        }
      }
    }
    expect(mismatched).toEqual([]);
  });

  it("overflows none of the tight rows at 390px coarse", async () => {
    // The gate for any type-scale change: no row a field sits in may clip.
    await mountPage(390, 844, "coarse");
    const overflowing: string[] = [];
    for (const sel of TIGHT_ROWS) {
      const found = [...document.querySelectorAll<HTMLElement>(sel)];
      expect(found.length, `${sel} is not in the shipped markup`).toBeGreaterThan(0);
      for (const el of found) {
        const over = el.scrollWidth - el.clientWidth;
        if (over > 0) {
          overflowing.push(`${sel}${el.id === "" ? "" : `#${el.id}`} over by ${over}px`);
        }
      }
    }
    expect(overflowing).toEqual([]);
  });

  it("moves no box control, because the box controls carry a rem size of their own", async () => {
    // The reset excludes no input type because checkbox and radio carry an explicit `rem` box: pushing
    // the RUNG (not the root, which moves `rem` boxes) to 3rem moves neither.
    await mountPage(1280, 800, "fine");
    const boxes = [...document.querySelectorAll<HTMLElement>('[type="checkbox"], [type="radio"]')];
    expect(boxes.length).toBeGreaterThan(10);
    const geom = (): string[] =>
      boxes.map((b) => {
        const r = b.getBoundingClientRect();
        return `${r.width.toFixed(2)}x${r.height.toFixed(2)}`;
      });
    const before = geom();

    document.documentElement.style.setProperty(BODY_RUNG, "3rem");
    try {
      const first = boxes[0];
      expect(first, "the page renders at least one box control").toBeDefined();
      if (first !== undefined) {
        expect(
          Number.parseFloat(getComputedStyle(first).fontSize),
          "the rung override actually reached the control",
        ).toBe(48);
      }
      expect(geom()).toEqual(before);
    } finally {
      document.documentElement.style.removeProperty(BODY_RUNG);
    }
  });
});
