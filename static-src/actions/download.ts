import { apiBlobOrError } from "../api-client.js";
import { ActionError } from "./index.js";

/** A download: a GET, or a POST whose `body` is sent as JSON. */
export interface DownloadRequest {
  readonly method: "GET" | "POST";
  readonly path: string;
  readonly body?: unknown;
}

/**
 * Read a download's 2xx body whole as a `Blob`, or throw the ActionError its action reports. A
 * transport failure carries `classifyFetchError`'s message and code, so `retryNetwork` reads it the
 * same way, a body cut off mid-read included; an HTTP refusal reads "Download failed" with its status.
 */
export async function readDownload(
  req: DownloadRequest,
  signal: AbortSignal,
  timeoutMs: number,
): Promise<Blob> {
  const r = await apiBlobOrError(req.method, req.path, req.body, signal, timeoutMs);
  if (r.ok) {
    return r.data;
  }
  if (r.status !== 0) {
    throw new ActionError("Download failed", { status: r.status });
  }
  if (r.code === "cancelled") {
    throw new ActionError("Request cancelled", { code: "cancelled" });
  }
  if (r.code === "timeout") {
    throw new ActionError("Request timed out", { status: 0, code: "timeout" });
  }
  throw new ActionError(r.error, { status: 0, code: "network" });
}
