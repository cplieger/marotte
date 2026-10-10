// The editor transition suites' staged server, tab strip, save and suggestion requests and confirm
// dialog. Each suite's `vi.mock` factories spread `mocks` over the real modules, so every suite
// drives the one state object here. Production modules are imported by the driver, never here:
// a factory imports this module while the module it replaces is still loading.
import { vi } from "vitest";
import type * as EditorActions from "../actions/editor.js";
import type { SaveOutcome } from "../actions/editor.js";
import { sha256Hex } from "../sha256.js";

interface Staged {
  readonly status: number;
  readonly body: unknown;
}
export interface SaveCall {
  readonly args: { readonly path: string; readonly content: string; readonly fileId?: string };
  readonly handlers: {
    readonly onSuccess: (o: SaveOutcome) => void;
    readonly onError: (e: Error) => void;
  };
}

type Suggestion = { output?: string; error?: string } | null;

export const server = {
  routes: new Map<string, Staged>(),
  holding: new Set<string>(),
  waiting: [] as { url: string; release: () => void }[],
  hits: new Map<string, number>(),
};
export const tabs = {
  active: "",
  minted: new Map<string, string>(),
  show: (_path: string): void => {
    /* pointed at the real activateFile by the driver */
  },
};
export const saves: SaveCall[] = [];
/** Each suggestion request, answered when the test resolves it. */
export const suggestions: ((answer: Suggestion) => void)[] = [];
export const confirms: ((ok: boolean) => void)[] = [];

export const mocks = {
  apiClient: () => ({
    apiGetTypedOrError: (url: string, decoder: (v: unknown) => unknown): Promise<unknown> => {
      server.hits.set(url, (server.hits.get(url) ?? 0) + 1);
      // The bytes staged when the request was made, as the server would have read them.
      const s = server.routes.get(url);
      const answer = (): unknown => {
        if (s === undefined) {
          return { ok: false, status: 404, data: null, error: "not found", body: {} };
        }
        if (s.status < 300) {
          return { ok: true, status: s.status, data: decoder(s.body), error: "" };
        }
        const error = (s.body as { error?: string }).error ?? "";
        return { ok: false, status: s.status, data: null, error, body: s.body };
      };
      if (server.holding.has(url)) {
        return new Promise((resolve) => {
          server.waiting.push({ url, release: () => resolve(answer()) });
        });
      }
      return Promise.resolve(answer());
    },
  }),
  editorActions: (real: typeof EditorActions) => ({
    saveFile: {
      ...real.saveFile,
      dispatch: (args: SaveCall["args"], handlers: SaveCall["handlers"]): Promise<void> => {
        saves.push({ args, handlers });
        return Promise.resolve();
      },
    },
    suggestResolution: {
      ...real.suggestResolution,
      dispatch: (): Promise<Suggestion> =>
        new Promise<Suggestion>((resolve) => {
          suggestions.push(resolve);
        }),
    },
  }),
  confirm: () => ({
    confirm: (): Promise<boolean> =>
      new Promise<boolean>((resolve) => {
        confirms.push(resolve);
      }),
  }),
  git: () => ({ markGitDirty: vi.fn() }),
  tabs: () => ({
    openEditorView: (path: string): Promise<void> => {
      let id = tabs.minted.get(path);
      if (id === undefined) {
        id = `tb_${String(tabs.minted.size + 1).padStart(3, "0")}`;
        tabs.minted.set(path, id);
      }
      if (tabs.active !== id) {
        tabs.active = id;
        tabs.show(path);
      }
      return Promise.resolve();
    },
    getActiveTabId: (): string => tabs.active,
    getActiveTabKind: (): string => "editor",
    tabIdFor: (_kind: string, ref = ""): string => tabs.minted.get(ref) ?? "",
    setTabDirty: (): void => undefined,
  }),
  router: () => ({ pushRoute: (): void => undefined }),
};

export const A = "/workspace/a.txt";
export const B = "/workspace/b.txt";
export const BIG = "/workspace/big.log";
export const CONFLICTED = "/workspace/merge.txt";
export const FIRST = "first bytes\n";
export const FOLLOWED = "bytes live refresh followed\n";
export const NEW = "bytes written after the reader moved on\n";
export const CONFLICT = "<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> branch\n";

export const idOf = (s: string): string => `sha256:${sha256Hex(new TextEncoder().encode(s))}`;
export const byId = <T extends HTMLElement>(id: string): T => document.getElementById(id) as T;
export const shown = (id: string): boolean => !byId(id).classList.contains("hidden");

/** The text of the pane's visible surface: the shared pane keeps a hidden surface's last paint. */
export function onScreen(): string {
  const area = byId<HTMLTextAreaElement>("editor-content");
  const surfaces = ["editor-viewer", "editor-markdown", "editor-notice", "editor-diff-pane"];
  return [
    ...surfaces.filter(shown).map((id) => byId(id).textContent),
    shown("editor-content") ? area.value : "",
  ].join("\n");
}
export const statURL = (path: string): string => `/api/file/stat?path=${encodeURIComponent(path)}`;
export const readURL = (path: string): string => `/api/file?path=${encodeURIComponent(path)}`;
export const showURL = (rel: string): string =>
  `/api/git/show?path=${encodeURIComponent(rel)}&ref=HEAD`;

/** The file on disk: its stat and its whole read answer `content`. */
export function stage(path: string, content: string): void {
  const size = new TextEncoder().encode(content).length;
  const meta = { path, modified: "2026-10-01T00:00:00Z", size, read_only: false };
  server.routes.set(statURL(path), {
    status: 200,
    body: { ...meta, file_id: idOf(content), large: false, binary: false, utf8: true },
  });
  server.routes.set(readURL(path), {
    status: 200,
    body: { ...meta, file_id: idOf(content), content, utf8: true },
  });
}

export function stageLarge(path: string): void {
  server.routes.set(statURL(path), {
    status: 200,
    body: {
      path,
      modified: "2026-10-01T00:00:00Z",
      size: 3 << 20,
      large: true,
      binary: false,
      utf8: false,
      read_only: false,
    },
  });
}

export function hold(url: string): void {
  server.holding.add(url);
}

/** Answer every held request, then let what follows it run to the end. */
export async function releaseAll(): Promise<void> {
  server.holding.clear();
  for (const w of server.waiting.splice(0)) {
    w.release();
  }
  await drain();
}

export async function drain(): Promise<void> {
  for (let i = 0; i < 4; i++) {
    await vi.advanceTimersByTimeAsync(0);
    for (let j = 0; j < 20; j++) {
      await Promise.resolve();
    }
  }
}

export async function heldRequest(url: string): Promise<void> {
  await vi.waitFor(() => {
    if (!server.waiting.some((w) => w.url === url)) {
      throw new Error(`${url} is not held`);
    }
  });
}

/** Clear every staged request and answer between tests. */
export function resetHarness(): void {
  server.routes.clear();
  server.holding.clear();
  server.waiting.length = 0;
  server.hits.clear();
  saves.length = 0;
  suggestions.length = 0;
  confirms.length = 0;
  tabs.active = "";
  tabs.minted.clear();
}
