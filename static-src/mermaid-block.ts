// A closed ```mermaid fence becomes a diagram drawn by pele, imported the first time one is seen; until it is drawn,
// from its opening fence on, a skeleton stands in for its source. The fence stays a decorated code block (hidden)
// beside the drawing, so Copy, the source toggle and the failure fallback all read the one `<pre>`. pele's SVG is
// inserted as markup: its security policy lets an app do that when it meets the duties `mermaid-svg.ts`
// RENDER_OPTIONS and `adoptSvg` carry, and the CSP's `script-src 'self'` refuses any handler anyway.

import { el } from "@cplieger/reactive";
import { extractLang } from "./code-blocks.js";
import { iconEl } from "./icon-el.js";
import { ICON_CODE_UI, ICON_DOWNLOAD, ICON_EXPAND_UI, ICON_EYE_UI } from "./icons.js";
import { openMermaidOverlay } from "./mermaid-overlay.js";
import {
  adoptSvg,
  diagramTitle,
  downloadSvg,
  isDrawn,
  loadedPele,
  loadPele,
  nextIdPrefix,
  RENDER_OPTIONS,
  type DrawnMount,
  type Pele,
} from "./mermaid-svg.js";

/** On the `.code-wrap`: `pending` while pele loads, `drawn`, or `failed` (the source stays, under a notice). */
const STATE_ATTR = "data-mermaid";

/** The narrowest a wide diagram is drawn inline, as a share of its natural width; past it the block scrolls
 *  sideways rather than shrinking labels below reading size. The full-screen view zooms further. */
const MIN_INLINE_SCALE = 0.6;

const SKELETON_CLASS = "mermaid-skeleton";
const DRAWING_LABEL = "Drawing diagram";

const RENDER_FAILED = "Diagram could not be rendered";
const LOAD_FAILED = "Diagram renderer could not be loaded";

/** pele's first message line names the fault ("Parse error on line 3:"); the rest quotes the source, which is
 *  already on screen. */
function failureText(pele: Pele, err: unknown): string {
  if (!(err instanceof pele.PeleError)) {
    return RENDER_FAILED;
  }
  const first = (err.message.split("\n")[0] ?? "").trim().replace(/:$/, "");
  return first === "" ? RENDER_FAILED : `${RENDER_FAILED}: ${first}`;
}

/** The `accTitle` the given lines declare, or null. */
function accTitle(lines: string): string | null {
  return /^[ \t]*accTitle[ \t]*:[ \t]*(\S.*?)[ \t]*$/m.exec(lines)?.[1] ?? null;
}

/** Put the skeleton in place of the source, named by the `accTitle` in `finished` (only whole lines, so a title
 *  still arriving is not announced word by word). */
function holdSkeleton(wrap: HTMLElement, pre: HTMLElement, finished: string): void {
  let skel = wrap.querySelector<HTMLElement>(`:scope > .${SKELETON_CLASS}`);
  if (skel === null) {
    skel = el(
      "div",
      { className: SKELETON_CLASS, role: "img", "aria-busy": "true" },
      el("div", { className: "skeleton" }),
    );
    wrap.insertBefore(skel, pre);
  }
  const label = accTitle(finished) ?? DRAWING_LABEL;
  if (skel.getAttribute("aria-label") !== label) {
    skel.setAttribute("aria-label", label);
  }
  pre.hidden = true;
}

/** The block's source back on screen, with no skeleton. */
function releaseSource(wrap: HTMLElement, pre: HTMLElement): void {
  wrap.querySelector(`:scope > .${SKELETON_CLASS}`)?.remove();
  pre.hidden = false;
}

/** The source stays the block's body; the notice sits between its title bar and its text. Text only, because a
 *  PeleError quotes the diagram (pele's security policy). */
function showFailure(wrap: HTMLElement, pre: HTMLElement, text: string): void {
  wrap.setAttribute(STATE_ATTR, "failed");
  releaseSource(wrap, pre);
  const notice = el("p", { className: "mermaid-notice" });
  notice.textContent = text;
  wrap.insertBefore(notice, pre);
}

function actionButton(label: string, icon: string, onClick: () => void): HTMLButtonElement {
  const btn = el(
    "button",
    { type: "button", className: "code-act-btn", "data-tooltip": label, "aria-label": label },
    iconEl(icon),
  ) as HTMLButtonElement;
  btn.addEventListener("click", onClick);
  return btn;
}

/** The source toggle, Download and Expand join the block's own Copy, which already reads the hidden `<pre>`. */
function addControls(
  pele: Pele,
  wrap: HTMLElement,
  pre: HTMLElement,
  canvas: HTMLElement,
  mounted: DrawnMount,
  source: string,
): void {
  const actions = wrap.querySelector(":scope > .code-head > .code-actions");
  if (actions === null) {
    return;
  }
  const toggle = actionButton("Show source", ICON_CODE_UI, () => {
    const showSource = pre.hidden;
    pre.hidden = !showSource;
    canvas.hidden = showSource;
    const label = showSource ? "Show diagram" : "Show source";
    toggle.setAttribute("aria-expanded", String(showSource));
    toggle.setAttribute("aria-label", label);
    toggle.setAttribute("data-tooltip", label);
    toggle.replaceChildren(iconEl(showSource ? ICON_EYE_UI : ICON_CODE_UI));
  });
  toggle.setAttribute("aria-expanded", "false");
  toggle.setAttribute("aria-controls", pre.id);
  const download = actionButton("Download SVG", ICON_DOWNLOAD, () => {
    downloadSvg(mounted.result.svg, canvas);
  });
  const expand = actionButton("Expand", ICON_EXPAND_UI, () => {
    const svg = canvas.querySelector<SVGSVGElement>(":scope > svg");
    openMermaidOverlay(pele, source, {
      title: (svg === null ? null : diagramTitle(svg)) ?? "Diagram",
      returnFocus: expand,
    });
  });
  actions.prepend(toggle);
  actions.append(download, expand);
}

function draw(pele: Pele, wrap: HTMLElement, pre: HTMLElement, source: string): void {
  const prefix = nextIdPrefix();
  const canvas = el("div", { className: "mermaid-canvas" });
  wrap.insertBefore(canvas, pre);
  const mounted = pele.mount(canvas, source, {
    ...RENDER_OPTIONS,
    // Inline the drawing is static, so a pinch or wheel over it is left to the page.
    zoom: false,
    onRender: (result) => {
      const svg = adoptSvg(canvas, prefix);
      svg?.style.setProperty(
        "min-width",
        `${String(Math.round(result.width * MIN_INLINE_SCALE))}px`,
      );
    },
  });
  if (!isDrawn(mounted)) {
    // pele drew its own error box into the canvas; the block's notice over the source replaces it.
    mounted.destroy();
    canvas.remove();
    showFailure(wrap, pre, failureText(pele, mounted.error));
    return;
  }
  wrap.querySelector(`:scope > .${SKELETON_CLASS}`)?.remove();
  wrap.setAttribute(STATE_ATTR, "drawn");
  pre.id = `${prefix}source`;
  pre.hidden = true;
  addControls(pele, wrap, pre, canvas, mounted, source);
}

function diagramType(pele: Pele, source: string): ReturnType<Pele["detectType"]> {
  try {
    return pele.detectType(source);
  } catch {
    // An undetectable type is `mount`'s PeleError to report, with its own message.
    return null;
  }
}

async function drawWhenLoaded(wrap: HTMLElement, pre: HTMLElement, source: string): Promise<void> {
  let pele: Pele;
  try {
    pele = await loadPele();
    await pele.load(diagramType(pele, source));
  } catch {
    showFailure(wrap, pre, LOAD_FAILED);
    return;
  }
  draw(pele, wrap, pre, source);
}

/**
 * While a top-level ```mermaid fence is still streaming into `host` (its decorated `pre`), hold its source behind
 * the skeleton. A nested fence is left as code, because only a top-level block reaches `renderMermaidBlock`.
 */
export function holdStreamingMermaid(host: HTMLElement, pre: HTMLElement): void {
  const code = pre.querySelector("code");
  const wrap = pre.parentElement;
  if (code === null || wrap?.parentElement !== host || extractLang(pre, code) !== "mermaid") {
    return;
  }
  const text = code.textContent;
  holdSkeleton(wrap, pre, text.slice(0, text.lastIndexOf("\n") + 1));
}

/** Give back the source of every streamed mermaid fence in `host` that never closed, which is never drawn. */
export function releaseOpenMermaid(host: HTMLElement): void {
  for (const wrap of host.querySelectorAll<HTMLElement>(
    `:scope > .code-wrap:not([${STATE_ATTR}])`,
  )) {
    const pre = wrap.querySelector<HTMLElement>(":scope > pre");
    if (pre !== null && wrap.querySelector(`:scope > .${SKELETON_CLASS}`) !== null) {
      releaseSource(wrap, pre);
    }
  }
}

/**
 * Upgrade a CLOSED, decorated ```mermaid fence to a diagram; any other block is left alone, as is one already
 * upgraded. Synchronous once pele and the diagram's type are loaded (a replayed chat repaints in one pass); the
 * first diagram of a type draws when its chunk arrives, behind the skeleton until then.
 */
export function renderMermaidBlock(pre: HTMLElement): void {
  const code = pre.querySelector("code");
  const wrap = pre.parentElement;
  if (
    code === null ||
    extractLang(pre, code) !== "mermaid" ||
    wrap?.classList.contains("code-wrap") !== true ||
    wrap.hasAttribute(STATE_ATTR)
  ) {
    return;
  }
  const source = code.textContent;
  if (source.trim() === "") {
    releaseSource(wrap, pre);
    return;
  }
  wrap.setAttribute(STATE_ATTR, "pending");
  const pele = loadedPele();
  const type = pele === null ? null : diagramType(pele, source);
  if (pele !== null && type !== null && pele.registered(type)) {
    draw(pele, wrap, pre, source);
    return;
  }
  holdSkeleton(wrap, pre, source);
  void drawWhenLoaded(wrap, pre, source);
}
