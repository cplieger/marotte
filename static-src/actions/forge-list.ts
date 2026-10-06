// The one action that reads GET /api/forges (forge-store.ts's request), and its wire decoder.

import { apiAction } from "./index.js";
import type { Decoder } from "../validators.js";
import { asObject, decodeArray } from "../validators.js";
import { decodeConfiguredForge } from "../wire/decoders.gen.js";
import type { ConfiguredForge, ForgeKind } from "../wire/types.gen.js";

const API_PATH_FORGES = "/api/forges" as const;

/** The /api/forges response. `oauth` reports which kinds support the browser
 *  device flow, so the Sources tab can offer it only where it exists. */
export interface ForgesListResponse {
  forges: ConfiguredForge[];
  kinds: ForgeKind[];
  oauth?: Partial<Record<ForgeKind, boolean>>;
}

const FORGE_KINDS: readonly ForgeKind[] = ["github", "gitlab", "codeberg", "gitea"];

const decodeForgesListResponse: Decoder<ForgesListResponse> = (v) => {
  const o = asObject(v, "$.forges_list");
  const out: ForgesListResponse = {
    forges: decodeArray(o["forges"], decodeConfiguredForge, "$.forges_list.forges"),
    kinds: decodeArray(
      o["kinds"],
      (el) => {
        if (typeof el !== "string" || !(FORGE_KINDS as readonly string[]).includes(el)) {
          throw new TypeError(`expected ForgeKind, got ${JSON.stringify(el)}`);
        }
        return el as ForgeKind;
      },
      "$.forges_list.kinds",
    ),
  };
  if (o["oauth"] !== undefined) {
    const oauthObj = asObject(o["oauth"], "$.forges_list.oauth");
    const partial: Partial<Record<ForgeKind, boolean>> = {};
    for (const [k, val] of Object.entries(oauthObj)) {
      if ((FORGE_KINDS as readonly string[]).includes(k) && typeof val === "boolean") {
        partial[k as ForgeKind] = val;
      }
    }
    out.oauth = partial;
  }
  return out;
};

/** Fetch the configured forges. Deduped so a poll tick, an SSE nudge and a
 *  read-through all coalesce into one request. Advisory data: no toast, no
 *  retry — the badge renders the failure as its own error state. */
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void as a generic argument for an action taking no args
export const listForges = apiAction<void, ForgesListResponse>({
  name: "forges.list",
  request: () => ({ method: "GET", path: API_PATH_FORGES }),
  decode: (data) => decodeForgesListResponse(data),
  dedupe: true,
  error: false,
  success: false,
});
