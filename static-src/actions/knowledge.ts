// Knowledge-base actions. The list is server-canonical (kiro-cli's global store) and refetched
// after every mutation, so no optimistic state.

import { apiAction, retryNetwork, RETRY_STANDARD } from "./index.js";

const KNOWLEDGE_API = "/api/knowledge";

interface AddArgs {
  path: string;
  name?: string;
}

/** Start a background index of a directory. `error: false`: the add form writes the failure
 *  beside its field from the dispatch's typed outcome. */
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

interface ReindexArgs {
  name: string;
}

// No auto-retry: the server issues an ASYNC kiro-cli `update`, so a timed-out POST may already be
// indexing. `update` matches on the source path, so the name resolves server-side (404 if not).
export const reindexKnowledge = apiAction<ReindexArgs, { message?: string }>({
  name: "knowledge.reindex",
  dedupe: (args) => `knowledge.reindex:${args.name}`,
  request: ({ name }) => ({
    method: "POST",
    path: `${KNOWLEDGE_API}/${encodeURIComponent(name)}/reindex`,
  }),
  error: "Could not re-index knowledge base",
});

interface CancelArgs {
  name: string;
}

// No auto-retry: a timed-out POST may already have stopped the index; a retry would 404.
export const cancelKnowledgeIndexing = apiAction<CancelArgs, { message?: string }>({
  name: "knowledge.cancel",
  dedupe: (args) => `knowledge.cancel:${args.name}`,
  request: ({ name }) => ({
    method: "POST",
    path: `${KNOWLEDGE_API}/${encodeURIComponent(name)}/cancel`,
  }),
  error: "Couldn't stop indexing",
});

// No auto-retry, for removeKnowledge's reason.
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for an action with no result
export const clearKnowledge = apiAction<void, void>({
  name: "knowledge.clear",
  request: () => ({ method: "DELETE", path: KNOWLEDGE_API }),
  error: "Couldn't clear knowledge bases",
});
