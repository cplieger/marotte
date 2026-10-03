import { apiAction } from "./index.js";
import { decodePreviewGrant, decodePreviewStamp } from "../wire/decoders.gen.js";
import type { PreviewGrant, PreviewStamp } from "../wire/types.gen.js";

/** A grant, or the server's sentence saying why the page cannot be previewed.
 *  A refusal is an answer the view renders, not a failure to report. */
export type GrantOutcome =
  | { readonly kind: "granted"; readonly grant: PreviewGrant }
  | { readonly kind: "refused"; readonly message: string };

const isRefusal = (status: number): boolean => status >= 400 && status < 500 && status !== 429;

/** Mints a fresh grant for one page. Not deduplicated: two loads in flight are
 *  two grants, so the later load's hint and stamp describe the later read. */
export const grantPreview = apiAction<string, GrantOutcome>({
  name: "preview.grant",
  request: (path) => ({ method: "POST", path: "/api/preview/grant", body: { path } }),
  decode: (data) => ({ kind: "granted", grant: decodePreviewGrant(data) }),
  decodeError: (info) =>
    isRefusal(info.status) || info.status === 503
      ? { kind: "success", value: { kind: "refused", message: info.message } }
      : undefined,
  error: false,
});

/** Reads the page folder's change stamp. A failure resolves null, which the
 *  live-reload loop reads as "no news". */
export const previewStamp = apiAction<string, PreviewStamp>({
  name: "preview.stamp",
  dedupe: (path) => path,
  request: (path) => ({
    method: "GET",
    path: `/api/preview/stamp?path=${encodeURIComponent(path)}`,
  }),
  decode: decodePreviewStamp,
  error: false,
});
