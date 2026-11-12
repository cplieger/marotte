// The one inert SVG render path: a diagram appears through an ELEMENT given a `src`, never through
// markup, so no renderer output passes through an HTML parser.
import { describe, it, expect, vi, beforeEach, afterEach, beforeAll, afterAll } from "vitest";
import fc from "fast-check";
import { renderSvgBlock } from "./svg-block.js";
import { createMarkdownStream, renderMarkdownInto } from "./markdown.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

const SVG = '<svg viewBox="0 0 10 10"><rect width="10" height="10"/></svg>';

describe("renderSvgBlock", () => {
  it("gives a figure holding an image", () => {
    const figure = renderSvgBlock(SVG);
    expect(figure?.tagName).toBe("FIGURE");
    expect(figure?.querySelector(":scope > img")).not.toBeNull();
  });

  it("encodes the source as a data: URL on the image's src", () => {
    const img = renderSvgBlock(SVG)?.querySelector("img");
    expect(img?.getAttribute("src")).toBe(`data:image/svg+xml,${encodeURIComponent(SVG)}`);
  });

  // THE invariant. The source text reaches the document only as a percent-encoded
  // URL, so no `<svg>`, `<script>` or `<rect>` node is ever parsed into the page.
  it("parses no markup: the SVG becomes no element of its own", () => {
    const figure = renderSvgBlock('<svg onload="alert(1)"><script>alert(2)</script></svg>');
    expect(figure).not.toBeNull();
    expect(figure?.querySelector("svg")).toBeNull();
    expect(figure?.querySelector("script")).toBeNull();
    expect(figure?.querySelectorAll("*")).toHaveLength(1); // the <img>, and only it
    expect(figure?.querySelector("img")?.getAttribute("onload")).toBeNull();
  });

  // A `data:` URL, not a blob URL: CSP is `img-src 'self' data:` and `blob:` is
  // absent, so createObjectURL would be blocked outright.
  it("uses a data: URL rather than a blob: URL", () => {
    const src = renderSvgBlock(SVG)?.querySelector("img")?.getAttribute("src") ?? "";
    expect(src.startsWith("data:image/svg+xml,")).toBe(true);
    expect(src.startsWith("blob:")).toBe(false);
  });

  it("carries alt text so the figure is not an empty announcement", () => {
    expect(renderSvgBlock(SVG)?.querySelector("img")?.getAttribute("alt")).not.toBe("");
  });

  // A diagram that cannot be built is better read as its source.
  it("declines an empty source", () => {
    expect(renderSvgBlock("   \n  ")).toBeNull();
  });

  it("degrades to a named message when the image fails to load", () => {
    const figure = renderSvgBlock(SVG);
    const img = figure?.querySelector("img");
    img?.dispatchEvent(new Event("error"));
    expect(figure?.querySelector("img")).toBeNull();
    expect(figure?.querySelector(".img-missing")?.textContent).toContain("could not be rendered");
  });
});

describe("on the shipped stylesheet", () => {
  let style: HTMLStyleElement;
  let column: HTMLElement;

  beforeAll(() => {
    style = mountAppCSS();
  });

  afterAll(() => {
    style.remove();
  });

  beforeEach(() => {
    column = document.createElement("div");
    column.className = "message assistant";
    document.body.appendChild(column);
  });

  afterEach(() => {
    column.remove();
    document.documentElement.removeAttribute("data-theme");
  });

  /** WCAG relative luminance of a computed colour, read back as the painted sRGB pixel. */
  function luminance(colour: string): number {
    const c = document.createElement("canvas");
    c.width = 1;
    c.height = 1;
    const ctx = c.getContext("2d");
    if (ctx === null) {
      throw new Error("no 2d context");
    }
    ctx.fillStyle = colour;
    ctx.fillRect(0, 0, 1, 1);
    const [r = 0, g = 0, b = 0] = [...ctx.getImageData(0, 0, 1, 1).data.slice(0, 3)].map((n) => {
      const v = n / 255;
      return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
    });
    return 0.2126 * r + 0.7152 * g + 0.0722 * b;
  }

  // An agent's SVG drawn in black ink, the browser's default for an <img>'d SVG, must read on
  // the diagram's backdrop whatever the app theme.
  it.each(["dark", "light"])("backs a diagram with a surface black ink reads on (%s)", (theme) => {
    document.documentElement.setAttribute("data-theme", theme);
    renderMarkdownInto(column, "```svg\n" + SVG + "\n```\n");
    const img = column.querySelector("figure > img");
    expect(img).not.toBeNull();
    const contrast = (luminance(getComputedStyle(img as Element).backgroundColor) + 0.05) / 0.05;
    expect(contrast).toBeGreaterThanOrEqual(4.5);
  });
});

describe("through the markdown renderer", () => {
  function render(md: string): HTMLElement {
    const host = document.createElement("div");
    renderMarkdownInto(host, md);
    return host;
  }

  it("renders a closed svg fence as a diagram", () => {
    const host = render("```svg\n" + SVG + "\n```\n");
    expect(host.querySelector("figure > img")).not.toBeNull();
    expect(host.querySelector("pre")).toBeNull();
  });

  // A converted diagram is no longer a code block, so it must not collect the
  // language label, Copy button or highlighting for source nobody can see.
  it("collects no code-block chrome", () => {
    const host = render("```svg\n" + SVG + "\n```\n");
    expect(host.querySelector(".cp-code")).toBeNull();
    expect(host.querySelector('[data-part="lang"]')).toBeNull();
    expect(host.textContent).not.toContain("<rect");
  });

  it("still renders every other fence as a code block", () => {
    for (const lang of ["go", "ts", "bash", "", "xml", "html"]) {
      const host = render("```" + lang + "\n" + SVG + "\n```\n");
      expect(host.querySelector(".cp-code > pre"), lang).not.toBeNull();
      expect(host.querySelector("figure"), lang).toBeNull();
    }
  });

  // Mermaid source is not SVG: mermaid-block.ts draws it, never this inert path.
  it("does not claim a mermaid fence", () => {
    const host = render("```mermaid\ngraph TD; A-->B;\n```\n");
    expect(host.querySelector("figure.svg-figure")).toBeNull();
  });

  it("leaves an empty svg fence as source", () => {
    const host = render("```svg\n   \n```\n");
    expect(host.querySelector("figure")).toBeNull();
    expect(host.querySelector(".cp-code > pre")).not.toBeNull();
  });

  // Inline `<svg>` in prose is TEXT and stays text. There is no HTML token in the parser.
  it("leaves inline svg markup in prose as literal text", () => {
    const host = render("here is <svg><script>alert(1)</script></svg> inline\n");
    expect(host.querySelector("svg")).toBeNull();
    expect(host.querySelector("script")).toBeNull();
    expect(host.querySelector("img")).toBeNull();
    expect(host.textContent).toContain("<svg>");
  });

  // The library's gate refuses `data:`, so the diagram's src must be set outside it.
  it("keeps the data: src the gate would refuse", () => {
    const host = render("```svg\n" + SVG + "\n```\n");
    expect(host.querySelector("img")?.getAttribute("src")).toMatch(/^data:image\/svg\+xml,/);
  });

  it("is safe against an unterminated fence: source, not a broken diagram", () => {
    const host = render("```svg\n" + SVG);
    expect(host.querySelector("figure")).toBeNull();
    expect(host.querySelector(".cp-code > pre")).not.toBeNull();
  });
});

describe("svg source never reaches an HTML parser sink", () => {
  it("renders a diagram without assigning innerHTML or calling insertAdjacentHTML", async () => {
    const proto = Element.prototype as unknown as Record<string, unknown>;
    const insert = vi.spyOn(Element.prototype, "insertAdjacentHTML");
    const descriptor = Object.getOwnPropertyDescriptor(proto, "innerHTML");
    const setter = vi.fn();
    Object.defineProperty(proto, "innerHTML", {
      configurable: true,
      get: () => "",
      set: setter,
    });
    const host = document.createElement("div");
    try {
      renderMarkdownInto(host, "```svg\n" + SVG + "\n```\n");
    } finally {
      if (descriptor !== undefined) {
        Object.defineProperty(proto, "innerHTML", descriptor);
      }
      insert.mockRestore();
    }
    expect(host.querySelector("figure > img")).not.toBeNull();
    expect(setter).not.toHaveBeenCalled();
    expect(insert).not.toHaveBeenCalled();
    await Promise.resolve();
  });
});

// The live path: the library builds the code block when the fence opens, so the diagram replaces
// a block that was on screen.
describe("a fence that streams in over several deltas", () => {
  let host: HTMLElement;

  beforeEach(() => {
    host = document.createElement("div");
    document.body.append(host);
  });

  afterEach(() => {
    host.remove();
  });

  it("shows the open fence as a code block until it closes", () => {
    const r = createMarkdownStream(host, { flushIntervalMs: 0 });
    r.write("```svg\n" + SVG.slice(0, 20));
    expect(host.querySelector(".cp-code")).not.toBeNull();
    expect(host.querySelector("figure")).toBeNull();
    r.end();
  });

  it("leaves no code-block chrome around the converted diagram", () => {
    const r = createMarkdownStream(host, { flushIntervalMs: 0 });
    r.write("```svg\n" + SVG.slice(0, 20));
    r.write(SVG.slice(20) + "\n```\n");
    r.end();

    expect(host.querySelectorAll("figure")).toHaveLength(1);
    expect(host.querySelector("figure > img")).not.toBeNull();
    expect(host.querySelector(".cp-code")).toBeNull();
    expect(host.querySelector("pre")).toBeNull();
  });

  it("keeps the code block on a streamed ordinary fence", () => {
    const r = createMarkdownStream(host, { flushIntervalMs: 0 });
    r.write("```go\npackage ");
    r.write("main\n```\n");
    r.end();
    expect(host.querySelector(".cp-code > pre")).not.toBeNull();
    // @cplieger/ui's markdown docs, "The code block": the label is the language's name.
    expect(host.querySelector('[data-part="lang"]')?.textContent).toBe("Go");
    expect(host.querySelector("figure")).toBeNull();
  });
});

// Property: no node from arbitrary source ever reaches the document; `<img>` needs no sanitizer.
describe("property: arbitrary svg source never becomes a node", () => {
  it("emits exactly one <img> and no element the source named", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(
        fc.stringMatching(/^[^`]*$/).filter((s) => s.trim() !== ""),
        (source) => {
          const host = document.createElement("div");
          renderMarkdownInto(host, "```svg\n" + source + "\n```\n");
          // Either it converted (one figure holding one img) or it declined and
          // left source behind. Both are safe; what must never happen is a node
          // parsed out of the source.
          const figure = host.querySelector("figure");
          if (figure === null) {
            return host.querySelector("script") === null && host.querySelector("svg") === null;
          }
          const inside = [...figure.querySelectorAll("*")];
          return (
            inside.length === 1 &&
            inside[0]?.tagName === "IMG" &&
            host.querySelector("script") === null &&
            host.querySelector("svg") === null &&
            host.querySelector("iframe") === null &&
            host.querySelector("[onload]") === null &&
            host.querySelector("[onerror]") === null
          );
        },
      ),
      { numRuns: 300 },
    );
    expect(result.failed).toBe(false);
  });

  // The source survives round-trip through the URL, so a rendered diagram is the
  // diagram the model wrote and not a filtered version of it.
  it("carries the source verbatim in the data: URL", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(
        fc.stringMatching(/^[^`\n]*$/).filter((s) => s.trim() !== ""),
        (source) => {
          const host = document.createElement("div");
          renderMarkdownInto(host, "```svg\n" + source + "\n```\n");
          const src = host.querySelector("img")?.getAttribute("src");
          if (src === undefined || src === null) {
            return true; // declined; covered by the property above
          }
          const decoded = decodeURIComponent(src.slice("data:image/svg+xml,".length));
          return decoded.trimEnd() === source.trimEnd();
        },
      ),
      { numRuns: 300 },
    );
    expect(result.failed).toBe(false);
  });
});
