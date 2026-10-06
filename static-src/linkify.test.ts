// Drives the real linkifyPaths() against a live DOM, so a change to the pattern, extension list or walk is caught.

import { describe, it, expect, beforeEach } from "vitest";
import fc from "fast-check";

import { linkifyPaths, initLinkifyCallbacks } from "./linkify.js";
import { FILE_EXTS } from "./file-extensions.js";

// The opener is injected, so the wiring is asserted against the app's handler. A plain closure re-injected per test:
// `mockReset: true` resets implementations between tests.
const opened: [string, number | undefined][] = [];

beforeEach(() => {
  opened.length = 0;
  initLinkifyCallbacks({
    open: (path, line) => {
      opened.push([path, line]);
    },
  });
});

function linkify(text: string): HTMLDivElement {
  const root = document.createElement("div");
  root.textContent = text;
  linkifyPaths(root);
  return root;
}

function links(root: HTMLElement): HTMLButtonElement[] {
  return [...root.querySelectorAll<HTMLButtonElement>("button.inline-file-link")];
}

describe("linkifyPaths: explicit cases (table-driven)", () => {
  it("turns a path mention in prose into a single button", () => {
    const root = linkify("see src/foo.ts for details");
    const ls = links(root);
    expect(ls).toHaveLength(1);
    expect(ls[0]!.title).toBe("src/foo.ts");
    expect(ls[0]!.textContent).toContain("foo.ts");
    // Surrounding prose is preserved.
    expect(root.textContent).toContain("see ");
    expect(root.textContent).toContain(" for details");
  });

  it("captures a line number and shows it in the label and title", () => {
    const root = linkify("open src/app.ts:42 now");
    const ls = links(root);
    expect(ls).toHaveLength(1);
    expect(ls[0]!.title).toBe("src/app.ts:42");
    expect(ls[0]!.textContent).toBe("app.ts:42");
  });

  it("consumes a line:col suffix but only the line is surfaced", () => {
    const root = linkify("at src/app.ts:42:7 here");
    const ls = links(root);
    expect(ls).toHaveLength(1);
    expect(ls[0]!.title).toBe("src/app.ts:42");
    expect(ls[0]!.textContent).toBe("app.ts:42");
  });

  it("linkifies multiple distinct paths in one text node", () => {
    const root = linkify("see a/b.ts and c/d.go");
    const ls = links(root);
    expect(ls).toHaveLength(2);
    expect(ls.map((b) => b.title)).toEqual(["a/b.ts", "c/d.go"]);
  });

  it("does not linkify a bare filename with no directory segment", () => {
    expect(links(linkify("just foo.ts here"))).toHaveLength(0);
  });

  it("does not linkify an unknown extension", () => {
    expect(links(linkify("see src/foo.xyz here"))).toHaveLength(0);
  });

  it("does not match a partial extension glued to trailing word chars", () => {
    // 'tsdoc' is not an extension: the lookahead rejects a partial 'src/foo.ts' match.
    expect(links(linkify("src/foo.tsdoc text"))).toHaveLength(0);
  });

  it("does not strip trailing punctuation into the path", () => {
    const root = linkify("(see src/foo.ts).");
    const ls = links(root);
    expect(ls).toHaveLength(1);
    expect(ls[0]!.title).toBe("src/foo.ts");
  });
});

describe("linkifyPaths: absolute paths", () => {
  it("linkifies an absolute path under the workspace root", () => {
    const root = linkify("wrote it to /workspace/out/shot.png");
    const ls = links(root);
    expect(ls).toHaveLength(1);
    expect(ls[0]!.title).toBe("/workspace/out/shot.png");
    expect(ls[0]!.textContent).toBe("shot.png");
  });

  it("captures a line number on an absolute path", () => {
    const root = linkify("at /workspace/marotte/main.go:174 now");
    const ls = links(root);
    expect(ls).toHaveLength(1);
    expect(ls[0]!.title).toBe("/workspace/marotte/main.go:174");
    expect(ls[0]!.textContent).toBe("main.go:174");
  });

  it("linkifies the uploads and config roots", () => {
    expect(links(linkify("see /uploads/a.png"))[0]!.title).toBe("/uploads/a.png");
    expect(links(linkify("see /config/tools.json"))[0]!.title).toBe("/config/tools.json");
  });

  it("does not linkify an absolute path outside the linkable roots", () => {
    expect(links(linkify("read /etc/nginx/nginx.conf"))).toHaveLength(0);
    expect(links(linkify("tail /var/log/syslog.log"))).toHaveLength(0);
  });

  it("does not linkify a root-shaped segment inside a URL", () => {
    expect(links(linkify("see https://example.com/workspace/a.md"))).toHaveLength(0);
    expect(links(linkify("see http://host/uploads/y.png"))).toHaveLength(0);
  });

  it("does not linkify a directory whose name merely starts with a root", () => {
    expect(links(linkify("see /workspaceother/a.md"))).toHaveLength(0);
  });

  it("clicking an absolute path opens it unchanged", () => {
    const root = linkify("open /workspace/out/shot.png now");
    links(root)[0]!.click();
    expect(opened).toEqual([["/workspace/out/shot.png", undefined]]);
  });
});

describe("linkifyPaths: skip zones", () => {
  it("leaves paths inside <code> untouched", () => {
    const root = document.createElement("div");
    root.innerHTML = `<code>src/foo.ts</code>`;
    linkifyPaths(root);
    expect(links(root)).toHaveLength(0);
    expect(root.querySelector("code")!.textContent).toBe("src/foo.ts");
  });

  it("leaves paths inside <pre> untouched", () => {
    const root = document.createElement("div");
    root.innerHTML = `<pre>run src/main.go now</pre>`;
    linkifyPaths(root);
    expect(links(root)).toHaveLength(0);
  });

  it("linkifies prose but skips an adjacent <code> sibling", () => {
    const root = document.createElement("div");
    root.innerHTML = `<span>edit src/foo.ts</span><code>src/bar.go</code>`;
    linkifyPaths(root);
    const ls = links(root);
    expect(ls).toHaveLength(1);
    expect(ls[0]!.title).toBe("src/foo.ts");
  });

  it("leaves a path inside an <a> untouched", () => {
    const root = document.createElement("div");
    root.innerHTML = `<a href="#">src/foo.ts</a>`;
    linkifyPaths(root);
    expect(links(root)).toHaveLength(0);
  });

  // A streamed link wraps its label in a per-chunk span, so the anchor sits one level above the text's parent.
  it("leaves a path wrapped in a span inside an <a> untouched", () => {
    const root = document.createElement("div");
    root.innerHTML = `<a href="#"><span data-vk-chunk-enter="">src/foo.ts</span></a>`;
    linkifyPaths(root);
    expect(links(root)).toHaveLength(0);
  });

  it("leaves a path inside a <button> untouched", () => {
    const root = document.createElement("div");
    root.innerHTML = `<button>src/foo.ts</button>`;
    linkifyPaths(root);
    expect(links(root)).toHaveLength(0);
  });

  // The root cases kill a mutation that walks only strictly above the root: for a direct child, the parent is the root.
  it("skips a <pre> ROOT, not only a <pre> descendant", () => {
    const pre = document.createElement("pre");
    pre.textContent = "src/foo.ts";
    linkifyPaths(pre);
    expect(links(pre)).toHaveLength(0);
    expect(pre.textContent).toBe("src/foo.ts");
  });

  it("skips a <code> ROOT, not only a <code> descendant", () => {
    const code = document.createElement("code");
    code.textContent = "src/foo.ts";
    linkifyPaths(code);
    expect(links(code)).toHaveLength(0);
    expect(code.textContent).toBe("src/foo.ts");
  });
});

describe("linkifyPaths: click wiring", () => {
  it("clicking a link opens the file at its line", () => {
    const root = linkify("open src/app.ts:42 now");
    links(root)[0]!.click();
    expect(opened).toEqual([["src/app.ts", 42]]);
  });

  it("clicking a path without a line opens with no line argument", () => {
    const root = linkify("open src/app.ts now");
    links(root)[0]!.click();
    expect(opened).toEqual([["src/app.ts", undefined]]);
  });
});

// Any path built from real extensions, safely bounded, is captured once with the right title (real regex and FILE_EXTS).

describe("linkifyPaths property-based", () => {
  const segment = fc
    .array(fc.constantFrom(..."abcdefghijklmnopqrstuvwxyz0123456789".split("")), {
      minLength: 1,
      maxLength: 8,
    })
    .map((cs) => cs.join(""));

  const ext = fc.constantFrom(...FILE_EXTS);

  const validPath = fc
    .tuple(fc.array(segment, { minLength: 1, maxLength: 3 }), segment, ext)
    .map(([dirs, base, e]) => `${dirs.join("/")}/${base}.${e}`);

  // Line/col suffix paired with the line value the production code surfaces.
  const lineSuffix = fc.oneof(
    fc.constant<{ suffix: string; line: number | undefined }>({ suffix: "", line: undefined }),
    fc.nat({ max: 9999 }).map((n) => ({ suffix: `:${String(n)}`, line: n })),
    fc
      .tuple(fc.nat({ max: 9999 }), fc.nat({ max: 200 }))
      .map(([l, c]) => ({ suffix: `:${String(l)}:${String(c)}`, line: l })),
  );

  // Boundary chars outside [\w/.-], so the lookbehind and lookahead pass.
  const before = fc.constantFrom(" ", "\n", "\t", "(", '"', "'", ",", ";", "[", "{");
  const after = fc.constantFrom(" ", "\n", "\t", ")", '"', "'", ",", ";", "]", "}");

  it("captures exactly one path with the expected title", () => {
    fc.assert(
      fc.property(validPath, lineSuffix, before, after, (path, { suffix, line }, pre, post) => {
        const root = linkify(`${pre}${path}${suffix}${post}`);
        const ls = links(root);
        expect(ls).toHaveLength(1);
        const expectedTitle = line === undefined ? path : `${path}:${String(line)}`;
        expect(ls[0]!.title).toBe(expectedTitle);
      }),
      { numRuns: 500 },
    );
  });

  it("never throws and never linkifies an extension-less segment", () => {
    fc.assert(
      fc.property(fc.array(segment, { minLength: 1, maxLength: 4 }), (segs) => {
        // A slash-joined path with no '.ext' never links.
        const root = linkify(` ${segs.join("/")} `);
        expect(links(root)).toHaveLength(0);
      }),
      { numRuns: 300 },
    );
  });
});
