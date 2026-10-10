// Upload policy before a byte leaves the browser. WHERE (UPLOADS_DIR) is the composer's alone;
// WHICH files to send (screenUploads) is EVERY door's, since the limit is the server's. A diagnosis,
// not a gate: it names the offending file instead of a bare 413.

/** Where a composer upload lands: one container-root folder, like an OS Downloads folder, created
 *  by the server at boot as a granted browse mount. Absolute, because it is both the upload target
 *  (resolved against mounts) and the attachment prefix (resolved against workspace-plus-uploads).
 *  Mirrors DefaultUploadDir (internal/marotte/domain_paths.go); TestUploadPolicyMatchesClient pins. */
export const UPLOADS_DIR = "/uploads";

/** The server's upload ceiling in bytes: maxUploadSize in internal/filebrowse/filebrowse.go, applied
 *  to the WHOLE multipart body and to each file (413 either way). A compile-time constant, so
 *  duplicated rather than fetched; TestUploadPolicyMatchesClient fails on drift. The pre-flight
 *  enforces MAX_UPLOAD_TOTAL_BYTES. */
export const MAX_UPLOAD_BYTES = 256 * 1024 * 1024;

/** Bytes held back from MAX_UPLOAD_BYTES: the limit covers multipart framing too, so a file of
 *  exactly the cap never fits. 1 MiB far exceeds framing (~1 KB per part, 25 parts) and leaves the
 *  hint a round number (255 MB); under-promising is the right direction. */
const MULTIPART_RESERVE_BYTES = 1024 * 1024;

/** What one composer upload may carry, ALL FILES TOGETHER: the server's request ceiling minus the
 *  framing reserve. A TOTAL, since two files each under the cap can 413 together; one file is a
 *  batch of one. */
export const MAX_UPLOAD_TOTAL_BYTES = MAX_UPLOAD_BYTES - MULTIPART_RESERVE_BYTES;

/** Files one composer gesture may carry. A dropped folder can hand over
 *  hundreds of entries, and the progress bar is a singleton, so a large batch
 *  is a long opaque wait ending in one attachment row per file. */
export const MAX_UPLOAD_FILES = 25;

/** Why a file was refused before upload. */
interface RejectedFile {
  name: string;
  reason: string;
}

/** What the pre-flight decided. `accepted` preserves input order. */
interface PreflightResult {
  accepted: File[];
  rejected: RejectedFile[];
}

/** Deliberately not files-shared.ts's formatSize: that one is a table cell's exact size ("52.4 MB"),
 *  and a limit reads better round. */
function limitLabel(bytes: number): string {
  return `${String(Math.round(bytes / (1024 * 1024)))} MB`;
}

/** The composer's one-line upload limit, naming the TOTAL, the limit that decides a drop. */
export function uploadLimitHint(): string {
  return `Up to ${limitLabel(MAX_UPLOAD_TOTAL_BYTES)} per upload, all files together`;
}

/**
 * Decide which files to upload, before any bytes are sent: drop and paste skip a dialog, and the
 * attachment row no longer knows sizes. An empty file is legal. The size check runs twice: per file
 * ("too big") and against the RUNNING TOTAL ("does not fit in what is left"), different sentences.
 */
export function preflightUploads(files: readonly File[]): PreflightResult {
  const accepted: File[] = [];
  const rejected: RejectedFile[] = [];
  let total = 0;
  for (const f of files) {
    if (f.size > MAX_UPLOAD_TOTAL_BYTES) {
      rejected.push({
        name: f.name,
        reason: `over the ${limitLabel(MAX_UPLOAD_TOTAL_BYTES)} limit`,
      });
      continue;
    }
    if (accepted.length >= MAX_UPLOAD_FILES) {
      rejected.push({ name: f.name, reason: `over ${String(MAX_UPLOAD_FILES)} files at once` });
      continue;
    }
    if (total + f.size > MAX_UPLOAD_TOTAL_BYTES) {
      rejected.push({
        name: f.name,
        reason: `over the ${limitLabel(MAX_UPLOAD_TOTAL_BYTES)} total for one upload`,
      });
      continue;
    }
    accepted.push(f);
    total += f.size;
  }
  return { accepted, rejected };
}

/** One sentence naming what was refused and why, for a toast. Returns "" when
 *  nothing was refused, so the caller can skip the toast on the common path. */
export function preflightMessage(rejected: readonly RejectedFile[]): string {
  if (rejected.length === 0) {
    return "";
  }
  const first = rejected[0];
  if (first === undefined) {
    return "";
  }
  if (rejected.length === 1) {
    return `Skipped ${first.name}: ${first.reason}`;
  }
  return `Skipped ${String(rejected.length)} files, starting with ${first.name}: ${first.reason}`;
}

/** What one door should upload, and what to say about the rest. `files` is null
 *  when nothing survived, so a caller's whole branch is "say the message, then
 *  stop if there is nothing left". */
interface ScreenedUpload {
  files: FileList | null;
  /** Empty on the common path, so a caller can skip the toast without a count. */
  skipped: string;
}

/**
 * Screen one upload gesture: pre-flight, then hand back a FileList the caller can dispatch. Shared
 * by the four doors; each owns its TARGET and reporting, so the message is returned, not toasted
 * (no DOM here). An unrefused FileList passes through UNCHANGED.
 */
export function screenUploads(files: FileList): ScreenedUpload {
  const { accepted, rejected } = preflightUploads(Array.from(files));
  const skipped = preflightMessage(rejected);
  if (accepted.length === 0) {
    return { files: null, skipped };
  }
  if (rejected.length === 0) {
    return { files, skipped };
  }
  const dt = new DataTransfer();
  for (const f of accepted) {
    dt.items.add(f);
  }
  return { files: dt.files, skipped };
}
