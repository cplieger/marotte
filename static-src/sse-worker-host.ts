// The profile's stream owner, hosted in the SharedWorker: `@cplieger/sse`'s `createWorkerHost`
// around marotte's two routes, plus the two decisions that are marotte's own. `sse-worker.ts` is
// the classic-script entry that wires `onconnect`.

import {
  type DigestClient,
  type DigestVerdict,
  type LifecycleEvent,
  type RevalidateContext,
  type State,
  type Stream,
  type TabSet,
  type VersionMap,
  type WorkerHost,
  createDigestClient,
  createVersionMap,
  createWorkerHost,
} from "@cplieger/sse";

/** The digest's own budget, matching the server's `RouteTimeout` on `POST /api/sync`. */
const DIGEST_TIMEOUT_MS = 10_000;

/** No subject moved: a verdict the tabs apply as nothing. */
const NOTHING_MOVED: DigestVerdict = { changed: [], removed: [] };

/** Whether a subject's set reaches a client only through a hello's connect hook. */
function hookCarried(subject: State): boolean {
  return subject.kind === "pending" || subject.kind === "status";
}

/** The library's `revalidate` on the host side: one digest over the profile's map, then one run
 *  fanned to every acknowledging tab carrying the verdict, settled when each has answered,
 *  expired or left. A full run and an empty map skip the digest; the run still reaches the tabs,
 *  because a wake refreshes a tab's active view whatever the map holds. */
export async function profileRevalidate(
  ctx: RevalidateContext,
  tabs: TabSet,
  versions: VersionMap,
  digest: DigestClient,
  stream: Stream,
): Promise<void> {
  if (ctx.full) {
    await tabs.run(ctx);
    return;
  }
  const snapshot = versions.snapshot();
  if (snapshot.held.length === 0) {
    await tabs.run(ctx, NOTHING_MOVED);
    return;
  }
  const result = await digest.check(snapshot, ctx.signal);
  if (result.kind === "must_refetch") {
    versions.bind(result.epoch);
    await tabs.run({ ...ctx, epoch: result.epoch, full: true });
    return;
  }
  await tabs.run(ctx, { changed: result.changed, removed: result.removed });
  if (ctx.cause !== "hello" && result.changed.some(hookCarried)) {
    stream.resetCursor();
    stream.reconnect();
  }
}

/** Every tab that attaches to a LIVE stream needs the connect hook's two snapshot frames
 *  (`pending_snapshot`, `status_snapshot`): those sets reach a client only through the hook, and
 *  the hook ran once, for the tabs attached at that connect. */
function reconnectForAttach(ev: LifecycleEvent, stream: Stream): void {
  if (ev.kind === "tab_attached" && ev.state === "open") {
    stream.reconnect();
  }
}

/** Build the host over marotte's routes. */
export function createSSEHost(): WorkerHost {
  const versions = createVersionMap();
  const digest = createDigestClient({ url: "/api/sync", timeoutMs: DIGEST_TIMEOUT_MS });
  const created: WorkerHost = createWorkerHost({
    url: "/api/events",
    // `SSE-Client` is filled by the host from the first attaching tab's tag, before the first
    // connect; an empty header would reach the server as an invalid tag.
    alive: { url: "/api/events/alive" },
    versions,
    revalidate: (ctx, tabs) => profileRevalidate(ctx, tabs, versions, digest, created.stream()),
    onLifecycle(ev) {
      reconnectForAttach(ev, created.stream());
    },
  });
  return created;
}
