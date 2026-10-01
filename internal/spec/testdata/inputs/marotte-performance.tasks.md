# Marotte UI Performance — Implementation Plan

Revision 8 (post review round 7). Order follows design §0; each acceptance test lives in the
task that makes it passable. House rules: greenfield shapes, delete what a shape replaces,
characterization-first on pinned suites (run the owning suite, name the pinned behaviors,
migrate deliberately — design §10), mock drift guards updated FIRST in any task that changes a
mocked module's exports, no new external dependencies, commits stay local. Work happens on a
marotte feature branch; the ui-primitives change is staged in its own repo.

## Phase 1 — Render pipeline

- [ ] 1. Per-chat message versions + transcript-fact rule
  - `SignalMap`-backed per-chat versions (`ensure(id, 0)`), per-chat microtask coalescing with
    a scheduled-id Set that skips removed ids; DELETE the active-chat gate and the global
    `messagesVersion` export; clear version signal + scheduled marker on removeChat/eviction.
  - Transcript-fact rule at `bumpMessages`: `setThinking`, `setTurnFailed`, `setTurnDone`,
    `clearTurnFailed`, `clearTurnDone`, `setWorkingLabel`, steer family
    (`recordSteerSent/Queued`, `promoteSteer`, clears) all bump.
  - Migrate all consumers: transcript effect `(watchActiveId, messagesVersionOf)`,
    `task-list.ts`, `store-load.ts`/`chat-search.ts`, `subagent-view.ts` (own chat's version);
    move the ordering comment at `chat.ts:313` with the rename.
  - Mock drift guards first: `__test-helpers__/store-mock.test.ts` (+ scroll/tabs guards if
    touched).
  - Tests: header tick no paint / message event paints; steer_injected mark with no message
    event; turn_ended finalize with no later event; prompt_failed footer; gap-path clears
    repaint; subagent background append; remove/evict-before-flush; handler suites
    (`handlers/{chat,turn,steer,messages}.test.ts`, `messages-events.test.ts`) migrated here.
  - _Requirements: 3.7, 8.1_

- [ ] 2. Live-anchor registry (identity-guarded)
  - Last-writer-wins `{messageID, el}` slot in `messages-blocks.ts`; register where
    `.streaming` is added to a top-level bubble (delegate-hosted excluded); identity-guarded
    clears (seal/finishNow/finalize/dispose clear only their own element); fallback
    re-registration from `messageStates`; `scroll.ts` `anchorProvider` = `getLiveAnchor`;
    DELETE the whole-tree qSA walk.
  - Tests: keep + convert the two-caret split-turn fixtures; no selector call in the follow
    path (spy).
  - _Requirements: 3.6, 8.1_

- [ ] 3. Paint bookkeeping + declared render causes
  - `bumpMessages(chatID, cause)` with the `RenderCause` taxonomy (chunk | tool | shape |
    fact) declared at the BRANCH that knows what changed, never at a function boundary.
    Call-site table (design §B2): `appendChunk` pure-delta growth of a mounted block →
    chunk; its isNew-message / new-block / pad-repair arms → shape; refusal-stamping arm →
    fact; signal-absent fallback → shape. `upsertToolCall` existing-call update → tool, with
    THREE exceptions: first `workflow_id` attachment → fact (it changes `turnRunIDs`; the
    keep-open FLIP lands via the run-state trigger below); FIRST attachment of
    `agent_subtask_id` (an id never changes once set) → shape PLUS the matching `tool_use`
    block's field updated and a
    structural re-home (container membership is the BLOCK's; the tool fast path never
    mounts or re-homes); signal-absent fallback → shape. New message / first tool call /
    block-index change → shape. `setTurnSummary` + `setCodeReferences` → shape. List ops
    (append/replace/prepend/rewind/evict/setMessages) → shape; transcript-fact writers (B1
    list) → fact. No-mutation branches declare NO cause and are NAMED (appendChunk
    unknown-chat + snapshot-dedup, setCodeReferences not-resident, setTurnSummary
    changed===false).
  - The table covers every `bumpMessages` CALLER: search reveal/re-fold → shape (task 14),
    stub fold-toggle → shape (task 13), fold-state writers → shape, `store-load.ts`'s
    page-load bump → shape. Run-state invalidation is the third fold trigger:
    `invalidateRun`'s settle path bumps the run's owning chat with `fact`; the owner is
    RESOLVED, not assumed — `invalidateRun` takes an optional owner recorded BEFORE its
    in-flight early return (a card can supply it while an ownerless fetch is running), the
    card site passes the `chatID` it holds, `noteRunChat` is also seeded from the transcript
    projection, and an unresolvable owner (parentless runs included) bumps nothing.
  - Per-chat microtask coalescing MERGES causes (chunk ⊂ tool ⊂ fact ⊂ shape); same-message
    different-block chunks stay chunk; the sync path (`emitMessagesFor`) paints the MERGED
    cause and clears the accumulator, and the microtask flush skips an empty one. `paint()`
    branches: chunk → tail bookkeeping only (no projection, no reconcile); tool → that
    turn's keyed update + its cached `turnRunIDs` (refresh-only, never mount);
    fact/shape → full projection + reconcile (`observeTurns`/`applyFoldPass` only on these).
  - `finalizeStreamingIfNeeded` via `messageStates` + registry; one children-walk card list on
    full passes; renderer-owned open-container Set (subagent blocks AND run-step rows);
    per-view reachable-block counter; fold `find-in-chat.ts`'s affordance observer into the
    scroll callback; rail `Map<turnMessageID, TurnSummary>` index; per-turn `turnRunIDs`
    cached at projection.
  - Characterization-first: `turns.node.test.ts` (+ cross-language outcome fixture),
    `messages-resume-reach.test.ts`, `turn-rail.test.ts`, `scroll.observers.test.ts`,
    `find-affordance-reactivity.test.ts`.
  - Tests: writer-BRANCH table MECE over every `bumpMessages` caller (each branch → exactly
    one cause OR a named no-mutation branch); old-turn same-length `message_updated`
    classifies shape and repaints that turn; fold-eligible turn stays open through the REAL
    ordering (workflow_id attach → fact pass → run fetch settles → invalidateRun fact bump —
    never a pre-seeded run store); a settled run's bump folds its turn; late
    `agent_subtask_id` attachment re-homes into subagent box AND run step (both via
    full-rebuild equivalence); call-count assertions over a 500-stub-turn synthetic (chunk
    flush: zero projection/reconcile/fold calls); equivalence property with structural
    identity fields in the alphabet (chunk growth, new-block chunks, first tool calls, tool
    updates, late workflow_id/agent_subtask_id/block_index transitions, appends, in-place
    replacement, refusal stamping, fact flips, prepends — DOM equivalence vs full rebuild
    each step); sync-bump-consumes-accumulator both directions; resume-counter equivalence.
  - _Requirements: 3.6, 8.1_

- [ ] 4. Streaming-path `:has()` → renderer classes
  - `.msg-row.is-empty`, `.tool-header.has-disclosure`, `.turn-card.is-bodyless`; CSS
    rewritten; class lifecycle in builders/update paths.
  - Characterization-first: `reasoning-live-cue.test.ts`.
  - Tests: class lifecycle in builder suites; stylelint green.
  - _Requirements: 3.6, 8.1_

- [ ] 5. Delta-carrying block signals + reveal `append`
  - Block signal value `{full, delta}` published by `appendChunk`; consumers: bubble live path
    uses `delta`, catch-up paths (`syncMountedText`, mount-with-history, `subagent-slice`)
    use `full`. Invariant doc at the type (depends on synchronous flush contract); watermark
    guard in the bubble (`accepted + delta.length !== full.length` → resync from `.full`).
  - `Reveal.append(delta)` replaces `setText(full)`; parts-array cursor; pacing constants and
    pinned drain numbers unchanged.
  - Tests: `reveal.test.ts` mechanical migration (same numbers); property
    `concat(emitted) === full` over random chunk splits; watermark resync case; bubble suites
    green.
  - _Requirements: 3.6, 7.2, 8.1_

## Phase 2 — Interaction layer

- [ ] 6. Duplicate-dispatch guard replaces the 400 ms discard
  - `guardDuplicateActivation` in `platform.ts` (same `pointerType`, `detail > 0`, delta <
    `GHOST_CLICK_MS = 50`; keyboard never filtered); wire `#new-chat`; DELETE `guardAction`;
    rationale comment cites duplicate pointer dispatch (the 300 ms class is already gone via
    `touch-action: manipulation`).
  - Tests: mouse 40 ms → 1, 60 ms → 2; keyboard 40 ms → 2; mixed 40 ms → 2; N clicks at
    100 ms → N.
  - _Requirements: 1.4, 8.1_

- [ ] 7. View swaps: synchronous swap + WAAPI entry animation
  - `showView` applies the class swap synchronously (guard logic kept); incoming view
    animates via `view.animate()` with a one-slot handle registry (new swap cancels the
    previous); animation explicitly skipped under `prefers-reduced-motion`, `document.hidden`,
    and pre-boot (`markBootDone` gates the animation only).
  - Convert ALL wrapper consumers — `tabs.ts`, settings panels, BOTH `files.ts` sites,
    `docs.ts` — then DELETE `dom.ts` `maybeViewTransition` with its three test mocks, the
    `uipViewTransition` import, and all ten `view-transition-name` declarations (nine views +
    `.chat-toolbar`).
  - Characterization-first: `a11y-keyboard.test.ts`, `a11y-labels.test.ts`.
  - Tests: swap final before any animation frame; handle replaced on rapid swaps; skipped
    under reduced motion/hidden/boot; mid-animation click lands on chrome AND incoming view
    controls; each converted site exercised.
  - _Requirements: 2.1, 2.2, 2.3, 8.1_

- [ ] 8. Stage the ui-primitives library option
  - `viewTransition(fn, { mode?: "queue" | "supersede" })`, default queue; generation guard in
    supersede mode (stale async callbacks no-op); extend the library's browser-mode suite;
    commit in `ui-primitives` only.
  - _Requirements: 7.4_

- [ ] 9. Pending-op state machine v2 in tabs-sync
  - `PendingOp` keyed by `opID` (id optional until response — server-minted opens), states
    `awaiting-response | confirmed-awaiting-frame | verifying`, MODE-FREE `onConfirm()`
    (client-local teardown only — the retention-off delete is task 11's server half); the
    SIX transitions from design §A2/A3 incl. SEMANTIC no-op confirmation (a close response
    with `closed: []` confirms regardless of the watermark — the client-behind case),
    immediate retire at response when `localVersion >= committedVersion`, retired-op
    immunity (a later failed response never rolls back a frame-confirmed op), the split
    failure arms (DEFINITIVE error → rollback + retire; TIMEOUT → `verifying`: no restore,
    no retire, bounded-backoff re-list, settled by matching frame OR authoritative
    list/snapshot — present → restore, absent → confirm; one timer per verifying remove,
    canceled on every settlement path), and the reopen re-check (an open row with the same
    `(kind, ref)` at confirm time skips chat-scoped teardown; an adopt overrides a pending
    remove's changed-upsert suppression, which is keyed by the remove's captured TAB IDS,
    never by ref).
  - Action replies WIDEN — a CLIENT-ONLY change (the server already answers these shapes;
    only the action layer strips them): `closeTabCommand` → `{closed, version}`,
    `openTabCommand` → `{subject, created, version}`; the machine consumes them; the
    "version is diagnostic" rule restated — only an EVENT advances the watermark.
  - The machine becomes the ONE consumer of `takeLocalOp` and forwards `local` to `apply`.
  - Tests: transition table BOTH orders; client-behind no-frame close (server v+1, local v,
    `closed: []` → semantic confirm); frame-confirm then failed response ignored; the two
    snapshot races; DEFINITIVE failure → rollback vs TIMEOUT → `verifying` (no restore);
    a frame settles a `verifying` op; an authoritative list settles it both ways;
    pin-frame suppression + adopt override + remote-reopen new-tab-id paints
    unsuppressed; last-tab close re-arms `scheduleEmpty` (deferred while any remove is
    pending OR verifying); version carried to the machine while the watermark advances on
    events only; version-rule assertions unchanged.
  - _Requirements: 1.6, 8.1_

- [ ] 10. Response-adopted opens, all kinds
  - `adoptSubject(subject, name?)` upsert via the frame-apply path + `emit` + pending adopt +
    activate; `openTab` adopts from EVERY `open_tab` response; `createSession` /
    `openTangentChat` adopt from create/fork responses and DROP the second POST
    (`chatFromReply` keeps `{chat, subject, version}` — the reply-widening contract);
    DELETE `whenOpen` + `OPEN_WAIT_MS` (no callers remain).
  - Characterization-first: `tabs-projection.test.ts`, `attention-wiring.test.ts`,
    `__test-helpers__/tabs-mock.test.ts`.
  - Tests: row present at dispatch resolution (no frame delivered); frame idempotence (no
    re-animation); re-list convergence via overlay; capacity-409 paints nothing; tangent
    parity; singleton/editor/run opens paint from response.
  - _Requirements: 1.1, 1.6, 2.3, 8.1_

- [ ] 11. Optimistic close: reversible gesture, deferred teardown, transactional delete
  - Gesture time (reversible only): subtree capture (rows + specs + owned view state — dirty
    editor `FileState` retained), projection/strip removal with exit animation, `forgetRow`,
    `activateFirst`, **store `setActive` moves with the gesture** (create-vs-send keys off
    the projection's active subject); last-tab close renders the EMPTY-STATE surface without
    disposing parked views (Send there creates a fresh chat; rollback preserves empty-state
    composer text via `restoreFailedSend` — never a relaxed `restoreComposerState`);
    `scheduleEmpty` deferred to settlement. NO `tearDown`/`onClose`, NO `removeChat` at
    gesture time.
  - **Server half (design §A2/A3 rev 7):** the retention predicate FAILS TOWARD KEEPING
    (named read beside `chatRetention`, never shared with it: read error → ON; absent key →
    default → ON; 0 → OFF), read INSIDE the lock. Escalation under the operation lock:
    decide the doomed set (the close's subtree via an exported `tabs.Store` closure read ×
    remaining refs × the predicate) + CAPTURE each doomed chat's teardown input
    `{chatID, sessionChain}`; `tabs.Close` = the COMMIT POINT (response success with the
    closed ids from here; a FAILED `tabs.Close` is nothing-committed → error response,
    records untouched); `chats.Delete` each doomed record after the tabs frame (recordless
    chats SKIPPED; zero-message chats deleted — a stated behavior change closing today's
    orphan-record leak, the stale client comment removed). After the lock, under a context
    DETACHED from the HTTP request with its own bound (`context.WithoutCancel(requestCtx)` +
    a fresh `WithTimeout` — `WithoutCancel` alone has no deadline), EXACTLY ONE teardown per
    closed chat
    tab: the DELETE grade for doomed chats — turn cancel, run cancellation BY THE CAPTURED
    CHAIN (`CancelForSessions`-shaped seam; `CancelForChat` re-reads the record and no-ops
    on a deleted chat; `DeleteChatState` widens to accept the captured input), bridge
    teardown, KAS session-chain reap FROM THE SAME CAPTURED CHAIN — and `closeChatTeardown`
    for the rest. Post-commit failures roll FORWARD (log ERROR, close still answers
    success); the client rollback path is reachable only when nothing committed.
    `membership.go`'s record-leads-on-delete header comment gains the two-paths statement
    in the same change. Client: DELETE the close-path `delete_chat` dispatch and
    `closeChatTab`'s `remote` flag; `onConfirm()` runs client-local cleanup exactly once;
    reopen re-check skips chat-scoped teardown when the same `(kind, ref)` is open again;
    `openTab` returns an OUTCOME preserving 404 distinct from generic failure (it swallows
    the null reply today; a network failure must never read as "ephemeral and gone") and
    `openChatTab` forwards it, so a retention-off History reopen that 404s drops the row,
    states the chat was ephemeral, and SKIPS activation. A KEYBOARD-initiated close of the
    LAST tab moves
    focus to the composer (the window opens in this task; the parked-view relocation half
    stays in task 15).
  - Definition-level `timeout: CLOSE_CONFIRM_MS = 5000`; rollback error → restore captured
    subtree in place (incl. re-activating the restored chat) + toast; timeout → verify via
    one re-list (present → restore + "close not confirmed"; absent → `onConfirm()` +
    silent; **re-list FAILED → the machine's `verifying` state: no restore, no retire,
    bounded-backoff re-verify with ONE timer per pending remove, settled first by any
    arriving frame/snapshot; restore only on authoritative presence** — design rev 8, gpt
    R6-H1/R7-H1). A
    restored row's `pinned` may lag one frame (absorbed by the next frame/snapshot;
    recorded).
  - Tests (Go): retention-off last-tab close deletes the record in the close operation
    (children included; `chat_deleted` after `tabs_changed`; History consistent), reaps
    the captured session chain, AND cancels a live chat-parented run per doomed chat (root
    and child fixtures, cancel asserted after the record is gone); a non-last close neither
    deletes nor reaps; retention ON deletes nothing; absent `config.json` → deletes
    nothing; unreadable `config.json` → deletes nothing; post-commit record-delete failure
    → success with closed ids; client-abandoned request (ctx canceled after commit) still
    completes the roll-forward; recordless chat skipped. (TS): error rollback restores a
    DIRTY EDITOR and the chat record (server refused → nothing committed); teardown exactly
    once in frame-first AND response-first orders; no close path dispatches `delete_chat`;
    reopen-from-History (retention ON: record intact; retention OFF: 404 → row dropped, NO
    activation); last-tab empty state + Send-creates-chat + rollback restores
    view/scroll/typed text; keyboard last-tab close → focus lands on the composer;
    committed-close + failed-verification → ZERO rows restored, op pending, next successful
    list settles it; timeout-verify arms (present → restore; absent → silent confirm);
    burst independence; children with parent.
  - _Requirements: 1.2, 1.3, 1.6, 8.1, 8.2_

## Phase 3 — Strip split

- [ ] 12. Tab-strip per-row subscriptions + context split
  - Per-open-row effect registry (`Map<chatRef, dispose>` synced on tabs `emit`); each effect
    tracks its chat's signal + dock/run inputs, writes only its row; `refreshContextUI` on its
    own `activeSession` effect.
  - Tests: one status flip → one row write (spy); closed-history sessions trigger nothing;
    lifecycle on open/close; context ring updates on active header change only.
  - _Requirements: 4.3, 8.1_

## Phase 4 — Transcript architecture

- [ ] 13. Three-tier turn residency over the fold policy
  - `mounted = foldOpen || distance < TURNS_WARM (5, in fold-state.ts beside OPEN_TAIL)`;
    fold policy untouched and authoritative; stub mount mode in `buildTurn` (header + footer,
    no inner reconcile, no effects); mountedness COMPUTED AND APPLIED in `applyFoldPass`
    (2→3 unmount deferred + height-compensated via the existing
    `deferWhileReading(preserveReadingPosition)` pair — the 1→3 flip cannot yank content);
    tier-2 bodies are `display: none` via the existing fold rendering (not cv — terminology
    per R2-L1).
  - On-demand build: fold-toggle click on a stub runs `setTurnOpen(true)` + body build +
    `bumpMessages(chatID, "shape")` in the same interaction; rail jump and search navigation
    call the same entry point; heavy cold builds yield in block batches
    (`scheduler.yield()` + fallback); pagination prepends land as stubs. Stub headers keep
    disclosure semantics (`aria-expanded`), stated in the a11y notes.
  - Characterization-first: `fold-state.test.ts` UNMODIFIED; `messages-skeleton`, `refusal`,
    `code-refs`, `messages-turn-actions`, `per-chat-store` suites migrate here.
  - Tests: mounted-derivation table (foldOpen × distance × explicit-false × search-open ×
    failed × live-run × streaming); 1→2→3 lifecycle leak assertions; 1→3 reader-position
    compensation; fold-click-on-stub immediate mount (+ keyboard case); stub expand parity
    (one copy of every block type); rail jump onto stub; explicit collapse of newest stays
    collapsed; stubs from pagination; a11y suites green.
  - _Requirements: 3.1, 3.2, 3.3, 3.5, 3.6, 8.1_

- [ ] 14. Search segments with segment-relative offsets
  - `internal/chat/search.go`: ordered segments per message ({messageID, kind, blockIndex?,
    subtaskID?, rune range}; a tool block's title and output are SEPARATE segments sharing
    the block index); `SearchHit` gains `block_index?`, `agent_subtask_id?`, `segment_kind`
    (content | reasoning | tool_title | tool_output | message), `segment_len`; `offset`
    REDEFINED as segment-relative rune offset. Filter-only hits (a query with filters, no
    free text) keep their one-synthetic-hit-per-message contract as `segment_kind: "message"`
    with offset 0, segment_len 0, no block index.
  - Wire honesty: the TS mirror in `chat-search.ts` stays hand-maintained; add a
    cross-language PARITY test over a shared JSON fixture (the `turn_outcomes.json`
    pattern). No wiregen claim (`chat.SearchHit` is not registered; the generated name is
    taken by the tools type).
  - Client: server hits drive navigation where no DOM mark exists — page in, mount stub, open
    delegate/reasoning chain, walk that turn, select by `(messageID, blockIndex,
    segment_kind)` + nearest-match (excerpt similarity, then relative position via
    `offset / segment_len`, ties to lowest index; below the similarity floor or with no
    rendered mark → block selection + stated notice, never a silent no-op); a `message` hit
    navigates to the message container (scroll + brief highlight) — the ranker's
    `segment_len` division is unreachable for that kind by routing. The reveal and re-fold
    bumps carry cause `shape`.
  - Characterization-first: `find-in-chat.test.ts`, `chat-search.test.ts`.
  - Tests (Go): duplicate text in parent + delegate blocks; duplicate text TWICE IN ONE BLOCK
    (distinct segment offsets); tool-output hit; legacy blockless message; rune boundaries;
    filter-only query incl. a tool-only assistant message → `message` hits.
    (TS): parity fixture; end-to-end hit inside a collapsed delegate in a stub turn;
    syntax-only hit → block selection + notice; `message` hit → container navigation;
    nearest-match with divergent markdown vs rendered text.
  - _Requirements: 3.4, 8.1, 8.2_

- [ ] 15. Parked chat views with pause/resume lifecycle
  - Multiplexer + `.transcript-view` carrying ALL NINE container declarations (design C2
    list); `.is-active` exclusivity; parked = `content-visibility: hidden` PLUS `inert`
    (deterministic focus/tab-order/find exclusion — cross-browser a11y exposure under cv
    alone diverges).
  - **Tool identity goes composite** (gpt R4-H2): `toolEls` + `toolEffects` re-key from bare
    `tool_call.id` to `toolCallSigKey(chatID, toolID)` (keyenc — never a template literal);
    `termToTool` KEEPS its terminal-id key (marotte mints terminal ids) and gains a
    chat-bearing VALUE so its `toolEls` lookup composes the composite key (the chat id is
    delivered and currently discarded at the call site); `pendingChunks` unchanged;
    pause/dispose operate through the owning view's composite keys.
  - `pauseMessage`/`resumeMessage` on retained `MsgRender` (design C2): pause disposes
    live-binding effects, `finishNow()` reveals, suspends run-card effects, releases clock
    holds (clock holders REFCOUNTED: `Map<workflowID, Set<RunCardView>>`), stops tail
    tickers, disposes the message's generic TOOL-CARD effects (composite keys), and detaches
    terminal bindings (parked `terminal_output` accumulates in a bounded 64 KB drop-oldest
    per-terminal buffer, drained once at resume); resume: settled messages need nothing
    (`syncMountedText`), still-streaming tail rebuilds its body fresh (B5 watermark guard),
    run + tool cards re-arm and re-read their snapshots.
  - Park = pause + detach observers + save `ViewHandle` (el, scrollTop, readingState,
    followBaseline, resumeLabelState, reachable-block counter, lastNewestId, pagination-pass
    ownership) + focus relocation to the composer; unpark = `.is-active` + restore +
    `scroll.ts` `attach(handle)` + one catch-up paint + resume. A keyboard-initiated close
    of the LAST tab also moves focus to the composer (matching the pointer path).
  - Container consumers re-pointed per the COMPLETE inventory: paint root/card walk, scroll
    observers, rail query, find walker + affordance callback, skeleton (comment moves with
    it), load-more furniture, `chat.ts` `load-error` node, `fadeInTranscript`.
  - **`teardownAll` under the multiplexer = the per-view real dispose applied to every view,
    covering the FULL operation set** (claude R4-M4): bind unbinds, streaming effect
    disposal, tool effect disposal (composite keys), block-render resets, per-message and
    per-chat signal clears, `messageStates` pruning, scroll reset, rail reset, container
    removal — never a bare empty reconcile or global signal clear. LRU `PARKED_VIEWS = 3`
    eviction and chat close/delete run the same per-view dispose.
  - Characterization-first: `scroll.test.ts`, `scroll.observers.test.ts` (real-layout
    sections), skeleton suite, `__test-helpers__/scroll-mock.test.ts`.
  - Tests: freeze under store appends AND `run_progress` AND `tool_call_update` AND
    `terminal_output` (zero DOM writes/rAF/observer callbacks); park-after-chunk →
    append-many-parked → unpark → append-again asserts EXACT text and one active
    effect/ticker per surface; terminal buffer drains once and drops oldest past 64 KB;
    two chats with IDENTICAL tool ids park/unpark without cross-corruption (composite-key
    case); unpark equivalence vs cold rebuild (one copy of every block type); two parked
    chats with different reading states; refcounted clock survives park while a run tab
    holds the same run; LRU + close disposal leaks; parked view is `inert` (focus/tab-order/
    find excluded); short-transcript bottom alignment through park/unpark; focus relocation
    on park (the keyboard LAST-TAB case is task 11's); `teardownAll` with parked views
    leaves no DOM and no state (op-set assertions); paint-on-empty hides without disposing.
  - _Requirements: 4.1, 4.2, 1.5, 8.1_

## Phase 5 — Memory + transport

- [ ] 16. Residency, leak fix, live-run inventory (server + client), eviction
  - Leak fix: `disposeMessage`/view-dispose/removeChat clear per-message block signals;
    eviction clears per-chat leftovers incl. version signal + scheduled marker.
  - `MessagesResidency = loaded | evicted | partial` (background ingest on evicted →
    partial; only a successful newest-page load sets loaded).
  - Server (design §D rev 6): `runlease.Lease` gains `ChatID`, stamped at `observeStart` —
    ADDITIVE at `runlease.Version = 1` (json tag, NO version bump: a bump discards every
    live lease at boot); a pre-upgrade row decodes with an empty chat id = "no chat to
    exempt", the same designed value parentless launch-verb leases carry. The live-runs
    projection is PRESENCE-based: `GET /api/runs/live` → `{runs: [{workflow_id, chat_id}]}`
    (envelope) for every live lease. **The sweep fix ships here** (claude R5-H4 + P-2): the
    `Origin == OriginAgent` exclusion moves INSIDE `SweepOrphaned`'s cancel arm, so the
    bookkeeping arm releases any lease whose run KAS reports terminal or unknown — presence
    becomes the non-terminal claim it says it is, bounded at boot. No KAS call
    on the endpoint, no utility-bridge spawn; History semantics untouched; wire type
    registered + generated decoder. Degrade rules: stale-live entries err toward keeping
    (corrected by the next run invalidation or the NEXT SERVER BOOT — `SweepOrphaned` is
    boot-only and no periodic sweep is invented; in-process persistence is accepted, memory
    not correctness); a failed rebuild KEEPS event-fed state and retries next sweep.
  - Client: `liveRunsByChat` fed by run lifecycle events, rebuilt at hydration and on
    `transport:gap` from the endpoint; `hasLiveRunForChat`.
  - Eviction sweep (`EVICT_SWEEP_MS = 5 min`, visibility-aware; `EVICT_IDLE_MS = 30 min`)
    with the five exemptions (active chat; busy/streaming; live run via registry; parked
    view; open subagent tab projecting the chat); `lastActivityAt` stamps incl. activation.
  - Characterization-first: `run-store.test.ts`, `store-load.test.ts`.
  - Tests: exemption table (each alone prevents eviction; disposed-run-card and subagent-tab
    cases); evict → remote append (partial) → activate without gap → REFETCHES; evict →
    reopen skeleton; registry rebuild from the endpoint on boot and after a gap; Go handler
    tests: chat-parented rows present with their chat id, terminal runs absent (via the
    sweep's bookkeeping arm — the mechanism, not a happy path), History unchanged, a
    pre-upgrade version-1 lease row decodes with empty chat id and exempts nothing, AND a
    server restart projects a still-live lease from persisted `runs.json` bytes (a paused
    chat-parented run exempts its chat with zero frames observed); sweep pauses hidden.
  - _Requirements: 5.1, 5.2, 5.3, 5.4, 8.1, 8.2_

- [ ] 17. Staleness-gated refetch (messages + rail)
  - `syncEpoch` on `transport:gap`; `epochAtStart` captured before each page fetch, stored as
    `loadedEpoch` on success; activation refetch iff `residency !== "loaded" || loadedEpoch
    !== syncEpoch`; rail gated the same + message-count change.
  - Tests: both fetch-races-gap orders; current chat → zero fetches; after gap → both; after
    evict → both.
  - _Requirements: 1.5, 8.1_

- [ ] 18. Early-ack prompt — server (admission IS the turn registry)
  - Split `OpenTurn` into **`ReserveTurn` (mints NO `Turn`)** — a bare per-chat admission
    slot: synchronous, no bridge, today's source semantics PLUS the new prompt-vs-prompt
    refusal — and **`StartTurn` ≡ today's full `open()`** at bridge-ready, immediately
    before the ACP call: the `Turn` record, metering baselines AND the model all stamped
    with the bridge live. The PRIMING turn's own open/finalize runs between reservation and
    `StartTurn` untouched (the reservation is not a `Turn`). The shell door calls
    `ReserveTurn` (a TRY, never a wait — `!echo hi` refuses immediately) + today's `open()`
    back-to-back; ONE mechanism for prompt/shell exclusion; the stale prompt-queue comment
    in `shell.go` updates.
  - Synchronous: validate → persist/broadcast/draft-clear (today's exact order) →
    `ReserveTurn` (held → bounded condition-variable wait with an EXPLICIT wake at
    bridge-ready, up to `ADMISSION_WAIT_MS = 20_000`; the answer keys on the HOLDER'S
    SOURCE via a registry accessor generalizing `PrimeTurnOpen`: prompt-class holder with a
    live bridge → plain 409; ANY other holder (spawn, shell bridged or not, prime) →
    `409 {"error", "reason": "starting"}` — the carrier is the unexported `statusError`
    struct gaining an optional `Reason` (`StatusError` is its constructor; a reason-bearing
    argument carries it) that `writeErr` emits as the additive envelope field; asserted
    `ADMISSION_WAIT_MS` < client API timeout) → inflight register → `TurnContext` → ack
    `{accepted, message_id}`. **The `reason` field is the wire change this task owns
    server-side**; existing clients ignore it.
  - Goroutine: `OpenBridge` → prime (own turn) → MCP wait → `StartTurn`; **epoch 0 →
    `error{prompt_failed}` + both slots released + `InflightDone`, never an ACP call** →
    `BeginPromptCall`/`EndPromptCall` → finalize (`SettleTurnOnResponse`) → **capture
    the result via the still-held epoch handle (`AwaitTurn` BEFORE `ReleaseTurn` — the
    registry drops the result at the last release), hold the handle until every
    epoch-based recovery predicate has been read** → release bridge slot → release
    reservation → `recoverEmptyTurn` arbitrates on the CAPTURED result, re-reserves +
    re-acquires with try (either refusal abandons) → `ReleaseTurn` LAST → context cancel,
    `InflightDone`. Bridge-lock refusal outside recovery = `error{prompt_failed}` +
    reservation release, never silence.
  - Characterization-first: `prompt_failure_test.go`, `prompt_mcp_timeout_test.go`,
    `prompt_refusal_test.go`, `shell_test.go`.
  - Tests: ack precedes turn completion AND OpenBridge; `!echo hi` during a prompt's blocked
    spawn refused IMMEDIATELY (no wait); prompt during a SHELL holder → `409 starting` on
    bridgeless AND bridged chats (holder-source predicate); second prompt during spawn waits
    then plain-409s once the prompt holder's bridge is live; 409-starting at the wait
    budget; the prime-lifecycle case; metering AND model stamped at StartTurn (spawn + MCP
    wait excluded; model non-empty on a cold chat); recovery reads the CAPTURED result and
    the test FAILS if `ReleaseTurn` precedes the recovery read; zero-epoch StartTurn →
    `prompt_failed` + slots released + no ACP call; prompt during recovery preempts AND
    acquires both slots (pause injected between the releases and the re-reserve); the
    bridge-ready wake fires; 409 state parity; shutdown pre-goroutine AND mid-Call; cancel
    between ack and BeginPromptCall; failure broadcast; idempotent retry replays the ack;
    `ADMISSION_WAIT_MS < API timeout` asserted.
  - _Requirements: 6.1, 6.2, 6.4, 8.2_

- [ ] 19. Early-ack prompt — client
  - Prompt dispatch at standard API timeout; DELETE `promptEchoed` + `PROMPT_ECHO_GRACE_MS`;
    GLOBAL hidden-abort stays (pin: non-prompt in-flight request still aborts).
  - **The `reason` field's client half**: `transport.send` lifts `reason` beside its
    error-string read and carves `"starting"` out of its 409-is-queued collapse;
    `sendPromptTo` AND `sendPrompt`'s result types widen to
    `"sent" | "queued" | "starting"` (+ failure) so `submit.ts` — the owner of the
    steer-vs-face decision and of the starting face, rendered through `send-state`'s error
    surface — branches on the VALUE, never error prose.
  - **`409 starting` is a POST-PERSIST failure class**: the user row is persisted and
    rendered (persist precedes reserve), so NO text restore (the `hasMessage` guard already
    enforces it), the send-error face reads "The chat is busy right now — send again to
    retry" (holder-neutral copy), and a re-send of the same text reuses the `lastFailed`
    message id so the server dedupes the append. The plain-409 steer conversion is gated on
    the ABSENCE of the `starting` reason. Pre-ack failures (network, 4xx/5xx before persist)
    keep text-restore + id-reuse; post-ack failures SSE-only.
  - Characterization-first: `actions/chat-prompt.test.ts`, `submit.test.ts`,
    `send-state.test.ts`, `transport.test.ts`; `actions/chat-prompt-echo.test.ts` deleted
    with its subject.
  - Tests: ack-only fake drives the send lifecycle; one test per failure class; 409-starting
    → error face + NO steer attempt + NO text restore + same-id re-send; plain 409 → steer
    as today (reaches `_session/steer`); `transport.send` surfaces `reason` and
    `sendPromptTo` returns `"starting"` (no prose matching); steer mid-turn unaffected;
    non-prompt hidden abort preserved.
  - _Requirements: 6.3, 6.5, 8.1_

## Phase 6 — Closure

- [ ] 20. Steering documentation updates (same change-set rule)
  - `marotte-client.md` (projection: adopt + pending-op machine + deferred-teardown close +
    whenOpen deletion; send discipline: early-ack + failure split incl. the 409-starting
    class; renderer: per-chat versions + transcript-fact rule + tiers + view handles +
    pause/resume + composite tool keys; scroll: anchor registry + per-view records),
    `marotte-ui.md` (view-swap section REWRITTEN: document VTs removed, entry-animation
    pattern, captured-content hit-testing rationale), `marotte.md` (prompt admission +
    wait-for-bridge-then-409 + starting variant; create adoption; optimism carve-outs with
    bounds; live-runs endpoint; `close_tab` row + invariant 4 + the "don't let any code
    path other than `cmdDeleteChat` remove a chat file" bullet all widened for the
    retention-off transactional delete; `closeChatTab` remote-flag prose removed),
    `ui-primitives.md` (staged option).
  - _Requirements: 7.3_

- [ ] 21. Full verification battery + evidence
  - `tsc --noEmit`, `vitest run` (full), `go build ./...` + `go test` on touched packages,
    stylelint on touched CSS; collect pass counts.
  - Stress check: synthetic 500-turn session — per-event paint bounded by the open window;
    open-window node count bounded; stub-weight residual documented with numbers.
  - _Requirements: 8.1, 8.2_

- [ ] 22. Manual verification script + morning report
  - DevTools checks: click-to-feedback < 100 ms on tab gestures (local server); no long task
    > 50 ms from a tab gesture on an idle app; usage tick causes no transcript reconcile;
    mid-animation clicks land (chrome + view content); protocol h2 behind Caddy; h1 with
    several busy chats stays responsive; iOS double-tap sanity after guard removal.
  - Morning report: per-task evidence, review-loop summary, adopted/rejected reviewer
    proposals with reasons, deferred decisions and tuning knobs, follow-ups needing approval
    (ui-primitives release, live-runs endpoint review, push/PR).
  - _Requirements: 8.3, 8.4_

## Adopted reviewer proposals (folded into tasks above)

Round 1: F-P1 (adopt every open) → 10; F-P2 (context split) → 12; F-P3 (rail gating) → 17;
F-P4 (delta signals) → 5; F-P5 (open-container Set) → 3; G-P1 (rail index) → 3; G-P2
(incremental resume count) → 3; G-P3 (yielded cold builds) → 13; G-P4 → superseded by the
chunk-only fast path in 3 (C-H5 showed incremental-by-messages is wrong; the fast path keeps
the win). Round 2: C-P2 (run-step scan) → 3; C-P3 (find observer fold-in) → 3.
Round 5: fable P1 (plain-409 windows named) → design §9/§E1; fable P2 (bridge-ready wake) →
18; claude P-1 (live-runs envelope) → 16; claude P-2 (sweep bookkeeping release) → 16;
claude P-3 (invalidateRun fact bump) → 3; claude P-4 (fail-toward-keeping reader) → 11;
claude P-5 (holder-source accessor) → 18; gpt P-1 (structural-identity oracle) → 3; gpt P-2
→ resolved by rejection (no journal; commit-point + roll-forward contract instead).
Deferred (recorded in design §12): C-P1 header per-field signals (measure-first follow-up).

## Traceability

| Requirement | Tasks |
|---|---|
| R1 instant feedback | 6, 9, 10, 11, 15, 17 |
| R2 input never blocked | 7, 8, 10 |
| R3 bounded per-conversation | 1, 2, 3, 4, 5, 13, 14 |
| R4 bounded per-tab | 12, 15 |
| R5 memory | 16 |
| R6 transport | 18, 19 |
| R7 invariants/strengths | all (esp. 8, 20; non-goals design §12) |
| R8 verification | 21, 22 (+ per-task tests) |
