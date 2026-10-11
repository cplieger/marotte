// The viewer's two content reads, GET /api/file and GET /api/git/show, each answered as one typed
// outcome: a success through its generated decoder, a refusal through its own, and a refusal whose
// body is not the shape its status promises as a plain failure.

import { apiGetTypedOrError, type Decoder } from "./api-client.js";
import { routeForPath } from "./editor-types.js";
import {
  decodeFileRead,
  decodeFileRefusal,
  decodeShowRefusal,
  decodeShowResponse,
} from "./wire/decoders.gen.js";
import type { FileRead, ShowRefusalCode, ShowResponse } from "./wire/types.gen.js";

const HTTP_TOO_LARGE = 413;
const HTTP_UNSUPPORTED = 415;

/** `failed.error` is the server's sentence, "" when it gave none or its refusal was malformed. */
type ReadAnswer =
  | { readonly kind: "read"; readonly read: FileRead }
  | { readonly kind: "too_large"; readonly size: number }
  | { readonly kind: "binary"; readonly error: string }
  | { readonly kind: "failed"; readonly status: number; readonly error: string };

/** The whole file, or why the server would not send it. */
export async function readFile(path: string, signal?: AbortSignal): Promise<ReadAnswer> {
  const r = await apiGetTypedOrError(routeForPath(path).readURL, decodeFileRead, signal);
  if (r.ok && r.data !== null) {
    return { kind: "read", read: r.data };
  }
  if (r.status !== HTTP_TOO_LARGE && r.status !== HTTP_UNSUPPORTED) {
    return { kind: "failed", status: r.status, error: r.error };
  }
  const refusal = decodedOrNull(decodeFileRefusal, r.body);
  if (r.status === HTTP_TOO_LARGE && refusal?.code === "too_large" && refusal.size !== undefined) {
    return { kind: "too_large", size: refusal.size };
  }
  if (r.status === HTTP_UNSUPPORTED && refusal?.code === "binary") {
    return { kind: "binary", error: refusal.error };
  }
  return { kind: "failed", status: r.status, error: "" };
}

/** A 200 is a blob or git's own `{error, detail}` envelope. */
export type ShowAnswer =
  | { readonly kind: "blob"; readonly show: ShowResponse }
  | { readonly kind: "git_error"; readonly error: string; readonly detail: string }
  | { readonly kind: "too_large" }
  /** A revision git holds but not as text. */
  | { readonly kind: "refused"; readonly code: Exclude<ShowRefusalCode, "too_large"> }
  | { readonly kind: "failed"; readonly error: string };

type ShowOk = Extract<ShowAnswer, { kind: "blob" | "git_error" }>;

function decodeShowOk(v: unknown): ShowOk {
  const o = v !== null && typeof v === "object" ? (v as Record<string, unknown>) : {};
  if (typeof o["error"] === "string") {
    return {
      kind: "git_error",
      error: o["error"],
      detail: typeof o["detail"] === "string" ? o["detail"] : "",
    };
  }
  return { kind: "blob", show: decodeShowResponse(v) };
}

/** `path` at `ref`; with `repo` "" the server resolves the owning repo from a workspace path. */
export async function showRevision(
  at: { readonly path: string; readonly ref: string; readonly repo: string },
  signal?: AbortSignal,
): Promise<ShowAnswer> {
  const repoParam = at.repo !== "" ? `&repo=${encodeURIComponent(at.repo)}` : "";
  const r = await apiGetTypedOrError(
    `/api/git/show?path=${encodeURIComponent(at.path)}&ref=${encodeURIComponent(at.ref)}${repoParam}`,
    decodeShowOk,
    signal,
  );
  if (r.ok && r.data !== null) {
    return r.data;
  }
  if (r.status !== HTTP_TOO_LARGE && r.status !== HTTP_UNSUPPORTED) {
    return { kind: "failed", error: r.error };
  }
  const code = decodedOrNull(decodeShowRefusal, r.body)?.code;
  if (r.status === HTTP_TOO_LARGE && code === "too_large") {
    return { kind: "too_large" };
  }
  if (r.status === HTTP_UNSUPPORTED && (code === "binary" || code === "not_utf8")) {
    return { kind: "refused", code };
  }
  return { kind: "failed", error: "" };
}

function decodedOrNull<T>(decode: Decoder<T>, body: unknown): T | null {
  try {
    return decode(body);
  } catch {
    return null;
  }
}
