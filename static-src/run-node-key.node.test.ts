// The node path key must equal the server's `workflow.PathKey` byte for byte, or a step's run-log turns
// and transcript file under a key its row never asks for. Both sides read the same fixture.

import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";

import { nodePathKey } from "./run-node-key.js";

interface NodePathKeyCase {
  name: string;
  path: string[];
  key: string;
}

function nodePathKeyCases(): NodePathKeyCase[] {
  const raw = readFileSync(
    new URL("../internal/workflow/testdata/node_path_key.json", import.meta.url),
    "utf8",
  );
  return (JSON.parse(raw) as { cases: NodePathKeyCase[] }).cases;
}

describe("nodePathKey agrees with the server's workflow.PathKey", () => {
  const cases = nodePathKeyCases();

  it.each(cases)("$name", ({ path, key }) => {
    expect(nodePathKey(path)).toBe(key);
  });

  it("keeps every case's key distinct, the slash-alike pairs included", () => {
    const keys = cases.map((c) => nodePathKey(c.path));
    expect(new Set(keys).size).toBe(cases.length);
  });
});
