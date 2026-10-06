// Property-based XSS invariants and table-driven edge cases. markdown.ts renders into real DOM nodes.

import { describe, it, expect, vi, afterEach } from "vitest";
import * as fc from "fast-check";
import { renderMarkdown, createMarkdownStream } from "./markdown.js";
import { adoptLinkGuard, setLinkCopyCallback } from "./link-guard.js";
import { exfilShaped } from "./utils-url.js";
import { settingsPayload } from "./__test-helpers__/settings.js";

describe("renderMarkdown XSS invariants (property-based)", () => {
  it("never produces <script in output", () => {
    fc.assert(
      fc.property(fc.string({ minLength: 0, maxLength: 500 }), (input) => {
        const html = renderMarkdown(input);
        expect(html.toLowerCase()).not.toContain("<script");
      }),
      { numRuns: 500 },
    );
  });

  // Every spelling of a blocked scheme: entity-encoded (decoded on the way to the gate) and angle-bracket (autolinks),
  // all reaching the same gate as a plain destination.
  const jsUrl = fc.constantFrom(
    "javascript:alert(1)",
    "JAVASCRIPT:alert(1)",
    "javascript:void(0)",
    "JavaScript:alert(document.cookie)",
    "javascript&#58;alert(1)",
    "java&#115;cript:alert(1)",
    "&#x6a;avascript:alert(1)",
    "&#106;avascript:alert(1)",
    "JAVASCRIPT&#58;alert(1)",
    "java&#115cript:alert(1)",
    "java\tscript:alert(1)",
    "&NewLine;javascript:alert(1)",
    "&#1;javascript:alert(1)",
    "&#x01;javascript:alert(1)",
    "&#31;javascript:alert(1)",
    "&#32;&#1;&#32;javascript:alert(1)",
  );

  /**
   * As the browser's URL parser reads it (strip leading/trailing C0 controls and spaces, then tabs and newlines).
   * Normalizing as `isSafeUrl` does would hide any gap between the gate and the browser.
   */
  const asBrowserReads = (attrValue: string): string =>
    attrValue
      .replace(/^[\x00-\x20]+|[\x00-\x20]+$/g, "") // eslint-disable-line no-control-regex
      .replace(/[\t\n\r]/g, "")
      .toLowerCase();

  const attrValues = (html: string): string[] =>
    (html.match(/(?:href|src)="([^"]*)"/g) ?? []).map((attr) =>
      attr.replace(/^(?:href|src)="/, "").replace(/"$/, ""),
    );

  it("never produces javascript: URIs in href/src attributes", () => {
    // Alphanumeric text, so the link syntax stays intact.
    const safeText = fc.stringMatching(/^[a-zA-Z0-9 ]{1,20}$/);
    const mdWithLink = fc.tuple(safeText, jsUrl).map(([text, url]) => `[${text}](${url})`);

    fc.assert(
      fc.property(mdWithLink, (input) => {
        const html = renderMarkdown(input);
        const values = attrValues(html);
        for (const val of values) {
          // Blocked schemes become "#".
          expect(asBrowserReads(val)).not.toMatch(/^javascript:/);
        }
        expect(values.length).toBeGreaterThan(0);
      }),
      { numRuns: 200 },
    );
  });

  it("never produces a blocked scheme from an angle autolink", () => {
    const blocked = fc.constantFrom(
      "javascript:alert(1)",
      "JavaScript:alert(1)",
      "javascript&#58;alert(1)",
      "&#x6a;avascript:alert(1)",
      "data:text/html,alert(1)",
      "DATA:text/html,x",
      "data&#58;text/html,x",
      "vbscript:msgbox",
      "VBSCRIPT:MsgBox",
      "file:///etc/passwd",
    );
    // Four spaces of lead would make indented code, and the property would hold vacuously.
    const mdWithAutolink = fc
      .tuple(fc.stringMatching(/^[a-zA-Z0-9][a-zA-Z0-9 ]{0,19}$/), blocked)
      .map(([lead, url]) => `${lead}<${url}>`);

    fc.assert(
      fc.property(mdWithAutolink, (input) => {
        const html = renderMarkdown(input);
        const values = attrValues(html);
        for (const val of values) {
          expect(asBrowserReads(val)).not.toMatch(/^(?:javascript|data|vbscript|file):/);
        }
        // Every input is a well-formed autolink, so an href must exist, or the property holds if autolinks never parse.
        expect(values.length).toBeGreaterThan(0);
        expect(html.toLowerCase()).not.toContain("<script");
        expect(html).not.toMatch(/<[^>]+\son\w+\s*=/i);
      }),
      { numRuns: 200 },
    );
  });

  it("never produces on* event handler attributes", () => {
    fc.assert(
      fc.property(fc.string({ minLength: 0, maxLength: 500 }), (input) => {
        const html = renderMarkdown(input);
        const withoutCode = html
          .replace(/<pre[^>]*>[\s\S]*?<\/pre>/g, "")
          .replace(/<code>[\s\S]*?<\/code>/g, "");
        // No on* attributes in tags.
        expect(withoutCode).not.toMatch(/<[^>]+\son\w+\s*=/i);
      }),
      { numRuns: 500 },
    );
  });

  it("never produces data: URIs in href/src attributes", () => {
    // A label that cannot break the image syntax, so the emitted-src guard applies.
    const safeText = fc.stringMatching(/^[a-zA-Z0-9 ]{1,20}$/);
    const mdWithDataUri = fc
      .tuple(
        safeText,
        fc.constantFrom(
          "data:text/html,<script>alert(1)</script>",
          "data:image/svg+xml,<svg onload=alert(1)>",
          "DATA:text/html,test",
          "data&#58;text/html,<script>alert(1)</script>",
          "&#100;ata:text/html,x",
          "&#x64;ata:image/svg+xml,<svg onload=alert(1)>",
          "&#1;data:text/html,x",
          "&#x1f;data:image/svg+xml,<svg onload=alert(1)>",
        ),
      )
      .map(([text, url]) => `![${text}](${url})`);

    fc.assert(
      fc.property(mdWithDataUri, (input) => {
        const html = renderMarkdown(input);
        const values = attrValues(html);
        for (const val of values) {
          expect(asBrowserReads(val)).not.toMatch(/^data:/);
        }
        expect(values.length).toBeGreaterThan(0);
      }),
      { numRuns: 200 },
    );
  });

  it("never produces vbscript: URIs in href/src attributes", () => {
    const safeText = fc.stringMatching(/^[a-zA-Z0-9 ]{1,20}$/);
    const vbUrl = fc.constantFrom(
      "vbscript:msgbox",
      "VBSCRIPT:MsgBox",
      "vbscript:Execute",
      "vbscript&#58;msgbox",
      "&#118;bscript:msgbox",
      "&#1;vbscript:msgbox",
    );
    const mdWithVbscript = fc.tuple(safeText, vbUrl).map(([text, url]) => `[${text}](${url})`);

    fc.assert(
      fc.property(mdWithVbscript, (input) => {
        const html = renderMarkdown(input);
        const values = attrValues(html);
        for (const val of values) {
          // Blocked schemes become "#".
          expect(asBrowserReads(val)).not.toMatch(/^vbscript:/);
        }
        expect(values.length).toBeGreaterThan(0);
      }),
      { numRuns: 200 },
    );
  });

  it("XSS payloads in link text are escaped", () => {
    fc.assert(
      fc.property(fc.string({ minLength: 1, maxLength: 200 }), fc.webUrl(), (text, url) => {
        const md = `[${text}](${url})`;
        const html = renderMarkdown(md);
        expect(html.toLowerCase()).not.toContain("<script");
      }),
      { numRuns: 100 },
    );
  });
});

describe("renderMarkdown edge cases (table-driven)", () => {
  const cases: { name: string; input: string; expected: string | RegExp }[] = [
    { name: "bold with **", input: "**bold**", expected: "<p><strong>bold</strong></p>" },
    { name: "bold with __", input: "__bold__", expected: "<p><strong>bold</strong></p>" },
    { name: "italic with *", input: "*italic*", expected: "<p><em>italic</em></p>" },
    { name: "italic with _", input: "_italic_", expected: "<p><em>italic</em></p>" },
    { name: "strikethrough", input: "~~strike~~", expected: "<p><s>strike</s></p>" },
    { name: "inline code", input: "`code`", expected: "<p><code>code</code></p>" },
    {
      name: "inline code with special chars",
      input: "`<script>alert(1)</script>`",
      expected: /&lt;script&gt;/,
    },
    {
      name: "nested bold in italic",
      input: "*hello **world***",
      expected: /<em>.*<strong>world<\/strong>.*<\/em>/,
    },

    // ATX only: smd-parser has no setext headings.
    { name: "h1 ATX", input: "# Heading", expected: /^<h1>.*Heading.*<\/h1>$/ },
    { name: "h2 ATX", input: "## Heading", expected: /^<h2>.*Heading.*<\/h2>$/ },
    { name: "h3 ATX", input: "### Heading", expected: /^<h3>.*Heading.*<\/h3>$/ },
    { name: "h4 ATX", input: "#### Heading", expected: /^<h4>.*Heading.*<\/h4>$/ },
    { name: "h5 ATX", input: "##### Heading", expected: /^<h5>.*Heading.*<\/h5>$/ },
    { name: "h6 ATX", input: "###### Heading", expected: /^<h6>.*Heading.*<\/h6>$/ },

    {
      name: "basic link",
      input: "[text](https://example.com)",
      expected: /href="https:\/\/example\.com"/,
    },
    { name: "link with target blank", input: "[x](https://a.com)", expected: /target="_blank"/ },
    { name: "link with rel noopener", input: "[x](https://a.com)", expected: /rel="noopener"/ },
    {
      name: "javascript: link blocked",
      input: "[click](javascript:alert(1))",
      expected: /href="#"/,
    },
    {
      name: "JAVASCRIPT: link blocked (case)",
      input: "[click](JAVASCRIPT:alert(1))",
      expected: /href="#"/,
    },
    {
      name: "javascript: link in angle brackets blocked",
      input: "[click](<javascript:alert(1)>)",
      expected: /href="#"/,
    },
    {
      name: "angle-bracketed link with a space is unwrapped",
      input: "[click](<https://example.com/a b>)",
      expected: /href="https:\/\/example\.com\/a b"/,
    },
    { name: "data: link blocked", input: "[click](data:text/html,<script>)", expected: /href="#"/ },
    { name: "vbscript: link blocked", input: "[click](vbscript:msgbox)", expected: /href="#"/ },
    { name: "file: link blocked", input: "[click](file:///etc/passwd)", expected: /href="#"/ },

    { name: "basic image", input: "![alt](https://img.png)", expected: /src="https:\/\/img\.png"/ },
    { name: "image alt text", input: "![my alt](https://img.png)", expected: /alt="my alt"/ },
    { name: "javascript: image blocked", input: "![x](javascript:alert(1))", expected: /src="#"/ },
    { name: "data: image blocked", input: "![x](data:image/svg+xml,<svg>)", expected: /src="#"/ },

    // pre gets class="code"; code gets class="language-X" when a lang is set.
    {
      name: "fenced code block",
      input: "```\ncode\n```",
      expected: /<pre class="code"><code>code<\/code><\/pre>/,
    },
    { name: "fenced code with lang", input: "```js\nvar x;\n```", expected: /class="language-js"/ },
    {
      name: "code block escapes HTML",
      input: "```\n<script>alert(1)</script>\n```",
      expected: /&lt;script&gt;alert\(1\)&lt;\/script&gt;/,
    },
    {
      name: "nested fences",
      input: "````\n```\ninner\n```\n````",
      expected: /<pre.*<code>```\ninner\n```<\/code><\/pre>/,
    },

    {
      name: "unordered list",
      input: "- item1\n- item2",
      expected: /<ul>.*<li>.*item1.*<\/li>.*<li>.*item2.*<\/li>.*<\/ul>/s,
    },
    {
      name: "ordered list",
      input: "1. first\n2. second",
      expected: /<ol>.*<li>.*first.*<\/li>.*<li>.*second.*<\/li>.*<\/ol>/s,
    },
    { name: "bullet with +", input: "+ item", expected: /<ul>.*<li>.*item.*<\/li>.*<\/ul>/s },
    { name: "bullet with *", input: "* item", expected: /<ul>.*<li>.*item.*<\/li>.*<\/ul>/s },

    { name: "blockquote", input: "> quoted", expected: /<blockquote>.*quoted.*<\/blockquote>/s },
    {
      name: "nested blockquote",
      input: "> > nested",
      expected: /<blockquote>.*<blockquote>.*nested.*<\/blockquote>.*<\/blockquote>/s,
    },

    // HTML5 void form: <hr>.
    { name: "hr with ---", input: "text\n\n---\n\nmore", expected: /<hr>/ },
    { name: "hr with * * *", input: "a\n\n* * *\n\nb", expected: /<hr>/ },

    // GFM tables; smd-parser preserves cell padding whitespace.
    {
      name: "basic table",
      input: "\n| A | B |\n|---|---|\n| 1 | 2 |\n",
      expected: /<table>.*<th[^>]*>\s*A\s*<\/th>.*<td[^>]*>\s*1\s*<\/td>/s,
    },
    {
      name: "table escapes cell content",
      input: "\n| <b> |\n|---|\n| <i> |\n",
      expected: /&lt;b&gt;/,
    },

    // Task lists; HTML5 boolean attrs emit as attr="".
    {
      name: "checked task",
      input: "- [x] done",
      expected: /<input type="checkbox" disabled="" aria-label="Task item" checked=""/,
    },
    {
      name: "unchecked task",
      input: "- [ ] todo",
      expected: /<input type="checkbox" disabled="" aria-label="Task item"/,
    },
    {
      name: "task list XSS in content",
      input: "- [x] <img onerror=alert(1)>",
      expected: /&lt;img onerror=alert\(1\)&gt;/,
    },

    // No inter-paragraph newline in smd-parser output.
    { name: "single paragraph", input: "hello", expected: "<p>hello</p>" },
    { name: "two paragraphs", input: "one\n\ntwo", expected: /<p>one<\/p>\s*<p>two<\/p>/ },
    // HTML5 void form <br>.
    { name: "line break with two spaces", input: "a  \nb", expected: /a ?<br>/ },

    { name: "empty string", input: "", expected: "" },
    // Whitespace-only input emits nothing (CommonMark-correct).
    { name: "only whitespace", input: "   ", expected: "" },
    // HTML in plain text is escaped, never passed through.
    {
      name: "HTML in text is escaped",
      input: "<div>test</div>",
      expected: /&lt;div&gt;test&lt;\/div&gt;/,
    },
    { name: "ampersand in text is escaped", input: "a & b", expected: /a &amp; b/ },
    // Unclosed fences auto-close at EOF (CommonMark-correct).
    {
      name: "unclosed fence auto-closes",
      input: "```\nno close",
      expected: /<pre class="code"><code>no close<\/code><\/pre>/,
    },
    {
      name: "consecutive lists merge",
      input: "1. a\n\n2. b",
      expected: /<ol>.*<li>.*a.*<\/li>.*<li>.*b.*<\/li>.*<\/ol>/s,
    },
  ];

  it.each(cases)("$name", ({ input, expected }) => {
    const html = renderMarkdown(input);
    if (typeof expected === "string") {
      expect(html).toBe(expected);
    } else {
      expect(html).toMatch(expected);
    }
  });
});

// Intraword underscores (CommonMark 6.2): a `_` run after a word character cannot open or close emphasis; `*` has
// no such exclusion.

describe("renderMarkdown intraword underscores (CommonMark 6.2)", () => {
  const cases: { name: string; input: string; expected: string | RegExp }[] = [
    {
      name: "the reported string renders literally",
      input: "run_progress shape, tool_call_update as a delta",
      expected: "<p>run_progress shape, tool_call_update as a delta</p>",
    },
    { name: "snake_case renders literally", input: "snake_case", expected: "<p>snake_case</p>" },
    { name: "a_b_c renders literally", input: "a_b_c", expected: "<p>a_b_c</p>" },
    { name: "a__b renders literally", input: "a__b", expected: "<p>a__b</p>" },
    {
      name: "MAX_RETRIES and MIN_WAIT render literally",
      input: "MAX_RETRIES and MIN_WAIT",
      expected: "<p>MAX_RETRIES and MIN_WAIT</p>",
    },

    {
      name: "digits are word characters",
      input: "5_000_000 rows",
      expected: "<p>5_000_000 rows</p>",
    },
    {
      name: "non-ASCII letters are word characters",
      input: "über_wert und café_bar",
      expected: "<p>über_wert und café_bar</p>",
    },
    {
      name: "CJK characters are word characters",
      input: "日本_語 test",
      expected: "<p>日本_語 test</p>",
    },
    {
      // A symbol is punctuation for flanking (`\p{S}`), so the run opens; the BMP control for the two astral cases.
      name: "a symbol before the underscore still opens",
      input: "\u2705_yay_",
      expected: "<p>\u2705<em>yay</em></p>",
    },
    {
      // The lookbehind reads the last code point: a lone low surrogate (Cs) would read as a word character.
      name: "an astral symbol before the underscore still opens",
      input: "\u{1F389}_yay_",
      expected: "<p>\u{1F389}<em>yay</em></p>",
    },
    {
      // An astral letter is a word character and still blocks; the pair is needed since either alone can pass by coincidence.
      name: "an astral letter before the underscore blocks the open",
      input: "\u{1D400}_yay_",
      expected: "<p>\u{1D400}_yay_</p>",
    },

    {
      name: "_emphasis_ at line start still emphasises",
      input: "_emphasis_",
      expected: "<p><em>emphasis</em></p>",
    },
    {
      name: "_em_ after a space still emphasises",
      input: "x _y_ z",
      expected: "<p>x <em>y</em> z</p>",
    },
    {
      name: "_em_ spanning words still emphasises",
      input: "a _b c_ d",
      expected: "<p>a <em>b c</em> d</p>",
    },
    {
      name: "__strong__ still emphasises",
      input: "__strong__",
      expected: "<p><strong>strong</strong></p>",
    },
    {
      name: "___tri___ still nests strong in em",
      input: "___tri___",
      expected: "<p><strong><em>tri</em></strong></p>",
    },

    {
      name: "* is still allowed intraword",
      input: "foo*bar*baz",
      expected: "<p>foo<em>bar</em>baz</p>",
    },
    {
      name: "*intraword* still emphasises",
      input: "*intraword*",
      expected: "<p><em>intraword</em></p>",
    },

    {
      name: "_ inside an inline code span stays literal",
      input: "`snake_case`",
      expected: "<p><code>snake_case</code></p>",
    },
    {
      name: "_ inside a fenced block stays literal",
      input: "```\nsnake_case\n```",
      expected: '<pre class="code"><code>snake_case</code></pre>',
    },
    {
      // A pre-existing divergence from CommonMark, from handleCommon's STRONG_AST guard. Characterization.
      name: "_ inside ** is literal today (characterization)",
      input: "**_both_**",
      expected: "<p><strong>_both_</strong></p>",
    },
    {
      name: "* inside __ still emphasises",
      input: "__*both*__",
      expected: "<p><strong><em>both</em></strong></p>",
    },

    {
      name: "a lone trailing underscore stays literal",
      input: "trailing_",
      expected: "<p>trailing_</p>",
    },
    {
      name: "an underscore before a space stays literal",
      input: "text_ end",
      expected: "<p>text_ end</p>",
    },
    {
      name: "an escaped underscore stays literal",
      input: "escaped \\_not em\\_ here",
      expected: "<p>escaped _not em_ here</p>",
    },

    {
      name: "punctuation before the underscore still opens",
      input: "(_em_)",
      expected: "<p>(<em>em</em>)</p>",
    },
    {
      name: "punctuation after the emphasis still closes",
      input: "_em_.",
      expected: "<p><em>em</em>.</p>",
    },
    {
      name: "an underscore right after a closed strong token still opens",
      input: "**bold**_it_",
      expected: "<p><strong>bold</strong><em>it</em></p>",
    },
    {
      name: "an underscore right after a closed code span still opens",
      input: "`c`_x_",
      expected: "<p><code>c</code><em>x</em></p>",
    },

    {
      name: "snake_case in a link label keeps the href",
      input: "[snake_case](https://e.com)",
      expected: '<p><a target="_blank" rel="noopener" href="https://e.com">snake_case</a></p>',
    },
    {
      name: "snake_case in a heading renders literally",
      input: "# snake_case heading",
      expected: "<h1>snake_case heading</h1>",
    },
    {
      name: "snake_case in a list item renders literally",
      input: "- item_name here",
      expected: "<ul><li>item_name here</li></ul>",
    },
    {
      name: "snake_case in an ordered list renders literally",
      input: "1. num_one and _em_",
      expected: "<ol><li>num_one and <em>em</em></li></ol>",
    },
    {
      name: "snake_case in a blockquote renders literally",
      input: "> quote_with_underscore",
      expected: "<blockquote><p>quote_with_underscore</p></blockquote>",
    },
    {
      // The `_` must not swallow the closing `|`, so structure is asserted too.
      name: "snake_case in a table cell renders literally",
      input: "\n| a_b | c |\n|---|---|\n| d_e | f |\n",
      expected:
        "<table><thead><tr><th> a_b </th><th> c </th></tr></thead>" +
        "<tbody><tr><td> d_e </td><td> f </td></tr></tbody></table>",
    },
    {
      name: "snake_case in a task list renders literally",
      input: "- [x] task_name _em_",
      expected:
        '<ul><li><input type="checkbox" disabled="" aria-label="Task item" checked=""> ' +
        "task_name <em>em</em></li></ul>",
    },

    {
      name: "a soft line break resets the lookbehind",
      input: "a_b\n_real_",
      expected: "<p>a_b<br><em>real</em></p>",
    },
    { name: "a <br> resets the lookbehind", input: "a<br>_x_", expected: "<p>a<br><em>x</em></p>" },
    {
      name: "a paragraph break resets the lookbehind",
      input: "a_b\n\n_real_",
      expected: "<p>a_b</p><p><em>real</em></p>",
    },
    {
      name: "an underscore after a horizontal rule still opens",
      input: "---\n\n_em_ after rule",
      expected: "<hr><p><em>em</em> after rule</p>",
    },
    {
      name: "strikethrough closes over an intraword underscore",
      input: "~~a_b~~",
      expected: "<p><s>a_b</s></p>",
    },
    {
      name: "an intraword underscore outside a code span stays literal",
      input: "a_b `c_d` e_f",
      expected: "<p>a_b <code>c_d</code> e_f</p>",
    },

    // Inside an open `_` token the rule applies at all three opening sites; only the close is ungated, or the token stays
    // open to the end of the line.
    {
      name: "the reported string inside __ renders literally",
      input: "__run_progress__",
      expected: "<p><strong>run_progress</strong></p>",
    },
    {
      name: "snake_case inside __ renders literally",
      input: "__snake_case in strong__",
      expected: "<p><strong>snake_case in strong</strong></p>",
    },
    {
      name: "a single intraword _ inside __ renders literally",
      input: "__a_b__",
      expected: "<p><strong>a_b</strong></p>",
    },
    {
      name: "an intraword __ inside _ stays literal",
      input: "_a__b__c_",
      expected: "<p><em>a__b__c</em></p>",
    },
    {
      name: "an unbalanced __ inside _ stays literal",
      input: "_a__b_",
      expected: "<p><em>a__b</em></p>",
    },
    {
      name: "an intraword _ inside __ stays literal",
      input: "__a_b_c__",
      expected: "<p><strong>a_b_c</strong></p>",
    },
    {
      name: "_em_ at a word boundary inside __ still emphasises",
      input: "__a _b_ c__",
      expected: "<p><strong>a <em>b</em> c</strong></p>",
    },
    // The outer run stays unclosed and is unwrapped at block close. CommonMark pairs these differently (needs a delimiter
    // stack); pinned: no character lost, nothing emphasised that the author did not close.
    {
      name: "a trailing __ inside _ stays literal",
      input: "_x__y__",
      expected: "<p>_x__y__</p>",
    },
    {
      name: "a trailing _ inside __ stays literal",
      input: "__x_y_",
      expected: "<p>__x_y_</p>",
    },
  ];

  it.each(cases)("$name", ({ input, expected }) => {
    const html = renderMarkdown(input);
    if (typeof expected === "string") {
      expect(html).toBe(expected);
    } else {
      expect(html).toMatch(expected);
    }
  });

  // A `_` run with word characters on both sides cannot close either; the opener is restored as text at block end.
  const closeCases: { name: string; input: string; expected: string }[] = [
    { name: "_foo_bar stays literal", input: "_foo_bar", expected: "<p>_foo_bar</p>" },
    { name: "_foo bar_baz stays literal", input: "_foo bar_baz", expected: "<p>_foo bar_baz</p>" },
    {
      name: "_internal_state stays literal",
      input: "_internal_state",
      expected: "<p>_internal_state</p>",
    },
    {
      name: "a run that does close later still emphasises",
      input: "_internal_state_here_",
      expected: "<p><em>internal_state_here</em></p>",
    },
    { name: "_foo_ still closes at end of input", input: "_foo_", expected: "<p><em>foo</em></p>" },
    {
      name: "_foo_. closes before punctuation",
      input: "_foo_.",
      expected: "<p><em>foo</em>.</p>",
    },
    {
      name: "_foo_ bar closes before a space",
      input: "_foo_ bar",
      expected: "<p><em>foo</em> bar</p>",
    },
    { name: "a _b c_ d is unaffected", input: "a _b c_ d", expected: "<p>a <em>b c</em> d</p>" },
    {
      name: "* is exempt by design",
      input: "*foo*bar",
      expected: "<p><em>foo</em>bar</p>",
    },
    {
      // The `__` close decides before its right context exists, so gating it is out of scope. Characterization.
      name: "__x_y_ stays literal (the __ close is out of scope)",
      input: "__x_y_",
      expected: "<p>__x_y_</p>",
    },
  ];

  it.each(closeCases)("$name", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });

  // The rule is per delimiter, so a run of three opens for its tail; that token never closes and the unwrap restores
  // the whole run (CommonMark-correct).
  it("a run of three or more underscores stays literal", () => {
    expect(renderMarkdown("a___b")).toBe("<p>a___b</p>");
    expect(renderMarkdown("a____b")).toBe("<p>a____b</p>");
  });
});

// Streaming and one-shot rendering agree (the append-only contract).

describe("createMarkdownStream streaming/one-shot equivalence", () => {
  it("streaming writeDelta at random split points produces same output as one-shot", () => {
    fc.assert(
      fc.property(
        fc.string({ minLength: 1, maxLength: 300 }),
        fc.array(fc.double({ min: 0, max: 1, noNaN: true }), { minLength: 1, maxLength: 8 }),
        (markdown, splitPoints) => {
          // Non-decreasing delta lengths from random split fractions.
          const sorted = [...splitPoints].sort((a, b) => a - b);
          const indices = sorted.map((f) => Math.floor(f * markdown.length));
          const cuts = [...new Set([...indices, markdown.length])].sort((a, b) => a - b);

          const streamEl = document.createElement("div");
          const renderer = createMarkdownStream(streamEl);
          let lastCut = 0;
          for (const cut of cuts) {
            if (cut > lastCut) {
              renderer.writeDelta(markdown.slice(lastCut, cut));
              lastCut = cut;
            }
          }
          renderer.end();

          const oneShotHtml = renderMarkdown(markdown);

          expect(streamEl.textContent).toBe(
            (() => {
              const tmp = document.createElement("div");
              tmp.innerHTML = oneShotHtml;
              return tmp.textContent;
            })(),
          );
        },
      ),
      { numRuns: 200 },
    );
  });

  it("end() is idempotent — second call is a no-op", () => {
    const el = document.createElement("div");
    const renderer = createMarkdownStream(el);
    renderer.writeDelta("hello world");
    renderer.end();
    const after = el.innerHTML;
    renderer.end(); // Changes nothing.
    expect(el.innerHTML).toBe(after);
  });

  it("writeDelta after end() is ignored", () => {
    const el = document.createElement("div");
    const renderer = createMarkdownStream(el);
    renderer.writeDelta("hello");
    renderer.end();
    const after = el.innerHTML;
    renderer.writeDelta(" more"); // Changes nothing.
    expect(el.innerHTML).toBe(after);
  });
});

// renderMarkdown is pure structure; renderMarkdownInto decorates; createMarkdownStream decorates and animates.

import { renderMarkdownInto } from "./markdown.js";

describe("markdown surface contracts", () => {
  it("renderMarkdown returns pure structure (no decoration, no animation marker)", () => {
    const html = renderMarkdown("```js\nconsole.log(1)\n```");
    // Pure parser output: no .code-wrap, .code-actions or data-vk-block-enter.
    expect(html).not.toContain("code-wrap");
    expect(html).not.toContain("code-actions");
    expect(html).not.toContain("data-vk-block-enter");
    expect(html).toContain("<pre");
    expect(html).toContain("<code");
  });

  it("renderMarkdown returns pure structure for paragraphs (no path linkify)", () => {
    const html = renderMarkdown("see foo/bar.ts for details");
    // Pure parser: no linkification.
    expect(html).not.toContain('href="');
    expect(html).toContain("foo/bar.ts");
  });

  it("renderMarkdownInto decorates code blocks (replay path)", () => {
    const el = document.createElement("div");
    renderMarkdownInto(el, "```js\nconsole.log(1)\n```");
    // The replay path wraps pre in .code-wrap with .code-actions.
    expect(el.querySelector(".code-wrap")).not.toBeNull();
    // No animation marker on replay.
    expect(el.querySelector("[data-vk-block-enter]")).toBeNull();
  });

  it("createMarkdownStream end() decorates AND tags blocks for animation", () => {
    const el = document.createElement("div");
    const r = createMarkdownStream(el);
    r.writeDelta("```js\ncode\n```");
    r.end();
    // Streaming: decoration and the entry marker.
    expect(el.querySelector(".code-wrap")).not.toBeNull();
    expect(el.querySelector("[data-vk-block-enter]")).not.toBeNull();
  });

  it("createMarkdownStream end() is a synchronous drain", () => {
    const el = document.createElement("div");
    const r = createMarkdownStream(el);
    // Larger than PARSE_SLICE_BYTES (4096) to force the async drain; end() completes synchronously.
    const big = "a".repeat(10_000);
    r.writeDelta(big);
    r.end();
    expect(el.textContent?.length).toBe(10_000);
  });
});

// A fence the model has not closed: the per-block callback fires only on close and `parser_end` closes nothing, so
// the tail needs its own sweep for highlight, language and Copy.

describe("markdown code-block decoration while streaming", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("decorates a fence that is still open, provisionally", () => {
    vi.useFakeTimers();
    const el = document.createElement("div");
    const r = createMarkdownStream(el);
    r.writeDelta("```go\nfunc main() {\n");
    // The write buffer flushes on its own interval; nothing has closed yet.
    vi.advanceTimersByTime(250);
    const wrap = el.querySelector(".code-wrap");
    expect(wrap?.getAttribute("data-code-state")).toBe("streaming");
    expect(el.querySelector(".code-lang")?.textContent).toBe("go");
  });

  it("promotes the same block to final once the fence closes", () => {
    vi.useFakeTimers();
    const el = document.createElement("div");
    const r = createMarkdownStream(el);
    r.writeDelta("```go\nfunc main() {\n");
    vi.advanceTimersByTime(250);
    r.writeDelta("}\n```\n");
    vi.advanceTimersByTime(250);
    expect(el.querySelectorAll(".code-head")).toHaveLength(1);
    expect(el.querySelector(".code-wrap")?.getAttribute("data-code-state")).toBe("final");
  });

  it("finalizes a fence the model never closed, at end()", () => {
    const el = document.createElement("div");
    const r = createMarkdownStream(el);
    r.writeDelta("```go\nfunc main() {\n");
    r.end();
    expect(el.querySelector(".code-wrap")?.getAttribute("data-code-state")).toBe("final");
    expect(el.querySelector(".code-lang")?.textContent).toBe("go");
  });

  it("decorates an unterminated fence on the replay path too", () => {
    const el = document.createElement("div");
    renderMarkdownInto(el, "```bash\necho hi");
    expect(el.querySelector(".code-wrap")).not.toBeNull();
    expect(el.querySelector(".code-lang")?.textContent).toBe("bash");
  });
});

// Math, parser through converter.

const MATHML_NS = "http://www.w3.org/1998/Math/MathML";

describe("markdown math rendering", () => {
  const cases: { name: string; md: string; display: boolean }[] = [
    { name: "$…$ inline", md: "the value $x^2$ here", display: false },
    { name: "\\(…\\) inline", md: "the value \\(x^2\\) here", display: false },
    { name: "$$…$$ block", md: "$$\nx^2\n$$\n", display: true },
    { name: "\\[…\\] block", md: "\\[\nx^2\n\\]\n", display: true },
  ];

  for (const tc of cases) {
    it(`renders ${tc.name} as native MathML`, () => {
      const el = document.createElement("div");
      renderMarkdownInto(el, tc.md);
      const math = el.querySelector("math");
      expect(math).not.toBeNull();
      expect(math?.namespaceURI).toBe(MATHML_NS);
      expect(math?.hasAttribute("display")).toBe(tc.display);
    });
  }

  it("survives a delimiter split across write chunks", () => {
    // The stream slices at a byte budget, so a delimiter can split; the parser's `pending` handles it.
    const el = document.createElement("div");
    const r = createMarkdownStream(el);
    r.writeDelta("area $");
    r.writeDelta("\\pi r^2");
    r.writeDelta("$ done");
    r.end();
    const math = el.querySelector("math");
    expect(math?.namespaceURI).toBe(MATHML_NS);
    expect(math?.textContent).toBe("\u03c0r2");
  });

  it("leaves an unsupported expression as its raw LaTeX", () => {
    const el = document.createElement("div");
    renderMarkdownInto(el, "a matrix $\\begin{pmatrix} a & b \\end{pmatrix}$ inline");
    expect(el.querySelector("math")).toBeNull();
    const host = el.querySelector("[data-math]");
    expect(host?.hasAttribute("data-math-raw")).toBe(true);
    expect(host?.textContent).toBe("\\begin{pmatrix} a & b \\end{pmatrix}");
  });

  it("restores the delimiter of an expression that never closes", () => {
    // An unclosed `$` is a dollar sign: the host is unwrapped at block close and the delimiter returns as text.
    const el = document.createElement("div");
    renderMarkdownInto(el, "unfinished $x^2 + y");
    expect(el.querySelector("math")).toBeNull();
    expect(el.querySelector("[data-math]")).toBeNull();
    expect(el.textContent).toBe("unfinished $x^2 + y");
  });

  it("needs a newline after the block opener, and says so by rendering the source", () => {
    // Known limitation: `\[` opens a block only before a newline, since it is also an escaped bracket.
    const el = document.createElement("div");
    renderMarkdownInto(el, "\\[x^2\\]\n");
    expect(el.querySelector("[data-math]")).toBeNull();
    expect(el.textContent).toBe("[x^2]");
  });

  it("does not treat a price or a bare dollar as math", () => {
    const el = document.createElement("div");
    renderMarkdownInto(el, "it costs $5 or $ 10");
    expect(el.querySelector("[data-math]")).toBeNull();
    expect(el.textContent).toContain("$5");
  });
});

// TOKEN_ARRAY_CAP is the depth limit; the saturating path must still consume its character, or it re-enters with the
// same state until the stack blows.

describe("renderMarkdown deep nesting", () => {
  it("renders 22 nested blockquotes, the last working depth", () => {
    const html = renderMarkdown(">".repeat(22) + " x");
    expect(html).toBe("<blockquote>".repeat(22) + "<p>x</p>" + "</blockquote>".repeat(22));
  });

  it("keeps the text when blockquote nesting saturates the token stack", () => {
    const html = renderMarkdown(">".repeat(23) + " x");
    expect(html).toContain("x");
  });

  it("does not throw on absurd blockquote nesting", () => {
    expect(() => renderMarkdown(">".repeat(200) + " x")).not.toThrow();
    expect(() => renderMarkdown("> ".repeat(30) + "x")).not.toThrow();
    expect(() => renderMarkdown(">".repeat(1000) + " x")).not.toThrow();
  });

  it("does not throw on absurd list, indent or emphasis nesting", () => {
    expect(() => renderMarkdown("- ".repeat(1024) + "x")).not.toThrow();
    expect(() => renderMarkdown(" ".repeat(1024) + "- x")).not.toThrow();
    expect(() => renderMarkdown("*".repeat(1024))).not.toThrow();
    expect(() => renderMarkdown(">".repeat(30) + " ```js`x")).not.toThrow();
  });

  it("does not throw on a table nested past the depth limit", () => {
    const table = " | a | b |\n| - | - |\n| 1 | 2 |";
    expect(() => renderMarkdown(">".repeat(21) + table)).not.toThrow();
    expect(() => renderMarkdown(">".repeat(200) + table)).not.toThrow();
    expect(() => renderMarkdown("> ".repeat(30) + table.slice(1))).not.toThrow();
  });

  it("renders a table nested 20 blockquotes deep, the last working depth", () => {
    const html = renderMarkdown(">".repeat(20) + " | a | b |\n| - | - |\n| 1 | 2 |");
    expect(html).toContain("<table><thead><tr><th> a </th><th> b </th></tr></thead>");
    expect(html).toContain("<tbody><tr><td> 1 </td><td> 2 </td></tr></tbody>");
  });

  // Where only some of a table's three tokens fit, cells must not land as text inside `<table>`, and the row handler
  // must not re-feed forever.
  it.each([21, 22])(
    "keeps every character as text when depth %i leaves no room for a table's row and cell",
    (depth) => {
      const html = renderMarkdown(">".repeat(depth) + " | a | b |\n| - | - |\n| 1 | 2 |");
      expect(html).not.toContain("<table");
      expect(html).toContain("| a | b |");
      expect(html).toContain("| - | - |");
      expect(html).toContain("| 1 | 2 |");
    },
  );

  // Past the cap every delimiter reads as literal text. The promotion fallthrough consumes the newline too, so the
  // line-scoped state must still reset for the next line's `>` run.
  it("reads a continuation line's block prefix as markers at the cap", () => {
    const el = document.createElement("div");
    el.innerHTML = renderMarkdown(">".repeat(23) + " a\n" + ">".repeat(23) + " b");
    expect(el.textContent).toBe(" a\n b\n");
    expect(el.querySelectorAll("blockquote")).toHaveLength(23);
  });

  it("keeps the surplus prefix character as text one past the cap", () => {
    const el = document.createElement("div");
    el.innerHTML = renderMarkdown(">".repeat(24) + " a\n" + ">".repeat(24) + " b");
    expect(el.textContent).toBe("> a\n> b\n");
  });

  it("leaves a continuation line below the cap unchanged", () => {
    expect(renderMarkdown(">".repeat(22) + " a\n" + ">".repeat(22) + " b")).toBe(
      "<blockquote>".repeat(22) + "<p>a<br>b</p>" + "</blockquote>".repeat(22),
    );
  });

  it("opens a list on the line after a saturated prose line", () => {
    const html = renderMarkdown(">".repeat(23) + " a\n- i");
    expect(html).toContain("<ul><li>i</li></ul>");
  });

  it("opens a table on the line after a saturated prose line", () => {
    const html = renderMarkdown(">".repeat(23) + " a\n| x |\n| - |\n| 1 |");
    expect(html).toContain("<table><thead><tr><th> x </th></tr></thead>");
  });

  it("keeps a bare hash run as text when the token stack is saturated", () => {
    const html = renderMarkdown(">".repeat(23) + " ##");
    expect(html).not.toContain("<h2");
    expect(html).toContain("##");
  });

  it.each([
    ["*a*", "*"],
    ["**a**", "**"],
    ["_a_", "_"],
    ["__a__", "__"],
    ["~~a~~", "~~"],
    ["`a`", "`"],
    ["[a](b)", "["],
    ["![a](b)", "!["],
    ["$a$", "$"],
  ])("keeps the %s opener as text when the token stack is saturated", (body, opener) => {
    const html = renderMarkdown(">".repeat(22) + " " + body);
    expect(html).toContain(opener + "a");
    expect(html).not.toContain("<em");
    expect(html).not.toContain("<strong");
    expect(html).not.toContain("<code");
    expect(html).not.toContain("<a ");
    expect(html).not.toContain("<img");
  });

  // After a refused push, an attribute must not reach the enclosing blockquote (a fence info as `class`, a list
  // number as `start`).
  it.each([
    ["```js\ncode\n```", "class", "```js"],
    ["3. item", "start", "3. item"],
    ["3) item", "start", "3) item"],
  ])("puts no attribute on the blockquote for %j past the cap", (body, attr, literal) => {
    const html = renderMarkdown(">".repeat(23) + " " + body);
    expect(html).not.toContain(attr + "=");
    expect(html).toContain(literal);
  });
});

// A held character at the end of input or a line is literal text: `parser_end` flushes `pending` with a synthetic
// newline, which must not consume it as an opener.

describe("renderMarkdown end-of-input characters", () => {
  const cases: { name: string; input: string; expected: string | RegExp }[] = [
    { name: "trailing <", input: "ab<", expected: "<p>ab&lt;</p>" },
    { name: "lone <", input: "<", expected: "<p>&lt;</p>" },
    { name: "trailing [", input: "ab[", expected: "<p>ab[</p>" },
    { name: "trailing backslash", input: "ab\\", expected: "<p>ab\\</p>" },
    { name: "trailing backtick", input: "ab`", expected: "<p>ab`</p>" },
    { name: "[ before a newline", input: "ab[\ncd", expected: "<p>ab[<br>cd</p>" },
    { name: "backtick before a newline", input: "ab`\ncd", expected: "<p>ab`<br>cd</p>" },
    {
      name: "backslash newline is still a hard break",
      input: "ab\\\ncd",
      expected: "<p>ab<br>cd</p>",
    },
    { name: "< followed by text is unchanged", input: "ab<x", expected: "<p>ab&lt;x</p>" },
    { name: "< followed by a space is unchanged", input: "ab< ", expected: "<p>ab&lt; </p>" },
    { name: "<br> is still a line break", input: "a<br>b", expected: "<p>a<br>b</p>" },
    {
      name: "a closed code span is unchanged",
      input: "`code`",
      expected: "<p><code>code</code></p>",
    },
    {
      name: "a closed link is unchanged",
      input: "[a](https://e.com)",
      expected: '<p><a target="_blank" rel="noopener" href="https://e.com">a</a></p>',
    },
  ];

  it.each(cases)("$name", ({ input, expected }) => {
    const html = renderMarkdown(input);
    if (typeof expected === "string") {
      expect(html).toBe(expected);
    } else {
      expect(html).toMatch(expected);
    }
  });
});

// Angle autolinks (CommonMark 6.5), the only carve-out in the escaping of `<`. Every other angle run stays escaped
// text. Both forms go through the same scheme gate as every href.

describe("renderMarkdown angle autolinks", () => {
  const A = '<a target="_blank" rel="noopener"';

  const cases: { name: string; input: string; expected: string }[] = [
    {
      name: "an https autolink",
      input: "<https://example.com>",
      expected: `<p>${A} href="https://example.com">https://example.com</a></p>`,
    },
    {
      name: "an autolink in prose",
      input: "see <http://e.com> ok",
      expected: `<p>see ${A} href="http://e.com">http://e.com</a> ok</p>`,
    },
    {
      name: "an autolink followed immediately by text",
      input: "<http://e.com>x",
      expected: `<p>${A} href="http://e.com">http://e.com</a>x</p>`,
    },
    {
      name: "an email autolink",
      input: "<foo@bar.example.com>",
      expected: `<p>${A} href="mailto:foo@bar.example.com">foo@bar.example.com</a></p>`,
    },
    {
      name: "an explicit mailto autolink",
      input: "<mailto:a@b.com>",
      expected: `<p>${A} href="mailto:a@b.com">mailto:a@b.com</a></p>`,
    },
    {
      // The parser recognises the autolink; the gate refuses the URL, so the text stays and the href is dead.
      name: "an off-allowlist scheme keeps its text and loses its href",
      input: "<ftp://e.com/x>",
      expected: `<p>${A} href="#">ftp://e.com/x</a></p>`,
    },
    {
      name: "a scheme with digits, plus, dot and dash",
      input: "<my-scheme+v1.0:x>",
      expected: `<p>${A} href="#">my-scheme+v1.0:x</a></p>`,
    },
    {
      name: "an autolink in a heading",
      input: "## <https://e.com>",
      expected: `<h2>${A} href="https://e.com">https://e.com</a></h2>`,
    },
    {
      name: "an autolink in a table cell",
      input: "| <https://e.com> |\n| - |\n| x |",
      expected:
        `<table><thead><tr><th> ${A} href="https://e.com">https://e.com</a> </th></tr></thead>` +
        "<tbody><tr><td> x </td></tr></tbody></table>",
    },
  ];

  it.each(cases)("$name", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });

  // Not 6.5 autolinks, so they keep the escaped-text reading.
  const escaped: { name: string; input: string; expected: string }[] = [
    {
      name: "a Rust generic",
      input: "Vec<String> in Rust",
      expected: "<p>Vec&lt;String&gt; in Rust</p>",
    },
    { name: "a bare type parameter", input: "<String>", expected: "<p>&lt;String&gt;</p>" },
    {
      name: "an inline tag",
      input: "use <b>bold</b> here",
      expected: "<p>use &lt;b&gt;bold&lt;/b&gt; here</p>",
    },
    {
      name: "a comment",
      input: "text <!-- c --> more",
      expected: "<p>text &lt;!-- c --&gt; more</p>",
    },
    { name: "a block tag", input: "<div>", expected: "<p>&lt;div&gt;</p>" },
    { name: "a one-character scheme", input: "<a:b>", expected: "<p>&lt;a:b&gt;</p>" },
    { name: "a scheme with no colon", input: "<https>", expected: "<p>&lt;https&gt;</p>" },
    { name: "no scheme at all", input: "<foo>", expected: "<p>&lt;foo&gt;</p>" },
    {
      name: "a space inside the brackets",
      input: "<http://e.com x>",
      expected: "<p>&lt;http://e.com x&gt;</p>",
    },
    { name: "angle brackets in prose", input: "a < b > c", expected: "<p>a &lt; b &gt; c</p>" },
    { name: "a less-than between numbers", input: "5 < 6", expected: "<p>5 &lt; 6</p>" },
    { name: "a trailing less-than", input: "a<", expected: "<p>a&lt;</p>" },
    { name: "an empty pair", input: "a<>", expected: "<p>a&lt;&gt;</p>" },
    {
      name: "a run broken by a newline",
      input: "<http://e.com\nx>",
      expected: "<p>&lt;http://e.com<br>x&gt;</p>",
    },
    {
      // An anchor inside an anchor is invalid HTML.
      name: "an autolink inside a link label",
      input: "[a <https://e.com> b](http://x.com)",
      expected: `<p>${A} href="http://x.com">a &lt;https://e.com&gt; b</a></p>`,
    },
    {
      name: "an autolink in a code span",
      input: "`<https://e.com>`",
      expected: "<p><code>&lt;https://e.com&gt;</code></p>",
    },
    {
      name: "an autolink in a fenced block",
      input: "```\n<https://e.com>\n```",
      expected: '<pre class="code"><code>&lt;https://e.com&gt;</code></pre>',
    },
  ];

  it.each(escaped)("leaves $name as escaped text", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });

  // The `<br>` half of the same deferral, unchanged.
  const lineBreaks: { name: string; input: string; expected: string }[] = [
    { name: "<br>", input: "line<br>next", expected: "<p>line<br>next</p>" },
    { name: "<br/>", input: "line<br/>next", expected: "<p>line<br>next</p>" },
    { name: "<br />", input: "line<br />next", expected: "<p>line<br>next</p>" },
    { name: "<br  >", input: "line<br  >next", expected: "<p>line<br>next</p>" },
    { name: "<br / >", input: "line<br / >next", expected: "<p>line<br>next</p>" },
    { name: "<brx>", input: "line<brx>next", expected: "<p>line&lt;brx&gt;next</p>" },
    { name: "<br/x>", input: "line<br/x>next", expected: "<p>line&lt;br/x&gt;next</p>" },
  ];

  it.each(lineBreaks)("still reads $name as a line break or text", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });

  // The same gate as an inline link, so an autolink adds no scheme a destination could not.
  const blocked: { name: string; input: string }[] = [
    { name: "javascript", input: "<javascript:alert(1)>" },
    { name: "javascript in mixed case", input: "<JavaScript:alert(1)>" },
    { name: "javascript with an encoded colon", input: "<javascript&#58;alert(1)>" },
    { name: "javascript with an encoded letter", input: "<java&#115;cript:alert(1)>" },
    { name: "data", input: "<data:text/html,x>" },
    { name: "vbscript", input: "<vbscript:msgbox>" },
    { name: "file", input: "<file:///etc/passwd>" },
  ];

  it.each(blocked)("blocks a $name autolink", ({ input }) => {
    const html = renderMarkdown(input);
    // The anchor assertion keeps this honest: without it the case passes if autolinks never parse.
    expect(html).toContain(`${A} href="#">`);
    expect(html.toLowerCase()).not.toMatch(/href="(?:javascript|data|vbscript|file):/);
  });

  it("blocks a scheme split by a tab", () => {
    // isSafeUrl strips internal whitespace and a tab disqualifies the autolink, so this is text either way.
    expect(renderMarkdown("<java\tscript:alert(1)>")).toBe("<p>&lt;java\tscript:alert(1)&gt;</p>");
  });
});

// Character references (CommonMark 6.2). A decoded character goes to the text buffer, never re-parsed, so `&#42;` is
// a literal asterisk. A destination is decoded before the scheme gate, or `javascript&#58;` would pass the gate.

describe("renderMarkdown character references", () => {
  const A = '<a target="_blank" rel="noopener"';

  const cases: { name: string; input: string; expected: string }[] = [
    {
      name: "named references in prose",
      input: "5 &lt; 6 &amp; 7 &copy;",
      expected: "<p>5 &lt; 6 &amp; 7 ©</p>",
    },
    { name: "a decimal reference", input: "&#35; hash", expected: "<p># hash</p>" },
    { name: "a hex reference", input: "&#x1F600; emoji", expected: "<p>😀 emoji</p>" },
    {
      name: "a hex reference with an uppercase X",
      input: "&#X23; hash",
      expected: "<p># hash</p>",
    },
    {
      name: "the longest name in the table",
      input: "&thetasym;",
      expected: "<p>ϑ</p>",
    },
    { name: "a no-break space", input: "a&nbsp;b", expected: "<p>a&nbsp;b</p>" },
    { name: "a reference in a heading", input: "## &amp; head", expected: "<h2>&amp; head</h2>" },
    {
      name: "a reference in a link label",
      input: "[&amp;](http://e.com)",
      expected: `<p>${A} href="http://e.com">&amp;</a></p>`,
    },
    {
      name: "a reference in a table cell",
      input: "| &amp; |\n| - |\n| x |",
      expected:
        "<table><thead><tr><th> &amp; </th></tr></thead>" +
        "<tbody><tr><td> x </td></tr></tbody></table>",
    },
    { name: "an ampersand ahead of a reference", input: "&&amp;", expected: "<p>&amp;&amp;</p>" },

    {
      name: "an encoded asterisk does not emphasise",
      input: "&#42;not bold&#42;",
      expected: "<p>*not bold*</p>",
    },
    {
      name: "an encoded angle bracket opens no element",
      input: "&#60;script&#62;",
      expected: "<p>&lt;script&gt;</p>",
    },
    {
      name: "an encoded backtick opens no code span",
      input: "&#96;not code&#96;",
      expected: "<p>`not code`</p>",
    },

    {
      name: "an unknown name",
      input: "&nosuchentity; text",
      expected: "<p>&amp;nosuchentity; text</p>",
    },
    // A name WHATWG defines and HTML 4.01 does not is invalid here and stays literal (CommonMark).
    {
      name: "a WHATWG-only name",
      input: "&NotNestedGreaterGreater; text",
      expected: "<p>&amp;NotNestedGreaterGreater; text</p>",
    },
    {
      name: "a short WHATWG-only name",
      input: "&nvinfin; text",
      expected: "<p>&amp;nvinfin; text</p>",
    },
    {
      name: "a WHATWG-only name in a destination",
      input: "[a](http://e.com/?a=&nvinfin;)",
      expected: `<p>${A} href="http://e.com/?a=&amp;nvinfin;">a</a></p>`,
    },
    { name: "a name with no semicolon", input: "&amp x", expected: "<p>&amp;amp x</p>" },
    { name: "a bare ampersand", input: "a & b", expected: "<p>a &amp; b</p>" },
    { name: "a non-hex digit", input: "&#xZ;", expected: "<p>&amp;#xZ;</p>" },
    { name: "an empty numeric reference", input: "&#;", expected: "<p>&amp;#;</p>" },
    { name: "an ampersand before a newline", input: "a &\nb", expected: "<p>a &amp;<br>b</p>" },

    { name: "code point zero", input: "&#0;", expected: "<p>\ufffd</p>" },
    { name: "a code point past the last plane", input: "&#x110000;", expected: "<p>\ufffd</p>" },
    { name: "a surrogate code point", input: "&#xD800;", expected: "<p>\ufffd</p>" },

    {
      name: "a reference in a destination",
      input: "[a](http://e.com/?a=1&amp;b=2)",
      expected: `<p>${A} href="http://e.com/?a=1&amp;b=2">a</a></p>`,
    },
    {
      name: "an encoded colon does not smuggle a javascript scheme",
      input: "[a](javascript&#58;alert(1))",
      expected: `<p>${A} href="#">a</a></p>`,
    },
    {
      name: "an encoded letter does not smuggle a javascript scheme",
      input: "[a](java&#115;cript:alert(1))",
      expected: `<p>${A} href="#">a</a></p>`,
    },
    {
      name: "a hex-encoded letter does not smuggle a javascript scheme",
      input: "[a](&#x6a;avascript:alert(1))",
      expected: `<p>${A} href="#">a</a></p>`,
    },
    {
      name: "an encoded colon does not smuggle a data scheme",
      input: "![a](data&#58;text/html,x)",
      expected: '<p><img alt="a" src="#"></p>',
    },
    // The URL parser drops leading C0 controls and spaces before the scheme, so a decoded control in front is still
    // `javascript:`.
    {
      name: "a decoded control does not smuggle a javascript scheme",
      input: "[a](&#1;javascript:alert(1))",
      expected: `<p>${A} href="#">a</a></p>`,
    },
    {
      name: "a hex-encoded control does not smuggle a javascript scheme",
      input: "[a](&#x1f;javascript:alert(1))",
      expected: `<p>${A} href="#">a</a></p>`,
    },
    {
      name: "a decoded control between decoded spaces does not smuggle a javascript scheme",
      input: "[a](&#32;&#1;&#32;javascript:alert(1))",
      expected: `<p>${A} href="#">a</a></p>`,
    },
    {
      name: "a decoded control does not smuggle a data scheme",
      input: "![a](&#1;data:text/html,x)",
      expected: '<p><img alt="a" src="#"></p>',
    },
    {
      name: "an invalid reference in a destination stays literal",
      input: "[a](http://e.com/?a=&nosuch;)",
      expected: `<p>${A} href="http://e.com/?a=&amp;nosuch;">a</a></p>`,
    },
    {
      name: "a reference in a title",
      input: '[a](http://e.com "a &amp; b")',
      expected: `<p>${A} href="http://e.com" title="a &amp; b">a</a></p>`,
    },
  ];

  it.each(cases)("$name", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });

  // The named references measured in this workspace's markdown, pinned by name.
  const measured: { name: string; input: string; expected: string }[] = [
    { name: "&mdash;", input: "a &mdash; b", expected: "<p>a — b</p>" },
    { name: "&gt;", input: "7 &gt; 6", expected: "<p>7 &gt; 6</p>" },
    { name: "&lt;", input: "5 &lt; 6", expected: "<p>5 &lt; 6</p>" },
    { name: "&nbsp;", input: "a&nbsp;b", expected: "<p>a&nbsp;b</p>" },
    { name: "&amp;", input: "a &amp; b", expected: "<p>a &amp; b</p>" },
    { name: "&quot;", input: "say &quot;hi&quot;", expected: '<p>say "hi"</p>' },
    { name: "&middot;", input: "a &middot; b", expected: "<p>a · b</p>" },
    { name: "&copy;", input: "&copy; 2026", expected: "<p>© 2026</p>" },
    { name: "&ndash;", input: "1&ndash;2", expected: "<p>1–2</p>" },
  ];

  it.each(measured)("decodes the measured name $name", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });

  // HTML 4.01 does not define `&apos;`, so it sits inline with the other XML predefined names.
  it("decodes &apos;, which HTML 4.01 does not define", () => {
    expect(renderMarkdown("it&apos;s")).toBe("<p>it's</p>");
  });

  // Both maps are object literals, so a bare index would answer these with an inherited member.
  const inherited = [
    "constructor",
    "toString",
    "valueOf",
    "hasOwnProperty",
    "isPrototypeOf",
    "propertyIsEnumerable",
    "toLocaleString",
  ];

  it.each(inherited)("leaves &%s; literal, like any absent name", (name) => {
    expect(renderMarkdown(`&${name};`)).toBe(`<p>&amp;${name};</p>`);
  });

  it("leaves an inherited member literal in a link destination too", () => {
    expect(renderMarkdown("[a](&constructor;)")).toBe(`<p>${A} href="&amp;constructor;">a</a></p>`);
  });

  // No reference is recognised inside code; those tokens never reach the inline checks.
  const code: { name: string; input: string; expected: string }[] = [
    { name: "a code span", input: "`&amp;`", expected: "<p><code>&amp;amp;</code></p>" },
    {
      name: "a fenced block",
      input: "```\n&amp;\n```",
      expected: '<pre class="code"><code>&amp;amp;</code></pre>',
    },
    {
      name: "an indented block",
      input: "    &amp;",
      expected: '<pre class="code"><code>&amp;amp;</code></pre>',
    },
    {
      name: "a numeric reference in a code span",
      input: "`&#42;`",
      expected: "<p><code>&amp;#42;</code></p>",
    },
  ];

  it.each(code)("leaves a reference in $name alone", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });
});

// `1)` marks an ordered list too (CommonMark 5.2); changing delimiter starts a new list; over nine digits is no marker.

describe("renderMarkdown ordered list delimiters", () => {
  const cases: { name: string; input: string; expected: string }[] = [
    {
      name: "a paren-delimited list",
      input: "1) first\n2) second",
      expected: "<ol><li>first</li><li>second</li></ol>",
    },
    {
      name: "a paren-delimited list starting past one",
      input: "3) three\n4) four",
      expected: '<ol start="3"><li>three</li><li>four</li></ol>',
    },
    { name: "a single paren-delimited item", input: "1) only", expected: "<ol><li>only</li></ol>" },
    {
      name: "a delimiter change from a dot starts a new list",
      input: "1. first\n2) second",
      expected: '<ol><li>first</li></ol><ol start="2"><li>second</li></ol>',
    },
    {
      name: "a delimiter change from a paren starts a new list",
      input: "1) first\n2. second",
      expected: '<ol><li>first</li></ol><ol start="2"><li>second</li></ol>',
    },
    {
      name: "an indented paren marker nests inside a dot list",
      input: "1. a\n   1) b",
      expected: "<ol><li>a<ol><li>b</li></ol></li></ol>",
    },
    {
      name: "a nine-digit marker is still a list",
      input: "123456789. x",
      expected: '<ol start="123456789"><li>x</li></ol>',
    },
    {
      name: "a ten-digit marker is not a list",
      input: "1234567890. x",
      expected: "<p>1234567890. x</p>",
    },
    { name: "a paren with no space is not a list", input: "1)first", expected: "<p>1)first</p>" },
    {
      name: "a paren inside prose is not a list",
      input: "see 1) this",
      expected: "<p>see 1) this</p>",
    },
    {
      name: "a dot-delimited list is unchanged",
      input: "1. first\n2. second",
      expected: "<ol><li>first</li><li>second</li></ol>",
    },
  ];

  it.each(cases)("$name", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });
});

// An ATX closing sequence (CommonMark 4.2): a `#` run after whitespace, followed only by whitespace, is syntax.

describe("renderMarkdown ATX closing sequence", () => {
  const cases: { name: string; input: string; expected: string }[] = [
    { name: "a two-hash closing run", input: "## heading ##", expected: "<h2>heading</h2>" },
    { name: "a one-hash closing run", input: "## heading #", expected: "<h2>heading</h2>" },
    { name: "a longer closing run", input: "## heading ###", expected: "<h2>heading</h2>" },
    { name: "two spaces before the run", input: "## heading  ##", expected: "<h2>heading</h2>" },
    {
      name: "trailing spaces after the run",
      input: "## heading ##   ",
      expected: "<h2>heading</h2>",
    },
    { name: "a tab before the run", input: "## heading\t##", expected: "<h2>heading</h2>" },
    { name: "a tab after the run", input: "## heading ##\t", expected: "<h2>heading</h2>" },
    {
      name: "only the last run closes the heading",
      input: "## heading ## #",
      expected: "<h2>heading ##</h2>",
    },
    {
      name: "a single hash before the closing run is content",
      input: "## heading # #",
      expected: "<h2>heading #</h2>",
    },
    {
      name: "inline code survives the strip",
      input: "## a `#` b ##",
      expected: "<h2>a <code>#</code> b</h2>",
    },
    {
      name: "emphasis survives the strip",
      input: "## a *b* ##",
      expected: "<h2>a <em>b</em></h2>",
    },
    { name: "an escaped hash is content", input: "## \\## ##", expected: "<h2>##</h2>" },
    { name: "two headings in a row", input: "# h #\n# i #", expected: "<h1>h</h1><h1>i</h1>" },
    {
      name: "a heading inside a blockquote",
      input: "> ## h ##",
      expected: "<blockquote><h2>h</h2></blockquote>",
    },
    {
      name: "a following paragraph is unaffected",
      input: "## h ##\ntext",
      expected: "<h2>h</h2><p>text</p>",
    },

    { name: "two hashes alone", input: "##", expected: "<h2></h2>" },
    { name: "one hash alone", input: "#", expected: "<h1></h1>" },
    { name: "six hashes alone", input: "######", expected: "<h6></h6>" },
    { name: "seven hashes alone is a paragraph", input: "#######", expected: "<p>#######</p>" },
    { name: "an opening run and a closing run", input: "## ##", expected: "<h2></h2>" },
    { name: "an opening run and one hash", input: "## #", expected: "<h2></h2>" },
    { name: "a bare run then a line", input: "##\nnext", expected: "<h2></h2><p>next</p>" },
  ];

  it.each(cases)("$name", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });

  // A `#` run that is not a closing sequence stays where it was typed.
  const unchanged: { name: string; input: string; expected: string }[] = [
    { name: "a run followed by text", input: "## heading #foo", expected: "<h2>heading #foo</h2>" },
    {
      name: "a run with no space before it",
      input: "## heading##",
      expected: "<h2>heading##</h2>",
    },
    {
      name: "a run with text after it",
      input: "## heading ## x",
      expected: "<h2>heading ## x</h2>",
    },
    { name: "a trailing space with no run", input: "## heading ", expected: "<h2>heading</h2>" },
    { name: "a plain heading", input: "## heading", expected: "<h2>heading</h2>" },
    { name: "a hash-space-only heading", input: "## ", expected: "<h2></h2>" },
    // A heading in a list item is a nested block this parser does not open; the run stays literal.
    {
      name: "a heading inside a list item",
      input: "- ## h ##",
      expected: "<ul><li>## h ##</li></ul>",
    },
  ];

  it.each(unchanged)("leaves $name unchanged", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });
});

// A closing fence is alone on its line (CommonMark 4.5): at most three spaces, at least as many backticks, then whitespace.

describe("renderMarkdown code fence close rule", () => {
  const cases: { name: string; input: string; expected: string }[] = [
    {
      name: "a backtick run after content does not close the fence",
      input: "```\nfoo ```\nbar\n",
      expected: '<pre class="code"><code>foo ```\nbar\n</code></pre>',
    },
    {
      name: "a backtick run glued to content keeps the content intact",
      input: "```\nfoo```\nbar\n",
      expected: '<pre class="code"><code>foo```\nbar\n</code></pre>',
    },
    {
      name: "a four-space indented fence is code content",
      input: "```\nfoo\n    ```\nbar\n",
      expected: '<pre class="code"><code>foo\n    ```\nbar\n</code></pre>',
    },
    {
      name: "a three-space indented fence still closes",
      input: "```\nfoo\n   ```\nbar\n",
      expected: '<pre class="code"><code>foo</code></pre><p>bar</p>',
    },
    {
      name: "a longer run than the opener closes",
      input: "```\nfoo\n`````\nbar\n",
      expected: '<pre class="code"><code>foo</code></pre><p>bar</p>',
    },
    {
      name: "a shorter run than the opener does not close",
      input: "````\nfoo\n```\nbar\n",
      expected: '<pre class="code"><code>foo\n```\nbar\n</code></pre>',
    },
    {
      name: "trailing space after the run still closes",
      input: "```\nfoo\n``` \nbar\n",
      expected: '<pre class="code"><code>foo</code></pre><p>bar</p>',
    },
    {
      name: "trailing tab after the run still closes",
      input: "```\nfoo\n```\t\nbar\n",
      expected: '<pre class="code"><code>foo</code></pre><p>bar</p>',
    },
    {
      name: "a run followed by text does not close",
      input: "```\nfoo ``` bar\n```\nend\n",
      expected: '<pre class="code"><code>foo ``` bar</code></pre><p>end</p>',
    },
    {
      name: "an info string mentioning backticks is unaffected",
      input: "```md\nuse ``` to fence\n```\nafter\n",
      expected:
        '<pre class="code"><code class="language-md">use ``` to fence</code></pre><p>after</p>',
    },
    {
      name: "an unterminated fence keeps its language",
      input: "```js\nlet x=1;",
      expected: '<pre class="code"><code class="language-js">let x=1;</code></pre>',
    },
    {
      name: "an empty fence closes on the first line",
      input: "```\n```\nafter\n",
      expected: '<pre class="code"><code></code></pre><p>after</p>',
    },
  ];

  it.each(cases)("$name", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });
});

// Balanced brackets in a link or image label (CommonMark 6.3).

describe("renderMarkdown brackets inside a link label", () => {
  const A = '<a target="_blank" rel="noopener"';

  it("keeps a bracketed run inside the label and keeps the href", () => {
    expect(renderMarkdown("[a [b] c](https://example.com)")).toBe(
      `<p>${A} href="https://example.com">a [b] c</a></p>`,
    );
  });

  it("handles brackets nested more than one deep", () => {
    expect(renderMarkdown("[a [b [c] d] e](https://e.com)")).toBe(
      `<p>${A} href="https://e.com">a [b [c] d] e</a></p>`,
    );
  });

  it("keeps a bracketed run inside an image alt", () => {
    expect(renderMarkdown("![alt [x] y](https://e.com/i.png)")).toBe(
      '<p><img alt="alt [x] y" src="https://e.com/i.png" loading="lazy"></p>',
    );
  });

  it("leaves a plain link unchanged", () => {
    expect(renderMarkdown("[a](https://e.com)")).toBe(`<p>${A} href="https://e.com">a</a></p>`);
  });

  it("renders a label with no destination as the text that was typed", () => {
    // Not a link: an href-less anchor styled as link text did nothing.
    expect(renderMarkdown("[a]b")).toBe("<p>[a]b</p>");
  });

  const noDestination: { name: string; input: string; expected: string }[] = [
    { name: "an image label", input: "![a]b", expected: "<p>![a]b</p>" },
    { name: "a label at end of input", input: "[a]", expected: "<p>[a]</p>" },
    { name: "a label before a newline", input: "[a]\nnext", expected: "<p>[a]<br>next</p>" },
    { name: "nested brackets", input: "[a [b] c]d", expected: "<p>[a [b] c]d</p>" },
    { name: "markup inside the label", input: "[a *b*]c", expected: "<p>[a <em>b</em>]c</p>" },
    {
      name: "a bracketed word in prose",
      input: "see [TODO] later",
      expected: "<p>see [TODO] later</p>",
    },
    {
      name: "a footnote marker",
      input: "as noted [1] above",
      expected: "<p>as noted [1] above</p>",
    },
    {
      name: "a parenthesis that is not a destination",
      input: "[a] (not a destination)",
      expected: "<p>[a] (not a destination)</p>",
    },
    {
      // What CommonMark renders for an undefined reference; definitions need a document-scoped map.
      name: "an unresolved reference link",
      input: "[a][ref]\n\n[ref]: http://e.com",
      expected: "<p>[a][ref]</p><p>[ref]: " + `${A} href="http://e.com">http://e.com</a></p>`,
    },
  ];

  it.each(noDestination)("renders $name as text", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });

  const unchanged: { name: string; input: string; expected: string }[] = [
    {
      name: "a checked task item",
      input: "- [x] done",
      expected:
        '<ul><li><input type="checkbox" disabled="" aria-label="Task item" checked=""> done</li></ul>',
    },
    {
      name: "an unchecked task item",
      input: "- [ ] todo",
      expected: '<ul><li><input type="checkbox" disabled="" aria-label="Task item"> todo</li></ul>',
    },
  ];

  it.each(unchanged)("leaves $name unchanged", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });

  it("leaves a partially arrived label alone while it streams", () => {
    // The block has not closed: the label sits in an href-less anchor until `(` or another character arrives.
    const el = document.createElement("div");
    const r = createMarkdownStream(el, { flushIntervalMs: 0 });
    r.writeDelta("see [the label");
    expect(el.querySelector("a")?.textContent).toBe("the labe");
    r.writeDelta("](https://e.com) end");
    r.end();
    expect(el.querySelector("a")?.getAttribute("href")).toBe("https://e.com");
  });
});

// Destinations and titles (CommonMark 6.3): quote or paren titles, angle-bracketed destinations, balanced parens.
// Any other shape keeps the whole run as the href.

describe("renderMarkdown link destinations and titles", () => {
  const A = '<a target="_blank" rel="noopener"';

  const cases: { name: string; input: string; expected: string }[] = [
    {
      name: "a double-quoted title",
      input: '[a](http://e.com "the title")',
      expected: `<p>${A} href="http://e.com" title="the title">a</a></p>`,
    },
    {
      name: "a single-quoted title",
      input: "[a](http://e.com 'the title')",
      expected: `<p>${A} href="http://e.com" title="the title">a</a></p>`,
    },
    {
      name: "a parenthesised title",
      input: "[a](http://e.com (the title))",
      expected: `<p>${A} href="http://e.com" title="the title">a</a></p>`,
    },
    {
      name: "a tab separating the title",
      input: '[a](http://e.com\t"t")',
      expected: `<p>${A} href="http://e.com" title="t">a</a></p>`,
    },
    {
      name: "an image title",
      input: '![a](http://e.com/i.png "t")',
      expected: '<p><img alt="a" src="http://e.com/i.png" loading="lazy" title="t"></p>',
    },
    {
      name: "an escaped quote inside a title",
      input: '[a](http://e.com "a \\" b")',
      expected: `<p>${A} href="http://e.com" title="a &quot; b">a</a></p>`,
    },
    {
      name: "an angle-bracketed destination",
      input: "[a](<http://e.com>)",
      expected: `<p>${A} href="http://e.com">a</a></p>`,
    },
    {
      // A space inside the brackets is legal; this parser never rewrites a URL.
      name: "a space inside an angle-bracketed destination",
      input: "[a](<http://e.com/a b>)",
      expected: `<p>${A} href="http://e.com/a b">a</a></p>`,
    },
    {
      name: "an angle-bracketed destination with a title",
      input: '[a](<http://e.com> "t")',
      expected: `<p>${A} href="http://e.com" title="t">a</a></p>`,
    },
    {
      name: "balanced parentheses in a destination",
      input: "[a](http://e.com/x(1))",
      expected: `<p>${A} href="http://e.com/x(1)">a</a></p>`,
    },
    {
      name: "parentheses nested two deep in a destination",
      input: "[a](http://e.com/x(y(z)))",
      expected: `<p>${A} href="http://e.com/x(y(z))">a</a></p>`,
    },
    {
      // A recorded divergence: both references render this literally; keeping the title loses nothing.
      name: "a title with no destination",
      input: '[a]( "the title")',
      expected: `<p>${A} href="" title="the title">a</a></p>`,
    },
    {
      name: "the scheme gate still fires on a link that carries a title",
      input: '[a](javascript:alert(1) "t")',
      expected: `<p>${A} href="#" title="t">a</a></p>`,
    },
  ];

  it.each(cases)("$name", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });

  // A run that is not `destination [whitespace title]` keeps the whole run as the href.
  const characterization: { name: string; input: string; expected: string }[] = [
    {
      name: "an unterminated title",
      input: '[a](http://e.com "the title)',
      expected: `<p>${A} href="http://e.com &quot;the title">a</a></p>`,
    },
    {
      name: "a space in a destination",
      input: "[a](http://e.com/a b)",
      expected: `<p>${A} href="http://e.com/a b">a</a></p>`,
    },
    {
      name: "text after a title",
      input: '[a](http://e.com "t" x)',
      expected: `<p>${A} href="http://e.com &quot;t&quot; x">a</a></p>`,
    },
    {
      name: "an empty destination",
      input: "[a]()",
      expected: `<p>${A} href="">a</a></p>`,
    },
    {
      name: "a plain link",
      input: "[a](http://e.com)",
      expected: `<p>${A} href="http://e.com">a</a></p>`,
    },
  ];

  it.each(characterization)("keeps today's reading for $name", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });
});

// CJK punctuation alone does not end a URL (real URLs carry it). The decidable case: a separator then a backtick,
// which RFC 3986 excludes, or the code span's opener is eaten.

describe("renderMarkdown bare URL with CJK punctuation", () => {
  it("cuts the URL at a separator followed by a backtick", () => {
    expect(renderMarkdown("见 https://example.com/2137，`96ed647b`）")).toBe(
      '<p>见 <a target="_blank" rel="noopener" href="https://example.com/2137">' +
        "https://example.com/2137</a>，<code>96ed647b</code>）</p>",
    );
  });

  it("leaves a separator followed by text in the URL (characterization)", () => {
    // Real URLs carry these characters raw, so the character alone proves nothing.
    expect(renderMarkdown("https://e.com/a，b")).toBe(
      '<p><a target="_blank" rel="noopener" href="https://e.com/a，b">https://e.com/a，b</a></p>',
    );
  });

  it("keeps CJK punctuation inside a URL that a space terminates", () => {
    expect(renderMarkdown("https://zh.wikipedia.org/wiki/苹果（公司） ok")).toBe(
      '<p><a target="_blank" rel="noopener" href="https://zh.wikipedia.org/wiki/苹果（公司">' +
        "https://zh.wikipedia.org/wiki/苹果（公司</a>） ok</p>",
    );
  });

  it("keeps a sentence-ender out of the href when a space follows", () => {
    expect(renderMarkdown("见 https://example.com。 然后")).toBe(
      '<p>见 <a target="_blank" rel="noopener" href="https://example.com">' +
        "https://example.com</a>。 然后</p>",
    );
  });

  it("leaves a URL abutting its opening ** unlinked (characterization)", () => {
    // handleCommon's emphasis arm leaves `**` in `pending`, so the raw-URL entry is never reached.
    expect(renderMarkdown("**https://example.com**")).toBe(
      "<p><strong>https://example.com</strong></p>",
    );
  });
});

// An unclosed inline opener: the parser cannot un-open a token, so the renderer unwraps the element at block end and
// restores the literal.

describe("renderMarkdown unclosed inline openers", () => {
  const cases: { name: string; input: string; expected: string }[] = [
    { name: "unclosed **", input: "a **b", expected: "<p>a **b</p>" },
    { name: "unclosed *", input: "a *b", expected: "<p>a *b</p>" },
    { name: "unclosed _", input: "a _b", expected: "<p>a _b</p>" },
    { name: "unclosed __", input: "a __b", expected: "<p>a __b</p>" },
    { name: "unclosed ~~", input: "a ~~b", expected: "<p>a ~~b</p>" },
    { name: "unclosed backtick", input: "the ` char", expected: "<p>the ` char</p>" },
    { name: "unclosed double backtick", input: "a ``b", expected: "<p>a ``b</p>" },
    { name: "unclosed [", input: "random[0,500)", expected: "<p>random[0,500)</p>" },
    { name: "unclosed ![", input: "an ![img", expected: "<p>an ![img</p>" },
    { name: "unclosed $", input: "a $x^2", expected: "<p>a $x^2</p>" },
    // The restored delimiter is the one consumed; marotte reads `\\(` as a math opener.
    { name: "unclosed \\(", input: "a \\(x^2", expected: "<p>a \\(x^2</p>" },
    {
      name: "the next paragraph is unaffected",
      input: "a **b\n\nnext para",
      expected: "<p>a **b</p><p>next para</p>",
    },
    { name: "inside a heading", input: "# a *b", expected: "<h1>a *b</h1>" },
    {
      name: "inside a blockquote",
      input: "> a *b",
      expected: "<blockquote><p>a *b</p></blockquote>",
    },
    {
      name: "inside a list item",
      input: "- a ` b\n- c",
      expected: "<ul><li>a ` b</li><li>c</li></ul>",
    },
    { name: "two nested unclosed runs", input: "a **b *c", expected: "<p>a **b *c</p>" },
    {
      name: "a soft break does not close a code span",
      input: "a ` b\nc",
      expected: "<p>a ` b<br>c</p>",
    },
    {
      // CommonMark resolves the link before emphasis, so the `*` is literal inside the label.
      name: "an unclosed run inside a link label leaves the link intact",
      input: "[a *b](https://e.com)",
      expected: '<p><a target="_blank" rel="noopener" href="https://e.com">a *b</a></p>',
    },
  ];

  it.each(cases)("$name", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });

  const unchanged: { name: string; input: string; expected: string }[] = [
    { name: "closed **", input: "a **b** c", expected: "<p>a <strong>b</strong> c</p>" },
    { name: "closed *", input: "*em*", expected: "<p><em>em</em></p>" },
    { name: "closed _", input: "_em_", expected: "<p><em>em</em></p>" },
    { name: "closed ~~", input: "~~gone~~", expected: "<p><s>gone</s></p>" },
    { name: "closed backtick", input: "`code`", expected: "<p><code>code</code></p>" },
    {
      name: "closed link",
      input: "[x](http://e.com)",
      expected: '<p><a target="_blank" rel="noopener" href="http://e.com">x</a></p>',
    },
    {
      name: "closed image",
      input: "![r](http://e.com/r.png)",
      expected: '<p><img alt="r" src="http://e.com/r.png" loading="lazy"></p>',
    },
    {
      name: "closed emphasis inside a link label",
      input: "[a *b* c](https://e.com)",
      expected: '<p><a target="_blank" rel="noopener" href="https://e.com">a <em>b</em> c</a></p>',
    },
  ];

  it.each(unchanged)("leaves $name unchanged", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });

  it("keeps a mid-arrival link streaming as it does today", () => {
    // The block has not closed: the label sits in an href-less anchor until `)` lands.
    const el = document.createElement("div");
    const r = createMarkdownStream(el, { flushIntervalMs: 0 });
    r.writeDelta("see [the label");
    // One character short: the parser holds the last one to disambiguate.
    expect(el.querySelector("a")?.textContent).toBe("the labe");
    r.writeDelta("](https://e.com) end");
    r.end();
    expect(el.querySelector("a")?.getAttribute("href")).toBe("https://e.com");
    expect(el.textContent).toBe("see the label end");
  });

  it("keeps a mid-arrival code span streaming as it does today", () => {
    const el = document.createElement("div");
    const r = createMarkdownStream(el, { flushIntervalMs: 0 });
    r.writeDelta("run `npm ");
    expect(el.querySelector("code")?.textContent).toBe("npm");
    r.writeDelta("test` now");
    r.end();
    expect(el.querySelector("code")?.textContent).toBe("npm test");
    expect(el.textContent).toBe("run npm test now");
  });
});

// GFM tables: a delimiter row is required, alignment is emitted, trailing whitespace is not a cell.

describe("renderMarkdown GFM tables", () => {
  const HEAD = "<table><thead><tr><th> a </th><th> b </th></tr></thead>";
  const BODY = "<tbody><tr><td> 1 </td><td> 2 </td></tr></tbody></table>";

  const cases: { name: string; input: string; expected: string }[] = [
    { name: "a real table", input: "| a | b |\n| - | - |\n| 1 | 2 |", expected: HEAD + BODY },
    {
      name: "a trailing space on the header row",
      input: "| a | b | \n| - | - |\n| 1 | 2 |",
      expected: HEAD + BODY,
    },
    {
      name: "a trailing tab on the header row",
      input: "| a | b |\t\n| - | - |\n| 1 | 2 |",
      expected: HEAD + BODY,
    },
    {
      name: "two trailing spaces on the header row",
      input: "| a | b |  \n| - | - |\n| 1 | 2 |",
      expected: HEAD + BODY,
    },
    {
      name: "a trailing space on a body row",
      input: "| a |\n| - |\n| 1 | \n| 2 |",
      expected:
        "<table><thead><tr><th> a </th></tr></thead>" +
        "<tbody><tr><td> 1 </td></tr><tr><td> 2 </td></tr></tbody></table>",
    },
    {
      name: "an intentionally empty middle cell",
      input: "| a | | b |\n| - | - | - |\n| 1 | 2 | 3 |",
      expected:
        "<table><thead><tr><th> a </th><th> </th><th> b </th></tr></thead>" +
        "<tbody><tr><td> 1 </td><td> 2 </td><td> 3 </td></tr></tbody></table>",
    },

    {
      // GFM's escape for a pipe in a cell; counting header cells otherwise loses the whole table.
      name: "an escaped pipe in the header",
      input: "| a \\| b |\n| - |\n| 1 |",
      expected:
        "<table><thead><tr><th> a | b </th></tr></thead>" +
        "<tbody><tr><td> 1 </td></tr></tbody></table>",
    },
    {
      name: "an escaped pipe in a header and a body cell",
      input: "| cmd \\| grep | note |\n| - | - |\n| ls \\| wc | ok |",
      expected:
        "<table><thead><tr><th> cmd | grep </th><th> note </th></tr></thead>" +
        "<tbody><tr><td> ls | wc </td><td> ok </td></tr></tbody></table>",
    },
    {
      name: "an escaped pipe in a body cell keeps the row's cell count",
      input: "| a | b |\n| - | - |\n| 1 \\| x | 2 |",
      expected: HEAD + "<tbody><tr><td> 1 | x </td><td> 2 </td></tr></tbody></table>",
    },
    {
      // One header cell over two delimiter cells, which GFM reads as a paragraph too.
      name: "an escaped pipe does not add a header cell",
      input: "| a \\| b |\n| - | - |\n| 1 | 2 |",
      expected: "<p>| a | b |<br>| - | - |<br>| 1 | 2 |</p>",
    },
    {
      name: "an escaped pipe closing the header row",
      input: "| a \\| b | c |\n| - | - |\n| 1 | 2 |",
      expected:
        "<table><thead><tr><th> a | b </th><th> c </th></tr></thead>" +
        "<tbody><tr><td> 1 </td><td> 2 </td></tr></tbody></table>",
    },
    {
      // The row handlers open a cell for it, so the count does too.
      name: "an empty header cell is still a cell",
      input: "||\n| - |\n| 1 |",
      expected:
        "<table><thead><tr><th></th></tr></thead>" + "<tbody><tr><td> 1 </td></tr></tbody></table>",
    },

    {
      name: "a leading pipe in prose is not a table",
      input: "| leading pipe in prose",
      expected: "<p>| leading pipe in prose</p>",
    },
    { name: "a lone pipe is not a table", input: "|", expected: "<p>|</p>" },
    {
      name: "a pipe line with no delimiter row is not a table",
      input: "| a | b |\nnot a table",
      expected: "<p>| a | b |<br>not a table</p>",
    },
    {
      name: "a delimiter row with the wrong cell count is not a delimiter row",
      input: "| a | b |\n| - |\n| 1 | 2 |",
      expected: "<p>| a | b |<br>| - |<br>| 1 | 2 |</p>",
    },
    {
      name: "a pipeless line cannot match a two-cell header",
      input: "| a | b |\n---\n| 1 | 2 |",
      expected: "<p>| a | b |</p><hr><p>| 1 | 2 |</p>",
    },
    {
      // A delimiter row needs no pipe; GFM renders this as a table too.
      name: "a single-column delimiter row needs no pipe",
      input: "| a |\n---\n| 1 |",
      expected:
        "<table><thead><tr><th> a </th></tr></thead>" +
        "<tbody><tr><td> 1 </td></tr></tbody></table>",
    },
    {
      name: "a table starting on the second line of a rejected candidate still opens",
      input: "| a | b |\n| x |\n| - |\n| 1 |",
      expected:
        "<p>| a | b |</p><table><thead><tr><th> x </th></tr></thead>" +
        "<tbody><tr><td> 1 </td></tr></tbody></table>",
    },
    {
      name: "colon forms are valid delimiter cells",
      input: "| a |\n| :- |\n| 1 |",
      expected:
        '<table><thead><tr><th style="text-align:left"> a </th></tr></thead>' +
        '<tbody><tr><td style="text-align:left"> 1 </td></tr></tbody></table>',
    },
    {
      // A tab is whitespace in a delimiter cell; the character gate must admit what the validity test accepts.
      name: "tabs around a delimiter cell",
      input: "| a |\n|\t-\t|\n| 1 |",
      expected:
        "<table><thead><tr><th> a </th></tr></thead>" +
        "<tbody><tr><td> 1 </td></tr></tbody></table>",
    },
    {
      name: "a tab inside a two-column delimiter row",
      input: "| a | b |\n|\t- | - |\n| 1 | 2 |",
      expected: HEAD + BODY,
    },
    {
      name: "a trailing tab on the delimiter row",
      input: "| a | b |\n| - | - |\t\n| 1 | 2 |",
      expected: HEAD + BODY,
    },
    {
      name: "a tab does not stop alignment being read",
      input: "| a |\n| :-\t|\n| 1 |",
      expected:
        '<table><thead><tr><th style="text-align:left"> a </th></tr></thead>' +
        '<tbody><tr><td style="text-align:left"> 1 </td></tr></tbody></table>',
    },
    {
      name: "a header-only table has no tbody",
      input: "| h |\n| - |",
      expected: "<table><thead><tr><th> h </th></tr></thead></table>",
    },
    { name: "an inline pipe is not a table", input: "a | b", expected: "<p>a | b</p>" },
    {
      name: "a lone pipe line after a table is not a second table",
      input: "| a |\n| - |\n| 1 |\n\n| 2 |",
      expected:
        "<table><thead><tr><th> a </th></tr></thead>" +
        "<tbody><tr><td> 1 </td></tr></tbody></table><p>| 2 |</p>",
    },
    {
      name: "a table still interrupts a paragraph",
      input: "text\n| a | b |\n| - | - |\n| 1 | 2 |",
      expected: "<p>text</p>" + HEAD + BODY,
    },
    {
      name: "an indented table still parses",
      input: "   | a | b |\n   | - | - |\n| 1 | 2 |",
      expected: HEAD + BODY,
    },

    {
      name: "all three alignments",
      input: "| a | b | c |\n|:--|:-:|--:|\n| 1 | 2 | 3 |",
      expected:
        '<table><thead><tr><th style="text-align:left"> a </th>' +
        '<th style="text-align:center"> b </th><th style="text-align:right"> c </th></tr></thead>' +
        '<tbody><tr><td style="text-align:left"> 1 </td>' +
        '<td style="text-align:center"> 2 </td><td style="text-align:right"> 3 </td></tr></tbody></table>',
    },
    {
      name: "a colon-free delimiter row asks for no alignment",
      input: "| a | b |\n| --- | --- |\n| 1 | 2 |",
      expected: HEAD + BODY,
    },
    {
      name: "alignment is per column",
      input: "| a | b |\n|:--| --- |\n| 1 | 2 |",
      expected:
        '<table><thead><tr><th style="text-align:left"> a </th><th> b </th></tr></thead>' +
        '<tbody><tr><td style="text-align:left"> 1 </td><td> 2 </td></tr></tbody></table>',
    },
    {
      name: "a short body row keeps its own columns' alignment",
      input: "| a | b | c |\n|:--|:-:|--:|\n| 1 |",
      expected:
        '<table><thead><tr><th style="text-align:left"> a </th>' +
        '<th style="text-align:center"> b </th><th style="text-align:right"> c </th></tr></thead>' +
        '<tbody><tr><td style="text-align:left"> 1 </td></tr></tbody></table>',
    },

    {
      name: "an unclosed run does not swallow the cell delimiters",
      input: "| a **b | c |\n| - | - |\n| 1 | 2 |",
      expected: "<table><thead><tr><th> a **b </th><th> c </th></tr></thead>" + BODY,
    },
    {
      name: "an intraword underscore in a cell stays literal",
      input: "\n| a_b | c |\n|---|---|\n| d_e | f |\n",
      expected:
        "<table><thead><tr><th> a_b </th><th> c </th></tr></thead>" +
        "<tbody><tr><td> d_e </td><td> f </td></tr></tbody></table>",
    },
    {
      // Not GFM (which nests the table); routing a block prefix through a held row is not attempted. Characterization.
      name: "a pipe line inside a blockquote is text (characterization)",
      input: "> | a | b |\n> | - | - |\n> | 1 | 2 |",
      expected: "<blockquote><p>| a | b |<br>| - | - |<br>| 1 | 2 |</p></blockquote>",
    },
    {
      // GFM reads `- ` at line start as a bullet, which outranks the delimiter row; `-|-` stays a candidate.
      name: "a bullet-marker delimiter row is not a delimiter row",
      input: "| a | b |\n- | -",
      expected: "<p>| a | b |</p><ul><li>| -</li></ul>",
    },
    {
      name: "a bullet-marker delimiter row with a body row underneath",
      input: "| a | b |\n- | -\n| 1 | 2 |",
      expected: "<p>| a | b |</p><ul><li>| -<br>| 1 | 2 |</li></ul>",
    },
    {
      name: "a star-marker delimiter row is not a delimiter row",
      input: "| a |\n* | *",
      expected: "<p>| a |</p><ul><li>| *</li></ul>",
    },
    {
      // Both references read this as setext, which this parser lacks. No text is lost. Characterization.
      name: "a single-cell header over a bare bullet (characterization)",
      input: "| a |\n- ",
      expected: "<p>| a |</p><ul><li></li></ul>",
    },
    {
      name: "a multi-dash delimiter row with no pipe is still a delimiter row",
      input: "| a | b |\n--- | ---\n| 1 | 2 |",
      expected: HEAD + BODY,
    },
    {
      // GFM truncates long rows; truncating deletes text, so rows keep their own cells. Characterization.
      name: "a body row is not normalised to the header's cell count (characterization)",
      input: "| a | b |\n| - | - |\n| 1 |\n| 1 | 2 | 3 |",
      expected:
        HEAD +
        "<tbody><tr><td> 1 </td></tr>" +
        "<tr><td> 1 </td><td> 2 </td><td> 3 </td></tr></tbody></table>",
    },

    {
      // GFM makes the closing pipe optional; the cell handler's newline arm closes the last cell.
      name: "a header row with no closing pipe",
      input: "| a\n| - |\n| 1 |",
      expected:
        "<table><thead><tr><th> a</th></tr></thead>" +
        "<tbody><tr><td> 1 </td></tr></tbody></table>",
    },
    {
      name: "an escaped pipe closing a header row with no closing pipe",
      input: "| a \\|\n| - |\n| 1 |",
      expected:
        "<table><thead><tr><th> a |</th></tr></thead>" +
        "<tbody><tr><td> 1 </td></tr></tbody></table>",
    },
    {
      name: "two header cells with no closing pipe",
      input: "| a | b\n| - | - |\n| 1 | 2 |",
      expected: "<table><thead><tr><th> a </th><th> b</th></tr></thead>" + BODY,
    },
    {
      name: "three header cells with no closing pipe",
      input: "| a | b | c\n| - | - | - |\n| 1 | 2 | 3 |",
      expected:
        "<table><thead><tr><th> a </th><th> b </th><th> c</th></tr></thead>" +
        "<tbody><tr><td> 1 </td><td> 2 </td><td> 3 </td></tr></tbody></table>",
    },
    {
      name: "no closing pipe on any row",
      input: "| a\n| -\n| 1",
      expected:
        "<table><thead><tr><th> a</th></tr></thead>" +
        "<tbody><tr><td> 1</td></tr></tbody></table>",
    },

    {
      // The cell walker keeps a code span intact, so the delimiter-row count must too.
      name: "a code span holding a pipe is one header cell",
      input: "| `a | b` |\n| - |\n| 1 |",
      expected:
        "<table><thead><tr><th> <code>a | b</code> </th></tr></thead>" +
        "<tbody><tr><td> 1 </td></tr></tbody></table>",
    },
    {
      name: "a two-column delimiter row does not match a code-span header",
      input: "| `a | b` |\n| - | - |\n| 1 | 2 |",
      expected: "<p>| <code>a | b</code> |<br>| - | - |<br>| 1 | 2 |</p>",
    },
    {
      name: "a double-backtick span holding a pipe is one header cell",
      input: "| ``a | b`` |\n| - |\n| 1 |",
      expected:
        "<table><thead><tr><th> <code>a | b</code> </th></tr></thead>" +
        "<tbody><tr><td> 1 </td></tr></tbody></table>",
    },
    {
      name: "an unclosed code span in a header opens no table",
      input: "| `a | b |\n| - | - |\n| 1 | 2 |",
      expected: "<p>| `a | b |<br>| - | - |<br>| 1 | 2 |</p>",
    },
    {
      name: "a code span in a body cell is unchanged",
      input: "| a | b |\n| - | - |\n| `1 | 2` | 3 |",
      expected: HEAD + "<tbody><tr><td> <code>1 | 2</code> </td><td> 3 </td></tr></tbody></table>",
    },
  ];

  it.each(cases)("$name", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });

  it("never paints the header row as paragraph text while streaming", () => {
    const el = document.createElement("div");
    const r = createMarkdownStream(el, { flushIntervalMs: 0 });
    r.writeDelta("| a | b |\n");
    expect(el.querySelector("table")).toBeNull();
    expect(el.textContent).toBe("");
    r.writeDelta("| - | - |\n");
    expect(el.querySelector("table")).not.toBeNull();
    r.writeDelta("| 1 | 2 |\n");
    r.end();
    expect(el.querySelectorAll("tbody tr")).toHaveLength(1);
  });

  it("falls back to a paragraph when the stream ends on the header row", () => {
    const el = document.createElement("div");
    const r = createMarkdownStream(el, { flushIntervalMs: 0 });
    r.writeDelta("| a | b |");
    expect(el.querySelector("table")).toBeNull();
    r.end();
    expect(el.querySelector("table")).toBeNull();
    expect(el.querySelectorAll("p")).toHaveLength(1);
    expect(el.textContent).toBe("| a | b |");
  });
});

describe("markdown link guard", () => {
  const PAYLOAD = `https://e.example/collect?d=${"%41".repeat(25)}`;

  afterEach(() => {
    adoptLinkGuard(settingsPayload({ guard_payload_links: true }));
    setLinkCopyCallback(() => {
      /* reset */
    });
  });

  function rendered(md: string): HTMLElement {
    const el = document.createElement("div");
    renderMarkdownInto(el, md);
    return el;
  }

  function copiedUrl(el: HTMLElement): unknown {
    const copied = vi.fn();
    setLinkCopyCallback(copied);
    (el.querySelector("button.link-withheld") as HTMLButtonElement | null)?.click();
    return copied.mock.calls[0]?.[0];
  }

  it("withholds a payload-shaped inline link behind a copy button when ON", () => {
    adoptLinkGuard(settingsPayload({ guard_payload_links: true }));
    const el = rendered(`see [file an issue](${PAYLOAD}) now`);
    const button = el.querySelector("button.link-withheld");
    expect(button?.textContent).toBe("file an issue");
    expect(copiedUrl(el)).toBe(PAYLOAD);
    expect(el.querySelector("a[href]")).toBeNull();
  });

  it("withholds a payload-shaped raw URL when ON", () => {
    adoptLinkGuard(settingsPayload({ guard_payload_links: true }));
    const el = rendered(`go to ${PAYLOAD} now`);
    expect(el.querySelector("button.link-withheld")?.textContent).toBe(PAYLOAD);
    expect(el.querySelector("a[href]")).toBeNull();
  });

  it("withholds navigation for an issue-prefill link when ON", () => {
    adoptLinkGuard(settingsPayload({ guard_payload_links: true }));
    const prefill = `https://github.com/o/r/issues/new?title=x&body=${"word%20".repeat(30)}`;
    const el = rendered(`please [file an issue](${prefill}) for this`);
    const button = el.querySelector("button.link-withheld");
    expect(button?.textContent).toBe("file an issue");
    expect(copiedUrl(el)).toBe(prefill);
    expect(el.querySelector("a[href]")).toBeNull();
  });

  it("withholds a payload-shaped angle autolink when ON", () => {
    adoptLinkGuard(settingsPayload({ guard_payload_links: true }));
    const el = rendered(`see <${PAYLOAD}> now`);
    expect(copiedUrl(el)).toBe(PAYLOAD);
    expect(el.querySelector("a[href]")).toBeNull();
  });

  it("leaves no navigable payload behind a reference-style link when ON", () => {
    adoptLinkGuard(settingsPayload({ guard_payload_links: true }));
    const el = rendered(`see [file an issue][r] now\n\n[r]: ${PAYLOAD}\n`);
    expect(copiedUrl(el)).toBe(PAYLOAD);
    expect(el.querySelector("a[href]")).toBeNull();
  });

  it.each([
    ["a classic GitHub token", `https://e.example/collect?t=ghp_${"a1B2c3".repeat(6)}`],
    ["a fine-grained GitHub token", `https://e.example/collect?t=github_pat_${"a1_B2".repeat(5)}`],
    ["a payload in a fragment with no query", `https://e.example/collect#${"%41".repeat(25)}`],
  ])("withholds a link carrying %s when ON", (_name, url) => {
    adoptLinkGuard(settingsPayload({ guard_payload_links: true }));
    const el = rendered(`see [x](${url}) now`);
    expect(copiedUrl(el)).toBe(url);
    expect(el.querySelector("a[href]")).toBeNull();
  });

  it("leaves an ordinary link alone when ON", () => {
    adoptLinkGuard(settingsPayload({ guard_payload_links: true }));
    const el = rendered("[docs](https://e.example/docs?page=2)");
    expect(el.querySelector("a")?.getAttribute("href")).toBe("https://e.example/docs?page=2");
    expect(el.querySelector("button")).toBeNull();
  });

  it.each([true, false])("still blocks a long-query javascript: link with the guard %s", (on) => {
    adoptLinkGuard(settingsPayload({ guard_payload_links: on }));
    const el = rendered(`[x](javascript:alert(1)?${"q".repeat(250)})`);
    expect(el.querySelector("a")?.getAttribute("href")).toBe("#");
    expect(el.querySelector("button.link-withheld")).toBeNull();
  });

  it("renders a payload-shaped link as an ordinary link when OFF", () => {
    adoptLinkGuard(settingsPayload({ guard_payload_links: false }));
    const el = rendered(`[file an issue](${PAYLOAD})`);
    const a = el.querySelector("a");
    expect(a?.getAttribute("href")).toBe(PAYLOAD);
    expect(a?.hasAttribute("data-payload-url")).toBe(true);
    expect(el.querySelector("button")).toBeNull();
  });

  it("withholds a link streamed one character at a time", () => {
    adoptLinkGuard(settingsPayload({ guard_payload_links: true }));
    const el = document.createElement("div");
    const r = createMarkdownStream(el, { flushIntervalMs: 0 });
    for (const ch of `see [file an issue](${PAYLOAD}) now`) {
      r.writeDelta(ch);
    }
    r.end();
    expect(el.querySelector("button.link-withheld")?.textContent).toBe("file an issue");
    expect(el.querySelector("a[href]")).toBeNull();
    expect(el.textContent).toBe("see file an issue now");
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
        const url = `https://e.example/p?${q}`;
        const el = rendered(produce(url));
        for (const a of el.querySelectorAll("a[href]")) {
          expect(exfilShaped(a.getAttribute("href") ?? "")).toBe(false);
        }
      }),
      { numRuns: 200 },
    );
  });
});
