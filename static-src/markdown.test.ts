// marotte's hooks over @cplieger/ui/markdown: what a chat answer, a document view and a live stream add to the
// library's rendering. The library's parser, gate and element vocabulary are its own tests'.

import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import * as fc from "fast-check";
import { payloadShaped } from "@cplieger/ui/markdown";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { settingsPayload } from "./__test-helpers__/settings.js";
import { adoptLinkGuard, setLinkCopyCallback } from "./link-guard.js";
import { initLinkifyCallbacks } from "./linkify.js";
import { createMarkdownStream, releaseMarkdownIn, renderMarkdownInto } from "./markdown.js";
import { _resetForTest as resetWorkspace, setWorkspaceRoot } from "./workspace.js";

const MATHML_NS = "http://www.w3.org/1998/Math/MathML";

const hosts: HTMLElement[] = [];

/** A host in the page, removed and released after each test. */
function mount(className = ""): HTMLElement {
  const host = document.createElement("div");
  host.className = className;
  document.body.append(host);
  hosts.push(host);
  return host;
}

afterEach(() => {
  for (const h of hosts.splice(0)) {
    releaseMarkdownIn(h);
    h.remove();
  }
});

function rendered(md: string): HTMLElement {
  const host = mount();
  renderMarkdownInto(host, md);
  return host;
}

/** A stream that parses each write as it lands, the reveal's pacing. */
function live(host: HTMLElement = mount()): {
  host: HTMLElement;
  stream: ReturnType<typeof createMarkdownStream>;
} {
  return { host, stream: createMarkdownStream(host, { flushIntervalMs: 0 }) };
}

describe("a rendered host", () => {
  it("takes the library's prose classes, replayed or streamed", () => {
    const replayed = rendered("text");
    const { host } = live();
    for (const h of [replayed, host]) {
      expect(h.classList.contains("cp")).toBe(true);
      expect(h.classList.contains("cp-prose")).toBe(true);
    }
  });

  // `#` sits under the page's own title, so a chat answer's top heading is an h2; the spec page's node
  // detail sits under its own h2.
  it("renders `#` as h2, or at the level asked for", () => {
    expect(rendered("# Title\n\n## Sub\n").querySelector("h2")?.textContent).toBe("Title");
    expect(rendered("# Title\n\n## Sub\n").querySelector("h3")?.textContent).toBe("Sub");
    const spec = mount();
    renderMarkdownInto(spec, "# Title\n", { headingLevel: 3 });
    expect(spec.querySelector("h3")?.textContent).toBe("Title");
    expect(spec.querySelector("h1, h2")).toBeNull();
  });

  it("drains a stream larger than one parse slice synchronously at end()", () => {
    const { host, stream } = live(mount());
    const big = "a".repeat(10_000);
    stream.write(big);
    stream.end();
    expect(host.textContent).toBe(big);
  });

  // @cplieger/ui's markdown docs, "Options": the library reads 1 MiB by default; a whole file's
  // view shows all the server sent, since the server holds a file over the viewer's cap back as
  // `large`.
  it("cuts a chat answer at 1 MiB and renders a whole file uncut", () => {
    const text = `${"word ".repeat(300_000)}end\n`;
    const chat = rendered(text);
    expect(chat.querySelector("[data-cp-truncated]"), "chat answer").not.toBeNull();
    const file = mount();
    renderMarkdownInto(file, text, { wholeFile: true });
    expect(file.querySelector("[data-cp-truncated]"), "whole file").toBeNull();
    expect(file.textContent.endsWith("end")).toBe(true);
  });

  // The CSP is `img-src 'self' data:`: another origin's image could never load, so it is a link.
  it("renders another origin's image as a link to it, and a same-origin one as an image", () => {
    const remote = rendered("![chart](https://e.example/chart.png)");
    expect(remote.querySelector("img")).toBeNull();
    expect(remote.querySelector("a")?.getAttribute("href")).toBe("https://e.example/chart.png");
    const local = rendered("![logo](/static/logo.png)");
    expect(local.querySelector("img")?.getAttribute("src")).toBe("/static/logo.png");
  });
});

describe("a code block while it streams", () => {
  it("is on screen from the opening fence with its label, not yet final", () => {
    const { host, stream } = live();
    stream.write("```go\nfunc main() {\n");
    const block = host.querySelector(".cp-code");
    expect(block).not.toBeNull();
    expect(block?.hasAttribute("data-code-state")).toBe(false);
    // @cplieger/ui's markdown docs, "The code block": the label is the language's name.
    expect(block?.querySelector('[data-part="lang"]')?.textContent).toBe("Go");
  });

  it("turns final in place once the fence closes", () => {
    const { host, stream } = live();
    stream.write("```go\nfunc main() {\n");
    const block = host.querySelector(".cp-code");
    stream.write("}\n```\n");
    expect(host.querySelectorAll(".cp-code")).toHaveLength(1);
    expect(host.querySelector(".cp-code")).toBe(block);
    expect(block?.getAttribute("data-code-state")).toBe("final");
  });

  it("turns final at end() when the answer never closed it", () => {
    const { host, stream } = live();
    stream.write("```go\nfunc main() {\n");
    stream.end();
    expect(host.querySelector(".cp-code")?.getAttribute("data-code-state")).toBe("final");
  });

  it("is final on the replay path too, closed or not", () => {
    expect(
      rendered("```bash\necho hi").querySelector(".cp-code")?.getAttribute("data-code-state"),
    ).toBe("final");
  });
});

// A block that streamed in is on screen already; fading it in again when it completes would blink it.
describe("the per-block fade", () => {
  let style: HTMLStyleElement;

  beforeAll(() => {
    style = mountAppCSS();
  });

  afterAll(() => {
    style.remove();
  });

  function fades(block: Element | null): boolean {
    return (block?.getAnimations() ?? []).some(
      (a) => a instanceof CSSAnimation && a.animationName === "vk-fade-in",
    );
  }

  it("plays on a block that appears complete in one write", () => {
    const { host, stream } = live(mount("message assistant streaming"));
    stream.write("Here:\n\n```js\nlet a = 1;\n```\n\nafter");
    const block = host.querySelector(".cp-code");
    expect(block?.hasAttribute("data-vk-block-enter")).toBe(true);
    expect(fades(block)).toBe(true);
  });

  it("does not play again on a block that streamed in and then completed", () => {
    const { host, stream } = live(mount("message assistant streaming"));
    stream.write("Here:\n\n```js\nlet a");
    const block = host.querySelector(".cp-code");
    stream.write(" = 1;\n```\n\nafter");
    expect(block?.getAttribute("data-code-state"), "the block completed").toBe("final");
    expect(block?.hasAttribute("data-vk-block-enter")).toBe(false);
    expect(fades(block)).toBe(false);
  });

  it("does not play again on a table whose rows streamed in", () => {
    const { host, stream } = live(mount("message assistant streaming"));
    stream.write("| a | b |\n|---|---|\n| 1 | 2 |\n");
    const region = host.querySelector(".cp-table-scroll");
    stream.write("| 3 | 4 |\n\nafter");
    expect(region?.isConnected).toBe(true);
    expect(region?.hasAttribute("data-vk-block-enter")).toBe(false);
  });

  it("never plays on a replayed answer", () => {
    const host = mount("message assistant");
    renderMarkdownInto(host, "```js\nlet a = 1;\n```\n\n> quoted\n");
    expect(host.querySelector("[data-vk-block-enter]")).toBeNull();
  });
});

describe("the streaming caret", () => {
  it("marks the element receiving text", () => {
    const { host, stream } = live();
    stream.write("hello");
    expect(host.querySelector("p")?.hasAttribute("data-vk-caret")).toBe(true);
  });

  // The library holds the last code point of a write until the next one, so each case writes past what it checks.
  it("moves with the insertion point, keeping exactly one caret", () => {
    const { host, stream } = live();
    stream.write("first\n\nsecond line");
    const marked = host.querySelectorAll("[data-vk-caret]");
    expect(marked).toHaveLength(1);
    expect(marked[0]?.textContent).toContain("second");
    expect(marked[0]?.textContent).not.toContain("first");
  });

  // Mid-inline is why the caret cannot be a :last-child rule: during a bold run the text goes into the strong.
  it("follows into an inline element mid-paragraph", () => {
    const { host, stream } = live();
    stream.write("plain **bold");
    const marked = host.querySelectorAll("[data-vk-caret]");
    expect(marked).toHaveLength(1);
    expect(marked[0]?.tagName).toBe("STRONG");
  });

  it("moves off an inline element the text never closed", () => {
    const { host, stream } = live();
    stream.write("**b");
    // A rule carries no text, so nothing else moves the caret once the paragraph closes.
    stream.write("\n\n---\n\n");
    expect(host.querySelector("hr")).not.toBeNull();
    expect(host.querySelector("strong")).toBeNull();
    const marked = host.querySelectorAll("[data-vk-caret]");
    expect(marked).toHaveLength(1);
    expect(marked[0]?.tagName).toBe("P");
    expect(marked[0]?.textContent).toBe("**b");
  });

  it("is gone once the stream ends", () => {
    const { host, stream } = live();
    stream.write("done");
    stream.end();
    expect(host.querySelector("[data-vk-caret]")).toBeNull();
  });

  it("is never written on the replay path", () => {
    expect(rendered("history").querySelector("[data-vk-caret]")).toBeNull();
  });
});

describe("the per-chunk fade spans", () => {
  it("wrap streamed prose", () => {
    const { host, stream } = live();
    stream.write("one ");
    stream.write("two three");
    const spans = [...host.querySelectorAll("p > [data-vk-chunk-enter]")];
    expect(spans.length).toBeGreaterThan(0);
    expect(spans.map((s) => s.textContent).join("")).toContain("one two");
  });

  // A span inside `code` would split the text the highlighter and the math converter read.
  it("never wrap text inside code", () => {
    const { host, stream } = live();
    stream.write("see `inline` and\n\n```go\npackage ");
    stream.write("main\n```\n");
    expect(host.querySelector("code [data-vk-chunk-enter]")).toBeNull();
    expect(host.querySelector("pre > code")?.textContent).toBe("package main");
  });

  it("are not written on the replay path", () => {
    expect(rendered("history").querySelector("[data-vk-chunk-enter]")).toBeNull();
  });
});

// The per-chunk fade animates each `<span data-vk-chunk-enter>` once on mount; re-inserting a node restarts
// its animation (measured in Chromium), so every move of streamed text marks the spans it moves settled.
describe("moving streamed text does not replay its fade", () => {
  const ANIM = "chunk-enter-probe";
  let style: HTMLStyleElement;

  beforeAll(() => {
    style = document.createElement("style");
    style.textContent = `
      @keyframes ${ANIM} { from { opacity: 0 } to { opacity: 1 } }
      .${ANIM}-scope [data-vk-chunk-enter] { animation: ${ANIM} 5s linear backwards }
      .${ANIM}-scope [data-vk-chunk-settled] { animation: none }
    `;
    document.head.append(style);
  });

  afterAll(() => {
    style.remove();
  });

  afterEach(() => {
    resetWorkspace();
  });

  function probe(): { host: HTMLElement; started: EventTarget[] } {
    const host = mount(`${ANIM}-scope`);
    const started: EventTarget[] = [];
    host.addEventListener("animationstart", (e) => {
      started.push(e.target as EventTarget);
    });
    return { host, started };
  }

  function nextFrame(): Promise<void> {
    return new Promise((resolve) => {
      requestAnimationFrame(() => {
        requestAnimationFrame(() => {
          resolve();
        });
      });
    });
  }

  async function expectMovedOnce(
    host: HTMLElement,
    started: EventTarget[],
    before: Element[],
    within: string,
  ): Promise<void> {
    await nextFrame();
    const moved = [...host.querySelectorAll(`${within} [data-vk-chunk-enter]`)];
    // Identity: the spans MOVE rather than being recreated.
    expect(moved.filter((s) => before.includes(s))).toEqual(before);
    expect(before.every((s) => s.hasAttribute("data-vk-chunk-settled"))).toBe(true);
    for (const span of before) {
      expect(started.filter((t) => t === span)).toHaveLength(1);
    }
  }

  it("when an unclosed strong gives its text back", async () => {
    const { host, started } = probe();
    const { stream } = live(host);
    stream.write("**one ");
    stream.write("two ");
    stream.write("three");
    const before = [...host.querySelectorAll("strong [data-vk-chunk-enter]")];
    expect(before.length).toBeGreaterThan(0);
    // The premise, polled: without the starts on the board there is nothing to re-fire.
    await vi.waitFor(() => {
      expect(started.length).toBeGreaterThanOrEqual(before.length);
    });
    stream.end();
    expect(host.querySelector("strong")).toBeNull();
    expect(host.textContent).toBe("**one two three");
    await expectMovedOnce(host, started, before, "p");
  });

  it("when a label with no destination is unwrapped", async () => {
    const { host, started } = probe();
    const { stream } = live(host);
    stream.write("see [the ");
    stream.write("label");
    const before = [...host.querySelectorAll("a [data-vk-chunk-enter]")];
    expect(before.length).toBeGreaterThan(0);
    await vi.waitFor(() => {
      expect(started.length).toBeGreaterThanOrEqual(before.length);
    });
    stream.write("] done");
    stream.end();
    expect(host.querySelector("a")).toBeNull();
    expect(host.textContent).toBe("see [the label] done");
    await expectMovedOnce(host, started, before, "p");
  });

  it("when a later root change rebuilds a streamed file link", async () => {
    setWorkspaceRoot("/workspace");
    const { host, started } = probe();
    const { stream } = live(host);
    stream.write("see [the ");
    stream.write("notes](/srv/proj/docs/notes.md) done");
    stream.end();
    const link = host.querySelector("a");
    const before = [...host.querySelectorAll("a [data-vk-chunk-enter]")];
    expect(before.length).toBeGreaterThan(0);
    await vi.waitFor(() => {
      expect(started.length).toBeGreaterThanOrEqual(before.length);
    });

    setWorkspaceRoot("/srv/proj");
    await nextFrame();

    const rebuilt = host.querySelector("a");
    // The premise: the new root routed the link, so its label really moved into a fresh anchor.
    expect(rebuilt).not.toBe(link);
    expect(rebuilt?.getAttribute("href")).toBe("/file//srv/proj/docs/notes.md");
    await expectMovedOnce(host, started, before, "a");
  });
});

describe("math", () => {
  const cases: { name: string; md: string; display: boolean }[] = [
    { name: "$…$ inline", md: "the value $x^2$ here", display: false },
    { name: "\\(…\\) inline", md: "the value \\(x^2\\) here", display: false },
    { name: "$$…$$ block", md: "$$\nx^2\n$$\n", display: true },
    { name: "\\[…\\] block", md: "\\[\nx^2\n\\]\n", display: true },
  ];

  for (const tc of cases) {
    it(`renders ${tc.name} as native MathML`, () => {
      const host = rendered(tc.md);
      const math = host.querySelector("math");
      expect(math?.namespaceURI).toBe(MATHML_NS);
      expect(math?.hasAttribute("display")).toBe(tc.display);
      expect(math?.parentElement?.getAttribute("data-math")).toBe(tc.display ? "block" : "inline");
    });
  }

  it("survives a delimiter split across writes", () => {
    const { host, stream } = live();
    stream.write("area $");
    stream.write("\\pi r^2");
    stream.write("$ done");
    stream.end();
    const math = host.querySelector("math");
    expect(math?.namespaceURI).toBe(MATHML_NS);
    expect(math?.textContent).toBe("\u03c0r2");
  });

  it("leaves an expression the converter does not know as its TeX", () => {
    const host = rendered("a matrix $\\begin{pmatrix} a & b \\end{pmatrix}$ inline");
    expect(host.querySelector("math")).toBeNull();
    const raw = host.querySelector("code[data-math]");
    expect(raw?.textContent).toBe("\\begin{pmatrix} a & b \\end{pmatrix}");
  });
});

// A markdown link to a served file opens it in a marotte tab, not a browser tab.
describe("links to served files", () => {
  // Re-injected per test, as a plain closure: `mockReset: true` would reset a vi.fn()
  // implementation between tests.
  const opened: [string, number | undefined][] = [];
  // Whether the link's own handler cancelled each click, read by an ancestor listener that then
  // cancels it anyway so the test page never navigates.
  const taken: boolean[] = [];

  beforeEach(() => {
    opened.length = 0;
    taken.length = 0;
    initLinkifyCallbacks({
      open: (path, line) => {
        opened.push([path, line]);
      },
    });
  });

  function linkHost(): HTMLElement {
    const host = mount();
    host.addEventListener("click", (e) => {
      taken.push(e.defaultPrevented);
      e.preventDefault();
    });
    return host;
  }

  function render(md: string): HTMLAnchorElement {
    const host = linkHost();
    renderMarkdownInto(host, md);
    const link = host.querySelector("a");
    expect(link, `no anchor rendered for ${md}`).not.toBeNull();
    return link as HTMLAnchorElement;
  }

  function click(link: HTMLAnchorElement, init: MouseEventInit = {}): void {
    link.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, ...init }));
  }

  it("points a workspace link at the editor route", () => {
    const link = render("[spec](/workspace/docs/spec.md)");
    expect(link.getAttribute("href")).toBe("/file//workspace/docs/spec.md");
  });

  it("drops the new-tab target and rel from a workspace link", () => {
    const link = render("[spec](/workspace/docs/spec.md)");
    expect(link.hasAttribute("target")).toBe(false);
    expect(link.hasAttribute("rel")).toBe(false);
  });

  it("points an uploads link at the editor route", () => {
    const link = render("[notes](/uploads/notes.txt)");
    expect(link.getAttribute("href")).toBe("/file//uploads/notes.txt");
  });

  it("carries a #L line into the editor route", () => {
    const link = render("[app](/workspace/src/app.ts#L42)");
    expect(link.getAttribute("href")).toBe("/file//workspace/src/app.ts#L42");
  });

  it("opens the file through the injected opener on a plain click", () => {
    const link = render("[spec](/workspace/docs/spec.md)");
    click(link);
    expect(opened).toEqual([["/workspace/docs/spec.md", undefined]]);
    expect(taken).toEqual([true]);
  });

  it("opens the file at its #L line on a plain click", () => {
    const link = render("[app](/workspace/src/app.ts#L42)");
    click(link);
    expect(opened).toEqual([["/workspace/src/app.ts", 42]]);
  });

  it("opens a linked image through the same opener", () => {
    const link = render("[shot](/workspace/out/shot.png)");
    click(link);
    expect(opened).toEqual([["/workspace/out/shot.png", undefined]]);
  });

  it.each([
    ["ctrlKey", { ctrlKey: true }],
    ["metaKey", { metaKey: true }],
    ["shiftKey", { shiftKey: true }],
    ["altKey", { altKey: true }],
    ["a middle button", { button: 1 }],
  ] as const)("leaves a click with %s to the browser", (_label, init) => {
    const link = render("[spec](/workspace/docs/spec.md)");
    // The premise: this is a file link, so only the modifier keeps it unhandled.
    expect(link.getAttribute("href")).toBe("/file//workspace/docs/spec.md");
    click(link, init);
    expect(opened).toEqual([]);
    expect(taken).toEqual([false]);
  });

  // @cplieger/ui's markdown docs, "Safety": every http(s) link opens a new tab, with
  // rel="noopener noreferrer nofollow".
  it("keeps an external link in a new browser tab", () => {
    const link = render("[site](https://example.com/a)");
    expect(link.getAttribute("href")).toBe("https://example.com/a");
    expect(link.getAttribute("target")).toBe("_blank");
    expect(link.getAttribute("rel")).toBe("noopener noreferrer nofollow");
    click(link);
    expect(opened).toEqual([]);
    expect(taken).toEqual([false]);
  });

  it("leaves a path outside every root to the browser", () => {
    const link = render("[x](/config/mcp.json)");
    expect(link.getAttribute("href")).toBe("/config/mcp.json");
    expect(link.getAttribute("target")).toBe("_blank");
  });

  // @cplieger/ui's markdown docs, "Safety": a relative destination with a `.` or `..` segment is
  // refused, so the link's words are plain text and nothing climbs out of a root.
  it.each(["/workspace/../config/mcp.json", "/uploads/./../config/x.json", "/workspace/./a.md"])(
    "gives %s no destination at all",
    (dest) => {
      const link = render(`[x](${dest})`);
      expect(link.hasAttribute("href")).toBe(false);
      click(link);
      expect(opened).toEqual([]);
    },
  );

  // A trailing slash names a directory, which the editor cannot open.
  it.each(["/workspace/marotte/", "/uploads/", "/workspace/marotte/?x=1"])(
    "leaves %s to the browser",
    (dest) => {
      const link = render(`[x](${dest})`);
      expect(link.getAttribute("href")).toBe(dest);
      expect(link.getAttribute("target")).toBe("_blank");
    },
  );

  it.each([
    ["/workspace/a.md?download=1", "/file//workspace/a.md", undefined],
    ["/workspace/a.md?v=1#L4", "/file//workspace/a.md#L4", 4],
  ] as const)("drops the query from %s and opens the file", (dest, href, line) => {
    const link = render(`[a](${dest})`);
    expect(link.getAttribute("href")).toBe(href);
    click(link);
    expect(opened).toEqual([["/workspace/a.md", line]]);
  });

  // The editor route has no heading anchors, so the file at its top is the nearest destination; the
  // plain link would land on a chat.
  it.each(["#install", "#L0", "#L12tail", "#L9007199254740992"])(
    "opens the file at its top for the fragment %s",
    (fragment) => {
      const link = render(`[a](/workspace/a.md${fragment})`);
      expect(link.getAttribute("href")).toBe("/file//workspace/a.md");
      click(link);
      expect(opened).toEqual([["/workspace/a.md", undefined]]);
    },
  );

  it("carries the largest exact line number", () => {
    const link = render("[a](/workspace/a.md#L9007199254740991)");
    expect(link.getAttribute("href")).toBe("/file//workspace/a.md#L9007199254740991");
  });

  it.each([":42", ":42:7"])("reads the %s suffix as line 42", (suffix) => {
    const link = render(`[c](/workspace/src/a.ts${suffix})`);
    expect(link.getAttribute("href")).toBe("/file//workspace/src/a.ts#L42");
    click(link);
    expect(opened).toEqual([["/workspace/src/a.ts", 42]]);
  });

  it("decodes a percent-escaped destination exactly once", () => {
    const link = render("[notes](/workspace/my%20notes.md)");
    expect(link.getAttribute("href")).toBe("/file//workspace/my%20notes.md");
    click(link);
    expect(opened).toEqual([["/workspace/my notes.md", undefined]]);
  });

  it("keeps a bare percent sign as part of the filename", () => {
    const link = render("[p](/workspace/100%.md)");
    expect(link.getAttribute("href")).toBe("/file//workspace/100%25.md");
    click(link);
    expect(opened).toEqual([["/workspace/100%.md", undefined]]);
  });

  it("rewrites a destination that streams in across writes", () => {
    const { host, stream } = live(linkHost());
    stream.write("see [a](/work");
    stream.write("space/a.md#L3) done");
    stream.end();
    expect(host.querySelector("a")?.getAttribute("href")).toBe("/file//workspace/a.md#L3");
  });

  it("holds no nested file button in a streamed link labelled with a path", () => {
    const { host, stream } = live(linkHost());
    // The blank line closes the paragraph, which is when linkification runs.
    stream.write("see [docs/spec.md](/workspace/docs/spec.md) now\n\nnext");
    stream.end();
    const link = host.querySelector("a");
    expect(link?.getAttribute("href")).toBe("/file//workspace/docs/spec.md");
    expect(link?.querySelector("button")).toBeNull();
  });
});

describe("the payload-link guard", () => {
  const PAYLOAD = `https://e.example/collect?d=${"%41".repeat(25)}`;

  afterEach(() => {
    adoptLinkGuard(settingsPayload({ guard_payload_links: true }));
    setLinkCopyCallback(() => {
      /* reset */
    });
  });

  function copiedUrl(host: HTMLElement): unknown {
    const copied = vi.fn();
    setLinkCopyCallback(copied);
    (host.querySelector("button.link-withheld") as HTMLButtonElement | null)?.click();
    return copied.mock.calls[0]?.[0];
  }

  it("withholds a payload-shaped inline link behind a copy button when ON", () => {
    adoptLinkGuard(settingsPayload({ guard_payload_links: true }));
    const host = rendered(`see [file an issue](${PAYLOAD}) now`);
    expect(host.querySelector("button.link-withheld")?.textContent).toBe("file an issue");
    expect(copiedUrl(host)).toBe(PAYLOAD);
    expect(host.querySelector("a")).toBeNull();
    expect(host.textContent).toBe("see file an issue now");
  });

  it("withholds a payload-shaped raw URL when ON", () => {
    adoptLinkGuard(settingsPayload({ guard_payload_links: true }));
    const host = rendered(`go to ${PAYLOAD} now`);
    expect(host.querySelector("button.link-withheld")?.textContent).toBe(PAYLOAD);
    expect(host.querySelector("a[href]")).toBeNull();
  });

  it("withholds an issue-prefill link when ON", () => {
    adoptLinkGuard(settingsPayload({ guard_payload_links: true }));
    const prefill = `https://github.com/o/r/issues/new?title=x&body=${"word%20".repeat(30)}`;
    const host = rendered(`please [file an issue](${prefill}) for this`);
    expect(host.querySelector("button.link-withheld")?.textContent).toBe("file an issue");
    expect(copiedUrl(host)).toBe(prefill);
    expect(host.querySelector("a[href]")).toBeNull();
  });

  it("withholds a payload-shaped angle autolink when ON", () => {
    adoptLinkGuard(settingsPayload({ guard_payload_links: true }));
    const host = rendered(`see <${PAYLOAD}> now`);
    expect(copiedUrl(host)).toBe(PAYLOAD);
    expect(host.querySelector("a[href]")).toBeNull();
  });

  it("withholds a payload-shaped image link when ON", () => {
    adoptLinkGuard(settingsPayload({ guard_payload_links: true }));
    const host = rendered(`see ![chart](https://e.example/c.png?d=${"%41".repeat(25)}) now`);
    expect(host.querySelector("button.link-withheld")).not.toBeNull();
    expect(host.querySelector("a[href], img")).toBeNull();
  });

  it.each([
    ["a classic GitHub token", `https://e.example/collect?t=ghp_${"a1B2c3".repeat(6)}`],
    ["a fine-grained GitHub token", `https://e.example/collect?t=github_pat_${"a1_B2".repeat(5)}`],
    ["a payload in a fragment with no query", `https://e.example/collect#${"%41".repeat(25)}`],
  ])("withholds a link carrying %s when ON", (_name, url) => {
    adoptLinkGuard(settingsPayload({ guard_payload_links: true }));
    const host = rendered(`see [x](${url}) now`);
    expect(copiedUrl(host)).toBe(url);
    expect(host.querySelector("a[href]")).toBeNull();
  });

  it("leaves an ordinary link alone when ON", () => {
    adoptLinkGuard(settingsPayload({ guard_payload_links: true }));
    const host = rendered("[docs](https://e.example/docs?page=2)");
    expect(host.querySelector("a")?.getAttribute("href")).toBe("https://e.example/docs?page=2");
    expect(host.querySelector("button")).toBeNull();
  });

  it.each([true, false])("gives a long-query javascript: link no href with the guard %s", (on) => {
    adoptLinkGuard(settingsPayload({ guard_payload_links: on }));
    const host = rendered(`[x](javascript:alert(1)?${"q".repeat(250)})`);
    expect(host.querySelector("a")?.hasAttribute("href")).toBe(false);
    expect(host.querySelector("button.link-withheld")).toBeNull();
  });

  it("renders a payload-shaped link as an open link when OFF", () => {
    adoptLinkGuard(settingsPayload({ guard_payload_links: false }));
    const host = rendered(`[file an issue](${PAYLOAD})`);
    const a = host.querySelector("a");
    expect(a?.getAttribute("href")).toBe(PAYLOAD);
    expect(a?.textContent).toBe("file an issue");
    expect(a?.hasAttribute("data-payload-url")).toBe(true);
    expect(host.querySelector("button")).toBeNull();
  });

  it("withholds a link streamed one character at a time", () => {
    adoptLinkGuard(settingsPayload({ guard_payload_links: true }));
    const { host, stream } = live();
    for (const ch of `see [file an issue](${PAYLOAD}) now`) {
      stream.write(ch);
    }
    stream.end();
    expect(host.querySelector("button.link-withheld")?.textContent).toBe("file an issue");
    expect(host.querySelector("a[href]")).toBeNull();
    expect(host.textContent).toBe("see file an issue now");
  });

  it("never leaves a payload-shaped address on a rendered anchor while ON", () => {
    adoptLinkGuard(settingsPayload({ guard_payload_links: true }));
    const query = fc.oneof(
      fc.string({ minLength: 0, maxLength: 260 }).map((s) => encodeURIComponent(s)),
      fc.constantFrom(
        `k=${"%2F".repeat(22)}`,
        `t=${"AKIA"}ABCDEFGHIJKLMNOP`,
        `b=${"Q".repeat(44)}`,
      ),
    );
    const producers: readonly ((url: string) => string)[] = [
      (url) => `x ${url} y`,
      (url) => `[t](${url})`,
      (url) => `x <${url}> y`,
      (url) => `x [t][r] y\n\n[r]: ${url}\n`,
    ];
    fc.assert(
      fc.property(query, fc.constantFrom(...producers), (q, produce) => {
        const host = document.createElement("div");
        renderMarkdownInto(host, produce(`https://e.example/p?${q}`));
        for (const a of host.querySelectorAll("a[href]")) {
          expect(payloadShaped(new URL(a.getAttribute("href") ?? "", location.href))).toBe(false);
        }
        releaseMarkdownIn(host);
      }),
      { numRuns: 200 },
    );
  });
});

// The live path adds only fade spans and a caret, so an answer reads the same live and replayed.
describe("a streamed answer and its replay", () => {
  it("hold the same text at any write boundaries", () => {
    fc.assert(
      fc.property(
        fc.string({ minLength: 1, maxLength: 300 }),
        fc.array(fc.double({ min: 0, max: 1, noNaN: true }), { minLength: 1, maxLength: 8 }),
        (markdown, fractions) => {
          const cuts = [
            ...new Set([...fractions.map((f) => Math.floor(f * markdown.length)), markdown.length]),
          ].sort((a, b) => a - b);
          const streamed = document.createElement("div");
          const stream = createMarkdownStream(streamed, { flushIntervalMs: 0 });
          let last = 0;
          for (const cut of cuts) {
            if (cut > last) {
              stream.write(markdown.slice(last, cut));
              last = cut;
            }
          }
          stream.end();
          const replayed = document.createElement("div");
          renderMarkdownInto(replayed, markdown);
          expect(streamed.textContent).toBe(replayed.textContent);
          releaseMarkdownIn(streamed);
          releaseMarkdownIn(replayed);
        },
      ),
      { numRuns: 200 },
    );
  });
});
