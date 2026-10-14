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
