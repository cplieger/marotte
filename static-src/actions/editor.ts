import { apiAction, defineAction, ActionError, retryNetwork, RETRY_STANDARD } from "./index.js";

import { routeForPath } from "../editor-types.js";

/** `internal/git.KindNotInRepo`: no repository owns the path, so there is no "before" and an
 *  all-add diff is correct. */
const GIT_ERR_NOT_IN_REPO = "not_in_repo";

/** `/api/file` statuses that answer about a changed file: 404 is a deleted working copy, 415 is
 *  binary (no text diff). */
const HTTP_NOT_FOUND = 404;
const HTTP_BINARY = 415;

/** Saves the active editor file (PUT). No auto-retry: a retry after further edits would overwrite
 *  them with the dispatch-time snapshot. */
export interface SaveFileResult {
  ok?: boolean;
  error?: string;
  /** Present only on a refused stale write: the file's current content, so
   *  the caller can show the diff rather than telling the user to reload. */
  content?: string;
  content_hash?: string;
}

export const saveFile = apiAction<
  { path: string; content: string; expectedHash?: string },
  SaveFileResult
>({
  name: "editor.save_file",
  scope: (args) => "file:" + args.path,
  retryable: retryNetwork,
  request: ({ path, content, expectedHash }) => ({
    method: "PUT",
    path: routeForPath(path).writeURL,
    // expected_hash is the digest the file had when this buffer loaded it;
    // the server refuses with 409 when it has changed since.
    body: expectedHash === undefined ? { content } : { content, expected_hash: expectedHash },
  }),
  // A 409 carries the current content, recovered as a success payload so the caller shows the diff.
  decodeError: (info) =>
    info.status === 409 ? { kind: "success", value: info.body ?? {} } : undefined,
  error: false,
});

/** Requests AI conflict resolution. No retry (not idempotent). */
export const suggestResolution = apiAction<
  { ours: string; theirs: string; context: string },
  { output?: string; error?: string }
>({
  name: "editor.suggest_resolution",
  // No dedupe: editor-conflict.ts's per-file generation counter handles supersession.
  request: (body) => ({ method: "POST", path: "/api/utility/resolve-conflict", body }),
  error: false,
});

export const fetchAgentLines = apiAction<
  { chatID: string; path: string },
  { changes: { start_line: number; end_line: number }[] }
>({
  name: "editor.fetch_agent_lines",
  dedupe: (args) => JSON.stringify([args.chatID, args.path]),
  request: ({ chatID, path }) => ({
    method: "GET",
    path: `/api/file-changes?chat_id=${encodeURIComponent(chatID)}&path=${encodeURIComponent(path)}`,
  }),
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  error: false,
});

/** The base pane's caption: "not in git" (no repo owns the path), "not in <ref>" (untracked or
 *  staged-new, signalled by `handleShow`'s `absent` marker because its content is legitimately
 *  empty), or the ref itself. */
function baseLabelFor(ref: string, gitErr: string, absentAtRef: boolean): string {
  if (gitErr === GIT_ERR_NOT_IN_REPO) {
    return "not in git";
  }
  return absentAtRef ? `not in ${ref}` : ref;
}

/** Fetches git diff sources for the editor diff view. `path` arrives absolute; `/api/file` gets it
 *  container-absolute and `/api/git/show` repo- or workspace-relative. The labels are claims about
 *  what each pane holds (see `baseLabelFor`; "deleted" vs "working tree" on the right). */
export const loadDiff = defineAction<
  { path: string; repo: string; ref: string },
  { oldContent: string; newContent: string; error: string; baseLabel: string; workingLabel: string }
>({
  name: "editor.load_diff",
  retryable: retryNetwork,
  run: async ({ path, repo, ref }, signal) => {
    const { apiGet, apiGetOrError } = await import("../api-client.js");
    const { relToWorkspace } = await import("../workspace.js");
    const repoParam = repo !== "" ? `&repo=${encodeURIComponent(repo)}` : "";
    // With an explicit repo the path is already repo-relative; with none,
    // the server resolves the owner from a workspace-relative path.
    const gitPath = repo !== "" ? path : relToWorkspace(path);
    // apiGetOrError: two working-copy statuses are answers (HTTP_NOT_FOUND / HTTP_BINARY).
    const [oldD, newD] = await Promise.all([
      apiGet<{ content?: string; error?: string; detail?: string; absent?: boolean }>(
        `/api/git/show?path=${encodeURIComponent(gitPath)}&ref=${encodeURIComponent(ref)}${repoParam}`,
        signal,
      ),
      apiGetOrError<{ content?: string; error?: string }>(
        `/api/file?path=${encodeURIComponent(path)}`,
        signal,
      ),
    ]);
    if (signal.aborted) {
      throw new ActionError("cancelled", { code: "cancelled" });
    }
    const deleted = newD.status === HTTP_NOT_FOUND;
    const binary = newD.status === HTTP_BINARY;
    // Each side is named — "one of them failed" is not actionable.
    if (!newD.ok && !deleted && !binary) {
      throw new ActionError(`Could not read the working copy of ${gitPath}`, { code: "network" });
    }
    if (oldD === null) {
      throw new ActionError(`Could not read ${ref} for ${gitPath}`, { code: "network" });
    }
    const gitErr = oldD.error ?? "";
    if (gitErr !== "" && gitErr !== GIT_ERR_NOT_IN_REPO) {
      throw new ActionError(`git could not read ${ref} for ${gitPath}: ${oldD.detail ?? gitErr}`, {
        code: "network",
      });
    }
    return {
      oldContent: oldD.content ?? "",
      newContent: newD.data?.content ?? "",
      error: binary
        ? `${gitPath} is a binary file, so there is no text diff to show.`
        : (newD.data?.error ?? ""),
      baseLabel: baseLabelFor(ref, gitErr, oldD.absent === true),
      workingLabel: deleted ? "deleted" : "working tree",
    };
  },
  error: "Could not load diff",
});
