import type { IdentityVerdict, SignedInAccount } from "../identity.js";

/** A signed-in verdict for `email`, with the vault's IAM Identity Center labels unless `over` names others. */
export function signedIn(
  email: string,
  over: Partial<SignedInAccount> = {},
): Extract<IdentityVerdict, { state: "signed_in" }> {
  return {
    state: "signed_in",
    email,
    method: "Logged in with IAM Identity Center",
    startUrl: "https://example.awsapps.com/start",
    region: "us-east-1",
    apiKey: false,
    ...over,
  };
}
