// ---------------------------------------------------------------------------
// Tests for spec-path.ts, mirroring the Go table in internal/spec/roots_test.go
// (TestDirOf) row for row: the two implementations are one contract, so a row
// added on one side is added on the other.
// ---------------------------------------------------------------------------

import { describe, it, expect } from "vitest";
import { specDirOf } from "./spec-path.js";

const CASES: readonly (readonly [name: string, rel: string, want: string | null])[] = [
  ["spec_file", ".kiro/specs/foo/tasks.md", ".kiro/specs/foo"],
  ["sidecar", ".kiro/specs/foo/tasks.meta.json", ".kiro/specs/foo"],
  ["the_directory", ".kiro/specs/foo", ".kiro/specs/foo"],
  ["trailing_slash", ".kiro/specs/foo/", ".kiro/specs/foo"],
  ["nested_file", ".kiro/specs/foo/notes/deep.md", ".kiro/specs/foo"],
  ["repo_root", "marotte/.kiro/specs/foo/design.md", "marotte/.kiro/specs/foo"],
  ["specs_alone", ".kiro/specs", null],
  ["specs_slash", ".kiro/specs/", null],
  ["dot_name", ".kiro/specs/./tasks.md", null],
  ["dotdot_name", ".kiro/specs/../steering/x.md", null],
  ["two_prefixes", "a/b/.kiro/specs/foo", null],
  ["dot_prefix", ".hidden/.kiro/specs/foo", null],
  ["absolute", "/workspace/.kiro/specs/foo", null],
  ["steering", ".kiro/steering/go.md", null],
  ["outside", "src/main.go", null],
  ["empty", "", null],
  ["specs_in_the_wrong", "specs/.kiro/foo", null],
];

describe("specDirOf", () => {
  it.each(CASES)("%s", (_name, rel, want) => {
    expect(specDirOf(rel)).toBe(want);
  });
});
