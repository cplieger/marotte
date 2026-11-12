// The editor's rendered-markdown read surface: every /docs document opens in the editor, and the `.kiro` inventory is
// markdown with YAML front-matter, so the read state renders it; the edit toggle shows the source. The DOM is built
// by markdown.ts as nodes, never through innerHTML. The prose container carries `.message.assistant`, the app's one
// rendered-markdown skin and its reading measure, rather than a second copy of the prose CSS.

import { el } from "@cplieger/reactive";
import { releaseMarkdownIn, renderMarkdownInto } from "./markdown.js";
import { splitFrontMatter, type FrontMatterField } from "./front-matter.js";

/** Render a markdown document into `host`: its front-matter as a metadata block, then its body as markdown, `#` as
 *  `h<headingLevel>`. Replaces whatever `host` held. */
export function renderMarkdownDoc(host: HTMLElement, text: string, headingLevel: 2 | 3 = 2): void {
  const doc = splitFrontMatter(text);
  const children: HTMLElement[] = [];
  if (doc.present && doc.fields.length > 0) {
    children.push(frontMatterBlock(doc.fields));
  }
  const prose = el("div", { className: "message assistant" });
  renderMarkdownInto(prose, doc.body, { headingLevel, wholeFile: true });
  children.push(prose);
  releaseMarkdownIn(host);
  host.replaceChildren(...children);
}

/** The front-matter metadata block.
 *
 *  Rendered rather than stripped because the header IS the interesting part of a
 *  steering doc: the inclusion mode answers "is this costing me tokens every
 *  session", `fileMatchPattern` answers "when does it load", and an agent's
 *  `model` and `tools` are the whole spec. Stripping it would hide the one thing
 *  a reader came for, and rendering it as markdown is not an option — a `---`
 *  fence is a horizontal rule and `key: value` lines are a paragraph.
 *
 *  A `<dl>` because that is what this is: declared terms and their values. Keys
 *  keep the author's order and spelling; nothing is defaulted in (an absent
 *  `inclusion` means the file did not declare one, and saying "always" here
 *  would be this surface inventing a steering rule for a skill). */
function frontMatterBlock(fields: FrontMatterField[]): HTMLElement {
  const list = el("dl", { className: "editor-fm-list" });
  for (const f of fields) {
    list.append(
      el("dt", { className: "editor-fm-key" }, f.key),
      el("dd", { className: "editor-fm-val" }, valueText(f)),
    );
  }
  return el(
    "section",
    { className: "editor-fm", "aria-label": "Document front-matter" },
    el("span", { className: "editor-fm-title" }, "Front-matter"),
    list,
  );
}

function valueText(f: FrontMatterField): string {
  return f.items.length > 0 ? f.items.join(", ") : f.value;
}
