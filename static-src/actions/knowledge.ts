// Knowledge-base actions: add / remove / re-index workspace knowledge contexts.
//
// The knowledge list is server-canonical (it lives in kiro-cli's global store,
// not marotte's chat store) and is refetched after every mutation, so these
// actions carry no optimistic state — `add` and `reindex` are async/background
// anyway (the server returns a "indexing in background" message). See
// knowledge.ts.
// ---------------------------------------------------------------------------

import { apiAction, retryNetwork, RETRY_STANDARD } from "./index.js";

/** Base path for the knowledge API — single source of truth. */
const KNOWLEDGE_API = "/api/knowledge";

// --- knowledge.add ---

interface AddArgs {
  path: string;
  name?: string;
}

/** POST /api/knowledge {path, name?} — start a background index of a directory.
 *  `error: false` because a validation message belongs beside the field it is
 *  about: `knowledge.ts`'s add form reads the failure off the dispatch's typed
 *  outcome and writes it into its own `<output>`. */
export const addKnowledge = apiAction<AddArgs, { message?: string }>({
  name: "knowledge.add",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: ({ path, name }) => ({
    method: "POST",
    path: KNOWLEDGE_API,
    body: name !== undefined && name !== "" ? { path, name } : { path },
  }),
  error: false,
});

// --- knowledge.remove ---

interface RemoveArgs {
  name: string;
}

// No auto-retry: a timed-out DELETE may have succeeded server-side; retrying
// would hit 404 and surface a misleading error.
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for an action with no result
export const removeKnowledge = apiAction<RemoveArgs, void>({
  name: "knowledge.remove",
  dedupe: (args) => `knowledge.remove:${args.name}`,
  request: ({ name }) => ({
    method: "DELETE",
    path: `${KNOWLEDGE_API}/${encodeURIComponent(name)}`,
  }),
  error: "Could not remove knowledge base",
});

// --- knowledge.reindex ---

interface ReindexArgs {
  name: string;
}

// No auto-retry: the server issues an ASYNC `update` to kiro-cli, so a timed-out
// POST may already be indexing and a retry would start a second pass over the
// same directory. The name resolves to its indexed PATH server-side (kiro-cli's
// `update` subcommand matches on the source path, not the name), so a base the
// server cannot resolve answers 404 rather than silently indexing nothing.
export const reindexKnowledge = apiAction<ReindexArgs, { message?: string }>({
  name: "knowledge.reindex",
  dedupe: (args) => `knowledge.reindex:${args.name}`,
  request: ({ name }) => ({
    method: "POST",
    path: `${KNOWLEDGE_API}/${encodeURIComponent(name)}/reindex`,
  }),
  error: "Could not re-index knowledge base",
});
