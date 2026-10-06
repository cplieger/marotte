// The UI audit walkers: one implementation, run against fixtures by vitest and injected into a
// live page over CDP through `pageSource`, so a runner prints the number vitest pinned. Constraints:
//  1. A walker is SELF-CONTAINED: serialised by `Function.prototype.toString()`, it may use only
//     its own body, page globals and the HELPERS `pageSource` emits.
//  2. ONLY ERASABLE TYPE SYNTAX: node type-stripping blanks annotations, so an enum, decorator or
//     parameter property reaches the page as a syntax error.
//  3. EVERY RESULT IS JSON (`Runtime.evaluate` returns by value); findings are deduped with a count.
//  4. NOTHING TOUCHES THE DOM AT MODULE LOAD: node imports this with no document.

/** A page-side audit. Takes its options by value and returns JSON. */
type Walker = (opts: never) => unknown;

// Helpers emitted beside the walker. The KEY is the emitted `const`'s name: the binding is by
// string, so renaming a key means renaming its call sites.

/** `tag#id.class.class`, bounded, so a finding names its element in one line. */
function describeEl(el: Element): string {
  const tag = el.tagName.toLowerCase();
  const id = el.id ? `#${el.id}` : "";
  const cls = [...el.classList]
    .slice(0, 4)
    .map((c) => `.${c}`)
    .join("");
  const role = el.getAttribute("role");
  const extra = role !== null && id === "" ? `[role=${role}]` : "";
  return `${tag}${id}${cls}${extra}`.slice(0, 120);
}

/** A computed length in px; absent, `auto` and `none` read 0. */
function lenPx(v: string): number {
  const n = Number.parseFloat(v);
  return Number.isFinite(n) ? n : 0;
}

/** The page's own route, for the report header. */
function routeOf(): string {
  return `${location.pathname}${location.search}${location.hash}`;
}

/** A token's value in px, read through a real box so the cascade decides it. 0 means it does not
 *  resolve (an invalid `var()` drops the declaration). */
function tokenPx(name: string): number {
  const probe = document.createElement("div");
  probe.style.cssText = `position:fixed;left:-9999px;top:0;inline-size:var(${name});block-size:var(${name})`;
  document.body.appendChild(probe);
  const px = probe.getBoundingClientRect().height;
  probe.remove();
  return px;
}

/** Hidden by a stylesheet (no boxes at all), not merely off-screen. */
function hasNoBox(el: Element): boolean {
  return el.getClientRects().length === 0;
}

/** The nearest ancestor whose transform is not a pure TRANSLATION, or null. Under a rotation a
 *  rect is the tilted box's AABB, so geometry judgements refuse to measure (as `icon-crisp.ts`
 *  does). The individual `scale`/`rotate`/`translate` properties are read separately: Chromium
 *  does NOT fold them into the computed `transform`. */
function distortedUnder(el: Element): Element | null {
  const flatMatrix = (v: string): boolean => {
    if (v === "" || v === "none") {
      return true;
    }
    const m = new DOMMatrixReadOnly(v);
    const off = [m.m11 - 1, m.m22 - 1, m.m33 - 1, m.m12, m.m13, m.m21, m.m23, m.m31, m.m32];
    return off.every((n) => Math.abs(n) < 0.001);
  };
  const allOnes = (v: string): boolean => {
    if (v === "" || v === "none" || v === "normal") {
      return true;
    }
    return v
      .trim()
      .split(/[\s,]+/u)
      .every((part) => {
        const n = Number.parseFloat(part);
        return !Number.isFinite(n) || Math.abs(n - 1) < 0.001;
      });
  };
  const noAngle = (v: string): boolean => {
    if (v === "" || v === "none") {
      return true;
    }
    // `<axis>? <angle>`, so the angle is the last token.
    const toks = v.trim().split(/[\s,]+/u);
    const deg = Number.parseFloat(toks[toks.length - 1] ?? "");
    return !Number.isFinite(deg) || Math.abs(deg) < 0.001;
  };

  let cur: Element | null = el.parentElement;
  while (cur !== null && cur !== document.documentElement) {
    const cs = getComputedStyle(cur);
    if (!flatMatrix(cs.transform)) {
      return cur;
    }
    if (!allOnes(cs.getPropertyValue("scale"))) {
      return cur;
    }
    if (!allOnes(cs.getPropertyValue("zoom"))) {
      return cur;
    }
    if (!noAngle(cs.getPropertyValue("rotate"))) {
      return cur;
    }
    cur = cur.parentElement;
  }
  return null;
}

/** Group by a key, keeping the FIRST payload and counting the rest. */
function bucket<T>(rows: readonly (readonly [string, T])[]): (T & { count: number })[] {
  const seen = new Map<string, T & { count: number }>();
  for (const [key, payload] of rows) {
    const hit = seen.get(key);
    if (hit === undefined) {
      seen.set(key, { ...payload, count: 1 });
    } else {
      hit.count += 1;
    }
  }
  return [...seen.values()];
}

/** Whether the point resolves to this element or inside it: a `::before` expander answers as its
 *  own element, a glyph as the button's child. */
function ownsPoint(el: Element, x: number, y: number): boolean {
  const hit = document.elementFromPoint(x, y);
  return hit !== null && (hit === el || el.contains(hit));
}

/** How far the element's TARGET reaches on one axis by hit test from its centre, capped at `cap`.
 *  Not a box read: a visually small control can grow its target with an absolutely positioned
 *  expander (61-mcp-tools.css). */
function targetExtent(el: Element, axis: "x" | "y", cap: number): number | null {
  const r = el.getBoundingClientRect();
  const cx = r.left + r.width / 2;
  const cy = r.top + r.height / 2;

  // NULL, never the painted box, when the element does not answer at its own centre: a control in a
  // CLOSED `<details>` keeps a rect (Chromium's `content-visibility` on `::details-content`) while
  // the `<summary>` is hit, and the box fallback would report it undersize.
  if (!ownsPoint(el, cx, cy)) {
    return null;
  }
  let lo = 0;
  let hi = 0;
  const limit = cap + 4;
  while (
    lo < limit &&
    ownsPoint(el, axis === "x" ? cx - lo - 1 : cx, axis === "y" ? cy - lo - 1 : cy)
  ) {
    lo += 1;
  }
  while (
    hi < limit &&
    ownsPoint(el, axis === "x" ? cx + hi + 1 : cx, axis === "y" ? cy + hi + 1 : cy)
  ) {
    hi += 1;
  }
  return lo + hi + 1;
}

/** Whether this control is a target IN A SENTENCE (WCAG 2.5.8's inline exception): inline-level
 *  in a container with its own text, so the line height constrains it. Keyed on display plus
 *  text, not tag: `linkify.ts` emits a `<button>` chip (14-tools.css `.inline-file-link`). This
 *  gates the FLOOR only; a labelled input still answers to the row rule. */
function inlineInText(el: Element, cs: CSSStyleDeclaration): boolean {
  if (!cs.display.startsWith("inline")) {
    return false;
  }
  const parent = el.parentElement;
  if (parent === null) {
    return false;
  }
  for (const node of [...parent.childNodes]) {
    // Node.TEXT_NODE as a number, so the serialized walker needs no `Node` binding.
    if (node.nodeType === 3 && (node.textContent ?? "").trim() !== "") {
      return true;
    }
  }
  return false;
}

/** Whether this control grows its TARGET with an absolutely positioned `::before`/`::after`
 *  expander. Read from computed style, so it answers off-screen; a decorative pseudo-element also
 *  matches, which is why it gates a REPORTING BUCKET rather than silence. */
function growsTarget(el: Element): boolean {
  for (const pseudo of ["::after", "::before"]) {
    const cs = getComputedStyle(el, pseudo);
    // `"none"` is the only absent value: in Chromium 152 `content: ""` computes to the string `""`.
    if (cs.content !== "none" && cs.position === "absolute") {
      return true;
    }
  }
  return false;
}

/** Whether the element's centre is in the viewport; off-screen targets are reported unmeasurable. */
function centreOnScreen(el: Element): boolean {
  const r = el.getBoundingClientRect();
  const cx = r.left + r.width / 2;
  const cy = r.top + r.height / 2;
  return cx >= 0 && cy >= 0 && cx <= window.innerWidth && cy <= window.innerHeight;
}

// --- reveal ----------------------------------------------------------------

/** Force stylesheet-hidden panels into layout (inline `display: revert`) so one pass covers them.
 *  Blunt: a reverted flex child gets its UA default, so re-audit the mounted view before trusting
 *  a NUMBER. Repeats to a fixed point (usually two passes). Revert cannot unhide a UA tag-hidden
 *  element (`<datalist>`, closed `<dialog>`); the revealed marker stops recounting, and a closed
 *  `<dialog>` is skipped so `revealed` counts only elements put into layout. */
function reveal(): { revealed: number; passes: number } {
  const SKIP = new Set(["SCRIPT", "STYLE", "TEMPLATE", "LINK", "META", "HEAD", "TITLE"]);
  let revealed = 0;
  let passes = 0;
  for (let pass = 0; pass < 3; pass++) {
    passes = pass + 1;
    let touched = 0;
    for (const el of [...document.body.querySelectorAll<HTMLElement>("*")]) {
      if (SKIP.has(el.tagName)) {
        continue;
      }
      if (el.closest("dialog:not([open])") !== null) {
        continue;
      }
      if (el.dataset["uiqaRevealed"] === "1") {
        continue;
      }
      const cs = getComputedStyle(el);
      const hiddenDisplay = cs.display === "none";
      const hiddenVis = cs.visibility === "hidden" || cs.contentVisibility === "hidden";
      if (!hiddenDisplay && !hiddenVis) {
        continue;
      }
      if (hiddenDisplay) {
        el.style.setProperty("display", "revert", "important");
      }
      if (hiddenVis) {
        el.style.setProperty("visibility", "visible", "important");
        el.style.setProperty("content-visibility", "visible", "important");
      }
      el.dataset["uiqaRevealed"] = "1";
      touched += 1;
    }
    revealed += touched;
    if (touched === 0) {
      break;
    }
  }
  return { revealed, passes };
}

// --- control height --------------------------------------------------------

/** Every form control whose height disagrees with the controls beside it, and every TARGET under
 *  the tier's hit floor. A ROW is a flex or grid parent plus a vertical band, clustered by rect
 *  overlap. A target in a sentence (`inlineInText`) is reported as `inlineExempt`. Box headers
 *  are out of scope (`height-sweep.mjs`). */
function controlHeight(opts: { tol: number; floor?: number }): unknown {
  const SEL = [
    "button",
    'input:not([type="hidden"])',
    "select",
    "textarea",
    "summary",
    '[role="button"]',
    '[role="switch"]',
    '[role="tab"]',
    '[role="separator"][tabindex]',
    "a[href]",
  ].join(", ");

  const tol = opts.tol;
  let floor = opts.floor ?? 0;
  let floorSource = "--floor";
  if (floor === 0) {
    floor = tokenPx("--hit-floor");
    floorSource = "--hit-floor";
  }
  if (floor === 0) {
    floor = tokenPx("--touch-target");
    floorSource = "--touch-target";
  }
  if (floor === 0) {
    floor = 24;
    floorSource = "default";
  }

  interface Member {
    el: string;
    h: number;
    kind: string;
    declaredH: string;
    minH: string;
    pad: string;
    fs: string;
    node: Element;
  }

  const viewportHidden: (readonly [string, { h: number; el: string }])[] = [];
  const unmeasurable: (readonly [string, { why: string; el: string }])[] = [];
  const undersizeRows: (readonly [string, Omit<Member, "node">])[] = [];
  const expandedRows: (readonly [string, Omit<Member, "node">])[] = [];
  const inlineRows: (readonly [string, { el: string }])[] = [];
  const measured: Member[] = [];

  for (const el of [...document.querySelectorAll(SEL)]) {
    const cs = getComputedStyle(el);

    // The inline exception governs the FLOOR, not the row: a labelled field still has a visible
    // height to match, and folding the gates made that case unreportable.
    const floorExempt = inlineInText(el, cs);
    if (floorExempt) {
      inlineRows.push([describeEl(el), { el: describeEl(el) }]);
    }
    if (hasNoBox(el)) {
      viewportHidden.push([describeEl(el), { h: 0, el: describeEl(el) }]);
      continue;
    }
    const distorted = distortedUnder(el);
    if (distorted !== null) {
      unmeasurable.push([
        `${describeEl(el)}|${describeEl(distorted)}`,
        { why: `a transformed ancestor, ${describeEl(distorted)}`, el: describeEl(el) },
      ]);
      continue;
    }
    const r = el.getBoundingClientRect();
    const role = el.getAttribute("role");
    const m: Member = {
      el: describeEl(el),
      h: Math.round(r.height * 100) / 100,
      kind:
        el.tagName === "A" || el.tagName === "DIV" || el.tagName === "SPAN"
          ? `role=${role ?? el.tagName.toLowerCase()}`
          : el.tagName.toLowerCase(),
      declaredH: cs.height,
      minH: cs.minHeight,
      pad: `${lenPx(cs.paddingTop)}+${lenPx(cs.paddingBottom)}`,
      fs: cs.fontSize,
      node: el,
    };
    // A control painted under the floor that grows its target with an expander has declared itself
    // visually small, so it is not in the one-height-per-row population; it lands in `expanded`.
    const shortSide = Math.min(r.height, r.width);
    if (shortSide + tol < floor && growsTarget(el)) {
      const { node: _n, ...row } = m;
      expandedRows.push([m.el, row]);
    } else {
      measured.push(m);
    }

    if (floorExempt) {
      continue;
    }

    // The UNDERSIZE half measures the TARGET (on screen only) for BOTH populations: an exempted
    // control must still prove its expander reaches.
    if (shortSide + tol >= floor) {
      continue;
    }
    if (!centreOnScreen(el)) {
      unmeasurable.push([
        `${describeEl(el)}|offscreen`,
        {
          why: "its centre is outside the viewport, so no hit test can answer",
          el: describeEl(el),
        },
      ]);
      continue;
    }
    const axis = r.height <= r.width ? "y" : "x";
    const reach = targetExtent(el, axis, floor);
    if (reach === null) {
      unmeasurable.push([
        `${describeEl(el)}|occluded`,
        {
          why: "something else answers at its centre — occluded, or inside a content-visibility:hidden subtree such as a closed <details>",
          el: describeEl(el),
        },
      ]);
      continue;
    }
    if (reach + tol >= floor) {
      continue;
    }
    const { node: _node, ...row } = m;
    undersizeRows.push([
      `${m.el}|${axis}`,
      { ...row, h: Math.round(reach * 100) / 100, kind: axis === "x" ? `${m.kind}:w` : m.kind },
    ]);
  }

  // ROWS: a flex or grid ancestor, then a band inside it.
  const byBox = new Map<Element, Member[]>();
  for (const m of measured) {
    let host: Element | null = m.node.parentElement;
    while (host !== null && host !== document.documentElement) {
      const d = getComputedStyle(host).display;
      if (d === "flex" || d === "inline-flex" || d === "grid" || d === "inline-grid") {
        break;
      }
      host = host.parentElement;
    }
    const key = host ?? m.node.parentElement;
    if (key === null) {
      continue;
    }
    const list = byBox.get(key);
    if (list === undefined) {
      byBox.set(key, [m]);
    } else {
      list.push(m);
    }
  }

  const mismatchRows: (readonly [
    string,
    { spread: number; box: string; members: Omit<Member, "node">[] },
  ])[] = [];
  const passRows: (readonly [string, { members: { el: string; h: number }[] }])[] = [];
  let rows = 0;

  for (const [host, members] of byBox) {
    const sorted = [...members].sort(
      (a, b) => a.node.getBoundingClientRect().top - b.node.getBoundingClientRect().top,
    );
    let band: Member[] = [];
    let bandBottom = -Infinity;
    const flush = (): void => {
      if (band.length === 0) {
        return;
      }
      rows += 1;
      const heights = band.map((m) => m.h);
      const spread = Math.round((Math.max(...heights) - Math.min(...heights)) * 100) / 100;
      const sig = band.map((m) => `${m.el}@${m.h}`).join("|");
      if (band.length > 1 && spread > tol) {
        mismatchRows.push([
          `${describeEl(host)}|${sig}`,
          {
            spread,
            box: describeEl(host),
            members: band.map(({ node: _node, ...rest }) => rest),
          },
        ]);
      } else {
        passRows.push([
          `${describeEl(host)}|${sig}`,
          { members: band.map((m) => ({ el: m.el, h: m.h })) },
        ]);
      }
      band = [];
      bandBottom = -Infinity;
    };
    for (const m of sorted) {
      const r = m.node.getBoundingClientRect();
      if (band.length > 0 && r.top >= bandBottom) {
        flush();
      }
      band.push(m);
      bandBottom = band.length === 1 ? r.bottom : Math.max(bandBottom, r.bottom);
    }
    flush();
  }

  return {
    route: routeOf(),
    viewport: { w: window.innerWidth, h: window.innerHeight },
    floor: Math.round(floor * 100) / 100,
    floorSource,
    pointer: document.documentElement.dataset["pointer"] ?? null,
    pointerCoarse: window.matchMedia("(pointer: coarse)").matches,
    scanned: measured.length,
    rows,
    mismatches: bucket(mismatchRows),
    undersize: bucket(undersizeRows),
    viewportHidden: bucket(viewportHidden),
    unmeasurable: bucket(unmeasurable),
    expanded: bucket(expandedRows),
    inlineExempt: bucket(inlineRows),
    passes: bucket(passRows),
  };
}

// --- radius ----------------------------------------------------------------

/** Every nested rounded corner that does not nest. Concentric arcs need
 *  `inner = outer - outer-border - inset` with EQUAL insets on the corner's two edges, so an
 *  anisotropic inset is its own verdict with no ideal. Exempt (reported): a stadium or circle
 *  (radius >= half the short side) and a child flush inside a clipping parent. A child that paints
 *  nothing at rest is judged and flagged hover-only. */
function radius(opts: { tol: number }): unknown {
  const tol = opts.tol;
  const CORNERS = [
    ["tl", "borderTopLeftRadius", "top", "left"],
    ["tr", "borderTopRightRadius", "top", "right"],
    ["br", "borderBottomRightRadius", "bottom", "right"],
    ["bl", "borderBottomLeftRadius", "bottom", "left"],
  ] as const;

  /** A corner's radius in px; a percentage resolves against the box, so `50%` reads as a stadium. */
  const cornerPx = (cs: CSSStyleDeclaration, prop: string, w: number, h: number): number => {
    const raw = cs.getPropertyValue(prop.replace(/[A-Z]/gu, (c) => `-${c.toLowerCase()}`));
    const first = raw.trim().split(/\s+/u)[0] ?? "0px";
    if (first.endsWith("%")) {
      return (Number.parseFloat(first) / 100) * Math.min(w, h);
    }
    return lenPx(first);
  };

  const rounded: Element[] = [];
  for (const el of [...document.body.querySelectorAll("*")]) {
    if (hasNoBox(el)) {
      continue;
    }
    const cs = getComputedStyle(el);
    const r = el.getBoundingClientRect();
    const any = CORNERS.some(([, prop]) => cornerPx(cs, prop, r.width, r.height) > 0);
    if (any) {
      rounded.push(el);
    }
  }

  const findings: (readonly [
    string,
    {
      verdict: string;
      childRadius: number;
      ideal: number | null;
      insetH: number;
      insetV: number;
      parentRadius: number;
      parentBorder: number;
      corners: string;
      childPaints: boolean;
      child: string;
      parent: string;
      note: string;
    },
  ])[] = [];
  const exempt: (readonly [string, { why: string; child: string; parent: string }])[] = [];
  const unmeasurable: (readonly [string, { why: string; child: string }])[] = [];
  let pairs = 0;

  for (const child of rounded) {
    let host: Element | null = child.parentElement;
    let hostCS: CSSStyleDeclaration | null = null;
    while (host !== null && host !== document.documentElement) {
      const cs = getComputedStyle(host);
      const hr = host.getBoundingClientRect();
      if (CORNERS.some(([, prop]) => cornerPx(cs, prop, hr.width, hr.height) > 0)) {
        hostCS = cs;
        break;
      }
      host = host.parentElement;
    }
    if (host === null || hostCS === null) {
      continue;
    }

    const distorted = distortedUnder(child);
    if (distorted !== null) {
      unmeasurable.push([
        `${describeEl(child)}|${describeEl(distorted)}`,
        { why: `a transformed ancestor, ${describeEl(distorted)}`, child: describeEl(child) },
      ]);
      continue;
    }

    const cs = getComputedStyle(child);
    const cr = child.getBoundingClientRect();
    const hr = host.getBoundingClientRect();
    const paints =
      (cs.backgroundColor !== "rgba(0, 0, 0, 0)" && cs.backgroundColor !== "transparent") ||
      cs.backgroundImage !== "none" ||
      (lenPx(cs.borderTopWidth) > 0 && cs.borderTopStyle !== "none");

    // Measured per edge from the parent's BORDER box inward, as the concentric rule subtracts.
    const borderWidths = {
      top: hostCS.borderTopWidth,
      bottom: hostCS.borderBottomWidth,
      left: hostCS.borderLeftWidth,
      right: hostCS.borderRightWidth,
    };
    const insets = {
      top: cr.top - (hr.top + lenPx(borderWidths.top)),
      bottom: hr.bottom - lenPx(borderWidths.bottom) - cr.bottom,
      left: cr.left - (hr.left + lenPx(borderWidths.left)),
      right: hr.right - lenPx(borderWidths.right) - cr.right,
    };
    const clips = hostCS.overflow === "hidden" || hostCS.overflow === "clip";
    const shortSide = Math.min(cr.width, cr.height);

    const bad: string[] = [];
    let worst: {
      verdict: string;
      childRadius: number;
      ideal: number | null;
      insetH: number;
      insetV: number;
      parentRadius: number;
      parentBorder: number;
      note: string;
    } | null = null;
    let exemptWhy: string | null = null;

    for (const [name, prop, vEdge, hEdge] of CORNERS) {
      const childR = cornerPx(cs, prop, cr.width, cr.height);
      const parentR = cornerPx(hostCS, prop, hr.width, hr.height);
      if (parentR <= 0) {
        continue;
      }
      if (childR >= shortSide / 2 - tol && childR > 0) {
        exemptWhy = "stadium or circle (a shape, not a nesting)";
        continue;
      }
      const insetV = Math.round(insets[vEdge] * 100) / 100;
      const insetH = Math.round(insets[hEdge] * 100) / 100;
      if (Math.abs(insetV) <= tol && Math.abs(insetH) <= tol && clips) {
        exemptWhy = "flush inside a clipping parent (takes its corner)";
        continue;
      }

      // Per edge: the inset PLUS that edge's own parent border (a one-sided accent rail differs).
      const borderV = lenPx(borderWidths[vEdge]);
      const borderH = lenPx(borderWidths[hEdge]);
      const gapV = Math.round((insetV + borderV) * 100) / 100;
      const gapH = Math.round((insetH + borderH) * 100) / 100;

      // A nesting only where the child's corner lies within the parent's arc region: no further in than
      // `parentR` and not outside the border box (a negative inset). Without this gate full-width bands
      // and small children all read "anisotropic". Skipped corners are not counted in `pairs`.
      const nests = (gap: number): boolean => gap >= -tol && gap <= parentR + tol;
      if (!nests(gapV) || !nests(gapH)) {
        continue;
      }
      pairs += 1;

      if (Math.abs(gapV - gapH) > tol) {
        bad.push(name);
        worst ??= {
          verdict: "anisotropic",
          childRadius: Math.round(childR * 100) / 100,
          ideal: null,
          insetH,
          insetV,
          parentRadius: Math.round(parentR * 100) / 100,
          parentBorder: borderV,
          note: `unsatisfiable at any radius: the two edges of this corner are ${gapV} and ${gapH} in — fix the inset or the border, not the radius`,
        };
        continue;
      }
      const ideal = Math.max(0, Math.round((parentR - gapV) * 100) / 100);
      if (Math.abs(childR - ideal) <= tol) {
        continue;
      }
      bad.push(name);
      worst ??= {
        verdict: childR > ideal ? "too-round" : "too-square",
        childRadius: Math.round(childR * 100) / 100,
        ideal,
        insetH,
        insetV,
        parentRadius: Math.round(parentR * 100) / 100,
        parentBorder: borderV,
        note: "",
      };
    }

    if (bad.length > 0 && worst !== null) {
      findings.push([
        `${describeEl(child)}|${describeEl(host)}|${worst.verdict}|${bad.join(",")}`,
        {
          ...worst,
          corners: bad.join(","),
          childPaints: paints,
          child: describeEl(child),
          parent: describeEl(host),
        },
      ]);
    } else if (exemptWhy !== null) {
      exempt.push([
        `${describeEl(child)}|${describeEl(host)}|${exemptWhy}`,
        { why: exemptWhy, child: describeEl(child), parent: describeEl(host) },
      ]);
    }
  }

  return {
    route: routeOf(),
    rounded: rounded.length,
    pairs,
    findings: bucket(findings),
    unmeasurable: bucket(unmeasurable),
    exempt: bucket(exempt),
  };
}

// --- colour, for contrast ---------------------------------------------------

/** A CSS colour as sRGB bytes plus alpha, parsed by PAINTING it: `getComputedStyle` returns the
 *  authored form (Chromium 152: `oklch()` stays `oklch(...)`, `color-mix()` becomes `oklab(...)`),
 *  while a 1x1 `getImageData` read gives exact 8-bit sRGB. The input must be COMPUTED or
 *  `CSS.supports`-validated: an invalid `fillStyle` assignment is silently ignored. */
function parseColor(css: string): { r: number; g: number; b: number; a: number } {
  const memo = parseColor as unknown as { cx?: CanvasRenderingContext2D };
  if (memo.cx === undefined) {
    const canvas = document.createElement("canvas");
    canvas.width = 1;
    canvas.height = 1;
    const cx = canvas.getContext("2d", { willReadFrequently: true });
    if (cx === null) {
      return { r: 0, g: 0, b: 0, a: 1 };
    }
    memo.cx = cx;
  }
  const cx = memo.cx;
  cx.clearRect(0, 0, 1, 1);
  cx.fillStyle = css;
  cx.fillRect(0, 0, 1, 1);
  const d = cx.getImageData(0, 0, 1, 1).data;
  return { r: d[0] ?? 0, g: d[1] ?? 0, b: d[2] ?? 0, a: (d[3] ?? 0) / 255 };
}

/** `#rrggbb`, for a report a reader compares by eye against a token table. */
function hexOf(c: { r: number; g: number; b: number }): string {
  const p = (n: number): string =>
    Math.max(0, Math.min(255, Math.round(n)))
      .toString(16)
      .padStart(2, "0");
  return `#${p(c.r)}${p(c.g)}${p(c.b)}`;
}

/** Source-over: `fg` at its own alpha onto an opaque `bg`. */
function over(
  fg: { r: number; g: number; b: number; a: number },
  bg: { r: number; g: number; b: number; a: number },
): { r: number; g: number; b: number; a: number } {
  const a = fg.a;
  return {
    r: fg.r * a + bg.r * (1 - a),
    g: fg.g * a + bg.g * (1 - a),
    b: fg.b * a + bg.b * (1 - a),
    a: 1,
  };
}

/** WCAG 2.x relative luminance, and the ratio over it. */
function contrastRatio(
  x: { r: number; g: number; b: number },
  y: { r: number; g: number; b: number },
): number {
  const lum = (c: { r: number; g: number; b: number }): number => {
    const ch = (v: number): number => {
      const s = v / 255;
      return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
    };
    return 0.2126 * ch(c.r) + 0.7152 * ch(c.g) + 0.0722 * ch(c.b);
  };
  const a = lum(x);
  const b = lum(y);
  const hi = Math.max(a, b);
  const lo = Math.min(a, b);
  return (hi + 0.05) / (lo + 0.05);
}

/** Every `opacity` from this element to the root, multiplied; applied to the text's alpha and to
 *  each background layer, so dimmed text is not measured against an undimmed surface. */
function foldedOpacity(el: Element): number {
  let out = 1;
  let cur: Element | null = el;
  while (cur !== null) {
    const o = Number.parseFloat(getComputedStyle(cur).opacity);
    if (Number.isFinite(o)) {
      out *= o;
    }
    cur = cur.parentElement;
  }
  return out;
}

/** Which ancestors (this element included) are dimming it, outermost last. */
function dimmedBy(el: Element): string[] {
  const out: string[] = [];
  let cur: Element | null = el;
  while (cur !== null) {
    const o = Number.parseFloat(getComputedStyle(cur).opacity);
    if (Number.isFinite(o) && o < 1) {
      out.push(`${describeEl(cur)}@${Math.round(o * 100) / 100}`);
    }
    cur = cur.parentElement;
  }
  return out;
}

/** The surface this element's text paints on: every background layer outward, each dimmed by its
 *  opacity chain, composited onto the UA's white canvas. A `background-image` REFUSES (unresolved). */
function surfaceUnder(el: Element): {
  color: { r: number; g: number; b: number; a: number } | null;
  from: string;
  imageFrom: string | null;
} {
  const layers: { c: { r: number; g: number; b: number; a: number }; who: string }[] = [];
  let cur: Element | null = el;
  while (cur !== null) {
    const cs = getComputedStyle(cur);
    if (cs.backgroundImage !== "none") {
      return { color: null, from: describeEl(cur), imageFrom: describeEl(cur) };
    }
    const c = parseColor(cs.backgroundColor);
    if (c.a > 0) {
      const dim = foldedOpacity(cur);
      layers.push({ c: { ...c, a: c.a * dim }, who: describeEl(cur) });
      if (c.a >= 1 && dim >= 1) {
        break;
      }
    }
    cur = cur.parentElement;
  }
  // The UA paints white under everything, so an all-transparent chain is white.
  let out = { r: 255, g: 255, b: 255, a: 1 };
  for (let i = layers.length - 1; i >= 0; i -= 1) {
    const layer = layers[i];
    if (layer !== undefined) {
      out = over(layer.c, out);
    }
  }
  return {
    color: out,
    from: layers.length === 0 ? "the UA canvas" : layers.map((l) => l.who).join(" < "),
    imageFrom: null,
  };
}

/** A custom property's value at this element when it is a colour, validated by `CSS.supports`
 *  because `parseColor` cannot report failure. */
function colorTokenAt(el: Element, name: string): string | null {
  const raw = getComputedStyle(el).getPropertyValue(name).trim();
  if (raw === "") {
    return null;
  }
  return CSS.supports("color", raw) ? raw : null;
}

// --- contrast ---------------------------------------------------------------

/** Every text-painting element against the surface it ACTUALLY paints on, every ancestor opacity
 *  folded in, floored at WCAG 1.4.3. Beside `scripts/css-contrast.py`'s token-graph check, it sees
 *  composited semi-transparent layers, ancestor `opacity` and the real font size and weight. Text
 *  nodes only (an svg glyph is 1.4.11's check). Reported, not ruled: text over a background-image
 *  (`unresolved`); `mix-blend-mode` and `filter` are not modelled. */
function contrast(opts: { inkToken?: string | null }): unknown {
  const SKIP = new Set([
    "SCRIPT",
    "STYLE",
    "TEMPLATE",
    "LINK",
    "META",
    "HEAD",
    "TITLE",
    "NOSCRIPT",
    "OPTION",
  ]);

  // Theme-settle check: a custom property inherits, so a deep leaf resolving a different ink value
  // means an inner `data-theme` or a subtree the flip missed. `mixed` means two palettes at once.
  const root = document.documentElement;
  const CANDIDATES = [
    "--c-text-primary",
    "--c-text-secondary",
    "--c-fg",
    "--fg",
    "--color-text",
    "--text",
  ];
  let inkToken: string | null = null;
  if (opts.inkToken === null) {
    inkToken = null;
  } else if (typeof opts.inkToken === "string") {
    inkToken = colorTokenAt(root, opts.inkToken) === null ? null : opts.inkToken;
  } else {
    for (const name of CANDIDATES) {
      if (colorTokenAt(root, name) !== null) {
        inkToken = name;
        break;
      }
    }
  }

  const leaves: Element[] = [];
  for (const el of [...document.body.querySelectorAll("*")]) {
    if (el.children.length === 0 && !hasNoBox(el) && !SKIP.has(el.tagName)) {
      leaves.push(el);
      if (leaves.length >= 40) {
        break;
      }
    }
  }
  let palette = "unavailable";
  let rootInk: string | null = null;
  let leafInk: string | null = null;
  if (inkToken !== null) {
    const rootRaw = colorTokenAt(root, inkToken);
    rootInk = rootRaw === null ? null : hexOf(parseColor(rootRaw));
    palette = "consistent";
    for (const leaf of leaves) {
      const raw = colorTokenAt(leaf, inkToken);
      const hex = raw === null ? null : hexOf(parseColor(raw));
      leafInk ??= hex;
      if (hex !== null && hex !== rootInk) {
        leafInk = hex;
        palette = "mixed";
        break;
      }
    }
    leafInk ??= rootInk;
  }

  interface Finding {
    ratio: number;
    floor: number;
    size: number;
    painted: string;
    bg: string;
    opacity: number;
    opacityFrom: string[];
    sel: string;
    bgFrom: string;
  }
  const findings: (readonly [string, Finding])[] = [];
  const dimmed: (readonly [
    string,
    { ratio: number; floor: number; opacityFrom: string[]; sel: string },
  ])[] = [];
  const unresolved: (readonly [string, { sel: string; bgFrom: string }])[] = [];
  const passes: (readonly [
    string,
    { ratio: number; floor: number; painted: string; bg: string; sel: string },
  ])[] = [];
  const inactive: (readonly [string, { sel: string }])[] = [];
  const backdrops = new Set<string>();
  let scanned = 0;

  for (const el of [...document.body.querySelectorAll("*")]) {
    if (SKIP.has(el.tagName)) {
      continue;
    }
    // A DIRECT text child, so one string is measured once.
    let hasText = false;
    for (const node of [...el.childNodes]) {
      if (node.nodeType === 3 && (node.textContent ?? "").trim() !== "") {
        hasText = true;
        break;
      }
    }
    if (!hasText || hasNoBox(el)) {
      continue;
    }
    const cs = getComputedStyle(el);
    if (cs.visibility !== "visible" || cs.contentVisibility === "hidden") {
      continue;
    }
    // NOT PAINTED, so its contrast concerns nothing a reader sees (collapsed disclosures, unexpanded
    // pill cards); `--reveal` audits those surfaces.
    const paintedAlpha = foldedOpacity(el);
    if (paintedAlpha <= 0.001) {
      continue;
    }
    // WCAG 1.4.3 exempts an INACTIVE component's text; counted, since "really disabled" is a judgement.
    if (el.closest(":disabled, [aria-disabled='true']") !== null) {
      inactive.push([describeEl(el), { sel: describeEl(el) }]);
      continue;
    }
    const r = el.getBoundingClientRect();
    // A screen-reader-only string is clipped to about a pixel: painted, not readable.
    if (r.width < 2 || r.height < 2) {
      continue;
    }

    const sel = describeEl(el);
    const surface = surfaceUnder(el);
    if (surface.color === null) {
      unresolved.push([sel, { sel, bgFrom: surface.imageFrom ?? surface.from }]);
      continue;
    }
    scanned += 1;

    const dim = paintedAlpha;
    const inkRaw = parseColor(cs.color);
    const ink = over({ ...inkRaw, a: inkRaw.a * dim }, surface.color);
    const ratio = Math.round(contrastRatio(ink, surface.color) * 100) / 100;

    const size = Math.round(Number.parseFloat(cs.fontSize) * 100) / 100;
    const weight = Number.parseFloat(cs.fontWeight);
    // WCAG 1.4.3's large-text floor: 18.66px bold (14pt) or 24px (18pt).
    const large = size >= 24 || (Number.isFinite(weight) && weight >= 700 && size >= 18.66);
    const floor = large ? 3 : 4.5;

    const painted = hexOf(ink);
    const bg = hexOf(surface.color);
    backdrops.add(bg);
    const opacity = Math.round(dim * 100) / 100;
    const opacityFrom = dim < 1 ? dimmedBy(el) : [];

    if (ratio + 0.005 < floor) {
      findings.push([
        `${sel}|${painted}|${bg}|${floor}`,
        {
          ratio,
          floor,
          size,
          painted,
          bg,
          opacity,
          opacityFrom,
          sel,
          bgFrom: surface.from,
        },
      ]);
    } else if (dim < 1) {
      // Passing under an opacity no static gate sees; a dimmed FAILURE is in `findings`, never both.
      dimmed.push([`${sel}|${painted}|${bg}`, { ratio, floor, opacityFrom, sel }]);
    } else {
      passes.push([`${sel}|${painted}|${bg}`, { ratio, floor, painted, bg, sel }]);
    }
  }

  return {
    route: routeOf(),
    theme: root.dataset["theme"] ?? "unset",
    palette,
    inkToken,
    rootInk,
    leafInk,
    scanned,
    findings: bucket(findings),
    dimmed: bucket(dimmed),
    unresolved: bucket(unresolved),
    inactive: bucket(inactive),
    backdrops: [...backdrops].sort(),
    passes: bucket(passes),
  };
}

// --- the injection contract ------------------------------------------------

/** The helpers `pageSource` emits beside a walker. The KEY is the emitted name a walker calls; the
 *  binding is by string, so renaming a key means renaming its call sites. */
const HELPERS = {
  describeEl,
  lenPx,
  routeOf,
  tokenPx,
  hasNoBox,
  distortedUnder,
  bucket,
  ownsPoint,
  targetExtent,
  inlineInText,
  growsTarget,
  centreOnScreen,
  parseColor,
  hexOf,
  over,
  contrastRatio,
  foldedOpacity,
  dimmedBy,
  surfaceUnder,
  colorTokenAt,
} as const;

/** Every walker a runner may ask for, by the name it passes. */
export const WALKERS: Readonly<Record<string, Walker>> = {
  reveal: reveal,
  "control-height": controlHeight,
  radius: radius,
  contrast: contrast,
};

/** One page-side expression: the helpers, then the walker applied to its options. An IIFE because
 *  `Runtime.evaluate` returns an EXPRESSION's value and the helpers must share the walker's scope. */
export function pageSource(name: string, opts: Readonly<Record<string, unknown>> = {}): string {
  const fn = WALKERS[name];
  if (fn === undefined) {
    throw new Error(`unknown walker "${name}" (known: ${Object.keys(WALKERS).join(", ")})`);
  }
  const prelude = Object.entries(HELPERS)
    .map(([alias, helper]) => `const ${alias} = ${helper.toString()};`)
    .join("\n");
  return `(() => {\n${prelude}\nreturn (${fn.toString()})(${JSON.stringify(opts)});\n})()`;
}
