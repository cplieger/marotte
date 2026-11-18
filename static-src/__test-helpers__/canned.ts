// The shape of tests/perf/record-canned.sh's output under static-src/perf/canned/.

/** The recorder's cap on the whole set (tests/perf/measure/canned.py MAX_BYTES), which holds every
 *  view's x1 answers and streams (4.8 MB recorded) and fails a recording that grows by more than
 *  a new view's worth. The whole run tree stays out because canned-routes.json names no decoder
 *  for its route, so the recorder refuses it. */
export const CANNED_MAX_BYTES = 6 * 1024 * 1024;

/** A wire/decoders.gen.ts export, `[]` for an array of it; or, for an answer that wraps wire
 *  types in an untyped object, the decoder of each such field, `?` after a field it may omit;
 *  null for a route the server answers with no generated wire type, held only to be an object,
 *  or with no JSON body. */
export type CannedDecoder = string | { readonly [field: string]: string } | null;

/** One recorded answer, `url` relative to the origin; `request` is the request's JSON body with
 *  every `op_id` dropped (absent: no body); `status` absent is 200, and a null `body` is an answer
 *  with no JSON body. */
export interface CannedAnswer {
  readonly route: string;
  readonly url: string;
  readonly request?: unknown;
  readonly decoder: CannedDecoder;
  readonly status?: number;
  readonly body: unknown;
}

/** The recorded events whose payload bus.ts types by hand, so wire/events.gen.ts has no decoder
 *  for them; any other recorded event must be one of EVENT_DECODERS' keys. */
export const UNDECODED_EVENTS: ReadonlySet<string> = new Set(["working_label"]);

/** A recorded frame: its envelope type, the bytes of its data, and `at` ms from its file's
 *  origin. */
interface CannedFrame {
  readonly at: number;
  readonly event: string;
  readonly bytes: number;
}

/** A frame as the hub sent it, `at` ms from its file's origin: the SSE id when it carried one,
 *  and its data parsed. */
export interface CannedPayloadFrame {
  readonly at: number;
  readonly id?: string;
  readonly envelope: unknown;
}

/** frames/chat-stream-live.json: the measured turn's frames, payloads dropped, `at` from the
 *  first frame. */
export interface CannedFrames {
  readonly frames: readonly CannedFrame[];
}

/** frames/connect.json: the hello, sent at epoch ms `at`, and the frames a fresh connection
 *  carried through the cold window, each `at` from the hello. */
export interface CannedConnect {
  readonly scenario: string;
  readonly at: number;
  readonly hello: { readonly [field: string]: unknown };
  readonly frames: readonly CannedPayloadFrame[];
}

/** frames/stream.<driver>.<scenario>-<window>.json: what one stream driver's window carried to a
 *  page holding `topics` (null: everything), `at` from the window's start; one file per window,
 *  driver, scope and the chats its frames name. */
export interface CannedStream {
  readonly driver: string;
  readonly scenario: string;
  readonly window: string;
  readonly topics: readonly string[] | null;
  readonly frames: readonly CannedPayloadFrame[];
}
