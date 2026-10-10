// Which view a file gets and what that view may offer, as one pure function of the file's facts
// and, for a diff, what git said of its base.
// The client never compares a size with the viewer's cap: `large` is the server's verdict.

import { isViewableImage } from "./file-extensions.js";

/** Whether a path's read state renders its markdown instead of showing source. Editing shows
 *  source for every file. */
export function rendersMarkdown(path: string): boolean {
  const lower = path.toLowerCase();
  return lower.endsWith(".md") || lower.endsWith(".markdown");
}

/** What the server has said about a file. Total: no fact is unknown or defaulted. */
export type FileFacts =
  /** Over the viewer's cap: no viewer at all. */
  | { readonly kind: "large"; readonly size: number }
  /** Stat answered; the bytes are not read yet. */
  | {
      readonly kind: "unread";
      readonly binary: boolean;
      readonly utf8: boolean;
      readonly readOnly: boolean;
      readonly size: number;
    }
  /** The whole file is read and adopted. */
  | {
      readonly kind: "small";
      readonly binary: boolean;
      readonly utf8: boolean;
      readonly conflict: boolean;
      readonly readOnly: boolean;
      readonly size: number;
    };

/** What the reader asked for: to read, to edit, or a diff. */
export type Requested = "read" | "edit" | "diff";

/** What git said of a diff's base: text it showed, or the reason it refused to. */
export type DiffBase = "text" | "binary" | "not_utf8";

type ViewKind =
  | "text"
  | "markdown"
  | "conflict"
  | "image"
  | "binary"
  | "large"
  | "diff"
  /** A diff whose base is not text: one sentence saying so. */
  | "undiffable";

export interface Rules {
  readonly view: ViewKind;
  /** Offer Edit (the textarea). */
  readonly edit: boolean;
  readonly highlight: boolean;
  /** The live toggle may appear. */
  readonly live: boolean;
  /** Offer Download; the large view's Download takes the Edit button's place. */
  readonly download: boolean;
  /** The header label saying why the file cannot be edited, or null. */
  readonly readOnlyReason: string | null;
}

export const LABEL_TOOL_OUTPUT = "KAS tool output";
export const LABEL_NOT_UTF8 = "Not UTF-8 text";
export const LABEL_GIT_REVISION = "Git revision";

/** The rules before any stat has answered: nothing is offered. */
const UNKNOWN: Rules = {
  view: "text",
  edit: false,
  highlight: false,
  live: false,
  download: false,
  readOnlyReason: null,
};

function isEditable(facts: FileFacts): facts is Extract<FileFacts, { kind: "small" }> {
  return facts.kind === "small" && facts.utf8 && !facts.readOnly;
}

/** A diff git refused a text base for. The binary notice offers the working copy's Download, so
 *  it needs a working copy the server has described; without one the diff gets the sentence. */
function refusedDiff(base: "binary" | "not_utf8", facts: FileFacts | null): Rules {
  const view = base === "binary" && facts !== null ? "binary" : "undiffable";
  return {
    view,
    edit: view === "undiffable" && facts !== null && isEditable(facts),
    highlight: false,
    live: true,
    download: facts !== null,
    readOnlyReason: null,
  };
}

/** `facts` is null before the first stat answers. `gitBase` is what a settled diff vs HEAD found at
 *  its base, and null for any other view. */
export function rulesFor(
  facts: FileFacts | null,
  path: string,
  requested: Requested,
  gitBase: DiffBase | null = null,
): Rules {
  if (facts?.kind === "large") {
    return {
      view: "large",
      edit: false,
      highlight: false,
      live: false,
      download: true,
      readOnlyReason: null,
    };
  }
  if (requested === "diff" && gitBase !== null && gitBase !== "text") {
    return refusedDiff(gitBase, facts);
  }
  if (facts === null) {
    return UNKNOWN;
  }
  const readOnlyReason = facts.readOnly
    ? LABEL_TOOL_OUTPUT
    : facts.utf8 || facts.binary
      ? null
      : LABEL_NOT_UTF8;
  if (isViewableImage(path)) {
    return {
      view: "image",
      edit: false,
      highlight: false,
      live: true,
      download: true,
      readOnlyReason: null,
    };
  }
  if (facts.binary) {
    return {
      view: "binary",
      edit: false,
      highlight: false,
      live: true,
      download: true,
      readOnlyReason: null,
    };
  }
  const editable = isEditable(facts);
  if (requested === "diff") {
    return {
      view: "diff",
      edit: editable,
      highlight: true,
      live: true,
      download: true,
      readOnlyReason: gitBase === null ? readOnlyReason : LABEL_GIT_REVISION,
    };
  }
  if (editable && facts.conflict) {
    return {
      view: "conflict",
      edit: true,
      highlight: false,
      live: true,
      download: true,
      readOnlyReason: null,
    };
  }
  if (requested === "read" && rendersMarkdown(path)) {
    return {
      view: "markdown",
      edit: editable,
      highlight: false,
      live: true,
      download: true,
      readOnlyReason,
    };
  }
  const editing = requested === "edit" && editable;
  return {
    view: "text",
    edit: editable,
    highlight: !editing,
    live: true,
    download: true,
    readOnlyReason,
  };
}
