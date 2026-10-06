// Streaming markdown parser for incremental DOM rendering. Port of
// streaming-markdown v0.2.15 (MIT, © 2024 Damian Tarnawski,
// https://github.com/thetarnav/streaming-markdown), forked so parser bugs are fixed
// here under this repo's invariant tests. The PARSER is append-only, so emitted
// content stays stable across flushes; the renderer may unwrap an unclosed inline
// token (smd-renderer.ts `unwrap_unclosed`). Raw HTML is emitted as text, never
// parsed; a character reference is decoded BEFORE a URL is validated and is never
// re-parsed. Named references are HTML 4.01's set, not WHATWG's.

import {
  handleRootContext,
  handleHeading,
  handleTable,
  handleTableRow,
  handleTableCell,
  handleCodeBlock,
  handleCodeFence,
  handleCodeInline,
  handleMaybeTask,
  handleStrong,
  handleItalic,
  handleMaybeEqBlock,
  handleMaybeURL,
  handleLinkOrImage,
  handleRawURL,
  handleMaybeAngle,
  handleMaybeEntity,
  handleCommon,
} from "./smd-parser-handlers.js";

// Re-export only the constants used by test consumers.
export {
  DOCUMENT,
  HEADING_1,
  HEADING_2,
  HEADING_3,
  HEADING_4,
  HEADING_5,
  HEADING_6,
} from "./smd-parser-types.js";

export type { Renderer, Parser } from "./smd-parser-types.js";

import type { Parser, Token, Renderer } from "./smd-parser-types.js";
import {
  DOCUMENT,
  BLOCKQUOTE,
  HEADING_1,
  HEADING_2,
  HEADING_3,
  HEADING_4,
  HEADING_5,
  HEADING_6,
  LINE_BREAK,
  LIST_ORDERED,
  LIST_UNORDERED,
  NEWLINE,
  TABLE,
  TABLE_ROW,
  TABLE_CELL,
  CODE_BLOCK,
  CODE_FENCE,
  CODE_INLINE,
  STRONG_AST,
  STRONG_UND,
  ITALIC_AST,
  ITALIC_UND,
  STRIKE,
  MAYBE_EQ_BLOCK,
  MAYBE_TASK,
  EQUATION_BLOCK,
  EQUATION_INLINE,
  IMAGE,
  LINK,
  RAW_URL,
  MAYBE_ANGLE,
  MAYBE_ENTITY,
  TOKEN_ARRAY_CAP,
  add_text,
  end_token,
  end_token_unresolved,
  end_tokens_to_indent,
  is_inline_token,
} from "./smd-parser-types.js";

const MAYBE_URL = 102 as Token; // local-only token, not in TOKENS

export function parser<T>(renderer: Renderer<T>): Parser {
  const tokens = new Uint32Array(TOKEN_ARRAY_CAP);
  tokens[0] = DOCUMENT;
  return {
    renderer: renderer as Renderer<unknown>,
    textBuf: "",
    pending: "",
    tokens,
    delims: new Array<string>(TOKEN_ARRAY_CAP).fill(""),
    len: 0,
    token: DOCUMENT,
    fence_end: 0,
    blockquote_idx: 0,
    hr_char: "",
    hr_chars: 0,
    fence_start: 0,
    spaces: new Uint8Array(TOKEN_ARRAY_CAP),
    indent: "",
    indent_len: 0,
    table_state: 0,
    prev_is_word: false,
    link_depth: 0,
    at_end: false,
    atx_close: false,
    write: parser_write,
  };
}

export function parser_end(p: Parser): void {
  if (p.pending.length > 0) {
    p.at_end = true;
    parser_write(p, "\n");
    // Whatever a handler is still holding was held for a character that never arrived, so it is
    // literal text — `<` held as a possible `<br>` is the case this recovers. Drop out of any
    // MAYBE_* token first, and strip the synthetic newline above, which is not input.
    const held = p.pending.endsWith("\n") ? p.pending.slice(0, -1) : p.pending;
    if (held !== "") {
      p.token = p.tokens[p.len] as Token;
      p.pending = "";
      p.textBuf += held;
      add_text(p);
    }
  }
  // Trailing inline tokens never closed. Stop at the first block token: an open paragraph or fence
  // is the streaming tail, and the code-block decoration sweeps rely on a fence staying open.
  while (is_inline_token(p.tokens[p.len] as Token)) {
    end_token_unresolved(p);
  }
}

const actionContinue = 0;
const actionBreak = 1;
const actionAlwaysContinue = 2;
type TokenAction = typeof actionContinue | typeof actionBreak | typeof actionAlwaysContinue;

type TokenHandler = (p: Parser, char: string, pending: string) => TokenAction;

const TOKEN_HANDLERS: Partial<Record<Token, TokenHandler>> = {
  [LINE_BREAK]: (p, char, pending) =>
    handleRootContext(p, char, pending) ? actionContinue : actionBreak,
  [DOCUMENT]: (p, char, pending) =>
    handleRootContext(p, char, pending) ? actionContinue : actionBreak,
  [BLOCKQUOTE]: (p, char, pending) =>
    handleRootContext(p, char, pending) ? actionContinue : actionBreak,
  [LIST_ORDERED]: (p, char, pending) =>
    handleRootContext(p, char, pending) ? actionContinue : actionBreak,
  [LIST_UNORDERED]: (p, char, pending) =>
    handleRootContext(p, char, pending) ? actionContinue : actionBreak,
  [HEADING_1]: (p, char, pending) =>
    handleHeading(p, char, pending) ? actionContinue : actionBreak,
  [HEADING_2]: (p, char, pending) =>
    handleHeading(p, char, pending) ? actionContinue : actionBreak,
  [HEADING_3]: (p, char, pending) =>
    handleHeading(p, char, pending) ? actionContinue : actionBreak,
  [HEADING_4]: (p, char, pending) =>
    handleHeading(p, char, pending) ? actionContinue : actionBreak,
  [HEADING_5]: (p, char, pending) =>
    handleHeading(p, char, pending) ? actionContinue : actionBreak,
  [HEADING_6]: (p, char, pending) =>
    handleHeading(p, char, pending) ? actionContinue : actionBreak,
  [TABLE]: (p, char, pending) => (handleTable(p, char, pending) ? actionContinue : actionBreak),
  [TABLE_ROW]: (p, char, pending) =>
    handleTableRow(p, char, pending) ? actionContinue : actionBreak,
  [TABLE_CELL]: (p, char, pending) =>
    handleTableCell(p, char, pending) ? actionContinue : actionBreak,
  [CODE_BLOCK]: (p, char, pending) => {
    handleCodeBlock(p, char, pending);
    return actionAlwaysContinue;
  },
  [CODE_FENCE]: (p, char, pending) => {
    handleCodeFence(p, char, pending);
    return actionAlwaysContinue;
  },
  [CODE_INLINE]: (p, char, pending) => {
    handleCodeInline(p, char, pending);
    return actionAlwaysContinue;
  },
  [MAYBE_TASK]: (p: Parser, char: string, pending: string) =>
    handleMaybeTask(p, char, pending) ? actionContinue : actionBreak,
  [STRONG_AST]: (p: Parser, char: string, _pending: string) =>
    handleStrong(p, char) ? actionContinue : actionBreak,
  [STRONG_UND]: (p: Parser, char: string, _pending: string) =>
    handleStrong(p, char) ? actionContinue : actionBreak,
  [ITALIC_AST]: (p, char, pending) =>
    handleItalic(p, char, pending) ? actionContinue : actionBreak,
  [ITALIC_UND]: (p, char, pending) =>
    handleItalic(p, char, pending) ? actionContinue : actionBreak,
  [STRIKE]: (p, _char, pending) => {
    if (pending === "~~") {
      add_text(p);
      end_token(p);
      p.pending = "";
      return actionContinue;
    }
    return actionBreak;
  },
  [MAYBE_EQ_BLOCK]: (p, char, _pending) => {
    handleMaybeEqBlock(p, char);
    return actionAlwaysContinue;
  },
  [EQUATION_BLOCK]: (p, _char, pending) => {
    // `$$` closes a block opened with `$$\n`: the newline before a closing fence leaves `pending`
    // non-empty, so a bare `$` cannot match. handleCommon's `$` equation guard is the other half.
    if (pending === "\\]" || pending === "$" || pending === "$$") {
      add_text(p);
      end_token(p);
      p.pending = "";
      return actionContinue;
    }
    return actionBreak;
  },
  [EQUATION_INLINE]: (p, char, pending) => {
    if (pending === "\\)" || p.pending.startsWith("$")) {
      add_text(p);
      end_token(p);
      p.pending = char === ")" ? "" : char;
      return actionContinue;
    }
    return actionBreak;
  },
  [MAYBE_URL]: (p: Parser, char: string, pending: string) => {
    handleMaybeURL(p, char, pending);
    return actionAlwaysContinue;
  },
  [LINK]: (p, char, pending) =>
    handleLinkOrImage(p, char, pending) ? actionContinue : actionBreak,
  [IMAGE]: (p, char, pending) =>
    handleLinkOrImage(p, char, pending) ? actionContinue : actionBreak,
  [RAW_URL]: (p, char, pending) => {
    handleRawURL(p, char, pending);
    return actionAlwaysContinue;
  },
  [MAYBE_ANGLE]: (p, char, pending) =>
    handleMaybeAngle(p, char, pending) ? actionContinue : actionBreak,
  [MAYBE_ENTITY]: (p: Parser, char: string, pending: string) =>
    handleMaybeEntity(p, char, pending) ? actionContinue : actionBreak,
};

export function parser_write(p: Parser, chunk: string): void {
  for (const char of chunk) {
    // The indent after a newline decides whether the previous block extends or a new one starts.
    if (p.token === NEWLINE) {
      switch (char) {
        case " ":
          p.indent_len += 1;
          continue;
        case "\t":
          p.indent_len += 4;
          continue;
      }

      const indent = end_tokens_to_indent(p, p.indent_len);
      p.indent_len = 0;
      p.token = p.tokens[p.len] as Token;

      if (indent > 0) {
        parser_write(p, " ".repeat(indent));
      }
    }

    const pending_with_char = p.pending + char;

    const handler = TOKEN_HANDLERS[p.token];
    if (handler !== undefined) {
      const action = handler(p, char, pending_with_char);
      if (action === actionContinue || action === actionAlwaysContinue) {
        continue;
      }
    }

    if (handleCommon(p, char, pending_with_char)) {
      continue;
    }

    // Raw URL detection: "foo http://..." can start anywhere a space or line boundary ends a word.
    if (
      p.token !== IMAGE &&
      p.token !== LINK &&
      p.token !== EQUATION_BLOCK &&
      p.token !== EQUATION_INLINE &&
      char === "h" &&
      (p.pending === " " || p.pending === "")
    ) {
      p.textBuf += p.pending;
      p.pending = char;
      p.token = MAYBE_URL;
      continue;
    }

    p.textBuf += p.pending;
    p.pending = char;
  }

  add_text(p);
}
