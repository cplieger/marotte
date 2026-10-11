import { apiAction, defineAction, ActionError, retryNetwork, RETRY_STANDARD } from "./index.js";

import { routeForPath } from "../editor-types.js";
import { statIdentity } from "../file-identity.js";
import type { DiffBase } from "../viewer-rules.js";
import type { ShowAnswer } from "../viewer-reads.js";
import type { FileRead, FileRefusal, FileStat, FileWriteResult } from "../wire/types.gen.js";
import { decodeFileRefusal, decodeFileStat, decodeFileWriteResult } from "../wire/decoders.gen.js";

/** `internal/git.kindNotInRepo`: no repository owns the path, so there is no "before" and an
 *  all-add diff is correct. */
const GIT_ERR_NOT_IN_REPO = "not_in_repo";

const HTTP_NOT_FOUND = 404;
const HTTP_CONFLICT = 409;

/** A save's answer: written, or refused because the file moved under the buffer. */
export type SaveOutcome =
  | { readonly kind: "saved"; readonly result: FileWriteResult }
  | { readonly kind: "stale"; readonly refusal: FileRefusal };

/** Saves the active editor file (PUT). No auto-retry: a retry after further edits would overwrite
 *  them with the dispatch-time snapshot. `fileId` absent writes unconditionally (Overwrite). */
export const saveFile = apiAction<{ path: string; content: string; fileId?: string }, SaveOutcome>({
  name: "editor.save_file",
  scope: (args) => "file:" + args.path,
  retryable: retryNetwork,
  request: ({ path, content, fileId }) => ({
    method: "PUT",
    path: routeForPath(path).writeURL,
    body: fileId === undefined ? { content } : { content, file_id: fileId },
  }),
  decode: (data) => ({ kind: "saved", result: decodeFileWriteResult(data) }),
  decodeError: (info) =>
    info.status === HTTP_CONFLICT && info.body !== undefined
      ? { kind: "success", value: { kind: "stale", refusal: decodeFileRefusal(info.body) } }
      : undefined,
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

/** A live check's answer. `skipped` is a tick that found the tab hidden and asked nothing. */
export type StatAnswer =
  | { readonly kind: "stat"; readonly stat: FileStat }
  | { readonly kind: "gone" }
  | { readonly kind: "refused" }
  | { readonly kind: "skipped" };

/** One live-refresh check of a file's facts. A failed request resolves null, which the poller
 *  counts as a miss; every answer the server gave resolves. */
export const statFile = defineAction<{ path: string; shown: () => boolean }, StatAnswer>({
  name: "editor.stat_file",
  run: async ({ path, shown }, signal) => {
    if (!shown()) {
      return { kind: "skipped" };
    }
    const { apiGetTypedOrError } = await import("../api-client.js");
    const r = await apiGetTypedOrError(routeForPath(path).statURL, decodeFileStat, signal);
    if (r.ok && r.data !== null) {
      return statIdentity(r.data) === null ? { kind: "refused" } : { kind: "stat", stat: r.data };
    }
    if (r.status === HTTP_NOT_FOUND) {
      return { kind: "gone" };
    }
    if (r.status === 0) {
      throw new ActionError(r.error, { code: "network" });
    }
    return { kind: "refused" };
  },
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

/** The diff's two sides, or the file's own view when either side is over the cap or the working
 *  copy is binary. The working side IS the read, null when the working copy is deleted, so the
 *  buffer and the diff can only be taken from the same bytes. */
type DiffSources =
  | {
      readonly kind: "diff";
      readonly oldContent: string;
      readonly base: DiffBase;
      readonly baseLabel: string;
      readonly workingLabel: string;
      readonly read: FileRead | null;
    }
  | { readonly kind: "too_large" }
  | { readonly kind: "binary" };

type BaseSide = Pick<Extract<DiffSources, { kind: "diff" }>, "oldContent" | "base" | "baseLabel">;

/** The base pane from git's answer, or the error that fails the diff. */
function baseSide(
  old: Exclude<ShowAnswer, { kind: "too_large" }>,
  ref: string,
  gitPath: string,
): BaseSide {
  switch (old.kind) {
    case "refused":
      return { oldContent: "", base: old.code, baseLabel: ref };
    case "failed":
      throw new ActionError(old.error !== "" ? old.error : `Could not read ${ref} for ${gitPath}`, {
        code: "network",
      });
    case "git_error":
      if (old.error !== GIT_ERR_NOT_IN_REPO) {
        const detail = old.detail !== "" ? old.detail : old.error;
        throw new ActionError(`git could not read ${ref} for ${gitPath}: ${detail}`, {
          code: "network",
        });
      }
      return { oldContent: "", base: "text", baseLabel: baseLabelFor(ref, old.error, false) };
    case "blob":
      return {
        oldContent: old.show.content,
        base: "text",
        baseLabel: baseLabelFor(ref, "", old.show.absent === true),
      };
  }
}

/** Fetches git diff sources for the editor diff view. `path` arrives absolute; `/api/file` gets it
 *  container-absolute and `/api/git/show` repo- or workspace-relative. */
export const loadDiff = defineAction<{ path: string; repo: string; ref: string }, DiffSources>({
  name: "editor.load_diff",
  retryable: retryNetwork,
  run: async ({ path, repo, ref }, signal) => {
    const { readFile, showRevision } = await import("../viewer-reads.js");
    const { relToWorkspace } = await import("../workspace.js");
    // With an explicit repo the path is already repo-relative; with none,
    // the server resolves the owner from a workspace-relative path.
    const gitPath = repo !== "" ? path : relToWorkspace(path);
    const [old, work] = await Promise.all([
      showRevision({ path: gitPath, ref, repo }, signal),
      readFile(path, signal),
    ]);
    if (signal.aborted) {
      throw new ActionError("cancelled", { code: "cancelled" });
    }
    if (work.kind === "too_large" || old.kind === "too_large") {
      return { kind: "too_large" };
    }
    if (work.kind === "binary") {
      return { kind: "binary" };
    }
    const deleted = work.kind === "failed" && work.status === HTTP_NOT_FOUND;
    if (work.kind === "failed" && !deleted) {
      throw new ActionError(
        work.error !== "" ? work.error : `Could not read the working copy of ${gitPath}`,
        { code: "network" },
      );
    }
    return {
      kind: "diff",
      ...baseSide(old, ref, gitPath),
      workingLabel: deleted ? "deleted" : "working tree",
      read: work.kind === "read" ? work.read : null,
    };
  },
  error: "Could not load diff",
});
