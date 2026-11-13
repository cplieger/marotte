// Memory actions (Docs → Memories). kiro-cli's store is refetched after every mutation, so no
// optimistic state. Addressed by id: a title can repeat across scopes.

import { API_TIMEOUT_MS, apiAction, defineAction } from "./index.js";
import { readDownload } from "./download.js";
import { stillOr } from "./still-applying.js";

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
  error: stillOr("saving the memory", "Couldn't save the memory"),
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
  error: stillOr("deleting the memory", "Couldn't delete the memory"),
});

interface SyncArgs {
  on: boolean;
}

/** Writes the account's memorySync choice; the section refetches on memory_sync_changed. */
export const setMemorySync = apiAction<SyncArgs>({
  name: "memory.sync",
  dedupe: () => "memory.sync",
  request: (body) => ({ method: "PUT", path: `${MEMORY_API}/sync`, body }),
  error: stillOr("changing memory sync", "Couldn't change memory sync"),
});

/** Posts another assistant's JSONL answer as the raw body; resolves to the server's import report, unread. */
export const importMemories = apiAction<{ content: string }>({
  name: "memory.import",
  dedupe: () => "memory.import",
  request: ({ content }) => ({
    method: "POST",
    path: `${MEMORY_API}/import`,
    rawBody: content,
    headers: { "Content-Type": "text/plain; charset=utf-8" },
  }),
  error: stillOr("importing memories", "Couldn't import the memories"),
});

function localDate(d: Date): string {
  const pad = (n: number): string => String(n).padStart(2, "0");
  return `${String(d.getFullYear())}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

/** Downloads kiro-cli's memory document (`GET /api/memory/export`) through a blob, so a failure is a toast. */
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for an action with no result
export const downloadMemoryExport = defineAction<Record<string, never>, void>({
  name: "memory.export",
  dedupe: () => "memory.export",
  run: async (_args, signal) => {
    const blob = await readDownload(
      { method: "GET", path: `${MEMORY_API}/export` },
      signal,
      API_TIMEOUT_MS,
    );
    if (signal.aborted) {
      return;
    }
    const url = URL.createObjectURL(blob);
    try {
      const a = document.createElement("a");
      a.href = url;
      a.download = `kiro-memories-${localDate(new Date())}.json`;
      document.body.appendChild(a);
      a.click();
      a.remove();
    } finally {
      URL.revokeObjectURL(url);
    }
  },
  error: (_args, err) => `Couldn't export the memories: ${err.message}`,
});
