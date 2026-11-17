// The client's copy of the workspace model catalog, and the one statement of what to show while
// it holds no models. Written by session-catalog.ts, read by every model surface.

import { signal, touch } from "@cplieger/reactive";
import { announce } from "./legacy-ui/announce.js";
import type { ModelInfo } from "./types.js";

/** Where the catalog's fetch has got to, which an empty list alone cannot say: loading, settled
 *  empty and failed all hold no models. */
export type CatalogPhase = "unknown" | "ready" | "unavailable";

/** The line to show where a model list would be, and whether an answer is still coming. */
interface CatalogNotice {
  readonly text: string;
  readonly busy: boolean;
  /** Whether asking again can change the answer, which is what offers the Retry. True for a
   *  settled empty catalog too: the server reports a cold KAS cache as `empty`, so a re-read can
   *  differ. */
  readonly retry: boolean;
}

/** The Retry's accessible name. `Retry` alone names no subject, and a keyboard user hears the
 *  name without the notice beside it. */
export const RETRY_LABEL = "Retry loading the model list";
const RETRY_STARTED = "Reloading the model list…";
const RETRY_LOADED = "Model list loaded.";

let models: ModelInfo[] = [];
let phase: CatalogPhase = "unknown";
let catalogRetry: (() => Promise<void>) | null = null;

const revision = signal(0);

function bump(): void {
  revision.value = revision.peek() + 1;
}

export function getCachedModels(): ModelInfo[] {
  return models;
}

export function setCatalogModels(next: ModelInfo[]): void {
  models = next;
  bump();
}

export function setCatalogPhase(next: CatalogPhase): void {
  if (next === phase) {
    return;
  }
  phase = next;
  bump();
}

/** A tracked read: an effect calling it re-runs when the models or the phase change. */
export function watchCatalog(): void {
  touch(revision);
}

/** The notice standing in for a model list, or null when there is a list to show. */
export function catalogNotice(): CatalogNotice | null {
  if (models.length > 0) {
    return null;
  }
  switch (phase) {
    case "unknown":
      return { text: "Loading models…", busy: true, retry: false };
    case "ready":
      return { text: "No models available yet.", busy: false, retry: true };
    case "unavailable":
      return { text: "Could not load the model list.", busy: false, retry: true };
  }
}

/** The re-read Retry runs, injected by app.ts; its promise settles once the read has. */
export function setCatalogRetry(run: () => Promise<void>): void {
  catalogRetry = run;
}

/** Ask for the catalog again and announce the start and the verdict. */
export function retryCatalog(): void {
  const run = catalogRetry;
  if (run === null) {
    return;
  }
  announce(RETRY_STARTED);
  void run().then(() => {
    const settled = catalogNotice();
    announce(settled === null ? RETRY_LOADED : settled.text);
  });
}
