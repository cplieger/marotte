// The one query grammar every in-file find engine parses before it searches, so one input gets
// one disposition on every surface.

/** The longest query, in UTF-8 bytes. */
const QUERY_MAX_BYTES = 1024;

export const QUERY_INVALID = "Search text must be one line, up to 1,024 bytes.";

type FindQuery =
  | { readonly kind: "empty" }
  | { readonly kind: "invalid"; readonly message: string }
  | { readonly kind: "ok"; readonly text: string };

export function parseFindQuery(raw: string): FindQuery {
  if (raw === "") {
    return { kind: "empty" };
  }
  if (
    raw.includes("\n") ||
    raw.includes("\r") ||
    new TextEncoder().encode(raw).length > QUERY_MAX_BYTES
  ) {
    return { kind: "invalid", message: QUERY_INVALID };
  }
  return { kind: "ok", text: raw };
}
