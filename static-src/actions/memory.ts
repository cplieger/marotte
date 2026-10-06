// Memory actions (Docs → Memories). kiro-cli's store is refetched after every mutation, so no
// optimistic state. Addressed by id: a title can repeat across scopes.

import { apiAction } from "./index.js";

const MEMORY_API = "/api/memory";

/** The fields an edit may change; an absent one is left as it is. */
export interface MemoryEdit {
  title?: string;
  summary?: string;
  content?: string;
}

interface UpdateArgs {
  id: string;
  edit: MemoryEdit;
}

// No auto-retry on either: a timed-out write may have landed.
export const updateMemory = apiAction<UpdateArgs>({
  name: "memory.update",
  dedupe: (args) => `memory.update:${args.id}`,
  request: ({ id, edit }) => ({
    method: "PATCH",
    path: `${MEMORY_API}/${encodeURIComponent(id)}`,
    body: edit,
  }),
  error: "Couldn't save the memory",
});

interface DeleteArgs {
  id: string;
}

export const deleteMemory = apiAction<DeleteArgs>({
  name: "memory.delete",
  dedupe: (args) => `memory.delete:${args.id}`,
  request: ({ id }) => ({
    method: "DELETE",
    path: `${MEMORY_API}/${encodeURIComponent(id)}`,
  }),
  error: "Couldn't delete the memory",
});
