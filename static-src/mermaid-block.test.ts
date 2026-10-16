// A closed ```mermaid fence in a reply is drawn by pele (the real library, loaded once below), inline and static,
// with its source one toggle away, and full screen with zoom. Measured over the shipped stylesheet.
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { emulateA11yMedia, resetA11yMedia } from "./__test-helpers__/a11y-media.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { setCopyCallback } from "./code-blocks.js";
import { createMarkdownStream, renderMarkdownInto, type MarkdownStream } from "./markdown.js";
import { exportableSvg, loadPele } from "./mermaid-svg.js";

const FLOW =
  "flowchart TD\n  accTitle: Order flow\n  A[Start] --> B{Paid?}\n  B -- yes --> C[Ship]\n";
/** Twelve lifelines: pele cannot narrow a sequence diagram past its participants, so this stays wide. */
const WIDE_SEQ =
  "sequenceDiagram\n" +
  [
    "Alpha",
    "Bravo",
    "Charlie",
    "Delta",
    "Echo",
    "Foxtrot",
    "Golf",
    "Hotel",
    "India",
    "Juliet",
    "Kilo",
    "Lima",
  ]
    .map(
      (p, i, all) =>
        `  ${p}Service->>${all[(i + 1) % all.length] ?? p}Service: request number ${String(i)}`,
    )
    .join("\n") +
  "\n";

let style: HTMLStyleElement;
let column: HTMLElement;

beforeAll(async () => {
  style = mountAppCSS();
  const pele = await loadPele();
  await Promise.all([pele.load("flowchart"), pele.load("sequence"), pele.load("gantt")]);
});

afterAll(() => {
  style.remove();
});

beforeEach(() => {
  column = document.createElement("div");
  column.className = "message assistant";
  column.style.width = "600px";
  document.body.appendChild(column);
});

afterEach(async () => {
  const open = document.querySelector<HTMLButtonElement>(
    'dialog.mermaid-overlay-dialog button[aria-label="Close"]',
  );
  if (open !== null) {
    open.click();
    await vi.waitFor(() => {
      expect(document.querySelector("dialog.mermaid-overlay-dialog")).toBeNull();
    });
  }
  column.remove();
  document.documentElement.removeAttribute("data-theme");
});

/** The test's own sRGB reading of a computed colour, by pixel, as a viewer would see it. */
function pixelHex(colour: string): string {
  const c = document.createElement("canvas");
  c.width = 1;
  c.height = 1;
  const ctx = c.getContext("2d");
  if (ctx === null) {
    throw new Error("no 2d context");
  }
  ctx.fillStyle = colour;
  ctx.fillRect(0, 0, 1, 1);
  return (
    "#" +
    [...ctx.getImageData(0, 0, 1, 1).data.slice(0, 3)]
      .map((n) => n.toString(16).padStart(2, "0"))
      .join("")
  );
}

function fence(source: string): string {
  return "```mermaid\n" + source + "```\n";
}

function render(md: string): HTMLElement {
  const host = document.createElement("div");
  column.appendChild(host);
  renderMarkdownInto(host, md);
  return host;
}

/** A live reply's renderer, parsing each write as it lands (the reveal's pacing). */
function live(): { host: HTMLElement; stream: MarkdownStream } {
  const host = document.createElement("div");
  column.appendChild(host);
  return { host, stream: createMarkdownStream(host, { flushIntervalMs: 0 }) };
}

/** Whether any of a fence's source is on screen: `hidden`, a CSS rule or a missing `<pre>` all answer false. */
function sourceShown(host: HTMLElement): boolean {
  return host.querySelector("pre")?.checkVisibility() === true;
}

function skeleton(host: HTMLElement): HTMLElement | null {
  return host.querySelector<HTMLElement>(".mermaid-skeleton");
}

function parts(host: HTMLElement): {
  wrap: HTMLElement;
  pre: HTMLElement;
  canvas: HTMLElement | null;
  svg: SVGSVGElement | null;
  button: (label: string) => HTMLButtonElement | null;
} {
  const wrap = host.querySelector<HTMLElement>(".code-wrap");
  const pre = host.querySelector<HTMLElement>("pre");
  if (wrap === null || pre === null) {
    throw new Error("no code block rendered");
  }
  return {
    wrap,
    pre,
    canvas: wrap.querySelector<HTMLElement>(".mermaid-canvas"),
    svg: wrap.querySelector<SVGSVGElement>(".mermaid-canvas > svg"),
    button: (label) =>
      wrap.querySelector<HTMLButtonElement>(`.code-actions > button[aria-label="${label}"]`),
  };
}

describe("which fences are drawn", () => {
  it("draws a closed mermaid fence in place of its source", () => {
    const { wrap, pre, svg } = parts(render(fence(FLOW)));
    expect(svg).not.toBeNull();
    expect(wrap.getAttribute("data-mermaid")).toBe("drawn");
    expect(pre.hidden).toBe(true);
    expect(pre.textContent).toBe(FLOW.trimEnd());
  });

  it("draws a fence closed at the very end of the reply", () => {
    const { svg } = parts(render("```mermaid\n" + FLOW + "```"));
    expect(svg).not.toBeNull();
  });

  it("leaves every other fence as code", () => {
    for (const lang of ["go", "", "mmd", "svg-mermaid"]) {
      const host = render("```" + lang + "\n" + FLOW + "```\n");
      expect(host.querySelector(".mermaid-canvas"), lang).toBeNull();
      expect(host.querySelector("pre")?.hidden, lang).toBe(false);
    }
  });

  it("leaves an empty mermaid fence as code", () => {
    const { canvas, wrap } = parts(render(fence("   \n")));
    expect(canvas).toBeNull();
    expect(wrap.hasAttribute("data-mermaid")).toBe(false);
  });

  it("never draws an unterminated fence, live or replayed, and shows its source", () => {
    const replayed = render("```mermaid\n" + FLOW);
    expect(parts(replayed).canvas).toBeNull();
    expect(sourceShown(replayed)).toBe(true);
    const { host, stream } = live();
    stream.writeDelta("```mermaid\n" + FLOW);
    stream.end();
    expect(host.querySelector(".mermaid-canvas")).toBeNull();
    expect(skeleton(host)).toBeNull();
    expect(sourceShown(host)).toBe(true);
  });
});

describe("while the fence streams in", () => {
  it("shows a skeleton, never the source, from the opening fence until the diagram", () => {
    const { host, stream } = live();
    const chunks = [
      "Here it is:\n\n```merm",
      "aid\n",
      "flowchart TD\n  accTitle: Order",
      " flow\n  A[Start] --> B{Paid?}\n",
      "  B -- yes --> C[Ship]\n",
    ];
    const names: (string | null)[] = [];
    for (const chunk of chunks) {
      stream.writeDelta(chunk);
      expect(sourceShown(host), `source on screen after ${JSON.stringify(chunk)}`).toBe(false);
      expect(host.querySelector(".mermaid-canvas")).toBeNull();
      names.push(skeleton(host)?.getAttribute("aria-label") ?? null);
    }
    expect(
      names,
      "a skeleton from the info string on, named by the accTitle once it is whole",
    ).toEqual([null, "Drawing diagram", "Drawing diagram", "Order flow", "Order flow"]);
    const skel = skeleton(host) as HTMLElement;
    expect(skel.getAttribute("role")).toBe("img");
    expect(skel.getAttribute("aria-busy")).toBe("true");
    expect(skel.getBoundingClientRect().height).toBeGreaterThan(0);

    const wrap = host.querySelector(".code-wrap");
    const pre = host.querySelector("pre");
    const observer = new MutationObserver(() => undefined);
    observer.observe(host, { childList: true, subtree: true });
    stream.writeDelta("```\n\nAfter.\n");
    const removed = observer
      .takeRecords()
      .flatMap((r) => [...r.removedNodes])
      .filter((n) => n instanceof Element);
    observer.disconnect();
    expect(host.querySelector(".mermaid-canvas > svg")).not.toBeNull();
    expect(skeleton(host)).toBeNull();
    expect(sourceShown(host)).toBe(false);
    expect(removed, "the skeleton is the one element taken out").toHaveLength(1);
    expect(removed[0]).toBe(skel);
    expect(host.querySelector(".code-wrap"), "the block itself stays in place").toBe(wrap);
    expect(host.querySelector("pre")).toBe(pre);
    stream.end();
    expect(host.querySelectorAll(".mermaid-canvas")).toHaveLength(1);
  });

  it("keeps the source toggle working on a diagram drawn from a stream", async () => {
    const { host, stream } = live();
    stream.writeDelta("```mermaid\n" + FLOW);
    stream.writeDelta("```\n");
    stream.end();
    const { pre, canvas, button } = parts(host);
    await userEvent.click(button("Show source") as HTMLButtonElement);
    expect(pre.checkVisibility()).toBe(true);
    expect(canvas?.hidden).toBe(true);
    expect(skeleton(host)).toBeNull();
  });

  it("pulses while it waits", () => {
    const { host, stream } = live();
    stream.writeDelta("```mermaid\nflowchart TD\n");
    const box = host.querySelector(".mermaid-skeleton > .skeleton") as HTMLElement;
    const [pulse] = box.getAnimations();
    expect(pulse?.effect?.getTiming().iterations).toBe(Infinity);
    expect(pulse?.playState).toBe("running");
  });

  it("streams every other fence as code", () => {
    const { host, stream } = live();
    stream.writeDelta("```go\npackage main\n");
    expect(sourceShown(host)).toBe(true);
    expect(skeleton(host)).toBeNull();
    stream.writeDelta("```\n");
    expect(sourceShown(host)).toBe(true);
    expect(skeleton(host)).toBeNull();
  });

  // Only a top-level fence is drawn (markdown.ts `decorate`), so a nested one would never leave its skeleton.
  it("streams a fence nested in a list as code", () => {
    const { host, stream } = live();
    stream.writeDelta("- step\n  ```mermaid\n  flowchart TD\n");
    expect(host.querySelector("li pre")).not.toBeNull();
    expect(sourceShown(host)).toBe(true);
    expect(skeleton(host)).toBeNull();
  });

  it("shows an empty fence as code once it closes", () => {
    const { host, stream } = live();
    stream.writeDelta("```mermaid\n");
    expect(skeleton(host)).not.toBeNull();
    stream.writeDelta("```\n");
    expect(skeleton(host)).toBeNull();
    expect(sourceShown(host)).toBe(true);
    expect(host.querySelector(".code-wrap")?.hasAttribute("data-mermaid")).toBe(false);
  });

  it("ends in the source under the notice when the streamed diagram cannot be drawn", () => {
    const { host, stream } = live();
    stream.writeDelta("```mermaid\nflowchart TD\n");
    stream.writeDelta("  A -->\n");
    expect(sourceShown(host)).toBe(false);
    stream.writeDelta("```\n");
    const { wrap, pre } = parts(host);
    expect(wrap.getAttribute("data-mermaid")).toBe("failed");
    expect(skeleton(host)).toBeNull();
    expect(pre.checkVisibility()).toBe(true);
    const notice = wrap.querySelector(".mermaid-notice");
    expect(notice?.textContent).toBe("Diagram could not be rendered: Parse error on line 3");
    expect(notice?.nextElementSibling).toBe(pre);
  });
});

describe("a diagram whose renderer is still loading", () => {
  // `pie` is the one type this file leaves unloaded, so its first block takes the async path.
  it("shows a skeleton in place of the source, then the diagram", async () => {
    const host = render(fence('pie\n  accTitle: Pets\n  "Dogs" : 386\n  "Cats" : 85\n'));
    const { wrap } = parts(host);
    expect(wrap.getAttribute("data-mermaid")).toBe("pending");
    expect(sourceShown(host)).toBe(false);
    expect(skeleton(host)?.getAttribute("aria-label")).toBe("Pets");
    await vi.waitFor(() => {
      expect(wrap.getAttribute("data-mermaid")).toBe("drawn");
    });
    expect(skeleton(host)).toBeNull();
    expect(host.querySelector(".mermaid-canvas > svg")).not.toBeNull();
    expect(sourceShown(host)).toBe(false);
  });
});

describe("the skeleton under reduced motion", () => {
  beforeAll(async () => {
    await emulateA11yMedia({ reducedMotion: "reduce" });
  });

  afterAll(async () => {
    await resetA11yMedia();
  });

  it("holds still", async () => {
    const { host, stream } = live();
    stream.writeDelta("```mermaid\nflowchart TD\n");
    const box = host.querySelector(".mermaid-skeleton > .skeleton") as HTMLElement;
    await vi.waitFor(() => {
      expect(box.getAnimations().filter((a) => a.playState === "running")).toEqual([]);
    });
    expect(getComputedStyle(box).opacity, "parked at the pulse's mean").toBe("0.275");
  });
});

describe("a diagram that cannot be drawn", () => {
  it("keeps the source as a code block under a one-line notice naming the fault", () => {
    const { wrap, pre, canvas } = parts(render(fence("flowchart TD\n  A -->\n")));
    expect(canvas).toBeNull();
    expect(pre.hidden).toBe(false);
    expect(wrap.getAttribute("data-mermaid")).toBe("failed");
    const notice = wrap.querySelector(".mermaid-notice");
    expect(notice?.textContent).toBe("Diagram could not be rendered: Parse error on line 3");
    expect(notice?.nextElementSibling).toBe(pre);
  });

  it("reports an unknown diagram type the same way", async () => {
    const { wrap, pre } = parts(render(fence("notadiagram\n  x\n")));
    await vi.waitFor(() => {
      expect(wrap.querySelector(".mermaid-notice")?.textContent).toBe(
        "Diagram could not be rendered: No diagram type detected for the given text.",
      );
    });
    expect(pre.hidden).toBe(false);
  });

  // pele's message quotes the source (its security policy), here on its first line; markup in it must stay text.
  it("shows the error as text, never as markup", () => {
    const bad = "<img src=x onerror=alert(1)>";
    const { wrap } = parts(
      render(fence(`gantt\n  dateFormat YYYY-MM-DD\n  section S\n  t1 :a1, ${bad}, 3d\n`)),
    );
    const notice = wrap.querySelector(".mermaid-notice");
    expect(notice?.textContent).toBe(`Diagram could not be rendered: Invalid date:${bad}`);
    expect(notice?.children).toHaveLength(0);
    expect(wrap.querySelector("img")).toBeNull();
  });

  // pele cannot even name the type of a diagram whose front matter does not parse.
  it("reports a broken front matter rather than failing the reply's render", async () => {
    const host = render(
      fence("---\ntitle: [unclosed\n---\nflowchart TD\n  A --> B\n") + "After.\n",
    );
    const { wrap } = parts(host);
    expect(host.textContent).toContain("After.");
    await vi.waitFor(() => {
      expect(wrap.querySelector(".mermaid-notice")?.textContent).toMatch(
        /^Diagram could not be rendered: Front matter is not valid YAML/,
      );
    });
  });

  it("refuses a diagram over pele's size limit the same way", () => {
    const { wrap, pre } = parts(render(fence("flowchart TD\n" + "  A --> B\n".repeat(6000))));
    expect(wrap.querySelector(".mermaid-notice")?.textContent).toBe(
      "Diagram could not be rendered: Diagram source is longer than the limit of 50000 characters.",
    );
    expect(pre.hidden).toBe(false);
  });
});

describe("the block's controls", () => {
  it("toggles between the diagram and its source, saying which is shown", async () => {
    const { pre, canvas, button } = parts(render(fence(FLOW)));
    const toggle = button("Show source");
    expect(toggle?.getAttribute("aria-expanded")).toBe("false");
    expect(toggle?.getAttribute("aria-controls")).toBe(pre.id);
    await userEvent.click(toggle as HTMLButtonElement);
    expect(pre.hidden).toBe(false);
    expect(canvas?.hidden).toBe(true);
    expect(toggle?.getAttribute("aria-expanded")).toBe("true");
    expect(toggle?.getAttribute("aria-label")).toBe("Show diagram");
    await userEvent.click(toggle as HTMLButtonElement);
    expect(pre.hidden).toBe(true);
    expect(canvas?.hidden).toBe(false);
    expect(toggle?.getAttribute("aria-label")).toBe("Show source");
  });

  it("copies the source, not the drawing", async () => {
    const copied: string[] = [];
    setCopyCallback((t) => copied.push(t));
    const { button } = parts(render(fence(FLOW)));
    await userEvent.click(button("Copy") as HTMLButtonElement);
    await userEvent.click(button("Expand") as HTMLButtonElement);
    await userEvent.click(
      document.querySelector(
        'dialog.mermaid-overlay-dialog button[aria-label="Copy"]',
      ) as HTMLButtonElement,
    );
    expect(copied).toEqual([FLOW.trimEnd(), FLOW.trimEnd()]);
  });

  it("orders the row source toggle, copy, download, expand", () => {
    const { wrap } = parts(render(fence(FLOW)));
    const labels = [...wrap.querySelectorAll(".code-actions > button")].map((b) =>
      b.getAttribute("aria-label"),
    );
    expect(labels).toEqual(["Show source", "Copy", "Download SVG", "Expand"]);
  });

  it("downloads the drawn SVG with its colours resolved", async () => {
    const { button, canvas } = parts(render(fence(FLOW)));
    let saved: Blob | undefined;
    vi.spyOn(URL, "createObjectURL").mockImplementation((b) => {
      saved = b as Blob;
      return "blob:diagram";
    });
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    await userEvent.click(button("Download SVG") as HTMLButtonElement);
    const text = await (saved as Blob).text();
    expect(saved?.type).toBe("image/svg+xml");
    expect(text).not.toContain("var(");
    const surface = pixelHex(
      getComputedStyle(canvas as HTMLElement).getPropertyValue("--pele-surface"),
    );
    expect(text).toContain(`--_s:${surface}`);
    expect(text).toContain(">Order flow</title>");
  });
});

describe("the drawn SVG in this page", () => {
  it("is an image named by the diagram's accTitle", () => {
    const { svg } = parts(render(fence(FLOW)));
    expect(svg?.getAttribute("role")).toBe("img");
    const labelId = svg?.getAttribute("aria-labelledby") ?? "";
    expect(document.getElementById(labelId)?.textContent).toBe("Order flow");
  });

  it("gets a generic name when the diagram has no accTitle", () => {
    const { svg } = parts(render(fence("flowchart TD\n  A --> B\n")));
    expect(svg?.getAttribute("aria-label")).toBe("Mermaid diagram");
  });

  // Two diagrams in one transcript must not label each other, and a diagram's names are not the app's ids.
  it("prefixes every id, so a diagram cannot shadow the app's own elements", () => {
    const target = document.createElement("div");
    target.id = "composer";
    const host = render(
      fence(
        'flowchart TD\n  accTitle: One\n  composer --> B\n  click composer "https://example.com"\n',
      ) + fence("flowchart TD\n  accTitle: Two\n  A --> B\n"),
    );
    document.body.appendChild(target);
    try {
      expect(document.getElementById("composer")).toBe(target);
      const svgs = [...host.querySelectorAll<SVGSVGElement>(".mermaid-canvas > svg")];
      expect(svgs).toHaveLength(2);
      const titles = svgs.map(
        (s) => document.getElementById(s.getAttribute("aria-labelledby") ?? "")?.textContent,
      );
      expect(titles).toEqual(["One", "Two"]);
      for (const node of host.querySelectorAll(".mermaid-canvas [id]")) {
        expect(node.id).toMatch(/^mmd\d+-/);
      }
    } finally {
      target.remove();
    }
  });

  it("keeps only web links, opening each in a new tab without a referrer", () => {
    const { svg } = parts(
      render(
        fence(
          'flowchart TD\n  A --> B\n  click A "https://example.com/a" "tip"\n  click B "javascript:alert(1)"\n',
        ),
      ),
    );
    const links = [...(svg?.querySelectorAll("a") ?? [])];
    expect(links.map((a) => a.getAttribute("href"))).toEqual(["https://example.com/a"]);
    expect(links[0]?.getAttribute("target")).toBe("_blank");
    expect(links[0]?.getAttribute("rel")).toBe("noopener noreferrer");
  });

  it("stays a named document, not an image, when it holds a link, so the link is announced", () => {
    const { svg } = parts(
      render(
        fence('flowchart TD\n  accTitle: Linked\n  A --> B\n  click A "https://example.com/a"\n'),
      ),
    );
    expect(svg?.getAttribute("role")).toBe("graphics-document document");
    const labelId = svg?.getAttribute("aria-labelledby") ?? "";
    expect(document.getElementById(labelId)?.textContent).toBe("Linked");
  });

  it("fetches no remote image", () => {
    const { svg } = parts(
      render(
        fence(
          'flowchart TD\n  A@{ img: "https://example.com/x.png" }\n  B@{ img: "/favicon.svg" }\n  A --> B\n',
        ),
      ),
    );
    const hrefs = [...(svg?.querySelectorAll("image") ?? [])].map((i) => i.getAttribute("href"));
    expect(hrefs).toEqual(["/favicon.svg"]);
  });

  it("restyles with the theme without being redrawn", () => {
    document.documentElement.setAttribute("data-theme", "dark");
    const { svg } = parts(render(fence(FLOW)));
    const node = svg?.querySelector(".pele-node rect, .pele-node path, .pele-node polygon");
    expect(node).not.toBeNull();
    const dark = getComputedStyle(node as Element).fill;
    document.documentElement.setAttribute("data-theme", "light");
    const light = getComputedStyle(node as Element).fill;
    expect(light).not.toBe(dark);
    expect(column.querySelector(".mermaid-canvas > svg")).toBe(svg);
    expect(svg?.querySelector(".pele-node rect, .pele-node path, .pele-node polygon")).toBe(node);
  });

  it("is static inline: a ctrl-wheel over it is left to the page", () => {
    const { svg } = parts(render(fence(FLOW)));
    const wheel = new WheelEvent("wheel", {
      ctrlKey: true,
      deltaY: -50,
      bubbles: true,
      cancelable: true,
    });
    svg?.dispatchEvent(wheel);
    expect(wheel.defaultPrevented).toBe(false);
    expect(svg?.hasAttribute("tabindex")).toBe(false);
    expect(svg?.style.touchAction).toBe("");
  });

  it("scrolls a diagram too wide to read inside its block rather than past the column", () => {
    column.style.width = "360px";
    const { canvas, svg } = parts(render(fence(WIDE_SEQ)));
    const natural = Number(svg?.getAttribute("width"));
    expect(natural).toBeGreaterThan(600);
    const drawn = svg?.getBoundingClientRect().width ?? 0;
    expect(drawn).toBeGreaterThanOrEqual(Math.round(natural * 0.6) - 1);
    expect(canvas?.scrollWidth).toBeGreaterThan(canvas?.clientWidth ?? 0);
    (canvas as HTMLElement).scrollLeft = 100;
    expect(canvas?.scrollLeft, "the block itself scrolls to reach the clipped part").toBe(100);
    expect(canvas?.getBoundingClientRect().right).toBeLessThanOrEqual(
      column.getBoundingClientRect().right + 1,
    );
  });
});

describe("the exported SVG", () => {
  it("carries the theme's resolved colours and background, and parses as SVG", async () => {
    const pele = await loadPele();
    const { canvas } = parts(render(fence(FLOW)));
    const raw = pele.render(FLOW).svg;
    const out = exportableSvg(raw, canvas as HTMLElement);
    expect(out).not.toContain("var(");
    const doc = new DOMParser().parseFromString(out, "image/svg+xml");
    expect(doc.querySelector("parsererror")).toBeNull();
    const root = doc.documentElement;
    const surface = pixelHex(
      getComputedStyle(canvas as HTMLElement).getPropertyValue("--pele-surface"),
    );
    const bg = pixelHex(getComputedStyle(canvas as HTMLElement).getPropertyValue("--pele-bg"));
    expect(root.getAttribute("style")).toContain(`--_s:${surface}`);
    expect(root.getAttribute("style")).toContain(`background-color:${bg}`);
    expect(root.getAttribute("style")).toContain("font-family:system-ui, sans-serif");
  });

  it("follows the theme it is exported in", async () => {
    const pele = await loadPele();
    const { canvas } = parts(render(fence(FLOW)));
    const raw = pele.render(FLOW).svg;
    document.documentElement.setAttribute("data-theme", "dark");
    const dark = exportableSvg(raw, canvas as HTMLElement);
    document.documentElement.setAttribute("data-theme", "light");
    const light = exportableSvg(raw, canvas as HTMLElement);
    expect(light).not.toBe(dark);
  });
});

describe("the full-screen view", () => {
  function overlay(): HTMLDialogElement | null {
    return document.querySelector<HTMLDialogElement>("dialog.mermaid-overlay-dialog");
  }

  it("opens a labelled modal, focused, with the page behind it held still", async () => {
    const { button } = parts(render(fence(FLOW)));
    await userEvent.click(button("Expand") as HTMLButtonElement);
    const dlg = overlay();
    expect(dlg?.open).toBe(true);
    expect(dlg?.matches(":modal")).toBe(true);
    expect(document.getElementById(dlg?.getAttribute("aria-labelledby") ?? "")?.textContent).toBe(
      "Order flow",
    );
    expect(document.activeElement?.getAttribute("aria-label")).toBe("Close");
    expect(document.body.style.position).toBe("fixed");
    // Layout boxes, not the client rect, which the entry animation's scale shrinks for a beat.
    expect(dlg?.offsetWidth).toBe(document.documentElement.clientWidth);
    expect(dlg?.offsetHeight).toBe(document.documentElement.clientHeight);
  });

  it("fills a phone's viewport too, where a modal is otherwise a capped card", async () => {
    await page.viewport(390, 844);
    try {
      const { button } = parts(render(fence(FLOW)));
      await userEvent.click(button("Expand") as HTMLButtonElement);
      const panel = overlay()?.querySelector<HTMLElement>(".mermaid-overlay");
      expect(panel?.offsetWidth).toBe(document.documentElement.clientWidth);
      expect(panel?.offsetHeight).toBe(document.documentElement.clientHeight);
    } finally {
      await page.viewport(1280, 720);
    }
  });

  it("zooms with its controls", async () => {
    const { button } = parts(render(fence(FLOW)));
    await userEvent.click(button("Expand") as HTMLButtonElement);
    const svg = overlay()?.querySelector<SVGSVGElement>(".mermaid-stage > svg");
    expect(svg?.getAttribute("tabindex")).toBe("0");
    const fitted = svg?.getAttribute("viewBox");
    await userEvent.click(
      overlay()?.querySelector('button[aria-label="Zoom in"]') as HTMLButtonElement,
    );
    const zoomed = svg?.getAttribute("viewBox");
    expect(zoomed).not.toBe(fitted);
    await userEvent.click(
      overlay()?.querySelector('button[aria-label="Reset"]') as HTMLButtonElement,
    );
    expect(svg?.getAttribute("viewBox")).toBe(fitted);
  });

  /** Open the view and let its entry animation finish: pele measures the drawing's box when a zoom starts, and the
   *  entry's scale would shrink that box for a beat. */
  async function openSettled(expand: HTMLButtonElement): Promise<SVGSVGElement> {
    await userEvent.click(expand);
    const dlg = overlay();
    await Promise.all(dlg?.getAnimations({ subtree: true }).map((a) => a.finished) ?? []);
    return dlg?.querySelector<SVGSVGElement>(".mermaid-stage > svg") as SVGSVGElement;
  }

  function wheel(svg: SVGSVGElement, init: WheelEventInit): WheelEvent {
    const ev = new WheelEvent("wheel", { ...init, bubbles: true, cancelable: true });
    svg.dispatchEvent(ev);
    return ev;
  }

  function viewBox(svg: SVGSVGElement): number[] {
    return (svg.getAttribute("viewBox") ?? "").split(" ").map(Number);
  }

  it("zooms on a plain mouse wheel, around the pointer, without scrolling anything", async () => {
    const { button } = parts(render(fence(FLOW)));
    const svg = await openSettled(button("Expand") as HTMLButtonElement);
    const stage = svg.parentElement as HTMLElement;
    const box = svg.getBoundingClientRect();
    const reset = overlay()?.querySelector('button[aria-label="Reset"]') as HTMLButtonElement;
    const at = (clientX: number, clientY: number): number[] => {
      const ev = wheel(svg, { deltaY: -50, clientX, clientY });
      expect(ev.defaultPrevented).toBe(true);
      return viewBox(svg);
    };
    const [x1, y1, w1 = NaN] = at(box.left + 2, box.top + 2);
    expect(w1, "the view now spans less than the stage at natural size").toBeLessThan(
      stage.clientWidth,
    );
    await userEvent.click(reset);
    const [x2, y2] = at(box.right - 2, box.bottom - 2);
    expect([x2, y2], "a wheel elsewhere zooms around elsewhere").not.toEqual([x1, y1]);
  });

  it("zooms as far for a wheel step counted in lines or pages as for the pixels it scrolls", async () => {
    const { button } = parts(render(fence(FLOW)));
    const svg = await openSettled(button("Expand") as HTMLButtonElement);
    const reset = overlay()?.querySelector('button[aria-label="Reset"]') as HTMLButtonElement;
    wheel(svg, { deltaY: -48 });
    const byPixels = viewBox(svg);
    await userEvent.click(reset);
    wheel(svg, { deltaY: -3, deltaMode: WheelEvent.DOM_DELTA_LINE });
    expect(viewBox(svg), "three lines").toEqual(byPixels);
    await userEvent.click(reset);
    wheel(svg, { deltaY: -1, deltaMode: WheelEvent.DOM_DELTA_PAGE });
    expect(viewBox(svg), "one page").toEqual(byPixels);
  });

  it("closes on the first Escape even while zoomed with the drawing focused", async () => {
    const { button } = parts(render(fence(FLOW)));
    const expand = button("Expand") as HTMLButtonElement;
    await userEvent.click(expand);
    const svg = overlay()?.querySelector<SVGSVGElement>(".mermaid-stage > svg");
    await userEvent.click(
      overlay()?.querySelector('button[aria-label="Zoom in"]') as HTMLButtonElement,
    );
    svg?.focus();
    expect(document.activeElement).toBe(svg);
    const zoomed = svg?.getAttribute("viewBox");
    await userEvent.keyboard("{Escape}");
    expect(svg?.getAttribute("viewBox"), "the key never reached pele's zoom reset").toBe(zoomed);
    await vi.waitFor(() => {
      expect(overlay()).toBeNull();
    });
    expect(document.activeElement).toBe(expand);
  });

  it("keeps the Escape that closed it from the page's own Escape handling", async () => {
    const { button } = parts(render(fence(FLOW)));
    await userEvent.click(button("Expand") as HTMLButtonElement);
    const seen = vi.fn();
    document.addEventListener("keydown", seen);
    try {
      await userEvent.keyboard("{Escape}");
    } finally {
      document.removeEventListener("keydown", seen);
    }
    expect(seen).not.toHaveBeenCalled();
  });

  it("fits the drawing inside the viewport", async () => {
    const { button } = parts(render(fence(WIDE_SEQ)));
    await userEvent.click(button("Expand") as HTMLButtonElement);
    const stage = overlay()?.querySelector<HTMLElement>(".mermaid-stage");
    const svg = stage?.querySelector("svg")?.getBoundingClientRect();
    const box = stage?.getBoundingClientRect();
    expect(svg?.width).toBeLessThanOrEqual((box?.width ?? 0) + 1);
    expect(svg?.height).toBeLessThanOrEqual((box?.height ?? 0) + 1);
  });

  it("closes on Escape and hands focus back to Expand", async () => {
    const { button } = parts(render(fence(FLOW)));
    const expand = button("Expand") as HTMLButtonElement;
    await userEvent.click(expand);
    await userEvent.keyboard("{Escape}");
    await vi.waitFor(() => {
      expect(overlay()).toBeNull();
    });
    expect(document.activeElement).toBe(expand);
    expect(document.body.style.position).toBe("");
  });

  // A pointer press does not focus a button in Safari, so the platform's own return lands on whatever held focus.
  it("hands focus back to Expand even when opening it did not focus it", async () => {
    const { button } = parts(render(fence(FLOW)));
    const expand = button("Expand") as HTMLButtonElement;
    const elsewhere = document.createElement("input");
    column.appendChild(elsewhere);
    elsewhere.focus();
    expand.click();
    await userEvent.keyboard("{Escape}");
    await vi.waitFor(() => {
      expect(overlay()).toBeNull();
    });
    expect(document.activeElement).toBe(expand);
  });

  it("closes from its Close button", async () => {
    const { button } = parts(render(fence(FLOW)));
    await userEvent.click(button("Expand") as HTMLButtonElement);
    await userEvent.click(
      overlay()?.querySelector('button[aria-label="Close"]') as HTMLButtonElement,
    );
    await vi.waitFor(() => {
      expect(overlay()).toBeNull();
    });
  });

  it("offers copy and download of the same diagram", async () => {
    const { button } = parts(render(fence(FLOW)));
    await userEvent.click(button("Expand") as HTMLButtonElement);
    const labels = [
      ...(overlay()?.querySelectorAll(".mermaid-overlay-actions > button") ?? []),
    ].map((b) => b.getAttribute("aria-label"));
    expect(labels).toEqual(["Zoom out", "Zoom in", "Reset", "Copy", "Download SVG", "Close"]);
  });
});
