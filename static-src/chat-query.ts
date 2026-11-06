// The chat search's query parse, the twin of Go's parseSearchQuery (internal/chat/search.go), pinned to it by
// internal/chat/testdata/search_query.json.

import { parseQuery, type Term } from "./textsearch/query.js";

/** A parsed chat query: the free-text terms and the scoped filters, `turn` -1 when absent. */
interface ChatQuery {
  readonly terms: readonly Term[];
  readonly file: string;
  readonly tool: string;
  readonly turn: number;
}

/** `file:` and `tool:` take any value, `turn:` a positive integer; anything else stays text. */
function isChatFilter(name: string, value: string): boolean {
  if (name === "file" || name === "tool") {
    return true;
  }
  return name === "turn" && positiveInt(value) > 0;
}

/** Go's `int` on the server: `strconv.Atoi` refuses a value past it. */
const MAX_INT64 = 9223372036854775807n;

/** `value` as Go's `strconv.Atoi` reads a positive decimal, else 0. */
function positiveInt(value: string): number {
  return /^\+?\d+$/u.test(value) && BigInt(value) <= MAX_INT64 ? Number(value) : 0;
}

export function parseChatQuery(raw: string): ChatQuery {
  const q = parseQuery(raw, isChatFilter);
  let file = "";
  let tool = "";
  let turn = -1;
  for (const f of q.filters) {
    if (f.name === "file") {
      file = f.value.toLowerCase();
    } else if (f.name === "tool") {
      tool = f.value.toLowerCase();
    } else {
      turn = positiveInt(f.value);
    }
  }
  return { terms: q.terms, file, tool, turn };
}
