// Forge connection rows as GET /api/forges answers them, for the suites that stage a store.

import type { ConfiguredForge } from "../wire/types.gen.js";
import type { ForgesListResponse } from "../actions/forge-list.js";

/** A connected github.com row signed in as `acct-1`; `over` replaces any field. */
export function forgeRow(over: Partial<ConfiguredForge> = {}): ConfiguredForge {
  return {
    id: "github:github.com",
    kind: "github",
    host: "github.com",
    account: "acct-1",
    status: "connected",
    ...over,
  };
}

/** The list route's answer over `forges`, every kind supported. */
export function forgesList(
  forges: readonly ConfiguredForge[],
  over: Partial<ForgesListResponse> = {},
): ForgesListResponse {
  return { forges: [...forges], kinds: ["github", "gitlab", "codeberg", "gitea"], ...over };
}

/** forge-store's usableAccount over staged `rows`, for a suite that stubs the store. */
export function usableAccountIn(rows: ForgesListResponse | null, id: string): string | undefined {
  const row = rows?.forges.find((f) => f.id === id);
  return row !== undefined && (row.status === "connected" || row.status === "retrying")
    ? row.account
    : undefined;
}

/** forge-store's row accessors over static `rows`: no read ever applies, so no apply hook runs. */
export function staticStore(rows: () => ForgesListResponse | null): {
  forgeRows: () => ForgesListResponse | null;
  usableAccount: (id: string) => string | undefined;
  onRowsApplied: (fn: () => void) => void;
} {
  return {
    forgeRows: rows,
    usableAccount: (id) => usableAccountIn(rows(), id),
    onRowsApplied: () => undefined,
  };
}
