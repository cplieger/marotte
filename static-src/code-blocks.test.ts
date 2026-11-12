import { describe, it, expect, vi, beforeAll, beforeEach, afterAll, afterEach } from "vitest";
import { renderMarkdown } from "@cplieger/ui/markdown";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { isRunnableShell, finishCodeBlock, setShellRunCallback } from "./code-blocks.js";
import { createMarkdownStream, renderMarkdownInto } from "./markdown.js";

describe("isRunnableShell", () => {
  const cases: { name: string; lang: string; text: string; expected: boolean }[] = [
    { name: "valid single-line shell command", lang: "bash", text: "echo hello", expected: true },
    { name: "empty lang treated as shell", lang: "", text: "ls -la", expected: true },
    { name: "sh lang accepted", lang: "sh", text: "pwd", expected: true },
    { name: "zsh lang accepted", lang: "zsh", text: "cd /tmp", expected: true },
    { name: "console lang accepted", lang: "console", text: "whoami", expected: true },
    { name: "terminal lang accepted", lang: "terminal", text: "date", expected: true },
    { name: "shell lang accepted", lang: "shell", text: "uname -a", expected: true },
    // highlight.md "Detection": an untagged fence is shell unless it reads as another language.
    { name: "untagged undetected text treated as shell", lang: "", text: "cd", expected: true },
    { name: "untagged $ prompt accepted", lang: "", text: "$ ls -la", expected: true },
    {
      name: "untagged Go rejected",
      lang: "",
      text: "package main\n\nfunc main() {\n}",
      expected: false,
    },
    {
      name: "untagged Python rejected",
      lang: "",
      text: "def foo():\n    return 1",
      expected: false,
    },
    { name: "untagged JSON rejected", lang: "", text: '{"a": 1}', expected: false },
    { name: "non-shell lang rejected", lang: "python", text: "print('hi')", expected: false },
    {
      name: "tagged shell kept whatever it reads as",
      lang: "bash",
      text: '{"a": 1}',
      expected: true,
    },
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

/**
 * The `.cp-code` block @cplieger/ui/markdown builds for a fence, given to `finishCodeBlock` as the `code` hook gives
 * it: the block, the tag as written and the fence's text.
 */
function fixture(lang: string, text: string): { root: HTMLElement; block: HTMLElement } {
  const root = document.createElement("div");
  renderMarkdown(root, `\`\`\`${lang}\nx\n\`\`\``, { highlight: false });
  const block = root.querySelector<HTMLElement>(".cp-code");
  if (block === null) {
    throw new Error("the library rendered no code block");
  }
  const code = block.querySelector("pre > code");
  if (code !== null) {
    code.textContent = text;
  }
  finishCodeBlock(block, lang, text);
  return { root, block };
}

/** The bar's buttons other than the library's Copy. */
function ownButtons(root: HTMLElement): HTMLButtonElement[] {
  return [
    ...root.querySelectorAll<HTMLButtonElement>('[data-part="actions"] > button:not(.cp-copy)'),
  ];
}

function runButton(root: HTMLElement): HTMLButtonElement | undefined {
  return ownButtons(root)[0];
}

describe("finishCodeBlock", () => {
  it("adds Run to a shell fence's actions, after the library's Copy", () => {
    setShellRunCallback(vi.fn());
    const { block } = fixture("bash", "echo hi");
    const actions = [...(block.querySelector('[data-part="actions"]')?.children ?? [])];
    expect(actions).toHaveLength(2);
    expect(actions[0]?.classList.contains("cp-copy")).toBe(true);
    expect(actions[1]?.getAttribute("aria-label")).toBe("Run in shell");
  });

  it("gives Run the bar's button family: quiet and small", () => {
    setShellRunCallback(vi.fn());
    const btn = runButton(fixture("bash", "echo hi").root);
    expect(btn?.classList.contains("cp-button")).toBe(true);
    expect(btn?.getAttribute("data-variant")).toBe("quiet");
    expect(btn?.getAttribute("data-size")).toBe("sm");
    expect(btn?.type).toBe("button");
  });

  it("offers Run on no other language", () => {
    expect(ownButtons(fixture("go", "func main() {}").root)).toHaveLength(0);
    expect(ownButtons(fixture("python", "print('hi')").root)).toHaveLength(0);
  });

  it("marks the block final, which ends the streaming dot", () => {
    expect(fixture("go", "x").block.getAttribute("data-code-state")).toBe("final");
  });

  it("wires Run to the shell callback with the trimmed command", () => {
    const run = vi.fn();
    setShellRunCallback(run);
    runButton(fixture("bash", "  echo hi\n").root)?.click();
    expect(run).toHaveBeenCalledWith("echo hi");
  });

  it("labels the shell button as running the command", () => {
    setShellRunCallback(vi.fn());
    const btn = runButton(fixture("bash", "echo hi").root);
    expect(btn?.getAttribute("data-tooltip")).toBe("Run in shell");
    expect(btn?.getAttribute("aria-label")).toBe("Run in shell");
  });

  it("gives a multi-line block the same Run button as a one-line block", () => {
    setShellRunCallback(vi.fn());
    const buttons = ownButtons(fixture("bash", "cd /tmp\nls -la").root);
    expect(buttons).toHaveLength(1);
    expect(buttons[0]?.disabled).toBe(false);
    expect(buttons[0]?.getAttribute("aria-label")).toBe("Run in shell");
  });
});

// Through marotte's hooks: the library calls `code` once, at the fence's close or at the end.
describe("Run on a rendered fence", () => {
  let host: HTMLElement;

  beforeEach(() => {
    host = document.createElement("div");
    document.body.append(host);
  });

  afterEach(() => {
    host.remove();
  });

  it("is in a replayed answer's shell fence and not in its Go fence", () => {
    setShellRunCallback(vi.fn());
    renderMarkdownInto(host, "```bash\necho hi\n```\n\n```go\nfunc main() {}\n```\n");
    const blocks = [...host.querySelectorAll<HTMLElement>(".cp-code")];
    expect(blocks).toHaveLength(2);
    expect(blocks[0]?.querySelector('[aria-label="Run in shell"]')).not.toBeNull();
    expect(blocks[1]?.querySelector('[aria-label="Run in shell"]')).toBeNull();
  });

  // markdown.md: an untagged fence is labelled by detection, and plain when detection is unsure.
  it("is on an untagged fence only when its label names no other language", () => {
    setShellRunCallback(vi.fn());
    renderMarkdownInto(
      host,
      [
        "```\npackage main\n\nfunc main() {\n}\n```",
        "```\ndef foo():\n    return 1\n```",
        "```\nls -la\n```",
        "```\n$ ls -la\n```",
        "```python\nls -la\n```",
      ].join("\n\n"),
    );
    const blocks = [...host.querySelectorAll<HTMLElement>(".cp-code")].map((b) => ({
      label: b.querySelector('[data-part="lang"]')?.textContent ?? "",
      run: b.querySelector('[aria-label="Run in shell"]') !== null,
    }));
    expect(blocks).toEqual([
      { label: "Go", run: false },
      { label: "Python", run: false },
      { label: "", run: true },
      { label: "Shell", run: true },
      { label: "Python", run: false },
    ]);
  });

  it("is withheld while the fence streams, then added once it closes", () => {
    setShellRunCallback(vi.fn());
    const stream = createMarkdownStream(host, { flushIntervalMs: 0 });
    stream.write("```bash\necho hi\n");
    const block = host.querySelector(".cp-code");
    // An incomplete command must not reach a shell.
    expect(block?.querySelector('[aria-label="Run in shell"]')).toBeNull();
    expect(block?.hasAttribute("data-code-state")).toBe(false);
    stream.write("```\n\n");
    expect(block?.getAttribute("data-code-state")).toBe("final");
    expect(block?.querySelectorAll('[aria-label="Run in shell"]')).toHaveLength(1);
    stream.end();
    expect(host.querySelectorAll('[aria-label="Run in shell"]')).toHaveLength(1);
  });

  it("is added to a fence the answer never closed, once the answer ends", () => {
    setShellRunCallback(vi.fn());
    const stream = createMarkdownStream(host, { flushIntervalMs: 0 });
    stream.write("```bash\necho hi\n");
    stream.end();
    const block = host.querySelector(".cp-code");
    expect(block?.getAttribute("data-code-state")).toBe("final");
    expect(block?.querySelectorAll('[aria-label="Run in shell"]')).toHaveLength(1);
  });
});

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
    root.classList.add("cp", "cp-prose");
    host = document.createElement("div");
    host.className = "message";
    host.style.width = width;
    host.appendChild(root);
    document.body.appendChild(host);
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
    root.classList.add("cp", "cp-prose");
    host = document.createElement("div");
    host.className = "message";
    host.style.width = "40rem";
    host.appendChild(root);
    document.body.appendChild(host);
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
