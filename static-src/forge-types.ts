import type { ForgeKind, PartialResult } from "./wire/types.gen.js";

export type { ForgeKind };

/** Why a list stopped short, in words, or "" when it is whole or what it lacks is
 *  the page `next` names: a page limit with a cursor is ordinary paging. */
export function partialWhy(p: PartialResult | undefined, next: string | undefined): string {
  if (p === undefined || (p.reason === "pagination_cap" && next !== undefined && next !== "")) {
    return "";
  }
  const left =
    p.omitted_at_least > 0 ? `. At least ${String(p.omitted_at_least)} more were not read` : "";
  return `${PARTIAL_REASONS.get(p.reason) ?? "the forge did not say why"}${left}`;
}

const PARTIAL_REASONS = new Map([
  ["budget", "the read stopped at its request budget"],
  ["rate_limited", "reads are being held back to stay within the forge's rate limit"],
  ["graphql_partial", "the forge answered with errors beside the rows"],
  ["pagination_cap", "the read reached its page limit"],
  ["result_window", "the forge serves only part of this list"],
]);

/** Human-readable display name for a forge kind. */
export function kindTitle(kind: ForgeKind): string {
  return FORGE_META[kind].title;
}

/** Forge kinds where the host is locked (not user-editable). GitHub stays
 *  editable because a GitHub Enterprise server is a GitHub connection. */
export const HOST_LOCKED_KINDS: readonly ForgeKind[] = ["codeberg"];

/** Default host per forge kind. */
export const DEFAULT_HOST: Record<ForgeKind, string> = {
  github: "github.com",
  gitlab: "gitlab.com",
  codeberg: "codeberg.org",
  gitea: "",
};

/** Display name and icon glyph per forge kind. */
export const FORGE_META: Record<
  ForgeKind,
  {
    title: string;
  }
> = {
  github: {
    title: "GitHub",
  },
  gitlab: {
    title: "GitLab",
  },
  codeberg: {
    title: "Codeberg",
  },
  gitea: {
    title: "Gitea / Forgejo",
  },
};

/** URL template for the account-management page on each forge kind. */
export const FORGE_URLS: Record<ForgeKind, (host: string) => string> = {
  github: (host) => `https://${host}/settings/profile`,
  gitlab: (host) => `https://${host}/-/profile`,
  codeberg: (host) => `https://${host}/user/settings`,
  gitea: (host) => (host === "" ? "" : `https://${host}/user/settings`),
};
