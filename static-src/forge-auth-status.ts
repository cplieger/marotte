// What a forge connection's status means on screen: one total switch over the server's ForgeStatus.

import type { ConfiguredForge, ForgeStatus } from "./wire/types.gen.js";

/** Which request a row offers: a Check, a new sign-in, or none when no request can succeed. */
type RowControl = "check" | "reconnect" | "none";

export interface RowStatus {
  /** The sentence under the account, "" when there is nothing to say. */
  line: string;
  /** "muted" for a state that heals by itself, "error" for one that needs a person. */
  tone: "none" | "muted" | "error";
  control: RowControl;
}

/** The client's copy of the server's usable class: a row whose credential reads and sends. */
export function usableStatus(s: ForgeStatus): boolean {
  return s === "connected" || s === "retrying";
}

export function rowStatus(a: ConfiguredForge, now: number = Date.now()): RowStatus {
  const message = a.error?.message ?? "";
  switch (a.status) {
    case "connected":
      return { line: "", tone: "none", control: "check" };
    case "retrying":
      return {
        line:
          a.error?.kind === "rate_limited"
            ? `The forge is rate limiting this account. ${retryWords(a.error.retry_at, now)}`
            : `The last check hit a temporary problem and runs again automatically. ${message}`,
        tone: "muted",
        control: "check",
      };
    case "refused":
      return {
        line:
          a.error?.code === "scope_insufficient"
            ? `The token is missing a permission this needs. Add it to the token on the forge, then check again. ${message}`
            : message,
        tone: "error",
        control: "check",
      };
    case "reconnect":
      return {
        line:
          message === ""
            ? "This account needs a new sign-in. Reconnect to sign in again."
            : `This account needs a new sign-in: ${message.replace(/\.$/, "")}.`,
        tone: "error",
        control: "reconnect",
      };
    case "unusable":
      return { line: message, tone: "error", control: "none" };
  }
}

/** The wait the forge's rate limit asked for, counted down to its deadline. */
function retryWords(retryAt: number | undefined, now: number): string {
  if (retryAt === undefined) {
    return "Try again later.";
  }
  const left = Math.ceil((retryAt - now) / 1000);
  if (left <= 0) {
    return "It can be checked again now.";
  }
  return `Try again in ${String(left)} second${left === 1 ? "" : "s"}.`;
}
