// How a context document id KAS names (a steering doc, a skill) reads and where it opens, shared by
// the steering box and the turn footer.

import { relBeneath, workspaceRoot } from "./workspace.js";

/** The words a steering box's title opens with; the server writes the same. */
export const STEERING_TITLE_PREFIX = "Steering loaded: ";

/** The absolute path a local `file:` URI names, or null for any other id. Mirrors the server's
 *  `localPath` (internal/translate), so both sides name a document alike. */
function localFilePath(id: string): string | null {
  if (!id.startsWith("file://")) {
    return null;
  }
  try {
    const u = new URL(id);
    if (u.hostname !== "" && u.hostname !== "localhost") {
      return null;
    }
    const path = decodeURIComponent(u.pathname);
    return path === "" ? null : path;
  } catch {
    return null;
  }
}

/** The id KAS gives a document the client injected opens `client:<session>:<name>`. */
const CLIENT_DOC_PREFIX = "client:";

/** A client-injected document whose id carries no name, so a session id never reaches the box. */
const CLIENT_DOC_LABEL = "client";

/** A document's name: a local file's base name, a client-injected document's own name, any other
 *  id as written. Mirrors the server's `steeringDocName`. */
export function docName(id: string): string {
  if (id.startsWith(CLIENT_DOC_PREFIX)) {
    const rest = id.slice(CLIENT_DOC_PREFIX.length);
    const sep = rest.indexOf(":");
    const name = sep < 0 ? "" : rest.slice(sep + 1);
    return name === "" ? CLIENT_DOC_LABEL : name;
  }
  // Trailing slashes trimmed first, as Go's `path.Base` does.
  const path = localFilePath(id)?.replace(/\/+$/, "") ?? "";
  const base = path.slice(path.lastIndexOf("/") + 1);
  return base === "" || base === "." ? id : base;
}

/** The path the editor opens for a document, or null when it cannot: a non-file id, or a file
 *  outside the workspace (a global `~/.kiro` document), or no workspace root yet. */
export function docTarget(id: string): string | null {
  const path = localFilePath(id);
  const root = workspaceRoot();
  return path !== null && root !== "" && relBeneath(root, path) !== null ? path : null;
}
