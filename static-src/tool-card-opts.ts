// What a tool card is built FROM, and the mapping from a domain tool call to it. Its own
// module so a test mocking `tool-card.js` cannot stub this field copy too and silently
// drop the field the case asserts.

import type { ToolStatus, ToolDiff, TextSpan, ToolCall } from "./types.js";
import type { ToolDenial, ToolDisclosed, ToolInteraction, ToolOffload } from "./types.js";

export interface BuildToolCardOpts {
  id: string;
  title: string;
  kind: string;
  status: ToolStatus;
  input?: Record<string, unknown>;
  output?: string;
  /** Style spans for `output`, parsed server-side. */
  outputSpans?: TextSpan[];
  diffs?: ToolDiff[];
  /** Live mode: spinner, start timestamp, raw-input block and expand-on-fail. */
  live: boolean;
  /** KAS's `_meta.kiro.disclosedContext`: the skill or steering document a
   *  `disclose_context` call loaded. */
  disclosed?: ToolDisclosed | undefined;
  /** KAS's `_meta.kiro.policyDenial`: the rule that refused this call. */
  denial?: ToolDenial | undefined;
  /** Where KAS wrote the full output when it was too large for the model;
   *  `output` is then its preview. */
  offload?: ToolOffload | undefined;
  /** How the call's approval or question was answered. */
  interaction?: ToolInteraction | undefined;
  /** The tool RAN CORRECTLY AND REFUSED; `status` stays `completed` and the reason is
   *  the tool's own `output`. */
  declined?: boolean;
  /** The workspace-relative file a hook card's hook is defined in. */
  sourcePath?: string;
  /** `input`, `output` and `diffs` are a PREVIEW of `GET /api/chats/{id}/tools/{id}`;
   *  set only by the transcript read path. */
  hasFull?: boolean;
  /** The full output's byte length, only alongside `hasFull`. */
  outputBytes?: number;
  /** Which chat to fetch the bulk from; absent for a workflow step's card. */
  chatID?: string;
  /** Build the details region ALREADY OPEN, restoring the reader's state across a
   *  re-mount; its only input is that state. A region opened by an EVENT goes through
   *  `expandToolDetails`, which animates. Never set by `toolCardOptsFor`. */
  detailsOpen?: boolean;
}

/** The `BuildToolCardOpts` a domain tool call describes. Guarded assignments, not a
 *  spread, because `exactOptionalPropertyTypes` refuses `undefined` for an optional
 *  property. `chatID` is `""` for work no chat owns (a parentless run's step). */
export function toolCardOptsFor(tc: ToolCall, live: boolean, chatID = ""): BuildToolCardOpts {
  const opts: BuildToolCardOpts = {
    id: tc.id,
    title: tc.title,
    kind: tc.kind,
    status: tc.status,
    live,
  };
  if (chatID !== "") {
    opts.chatID = chatID;
  }
  if (tc.has_full === true) {
    opts.hasFull = true;
    if (tc.output_bytes !== undefined) {
      opts.outputBytes = tc.output_bytes;
    }
  }
  const rawInput = tc.input as Record<string, unknown> | undefined;
  if (rawInput !== undefined) {
    opts.input = rawInput;
  }
  if (tc.output !== undefined) {
    opts.output = tc.output;
  }
  if (tc.output_spans !== undefined && tc.output_spans.length > 0) {
    opts.outputSpans = tc.output_spans;
  }
  if (tc.diffs !== undefined && tc.diffs.length > 0) {
    opts.diffs = tc.diffs;
  }
  if (tc.disclosed !== undefined) {
    opts.disclosed = tc.disclosed;
  }
  if (tc.denial !== undefined) {
    opts.denial = tc.denial;
  }
  if (tc.offload !== undefined) {
    opts.offload = tc.offload;
  }
  if (tc.interaction !== undefined) {
    opts.interaction = tc.interaction;
  }
  if (tc.declined === true) {
    opts.declined = true;
  }
  if (tc.source_path !== undefined && tc.source_path !== "") {
    opts.sourcePath = tc.source_path;
  }
  return opts;
}
