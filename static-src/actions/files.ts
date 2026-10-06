import {
  apiAction,
  defineAction,
  ActionError,
  classifyFetchError,
  hasErrorString,
  retryNetwork,
  RETRY_STANDARD,
  withTimeout,
  API_TIMEOUT_MS,
} from "./index.js";

import { joinPath } from "../files-shared.js";
import { uploadFiles } from "../upload.js";
// `joinKey` (not `joinPath`) builds every idempotency/dedupe key here, so no field's content can
// forge a component boundary. The Go middleware treats the key as opaque, so only within-client
// consistency matters.
import { join as joinKey } from "@cplieger/keyenc";

const API_FILES_ACTION = "/api/files/action";
const API_FILES_DOWNLOAD = "/api/files/download";
/** 2× standard timeout for large workspace archive downloads. */
const DOWNLOAD_TIMEOUT_MS = 60_000;
import { truncate } from "../strings.js";

interface CreateArgs {
  dir: string;
  name: string;
}

export const createFile = apiAction<CreateArgs>({
  name: "files.create_file",
  scope: (args) => "dir:" + args.dir,
  retry: RETRY_STANDARD,
  retryable: retryNetwork,
  idempotencyKey: (args) => joinKey("files.create", args.dir, args.name),
  request: (args) => ({
    method: "POST",
    path: API_FILES_ACTION,
    body: { action: "touch", path: joinPath(args.dir, args.name) },
  }),
  error: "Could not create file",
});

export const createFolder = apiAction<CreateArgs>({
  name: "files.create_folder",
  scope: (args) => "dir:" + args.dir,
  retry: RETRY_STANDARD,
  retryable: retryNetwork,
  idempotencyKey: (args) => joinKey("files.create_folder", args.dir, args.name),
  request: (args) => ({
    method: "POST",
    path: API_FILES_ACTION,
    body: { action: "mkdir", path: joinPath(args.dir, args.name) },
  }),
  error: "Could not create folder",
});

export const renameFile = apiAction<{ dir: string; original: string; newName: string }>({
  name: "files.rename",
  scope: (args) => "file:" + args.dir + "/" + args.original,
  // Components, not a string: "->" is a legal filename sequence, so a built key collided and the
  // second rename replayed the first's cached 200.
  idempotencyKey: (args) => joinKey("files.rename", args.dir, args.original, args.newName),
  request: ({ dir, original, newName }) => ({
    method: "POST",
    path: API_FILES_ACTION,
    body: { action: "rename", path: joinPath(dir, original), name: newName },
  }),
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  error: (args) => `Could not rename "${truncate(args.original)}"`,
});

interface DeleteArgs {
  dir: string;
  names: string[];
  listEl: HTMLElement;
}

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args/result
export const deleteFilesBatch = defineAction<DeleteArgs, void>({
  name: "files.delete",
  scope: (args) => "dir:" + args.dir,
  // Nested join: a comma-joined list could not distinguish ["a,b"] from ["a","b"].
  dedupe: (args) => joinKey("files.delete", joinKey(...args.names.slice().sort())),
  // Must NOT retry: a timeout may mean some items were already deleted. `listEl` is non-serializable,
  // which is safe only because retry is off and dedupe reads only `names`.
  run: async (args, signal) => {
    const timedSignal = withTimeout(signal, API_TIMEOUT_MS);
    const results = await Promise.all(
      args.names.map((name) => {
        const init: RequestInit = {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ action: "delete", path: joinPath(args.dir, name) }),
          signal: timedSignal,
        };
        return fetch(API_FILES_ACTION, init).then(
          async (r) => {
            if (!r.ok) {
              let serverError = "";
              try {
                const body: unknown = await r.json();
                if (hasErrorString(body)) {
                  serverError = body.error;
                }
              } catch {
                /* ignore */
              }
              return {
                ok: false as const,
                name,
                error: serverError || `HTTP ${String(r.status)}`,
                status: r.status,
              };
            }
            return { ok: true as const, name };
          },
          (e: unknown) => {
            if (signal.aborted) {
              return { ok: false as const, name, error: "cancelled", status: 0 };
            }
            if (e instanceof DOMException) {
              return { ok: false as const, name, error: "Request timed out", status: 0 };
            }
            return { ok: false as const, name, error: "network error", status: 0 };
          },
        );
      }),
    );
    const failed = results.filter(
      (r): r is { ok: false; name: string; error: string; status: number } => !r.ok,
    );
    if (failed.length > 0) {
      const allNetwork = failed.every((f) => f.status === 0);
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- guarded by failed.length > 0
      const firstErr = failed[0]!.error;
      if (signal.aborted) {
        throw new ActionError("cancelled", { code: "cancelled" });
      }
      const names = failed.map((f) => f.name).join(", ");
      const word =
        failed.length === 1
          ? "Could not delete"
          : `Could not delete ${String(failed.length)} items`;
      throw new ActionError(`${word} (${names}): ${firstErr}`, {
        // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- guarded by failed.length > 0
        status: failed[0]!.status,
        ...(allNetwork ? { code: firstErr === "Request timed out" ? "timeout" : "network" } : {}),
      });
    }
  },
  optimistic: (args) => {
    for (const row of [...args.listEl.children]) {
      if (!(row instanceof HTMLDivElement)) {
        continue;
      }
      if (args.names.includes(row.dataset["name"] ?? "")) {
        row.classList.add("fb-row-exiting");
      }
    }
    return undefined;
  },
  rollback: (args) => {
    // A no-op once loadDir() has replaced the rows; the fresh listing is the source of truth.
    for (const row of [...args.listEl.children]) {
      if (!(row instanceof HTMLDivElement)) {
        continue;
      }
      row.classList.remove("fb-row-exiting");
    }
  },
  error: (_args, err) => err.message,
});

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args/result
export const downloadFiles = defineAction<{ paths: string[] }, void>({
  name: "files.download",
  retryable: retryNetwork,
  run: async (args, signal) => {
    let r: Response;
    try {
      r = await fetch(API_FILES_DOWNLOAD, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ paths: args.paths }),
        signal: withTimeout(signal, DOWNLOAD_TIMEOUT_MS),
      });
    } catch (e) {
      throw classifyFetchError(e, signal);
    }
    if (!r.ok) {
      throw new ActionError("Download failed", { status: r.status });
    }
    const blob = await r.blob();
    if (signal.aborted) {
      return;
    }
    const url = URL.createObjectURL(blob);
    try {
      // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition -- defensive check after async
      if (signal.aborted) {
        return;
      }
      const a = document.createElement("a");
      a.href = url;
      a.download = "download.zip";
      document.body.appendChild(a);
      a.click();
      a.remove();
    } finally {
      URL.revokeObjectURL(url);
    }
  },
  error: "Download failed",
});

// --- files.upload ---

interface UploadArgs {
  files: FileList;
  targetDir: string;
}

/** The paths a failed upload batch DID write, on the rejection's `cause` (a partial batch is not
 *  rolled back). Read it with partialUploadOf: a rejection's cause is `unknown`. */
interface PartialUpload {
  uploaded: string[];
}

/** Recover the partial batch from a rejected upload's error. Returns [] for
 *  every other failure shape, so callers need no branch. */
export function partialUploadOf(cause: unknown): string[] {
  if (typeof cause !== "object" || cause === null || !("uploaded" in cause)) {
    return [];
  }
  const { uploaded } = cause;
  return Array.isArray(uploaded) ? uploaded.filter((p): p is string => typeof p === "string") : [];
}

export const upload = defineAction<UploadArgs, string[]>({
  name: "files.upload",
  scope: "upload",
  // Not retryable: a multipart XHR cannot safely retry after partial transmission. `scope: "upload"`
  // serializes dispatches, so two rapid drops cannot both pass or both reject.
  run: (args, signal) => {
    return new Promise<string[]>((resolve, reject) => {
      uploadFiles({
        files: args.files,
        targetDir: args.targetDir,
        signal,
        onComplete: (paths) => {
          resolve(paths);
        },
        onError: (msg, uploaded) => {
          const partial: PartialUpload = { uploaded };
          reject(new ActionError(msg, { cause: partial }));
        },
      });
    });
  },
  success: (_args, paths) =>
    paths.length === 1 ? "Uploaded 1 file" : `Uploaded ${String(paths.length)} files`,
  error: "Upload failed",
});
