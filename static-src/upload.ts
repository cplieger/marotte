// Universal file upload with progress. XMLHttpRequest, because fetch exposes no upload progress; one
// of two places that bypass api-client.ts (the other is transport.ts's SSE).

import { $ } from "./dom.js";
import { hasErrorString } from "./actions/index.js";
import { joinPath } from "./files-shared.js";

export interface UploadOptions {
  files: FileList;
  targetDir: string;
  /** Called on successful upload. Receives the resolved workspace paths
   *  (targetDir + filename) for each uploaded file. */
  onComplete?: (paths: string[]) => void;
  /** Called on failure. `uploaded` holds the paths that DID land: a partial batch is not rolled back
   *  (respondUploadError, internal/filebrowse/upload.go). Empty on a transport failure. */
  onError?: (msg: string, uploaded: string[]) => void;
  /** Optional signal for programmatic cancellation (e.g. chat delete, navigation). */
  signal?: AbortSignal;
}

// The pure core: response shaping with no DOM or XHR, exported for tests; uploadFiles is the shell.

/** Server filenames mapped onto container-absolute paths under the target, through `joinPath`, so a
 *  partial batch spells paths exactly as a whole one. Every target is absolute, so there is no root
 *  case: a bare name would resolve under the workspace. */
export function resolvePaths(targetDir: string, names: string[]): string[] {
  return names.map((name) => joinPath(targetDir, name));
}

/** The `uploaded` names out of a response body, empty when the body carries
 *  none (an older server, a proxy error page, an unparseable body). */
export function uploadedNames(responseText: string): string[] {
  try {
    const body = JSON.parse(responseText) as { uploaded?: unknown };
    if (Array.isArray(body.uploaded)) {
      return body.uploaded.filter((n: unknown): n is string => typeof n === "string");
    }
  } catch {
    /* not JSON; no names to recover */
  }
  return [];
}

/** The failure sentence for a partial batch. The server's message names no file (generic 413/500),
 *  so the first file after the last success is the one that failed. */
export function batchFailureMessage(
  files: FileList,
  uploadedCount: number,
  serverMsg: string,
): string {
  if (uploadedCount === 0 || uploadedCount >= files.length) {
    return serverMsg;
  }
  const failed = files[uploadedCount];
  const which = failed === undefined ? "the next file" : failed.name;
  return `${String(uploadedCount)} of ${String(files.length)} uploaded, then ${which} failed: ${serverMsg}`;
}

// One upload at a time: the progress bar is a singleton shared by browser upload and chat drop.
// The action's `scope: "upload"` queues later dispatches.

export function uploadFiles(opts: UploadOptions): void {
  // If already cancelled, bail out before showing any progress UI.
  if (opts.signal?.aborted) {
    opts.onError?.("Upload cancelled", []);
    return;
  }

  const form = new FormData();
  form.append("dir", opts.targetDir);
  for (const f of opts.files) {
    form.append("files", f);
  }

  const progress = $.uploadProgress;
  const bar = $.uploadProgressBar;
  const label = $.uploadProgressLabel;
  const cancelBtn = $.uploadProgressCancel;

  // A plain layout div, not `role="progressbar"` (which would hide the Cancel child). The native
  // <progress> reports its value, so it needs only a NAME: `aria-label`, since the label span's text
  // churns with the percentage.
  progress.classList.remove("upload-closed");
  bar.setAttribute("aria-label", `Uploading ${String(opts.files.length)} file(s)`);
  bar.value = 0;
  label.textContent = `Uploading ${String(opts.files.length)} file(s)...`;

  const xhr = new XMLHttpRequest();

  // Wire the user-visible cancel button to xhr.abort(). We still expose
  // opts.signal for programmatic abort (e.g. chat delete, navigation);
  // both paths converge on xhr.abort() below.
  cancelBtn.classList.remove("hidden");
  const onCancelClick = (): void => {
    xhr.abort();
  };
  cancelBtn.addEventListener("click", onCancelClick, { once: true });
  const onSignalAbort = (): void => {
    xhr.abort();
  };
  const teardownCancelUI = (): void => {
    cancelBtn.classList.add("hidden");
    cancelBtn.removeEventListener("click", onCancelClick);
    opts.signal?.removeEventListener("abort", onSignalAbort);
  };

  xhr.open("POST", "/api/file/upload");
  xhr.timeout = 300_000; // 5 minutes
  xhr.upload.addEventListener("progress", (e: ProgressEvent) => {
    if (e.lengthComputable) {
      const pct = Math.round((e.loaded / e.total) * 100);
      bar.value = pct;
      label.textContent = `Uploading... ${String(pct)}%`;
    } else {
      // Total size unknown. A <progress> with no `value` IS the indeterminate
      // rendering, which is the native spelling of the aria-valuenow removal
      // this replaced; the next determinate tick assigns one again.
      bar.removeAttribute("value");
      label.textContent = "Uploading...";
    }
  });
  xhr.addEventListener("load", () => {
    teardownCancelUI();
    if (xhr.status >= 200 && xhr.status < 300) {
      bar.value = 100;
      label.textContent = "Upload complete";
      setTimeout(() => {
        progress.classList.add("upload-closed");
      }, 1500);
      // Use server-returned filenames (sanitized via filepath.Base) when
      // available; fall back to client names if the server returns no array.
      const names = uploadedNames(xhr.responseText);
      const paths =
        names.length > 0
          ? resolvePaths(opts.targetDir, names)
          : resolvePaths(
              opts.targetDir,
              Array.from(opts.files, (f) => f.name),
            );
      opts.onComplete?.(paths);
    } else {
      let serverMsg = `Upload failed (${String(xhr.status)})`;
      try {
        const body: unknown = JSON.parse(xhr.responseText);
        if (hasErrorString(body)) {
          serverMsg = body.error;
        }
      } catch {
        /* ignore */
      }
      const landed = resolvePaths(opts.targetDir, uploadedNames(xhr.responseText));
      const msg = batchFailureMessage(opts.files, landed.length, serverMsg);
      label.textContent = msg;
      setTimeout(() => {
        progress.classList.add("upload-closed");
      }, 2000);
      opts.onError?.(msg, landed);
    }
  });
  xhr.addEventListener("error", () => {
    teardownCancelUI();
    label.textContent = "Upload failed";
    setTimeout(() => {
      progress.classList.add("upload-closed");
    }, 2000);
    opts.onError?.("Upload failed", []);
  });
  xhr.addEventListener("timeout", () => {
    teardownCancelUI();
    label.textContent = "Upload timed out";
    setTimeout(() => {
      progress.classList.add("upload-closed");
    }, 2000);
    opts.onError?.("Upload timed out", []);
  });
  xhr.addEventListener("abort", () => {
    teardownCancelUI();
    label.textContent = "Upload cancelled";
    setTimeout(() => {
      progress.classList.add("upload-closed");
    }, 1500);
    opts.onError?.("Upload cancelled", []);
  });
  if (opts.signal) {
    opts.signal.addEventListener("abort", onSignalAbort, { once: true });
  }
  xhr.send(form);
}
