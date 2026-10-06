// System handlers: connected, settings_updated, transport:reconcile, transport:resumed, the two
// connect-hook snapshots, compaction_started, mode_changed. `transport:reconcile` fires when every
// held claim is void (new hub epoch, or must_refetch) and re-reads the whole projection; the
// everyday wake is the adapter's digest.

import { onSSE, onBus, BUS_RECONCILE, BUS_PAGE_RESUMED, decodeEnvelope, dispatch } from "../bus.js";
import { adoptThemeFromSettings, applyGeneralPanel, syncSettings } from "../settings.js";
import { restoreLastModel, restoreLastEffort } from "../session-context.js";
import { adoptLinkGuard } from "../link-guard.js";
import { setWorkspaceRoot } from "../workspace.js";
import {
  getSessions,
  setAgentStatus,
  setCurrentMode,
  setThinking,
  forgetSteers,
} from "../store.js";
import { refreshActiveView } from "../tabs.js";
import { dropDecisions, dropRunDecisions } from "../decision-dock.js";
import { closeNotificationsExcept } from "../notify.js";
import { askTarget, chatTarget, pushTargetTag, runTarget } from "../push-subject.js";
import { loadList, scheduleListRetry } from "../store-load.js";
import { clearTurnState } from "../turn-teardown.js";
import { refreshRetention } from "../retention.js";
import { adoptConnectRuns, invalidateCachedRuns, rebuildLiveRuns } from "../run-store.js";
import { fetchCatalog } from "../session-catalog.js";
import { invalidateTurnRails } from "../turn-rail.js";
import type { SSEPayloads } from "../bus.js";
import type { ServerEvent } from "../types.js";
import type { ConnectedPayload, RunInputNeededPayload } from "../wire/types.gen.js";

// The handshake alone states the workspace root (for relative agent paths); recorded here because
// transport.ts's hook skips a page's first connection.
onSSE("connected", (_chatID, p) => {
  if (typeof p.workspace === "string" && p.workspace !== "") {
    setWorkspaceRoot(p.workspace);
  }
  reconcileThinking(p);
  // OUTSIDE the reconcile's own gate on purpose: the inventory is workspace-global, so
  // it is not scoped by the chat filter and carries its own completeness flag, and an
  // early return inside the reconcile must not swallow it.
  adoptConnectRuns(p);
});

/** Reconcile every chat's `thinking` against the handshake's busy set, in BOTH directions while
 *  `busy_stated`: clear a latch the server does not confirm, adopt one no stream will re-announce
 *  (a reload replays no frames). Never writes `setTurnOpen` (one writer: the newest-page window
 *  GET). Order-independent against `loadList`. */
function reconcileThinking(p: ConnectedPayload): void {
  // A scoped or over-cap list states nothing about omitted chats, and a withheld one names none, so
  // the flag gates both arms.
  if (!p.busy_stated) {
    return;
  }
  const busy = new Set(p.busy_chats ?? []);
  let cleared = 0;
  let adopted = 0;
  for (const s of getSessions()) {
    if (busy.has(s.id)) {
      // On the TRANSITION only, like `markTurnLive`: `setThinking(true)` clears the previous
      // turn's verdicts, which is what a `done` chat that is working again needs, and running
      // it over a live latch would re-clear them on every reconnect.
      if (!s.thinking) {
        setThinking(s.id, true);
        adopted++;
      }
      continue;
    }
    if (!s.thinking) {
      continue;
    }
    // `busy_chats` names a chat with a turn of its OWN in flight, so an omitted chat's
    // `thinking` is this client's memory of a stream the server does not confirm.
    clearTurnState(s.id);
    cleared++;
  }
  if (cleared > 0) {
    console.warn(
      `[connect] cleared ${String(cleared)} stale thinking latches the server does not confirm`,
    );
  }
  if (adopted > 0) {
    // Debug rather than warn: any reload while a chat is working reaches this, so it is the
    // ordinary case rather than a disagreement with the server.
    console.debug(`[connect] adopted ${String(adopted)} turns the server reports in flight`);
  }
}

onSSE("settings_updated", () => {
  // Use restoreLastModel (cache-only), not setLastModel — that calls
  // patchSettings, which re-broadcasts settings_updated, looping forever.
  void syncSettings().then((s) => {
    // Null means the refetch failed; the local cache is a better answer
    // than inventing one, and the next frame or reload re-syncs.
    if (s === null) {
      return;
    }
    restoreLastModel(s.last_model);
    restoreLastEffort(s.last_effort_by_model);
    adoptLinkGuard(s);
    // A theme chosen on another device lands here. Safe against a loop:
    // syncSettings already seeded the write tracker from this payload.
    adoptThemeFromSettings(s);
    // Re-seeds the controls on a second device; `refreshRetention` and the spawn-time reads carry the
    // effect.
    applyGeneralPanel(s);
  });
  void refreshRetention();
});

onBus(BUS_RECONCILE, ({ cause, signal }) => {
  // The hello's snapshot frames re-establish the docks, so nothing here touches them; and nothing
  // here may assert an outcome.
  const sessions = getSessions();
  for (const s of sessions) {
    clearTurnState(s.id);
  }
  console.warn(`[reconcile:${cause}] tore down`, sessions.length, "sessions");
  // No tab reconcile: app.ts re-reads the tab collection on this event. A failed list load leaves
  // rows the reconcile licensed dropping; `scheduleListRetry` decides whether the server failed.
  void loadList(signal).then((ok) => {
    if (!ok) {
      scheduleListRetry();
    }
  });
  // ONE token for both run readers below: they act on the same event a network round
  // trip apart, so without it every live run is fetched twice. run-store.ts
  // `answeredCause` owns what the token means.
  const token = `reconcile:${cause}`;
  // The live-runs inventory is event-fed, so a lost stream leaves it blind to any
  // run that started or settled meanwhile; re-read the server's presence-based
  // projection.
  void rebuildLiveRuns(token, signal);
  // A run's node state is APPLIED from `run_progress` rather than refetched, so
  // frames lost leave a stale tree with nothing to notice it. This is the one
  // moment the client knows it may have missed some.
  invalidateCachedRuns(token);
  // The catalog rides no frame, so a gap must re-read it.
  void fetchCatalog({ signal });
  // The rail's `GET /api/chats/{id}/turns` carries no stamp, so its records are
  // invalidated by hand here; each chat re-reads its index at its next activation.
  invalidateTurnRails();
  // LAST: the active view's own gate reads the map the bind just cleared, so the
  // refresh it decides on is a real one.
  refreshActiveView();
});

/** The tag a pending item's banner carries, as its handler computed it: a run ask's run, a
 *  request-shaped ask's `askTarget`, else the chat. */
function liveAskTag(evt: ServerEvent): string {
  const chatID = evt.chat_id ?? "";
  switch (evt.type) {
    case "run_input_needed":
      return pushTargetTag(runTarget((evt.payload as RunInputNeededPayload).workflow_id));
    case "permission_needed":
    case "elicitation_needed":
    case "user_input_needed":
      return pushTargetTag(askTarget(chatID, (evt.payload as SSEPayloads[typeof evt.type]).run_id));
    default:
      return pushTargetTag(chatTarget(chatID));
  }
}

// The pending set, WHOLE and possibly empty: replace, never merge (a row resolved elsewhere left no
// frame). Each item re-dispatches through the envelope door to the live handler.
onSSE("pending_snapshot", (_chatID, p) => {
  // Decoded first, so the set of chats the snapshot still names is known before
  // anything is dropped; a malformed item is reported and skipped, not fatal to the
  // frame.
  const items: ServerEvent[] = [];
  for (const item of p.items) {
    try {
      items.push(decodeEnvelope(item));
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : String(e);
      console.error("sse: pending_snapshot item rejected:", msg);
    }
  }
  // Every chat or run banner the set does not name comes down, whether or not this
  // tab ever rendered the ask: the tab that slept through it is the one holding it.
  void closeNotificationsExcept(new Set(items.map(liveAskTag)));
  for (const s of getSessions()) {
    dropDecisions(s.id);
    // FORGOTTEN, not promoted: the items below re-offer whatever KAS still buffers.
    forgetSteers(s.id);
  }
  // A run's own asks are keyed to `run:<workflowId>`, which is no chat and so has no
  // session row for the loop above to reach.
  dropRunDecisions();
  for (const evt of items) {
    dispatch(evt);
  }
});

// The waiting-status set REPLACES every chat's agent status; a later `chat_status` re-sets a
// running chat's own.
onSSE("status_snapshot", (_chatID, p) => {
  const rows = new Map(p.rows.map((r) => [r.chat_id, r]));
  for (const s of getSessions()) {
    const row = rows.get(s.id);
    if (row === undefined) {
      setAgentStatus(s.id, "");
    } else {
      setAgentStatus(s.id, row.status);
    }
  }
});

// Views with a digest subject are answered by the adapter's digest right after; the active view of
// any other kind refreshes here.
onBus(BUS_PAGE_RESUMED, () => {
  refreshActiveView();
});

// No slash-command palette: of a session's commands only skills lack another door, and none is
// invocable, so skills live on the /docs Skills tab.

// compaction_started is advisory only: `thinking` is already true (set by the prompt send),
// and the durable record is the `compaction` ENTRY, appended where the compaction happened —
// which is also what marks the dock's rows (`handlers/entries.ts`).
onSSE("compaction_started", () => {
  // intentional no-op
});

// Mode switch echo: reflects an agent-initiated mode change so any UI
// reading current_mode_id stays current without waiting for the next
// chat_updated rebuild.
onSSE("mode_changed", (chatID, p) => {
  if (chatID === "") {
    return;
  }
  if (typeof p.mode_id !== "string" || p.mode_id === "") {
    return;
  }
  setCurrentMode(chatID, p.mode_id);
});
