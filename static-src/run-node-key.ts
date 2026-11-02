import { join } from "@cplieger/keyenc";
import type { RunNode } from "./run-store.js";

/** A node path as the server's `workflow.PathKey` spells it; that doc owns the rule. */
export function nodePathKey(path: readonly string[]): string {
  return join(...path);
}

/** A repeat child carrying no `iteration` falls back to its `nodeId`: a row in the wrong place
 *  beats content that vanishes, the same call the server's own `runNodePath` makes when a frame
 *  carries no path. */
export function nodePathSegment(node: RunNode, parent: RunNode | undefined): string {
  if (parent?.type === "repeat" && node.iteration !== undefined) {
    return `iter-${String(node.iteration)}`;
  }
  return node.nodeId;
}
