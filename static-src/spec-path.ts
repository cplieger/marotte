// The spec directory a workspace-relative path is in or is: the TypeScript twin of Go's
// `spec.DirOf` (internal/spec/roots.go). The two must agree on every input, because the client uses
// the answer as the `dir` the spec route and the `spec_changed` frame both address.

const SPEC_DIR_RE = /^(?:([^/.][^/]*)\/)?\.kiro\/specs\/([^/]+)(?:\/|$)/;

/** The spec directory `rel` names (`.kiro/specs/<name>` or `<repo>/.kiro/specs/<name>`), or null
 *  when it is not inside one. Purely lexical; `.` and `..` are refused. */
export function specDirOf(rel: string): string | null {
  const m = SPEC_DIR_RE.exec(rel);
  if (m === null) {
    return null;
  }
  const name = m[2];
  if (name === undefined || name === "." || name === "..") {
    return null;
  }
  const prefix = m[1] !== undefined ? `${m[1]}/` : "";
  return `${prefix}.kiro/specs/${name}`;
}
