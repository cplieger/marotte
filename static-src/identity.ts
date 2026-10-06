// Who is signed in, as a three-state answer. `unavailable` must not read as a sign-out, or boot stalls behind the
// splash. One module because the boot chain and the login modal ask the same question; it maps, and what to render
// for `unavailable` is the boot chain's call.

import { apiGetTyped } from "./api-client.js";
import { decodeWhoamiResponse } from "./wire/decoders.gen.js";

/**
 * The three answers, plus the transport's. A discriminated union, so a branch over it is total and `unavailable`
 * cannot be mistaken for signed out.
 */
export type IdentityVerdict =
  | { state: "signed_in"; email: string }
  | { state: "signed_out" }
  | { state: "unavailable"; reason: string };

/**
 * A null response (fetch failed or did not decode) makes the same claim as the server's `unavailable`; only the
 * reason differs, since the remedies differ.
 */
const REASON_UNREACHABLE = "marotte could not be reached";

/** Read the current identity. Never rejects: every failure is the `unavailable` arm. */
export async function resolveIdentity(): Promise<IdentityVerdict> {
  const d = await apiGetTyped("/api/whoami", decodeWhoamiResponse);
  if (d === null) {
    return { state: "unavailable", reason: REASON_UNREACHABLE };
  }
  switch (d.state) {
    case "signed_in":
      return { state: "signed_in", email: d.email ?? "" };
    case "signed_out":
      return { state: "signed_out" };
    case "unavailable":
      return { state: "unavailable", reason: d.reason ?? REASON_UNREACHABLE };
  }
}

// No email accessor: callers hand the whole verdict to `renderIdentity`, so the row says "unknown" for `unavailable`.
