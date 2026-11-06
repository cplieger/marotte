import { describe, it, expect, vi, beforeAll, afterAll, afterEach } from "vitest";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import {
  isRunnableShell,
  extractLang,
  decorateCodeBlocks,
  decorateStreamingCodeTail,
  setCopyCallback,
  setShellRunCallback,
} from "./code-blocks.js";

describe("isRunnableShell", () => {
  const cases: { name: string; lang: string; text: string; expected: boolean }[] = [
    { name: "valid single-line shell command", lang: "bash", text: "echo hello", expected: true },
    { name: "empty lang treated as shell", lang: "", text: "ls -la", expected: true },
    { name: "sh lang accepted", lang: "sh", text: "pwd", expected: true },
    { name: "zsh lang accepted", lang: "zsh", text: "cd /tmp", expected: true },
    { name: "console lang accepted", lang: "console", text: "whoami", expected: true },
    { name: "terminal lang accepted", lang: "terminal", text: "date", expected: true },
    { name: "shell lang accepted", lang: "shell", text: "uname -a", expected: true },
    { name: "non-shell lang rejected", lang: "python", text: "print('hi')", expected: false },
    { name: "non-shell lang go rejected", lang: "go", text: "fmt.Println()", expected: false },
    { name: "empty text rejected", lang: "bash", text: "", expected: false },
    { name: "whitespace-only text rejected", lang: "bash", text: "   \n  ", expected: false },
    {
      name: "shebang prefix rejected",
      lang: "bash",
      text: "#!/bin/bash\necho hi",
      expected: false,
    },
    {
      name: "shebang with env rejected",
      lang: "sh",
      text: "#!/usr/bin/env bash\nls",
      expected: false,
    },
    // Run takes a block of any line count; shell.ts sends it as one submission.
    { name: "4-line script accepted", lang: "bash", text: "a\nb\nc\nd", expected: true },
    { name: "2-line block accepted", lang: "bash", text: "cd /tmp\nls -la", expected: true },
    {
      name: "blank lines between commands accepted",
      lang: "bash",
      text: "a\n\nb\n\nc",
      expected: true,
    },
    {
      name: "CRLF line ends accepted",
      lang: "bash",
      text: "echo hi\r\necho bye",
      expected: true,
    },
    {
      name: "a multi-line shebang script rejected",
      lang: "zsh",
      text: "  #!/bin/zsh\nprint hi\n",
      expected: false,
    },
    {
      name: "a multi-line non-shell block rejected",
      lang: "python",
      text: "import os\nprint(os.getcwd())",
      expected: false,
    },
    {
      name: "trailing newline trimmed away, still single-line",
      lang: "bash",
      text: "echo hi\n",
      expected: true,
    },
    {
      name: "trailing CRLF trimmed away, still single-line",
      lang: "bash",
      text: "echo hi\r\n",
      expected: true,
    },
    // No word denylist: nothing executes until a keystroke.
    { name: "sudo command offered", lang: "bash", text: "sudo apt install foo", expected: true },
    { name: "ssh command offered", lang: "bash", text: "ssh user@host", expected: true },
    { name: "scp command offered", lang: "bash", text: "scp file.txt host:/tmp/", expected: true },
    { name: "rsync command offered", lang: "bash", text: "rsync -av src/ dest/", expected: true },
    {
      name: "a destructive one-liner is offered, not filtered",
      lang: "sh",
      text: "echo hi && sudo rm -rf /",
      expected: true,
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      expect(isRunnableShell(tc.lang, tc.text)).toBe(tc.expected);
    });
  }
});

describe("extractLang", () => {
  // The markdown renderer puts `language-<tag>` on the <code>; the <pre> channel serves callers that pass it there.
  const cases: { name: string; pre: string; code: string | null; want: string }[] = [
    { name: "the code element's language- class", pre: "code", code: "language-go", want: "go" },
    { name: "the pre element's own class", pre: "code python", code: null, want: "python" },
    {
      name: "the pre channel wins over the code channel",
      pre: "code rust",
      code: "language-go",
      want: "rust",
    },
    { name: "no language anywhere", pre: "code", code: null, want: "" },
    { name: "no classes at all", pre: "", code: null, want: "" },
    { name: "the tag is lowercased", pre: "code", code: "language-TypeScript", want: "typescript" },
    {
      name: "an unknown tag passes through",
      pre: "code",
      code: "language-brainfuck",
      want: "brainfuck",
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      const pre = document.createElement("pre");
      pre.className = tc.pre;
      let code: HTMLElement | null = null;
      if (tc.code !== null) {
        code = document.createElement("code");
        code.className = tc.code;
        pre.appendChild(code);
      }
      expect(extractLang(pre, code)).toBe(tc.want);
    });
  }
});

function fixture(lang: string, text: string): { root: HTMLElement; pre: HTMLElement } {
  const root = document.createElement("div");
  const pre = document.createElement("pre");
  pre.className = "code";
  const code = document.createElement("code");
  if (lang !== "") {
    code.className = `language-${lang}`;
  }
  code.textContent = text;
  pre.appendChild(code);
  root.appendChild(pre);
  return { root, pre };
}

function head(root: HTMLElement): HTMLElement | null {
  return root.querySelector(".code-wrap > .code-head");
}

describe("decorateCodeBlocks: the title bar", () => {
  it("prints the raw fence tag as the language label", () => {
    const { root } = fixture("go", "func main() {}");
    decorateCodeBlocks(root);
    expect(head(root)?.querySelector(".code-lang")?.textContent).toBe("go");
  });

  it("prints an unrecognised fence tag verbatim rather than dropping it", () => {
    // The label is the author's tag; `normalizeLang` decides highlighting only.
    const { root } = fixture("brainfuck", "++++");
    decorateCodeBlocks(root);
    expect(root.querySelector(".code-lang")?.textContent).toBe("brainfuck");
  });

  it("leaves the label empty for a fence with no tag, and still builds the bar", () => {
    const { root } = fixture("", "ls -la");
    decorateCodeBlocks(root);
    expect(root.querySelector(".code-lang")?.textContent).toBe("");
    expect(head(root)).not.toBeNull();
  });

  it("puts the actions inside the title bar, not floating over the pre", () => {
    const { root } = fixture("go", "func main() {}");
    decorateCodeBlocks(root);
    expect(head(root)?.querySelector(".code-actions")).not.toBeNull();
    expect(root.querySelector(".code-wrap > .code-actions")).toBeNull();
  });

  it("puts the bar before the pre inside the wrapper", () => {
    const { root } = fixture("go", "x");
    decorateCodeBlocks(root);
    const kids = [...(root.querySelector(".code-wrap")?.children ?? [])].map((e) =>
      e.tagName.toLowerCase(),
    );
    expect(kids).toEqual(["div", "pre"]);
  });

  it("is idempotent: a second pass adds no second bar and no second button", () => {
    const { root } = fixture("bash", "echo hi");
    decorateCodeBlocks(root);
    decorateCodeBlocks(root);
    decorateCodeBlocks(root);
    expect(root.querySelectorAll(".code-head")).toHaveLength(1);
    expect(root.querySelectorAll(".code-act-btn")).toHaveLength(2); // copy + run
  });

  it("highlights a known language and leaves an unknown one as text", () => {
    const { root: known } = fixture("go", "func main() {}");
    decorateCodeBlocks(known);
    expect(known.querySelector("code")?.querySelector(".hl-kw, [class^='hl-']")).not.toBeNull();

    const { root: unknown } = fixture("brainfuck", "++++");
    decorateCodeBlocks(unknown);
    expect(unknown.querySelector("code")?.children).toHaveLength(0);
  });

  it("offers Run only for a runnable shell snippet", () => {
    const { root: shell } = fixture("bash", "echo hi");
    decorateCodeBlocks(shell);
    expect(shell.querySelectorAll(".code-act-btn")).toHaveLength(2);

    const { root: go } = fixture("go", "func main() {}");
    decorateCodeBlocks(go);
    expect(go.querySelectorAll(".code-act-btn")).toHaveLength(1);
  });
});

describe("decorateStreamingCodeTail: a fence that has not closed", () => {
  it("gives the open block its bar and its Copy button", () => {
    // The renderer's per-block callback fires only on close, so without this sweep a streaming block has no chrome.
    const { root } = fixture("go", "func main() {");
    decorateStreamingCodeTail(root);
    expect(root.querySelector(".code-wrap")?.getAttribute("data-code-state")).toBe("streaming");
    expect(root.querySelector(".code-lang")?.textContent).toBe("go");
    expect(root.querySelectorAll(".code-act-btn")).toHaveLength(1);
  });

  it("withholds highlighting and Run while the text is still growing", () => {
    const { root } = fixture("bash", "echo hi");
    decorateStreamingCodeTail(root);
    // No Run: an incomplete command must not reach a shell.
    expect(root.querySelectorAll(".code-act-btn")).toHaveLength(1);
    expect(root.querySelector("code")?.children).toHaveLength(0);
  });

  it("upgrades in place when the fence closes: highlight, Run, one bar", () => {
    const { root } = fixture("bash", "echo hi");
    decorateStreamingCodeTail(root);
    decorateCodeBlocks(root);
    expect(root.querySelector(".code-wrap")?.getAttribute("data-code-state")).toBe("final");
    expect(root.querySelectorAll(".code-head")).toHaveLength(1);
    expect(root.querySelectorAll(".code-act-btn")).toHaveLength(2);
  });

  it("leaves a block that already closed alone", () => {
    // The close callback runs inside the parse slice, so the tail sweep after it sees the just-finalized block as last.
    const { root } = fixture("bash", "echo hi");
    decorateCodeBlocks(root);
    decorateStreamingCodeTail(root);
    expect(root.querySelector(".code-wrap")?.getAttribute("data-code-state")).toBe("final");
    expect(root.querySelectorAll(".code-act-btn")).toHaveLength(2);
  });

  it("copies the text as it is at CLICK time, not as it was when the button was built", () => {
    const copied: string[] = [];
    setCopyCallback((t) => copied.push(t));
    const { root } = fixture("go", "func main() {");
    decorateStreamingCodeTail(root);
    const code = root.querySelector("code");
    code?.appendChild(document.createTextNode("\n\tprintln()\n}"));
    root.querySelector<HTMLButtonElement>(".code-act-btn")?.click();
    expect(copied).toEqual(["func main() {\n\tprintln()\n}"]);
  });

  it("does nothing when there is no code block", () => {
    const root = document.createElement("div");
    root.appendChild(document.createElement("p"));
    decorateStreamingCodeTail(root);
    expect(root.querySelector(".code-wrap")).toBeNull();
  });

  it("wires Run to the shell callback with the trimmed command", () => {
    const run = vi.fn();
    setShellRunCallback(run);
    const { root } = fixture("bash", "  echo hi\n");
    decorateCodeBlocks(root);
    const buttons = [...root.querySelectorAll<HTMLButtonElement>(".code-act-btn")];
    buttons[1]?.click();
    expect(run).toHaveBeenCalledWith("echo hi");
  });

  it("labels the shell button as running the command", () => {
    setShellRunCallback(vi.fn());
    const { root } = fixture("bash", "echo hi");
    decorateCodeBlocks(root);
    const buttons = [...root.querySelectorAll<HTMLButtonElement>(".code-act-btn")];
    expect(buttons[1]?.getAttribute("data-tooltip")).toBe("Run in shell");
    expect(buttons[1]?.getAttribute("aria-label")).toBe("Run in shell");
  });

  it("gives a multi-line block the same Run button as a one-line block", () => {
    setShellRunCallback(vi.fn());
    const { root } = fixture("bash", "cd /tmp\nls -la");
    decorateCodeBlocks(root);
    const buttons = [...root.querySelectorAll<HTMLButtonElement>(".code-act-btn")];
    expect(buttons).toHaveLength(2);
    expect(buttons[1]?.disabled).toBe(false);
    expect(buttons[1]?.getAttribute("data-tooltip")).toBe("Run in shell");
    expect(buttons[1]?.getAttribute("aria-label")).toBe("Run in shell");
  });

  it("withholds Run from a multi-line block that is still streaming", () => {
    const { root } = fixture("bash", "cd /tmp\nls -la");
    decorateStreamingCodeTail(root);
    expect(root.querySelectorAll(".code-act-btn")).toHaveLength(1);
  });
});

function runButton(root: HTMLElement): HTMLButtonElement | undefined {
  return [...root.querySelectorAll<HTMLButtonElement>(".code-act-btn")][1];
}

describe("Run never sends a character the reader cannot see", () => {
  // Each is a code point Unicode classes as a control (Cc), format (Cf), private-use (Co),
  // surrogate (Cs), line or paragraph separator (Zl, Zp), a space other than U+0020 (Zs) or a
  // default-ignorable: none of them shows as itself in a code block.
  const hidden: { name: string; text: string }[] = [
    { name: "a right-to-left override (U+202E)", text: "echo \u202Egnp.exe" },
    { name: "a left-to-right isolate (U+2066)", text: "echo \u2066a\u2069" },
    { name: "an Arabic letter mark (U+061C)", text: "ls \u061C-la" },
    { name: "a zero-width space (U+200B)", text: "rm\u200B -rf x" },
    { name: "a zero-width joiner (U+200D)", text: "ls\u200D" },
    { name: "a word joiner (U+2060)", text: "ls\u2060 -la" },
    { name: "a byte-order mark (U+FEFF)", text: "l\uFEFFs" },
    { name: "a soft hyphen (U+00AD)", text: "cu\u00ADrl x" },
    { name: "a no-break space (U+00A0)", text: "ls\u00A0-la" },
    { name: "an ideographic space (U+3000)", text: "ls\u3000-la" },
    { name: "a tab, which renders as spaces", text: "ls\t-la" },
    { name: "an escape control (U+001B)", text: "echo \u001B[2Jhi" },
    { name: "a delete control (U+007F)", text: "echo a\u007F" },
    { name: "a line separator (U+2028)", text: "echo a\u2028rm x" },
    { name: "a paragraph separator (U+2029)", text: "echo a\u2029rm x" },
    { name: "a private-use character (U+E000)", text: "echo \uE000" },
    { name: "a lone surrogate (U+D800)", text: "echo \uD800" },
    { name: "a tag character (U+E0041)", text: "echo hi\u{E0041}" },
    { name: "a variation selector (U+FE0F)", text: "echo a\uFE0F" },
    { name: "a combining grapheme joiner (U+034F)", text: "echo a\u034F" },
    { name: "a Hangul filler (U+3164)", text: "echo \u3164" },
    // CSS Text 3 section 4 renders a carriage return as a space, so it is no line end.
    { name: "a lone carriage return (U+000D)", text: "echo hi\rrm -rf /tmp/x" },
    { name: "a paste-end sequence on a later line", text: "echo a\n\u001B[201~rm x" },
    { name: "a tab on a later line", text: "echo a\nls\t-la" },
    { name: "lone surrogates split by a line end", text: "echo a\uD83D\n\uDE00ls" },
  ];

  for (const tc of hidden) {
    it(`disables Run for ${tc.name} and sends nothing`, () => {
      const run = vi.fn();
      setShellRunCallback(run);
      const { root } = fixture("bash", tc.text);
      decorateCodeBlocks(root);
      const btn = runButton(root);
      expect(btn?.disabled).toBe(true);
      expect(btn?.getAttribute("data-tooltip")).toBe(
        "Contains hidden characters. Copy it to inspect.",
      );
      expect(btn?.getAttribute("aria-label")).toBe(
        "Contains hidden characters. Copy it to inspect.",
      );
      btn?.click();
      expect(run).not.toHaveBeenCalled();
    });
  }

  const visible: { name: string; text: string }[] = [
    { name: "plain ASCII with spaces", text: "git log --oneline -3" },
    { name: "accented letters", text: "echo café" },
    { name: "CJK text", text: "echo 日本語" },
    { name: "an emoji with no variation selector", text: "echo 🚀" },
  ];

  for (const tc of visible) {
    it(`keeps Run enabled for ${tc.name}`, () => {
      const run = vi.fn();
      setShellRunCallback(run);
      const { root } = fixture("bash", tc.text);
      decorateCodeBlocks(root);
      const btn = runButton(root);
      expect(btn?.disabled).toBe(false);
      btn?.click();
      expect(run).toHaveBeenCalledWith(tc.text);
    });
  }

  it("drops hidden whitespace around the command and runs what is left", () => {
    const run = vi.fn();
    setShellRunCallback(run);
    const { root } = fixture("bash", "\t\u00A0echo hi\u3000\t");
    decorateCodeBlocks(root);
    const btn = runButton(root);
    expect(btn?.disabled).toBe(false);
    btn?.click();
    expect(run).toHaveBeenCalledWith("echo hi");
  });
});

describe("Run on a line that is partly out of view", () => {
  let style: HTMLStyleElement;
  let host: HTMLElement | null = null;

  beforeAll(() => {
    style = mountAppCSS();
  });

  afterAll(() => {
    style.remove();
  });

  afterEach(() => {
    host?.remove();
    host = null;
  });

  function mounted(text: string, width: string): HTMLElement {
    const { root } = fixture("bash", text);
    host = document.createElement("div");
    host.className = "message";
    host.style.width = width;
    host.appendChild(root);
    document.body.appendChild(host);
    decorateCodeBlocks(root);
    return root;
  }

  function openAsk(): HTMLDialogElement | null {
    return document.querySelector<HTMLDialogElement>("dialog.uip-ask[open]");
  }

  const long = `curl -fsSL https://example.invalid/${"a".repeat(120)}/install.sh`;

  it("runs a line that fits on one click, with no dialog", () => {
    const run = vi.fn();
    setShellRunCallback(run);
    const root = mounted("echo hi", "40rem");
    runButton(root)?.click();
    expect(run).toHaveBeenCalledWith("echo hi");
    expect(openAsk()).toBeNull();
  });

  it("asks first, showing the whole command, and Cancel sends nothing", async () => {
    const run = vi.fn();
    setShellRunCallback(run);
    const root = mounted(long, "12rem");
    runButton(root)?.click();
    expect(run).not.toHaveBeenCalled();
    const dlg = openAsk();
    expect(dlg).not.toBeNull();
    const msg = dlg?.querySelector<HTMLElement>(".uip-ask-msg");
    expect(msg?.textContent).toContain(long);
    // The whole command is on screen: the message wraps it instead of scrolling it.
    expect(msg?.scrollWidth).toBeLessThanOrEqual(msg?.clientWidth ?? 0);
    expect(dlg?.scrollWidth).toBeLessThanOrEqual(dlg?.clientWidth ?? 0);
    dlg?.querySelector<HTMLButtonElement>(".uip-ask-cancel")?.click();
    await new Promise((r) => setTimeout(r, 0));
    expect(run).not.toHaveBeenCalled();
  });

  it("sends exactly the command it showed once the reader confirms", async () => {
    const run = vi.fn();
    setShellRunCallback(run);
    const root = mounted(long, "12rem");
    runButton(root)?.click();
    const ok = openAsk()?.querySelector<HTMLButtonElement>(".uip-ask-ok");
    expect(ok?.textContent).toBe("Run");
    ok?.click();
    await new Promise((r) => setTimeout(r, 0));
    expect(run).toHaveBeenCalledTimes(1);
    expect(run).toHaveBeenCalledWith(long);
  });

  it("keeps the command's own spacing in the dialog", async () => {
    setShellRunCallback(vi.fn());
    const cmd = `echo "a      b" ${"x".repeat(120)}`;
    const root = mounted(cmd, "12rem");
    runButton(root)?.click();
    const dlg = openAsk();
    const node = dlg?.querySelector(".uip-ask-msg")?.firstChild;
    expect(node).toBeInstanceOf(Text);
    const text = node?.textContent ?? "";
    const measure = (from: number, to: number): number => {
      const r = document.createRange();
      r.setStart(node as Text, from);
      r.setEnd(node as Text, to);
      return r.getBoundingClientRect().width;
    };
    const runAt = text.indexOf("a      b") + 1;
    const one = measure(runAt, runAt + 1);
    expect(one).toBeGreaterThan(0);
    // Collapsed white space would draw the six spaces as one.
    expect(measure(runAt, runAt + 6)).toBeGreaterThan(one * 4);
    dlg?.querySelector<HTMLButtonElement>(".uip-ask-cancel")?.click();
    await new Promise((r) => setTimeout(r, 0));
  });
});

describe("Run on a multi-line block", () => {
  let style: HTMLStyleElement;
  let host: HTMLElement | null = null;

  beforeAll(() => {
    style = mountAppCSS();
  });

  afterAll(() => {
    style.remove();
  });

  afterEach(() => {
    host?.remove();
    host = null;
  });

  function mounted(text: string): HTMLElement {
    const { root } = fixture("bash", text);
    host = document.createElement("div");
    host.className = "message";
    host.style.width = "40rem";
    host.appendChild(root);
    document.body.appendChild(host);
    decorateCodeBlocks(root);
    return root;
  }

  function openAsk(): HTMLDialogElement | null {
    return document.querySelector<HTMLDialogElement>("dialog.uip-ask[open]");
  }

  const block = "cd /tmp\nls -la";

  it("asks first even when every line is in view, showing the whole block", async () => {
    const run = vi.fn();
    setShellRunCallback(run);
    const root = mounted(block);
    const pre = root.querySelector("pre");
    expect(pre?.scrollWidth).toBeLessThanOrEqual(pre?.clientWidth ?? 0);
    runButton(root)?.click();
    expect(run).not.toHaveBeenCalled();
    const dlg = openAsk();
    expect(dlg).not.toBeNull();
    expect(dlg?.querySelector(".uip-ask-msg")?.textContent).toContain(block);
    dlg?.querySelector<HTMLButtonElement>(".uip-ask-cancel")?.click();
    await new Promise((r) => setTimeout(r, 0));
  });

  it("sends nothing when the reader cancels", async () => {
    const run = vi.fn();
    setShellRunCallback(run);
    const root = mounted(block);
    runButton(root)?.click();
    openAsk()?.querySelector<HTMLButtonElement>(".uip-ask-cancel")?.click();
    await new Promise((r) => setTimeout(r, 0));
    expect(run).not.toHaveBeenCalled();
  });

  it("sends the whole block once, as one command, when the reader confirms", async () => {
    const run = vi.fn();
    setShellRunCallback(run);
    const root = mounted(`${block}\n`);
    runButton(root)?.click();
    const ok = openAsk()?.querySelector<HTMLButtonElement>(".uip-ask-ok");
    expect(ok?.textContent).toBe("Run");
    ok?.click();
    await new Promise((r) => setTimeout(r, 0));
    expect(run).toHaveBeenCalledTimes(1);
    expect(run).toHaveBeenCalledWith(block);
  });

  it("turns CRLF line ends into line feeds before it shows or sends the block", async () => {
    const run = vi.fn();
    setShellRunCallback(run);
    const root = mounted("cd /tmp\r\nls -la\r\n");
    runButton(root)?.click();
    const dlg = openAsk();
    expect(dlg?.querySelector(".uip-ask-msg")?.textContent).toContain(block);
    dlg?.querySelector<HTMLButtonElement>(".uip-ask-ok")?.click();
    await new Promise((r) => setTimeout(r, 0));
    expect(run).toHaveBeenCalledWith(block);
  });
});
