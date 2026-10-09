import { join } from "@cplieger/keyenc";

/** A node path as the server's `workflow.PathKey` spells it; that doc owns the rule. */
export function nodePathKey(path: readonly string[]): string {
  return join(...path);
}
