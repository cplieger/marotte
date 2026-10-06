// The pre-session catalog's fetch policy. No DOM and no app.ts import, so it is
// testable: the reader and the sinks arrive as parameters.

import { pollUntil } from "./actions/index.js";
// Type-only, so picker.ts stays the one owner of the phase state and its copy.
import type { CatalogPhase } from "./picker.js";
import type { CatalogState } from "./wire/types.gen.js";

/** Must exceed the server's 45s budget (configTemplateTimeout,
 *  internal/agent/config_template.go): a cold first call spawns kiro-cli and
 *  unpacks the KAS runtime. Move the two together. */
export const CATALOG_REQUEST_TIMEOUT_MS = 50_000;

// The total budget is the bound that binds: about three ~50s attempts fit in
// 180s. MAX_ATTEMPTS only guards a fast failure (a 4xx) multiplying requests.
const RETRY_INTERVAL_MS = 2_000;
const RETRY_BACKOFF = { factor: 2, maxMs: 30_000 };
const MAX_ATTEMPTS = 6;
const RETRY_BUDGET_MS = 180_000;

/** One catalog answer, narrowed to the field the retry policy reads. */
export interface CatalogAnswer {
  readonly catalog: CatalogState;
}

/** What a read's verdict means to the fetch policy: apply it, or keep asking. */
export type CatalogVerdict = "usable" | "retry";

/** Total over the wire enum with no default arm, so a new value is a compile error.
 *  `empty` is usable: the endpoint is a pure cache read, so re-asking re-reads the
 *  same cache. `unavailable` converges (usually a first call that never reached KAS). */
export function readVerdict(catalog: CatalogState): CatalogVerdict {
  switch (catalog) {
    case "ready":
    case "empty":
      return "usable";
    case "unavailable":
      return "retry";
  }
}

/** What one refresh needs from its caller. */
export interface CatalogRefresh<T extends CatalogAnswer> {
  /** One read of the endpoint. `null` is a transient failure (network, decode). */
  readonly read: (signal: AbortSignal) => Promise<T | null>;
  /** Apply a usable answer. Never called on `retry`, so a degraded read cannot
   *  replace a vocabulary a successful fetch already landed. */
  readonly apply: (answer: T) => void;
  readonly setPhase: (phase: CatalogPhase) => void;
}

/** The single refresh slot, held for the life of one bounded loop. */
let inFlight: AbortController | undefined;

/** One read, then a bounded retry on the one verdict that can converge; settles
 *  `unavailable` when the budget is spent. `reset` restarts a running loop (a
 *  login may have fixed the read); every other caller declines while one runs. */
export async function refreshCatalog<T extends CatalogAnswer>(
  deps: CatalogRefresh<T>,
  opts: { readonly reset?: boolean; readonly signal?: AbortSignal } = {},
): Promise<void> {
  if (inFlight !== undefined) {
    if (opts.reset !== true) {
      return;
    }
    inFlight.abort();
  }
  const own = new AbortController();
  inFlight = own;
  // The caller's signal bounds the whole loop: a per-read cancel would retry
  // reads that abort on arrival for the full budget.
  const signal =
    opts.signal === undefined ? own.signal : AbortSignal.any([own.signal, opts.signal]);
  try {
    await runRefresh(deps, signal);
  } finally {
    // Identity-guarded: a reset already claimed the slot, so the loser must not clear it.
    if (inFlight === own) {
      inFlight = undefined;
    }
  }
}

async function runRefresh<T extends CatalogAnswer>(
  deps: CatalogRefresh<T>,
  signal: AbortSignal,
): Promise<void> {
  const first = await deps.read(signal);
  if (signal.aborted) {
    return;
  }
  if (first !== null && readVerdict(first.catalog) === "usable") {
    accept(deps, first);
    return;
  }
  const outcome = await pollUntil(deps.read, {
    intervalMs: RETRY_INTERVAL_MS,
    // Checked before onPoll, so a terminal result is applied from outcome.result below.
    until: (d) => readVerdict(d.catalog) === "usable",
    maxAttempts: MAX_ATTEMPTS,
    timeoutMs: RETRY_BUDGET_MS,
    backoff: RETRY_BACKOFF,
    signal,
  });
  if (outcome.status === "done") {
    accept(deps, outcome.result);
    return;
  }
  // Exhausted settles the phase, so "Loading models…" becomes a failure line. An
  // abort reports nothing: a newer loop owns the slot.
  if (outcome.status === "timeout") {
    deps.setPhase("unavailable");
  }
}

function accept<T extends CatalogAnswer>(deps: CatalogRefresh<T>, answer: T): void {
  deps.setPhase("ready");
  deps.apply(answer);
}
