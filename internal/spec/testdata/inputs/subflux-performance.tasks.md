# Subflux UI Performance — Implementation Plan (rev 15)

Build rules (carried from the marotte exercise, plus the user mandate of 2026-08-30):

- Characterization-first on every touched suite; deliberate test deletions and replacements are named in the task's report.
- **Any real bug encountered in scope during a task is fixed in that task's change-set and reported.**
- **Wire-breaking pairs move in ONE task**: server half, client half, and test consumers in the same change-set.
- Preserve the R9 inventory verbatim; the matrix below names the owning task and pin per surface.
- Commits local, conventional subjects, one commit per task; full battery green before every commit (`go build ./... && go test ./... -count=1`; static-src: both tsconfigs + eslint + stylelint + prettier + `npm test`).
- Wire-type changes regenerate `wire/*.gen.ts` in the same commit. A new SSE event variant is THREE edits (the `//wiregen:union` directive, the sealed interface method, `wirespec.DiscriminatorMap["EventData"]`) plus regen.
- **Verification lanes**: the vitest DEFAULT project runs unsuffixed tests in REAL headless Chromium (1280×720). Computed-style, geometry, containment-state, and scroll assertions are ordinary vitest tests. The sidecar owns screenshot parity and profiling; find-in-page is a MANUAL sign-off check.
- Subagent dispatch: disjoint file sets; verify independently; no shared-todo reliance.

## R9 characterization matrix

| R9 surface | Owning task | Pin |
|---|---|---|
| Keyed reconcile: focus/selection survive merges | 5 | focused row keeps focus through a heal + a no-op setAll |
| Library pagination (50) + zero-fetch back-nav | 8 | back-nav renders from a COMPLETE cache with zero fetches; incomplete cache refetches |
| History pagination (50/page) | 12, 17 | loadMore appends one page in one reconcile (17); an event reload NEVER cancels an in-flight Show more — foreground priority, storm oracle (12) |
| Abort-on-supersede | 6, 7 | superseded per-id GET aborted; stale generation discarded |
| Optimistic flows + rollback | 12 | download-button flip + rollback pinned BEFORE the pollers are deleted |
| a11y roles/labels | 16 | desktop: table/rowgroup/row/cell/columnheader + `aria-labelledby` (harness populates `lib-heading`); mobile: what renders (accepted, matching today) |
| Reduced motion + navigation-only view transitions | 16 | heals never invoke a view transition; navigation uses the queued wrapper; `prefers-reduced-motion` suppresses as today |
| Pause-when-hidden polling | 11 | hidden tab issues zero status polls |
| Compositor-only animations | 16 | badge/hover parity in the sidecar gate |
| No innerHTML | all | eslint rule stays green |

## Phase 1 — Server foundations (Go)

- [x] 1. arr access: ONE arr-read wrapper owning waves, writes, and coalescing (A4)
  - The four read FAMILIES (series list, movie list, episodes-by-series, exclude-tag
    resolution; `ARR_CACHE_TTL_S = 15`) route through one WRAPPER in the arr layer,
    backed by `internal/cache`'s `Get`/`Set` — the cache API is UNTOUCHED (provider
    lookups elsewhere keep `GetOrFetch`), but arr reads do NOT use `GetOrFetch`: the
    wrapper owns coalescing and write ordering for plain and marked reads alike, and
    serializes each key's read-begin comparison and `Set` under its own PER-KEY MUTEX;
    an entry's value is `{payload, readBegin, derived indexes}` written atomically (the
    tvdb→row / tmdb→row indexes live INSIDE the entry value; the exclude-tag entry
    value is `{ids, unmatched}` — both halves of the resolution), and cached values are
    IMMUTABLE by convention. The parsed `recovery=1` travels as a CONTEXT VALUE set by
    the handler before the consumer-interface call (the wrapper reads it to select
    wave vs plain admission). Exclude-tag resolution is
    keyed `(arr instance, tag name set)` — two keys at the reference configuration,
    following the poller's by-source cacheKey (`poller_run.go:315,377`) — and CACHES
    BENEATH THE FAIL-OPEN: reach an ERROR-RETURNING form
    (`resolveExcludeTagIDsErr(ctx, tags, logMissing) (map[int]struct{}, error)`; the
    shipped `resolveExcludeTagIDs` — fail-open log+nil, `arrsvc.go:101-115` — becomes
    its projection); the wrapper caches only `err == nil` results (a fail-open nil is
    NEVER cached); MARKED reads PROPAGATE the error (an exclude-tag wave failure or
    refusal is a typed leg failure at the HTTP response, never a silent
    empty-exclusion 200 — the coverage handler receives an error-returning recovery
    dependency, mapped to the wire by SENTINEL errors: the coverage and media handlers
    map the refusal sentinel to `httpapi.TooManyRequestsC` — 429 on the wire, never a
    500 through the generic `InternalErrorC` arm — and a wave execution failure to the
    handler family's upstream-failure status); PLAIN reads and the scan path keep the
    fail-open projection at their call sites, and `logMissing` stays OUT of the cache
    key (the caller-side hint logs the cached `unmatched` NAMES verbatim — names are
    not derivable from an id set — so it fires on cache hits too). The coverage
    consumer interfaces (`CoverageSonarrClient`/`CoverageRadarrClient`, two methods
    each) gain EXACTLY ONE method — the error-returning exclude-tag form (two→three);
    the derived unions in `server_types.go` widen nine/seven → ten/eight, their
    width-doc comments updating in the same change-set; every other shape is
    unchanged. WAVES: a `?recovery=1` read is served by a wave whose
    upstream read BEGAN at or after the request's ARRIVAL — no exception. A wave's START
    = the instant its upstream read begins (one `time.Now` after the global permit,
    immediately before the arr call; `eligibleAt` is a different value, never the
    freshness measurand); a wave is JOINABLE from record creation until read-begin (join
    and read-begin serialized). Admission arms, in order: (1) JOIN any live pre-read
    wave; (2) else create a wave now if no wave for the resource BEGAN a read within
    `RECOVERY_WAVE_FLOOR_MS = 2000` (the floor measures read-begin instants); (3) else
    SCHEDULE the shared follow-up at the previous wave's START + floor and join it —
    DELAY, not degrade. A reader arriving after read-begin falls to arm 3. A wave's
    write lands only if its read-begin is newer than the entry's current one — and so
    does EVERY writer's (plain and scan write-through included), so a slow plain fetch
    never clobbers a newer wave. Plain reads coalesce through the wrapper's own per-key
    singleflight; a marked read never joins a plain flight and vice versa. A wave with
    zero waiters before read-begin is discarded unexecuted (timer/queued-acquire
    cancelled; liveness RE-CHECKED after permit acquisition); the upstream call runs on
    a context DETACHED from every waiter's (the `GetOrFetchCtx` precedent); wrapper
    timers run under the server's `bgWg`; instants are `time.Now` values captured once,
    never rounded/serialized. AGGREGATE CEILING + ADMISSION BUDGET: wave execution
    admits through ONE wrapper-owned global FIFO queue, `RECOVERY_MAX_CONCURRENT_WAVES
    = 2` (an explicit queue — x/sync's semaphore documents no FIFO order); a wave not
    admitted within `RECOVERY_ADMISSION_BUDGET_MS = 6000` of its scheduled instant
    answers every waiter a TYPED 429 (the client treats it as a genuine leg failure:
    abort → latch → ladder; retries JOIN live waves, so contention drains). EXECUTION:
    an ADMITTED pass runs single-attempt under `RECOVERY_EXECUTION_BUDGET_MS = 20 000`
    on a context derived from the SERVER LIFETIME context (shutdown cancels it) — the
    wrapper holds its OWN per-side arrapi clients, `WithMaxAttempts(1)` with the
    execution budget as their timeout (`WithTimeout`), built in activation's PREPARE
    beside the shipped clients (`activate.go:130-146`) and published/closed atomically
    with them (`reload.go:107-116`); on a publish of rebuilt clients the wrapper
    CLEARS its cache for that arr instance and REVOKES in-flight wave writes (a ctx
    deadline alone would merely truncate the 3-attempt ladder); expiry = a typed
    failure FANNED OUT to every waiter (each affected transaction latches once; each
    retry's read joins whatever wave is then live for its key or starts one under the
    floor). THE
    CLIENT-FACING BOUND: `RECOVERY_REQUEST_DEADLINE_MS = 25 000`, ONE PER MARKED HTTP
    REQUEST, carried on the same context value as `recovery=1` and armed by the
    handler at SERVER request arrival (the 5 s margin under the client's 30 s absorbs
    dispatch/network skew) — EVERY wrapper read the request makes (a summary's list
    AND tag read; a gated episodes read's list wait AND own wave) charges against that
    one deadline; expiry answers THAT request the typed refusal immediately (its
    readers detach; a wave may complete for other requests — which does NOT help the
    refused request's retry: its arrival postdates that wave's read-begin, so it waits
    for a fresh pass, accepted); no marked request waits past 25 s. The per-key lock protects only the joinable→started
    transition and is released before the arr call; a post-permit discard RELEASES its
    permit and advances the FIFO; the scan write-through does NOT pass the admission
    queue or budget. ORDERED
    MEMBERSHIP GATE: a MARKED episodes-by-series read whose id misses the cached series
    list first awaits/joins the SAME recovery's series-list wave and re-checks; only a
    post-wave miss answers 404 with NO upstream call. A PLAIN episodes read keeps
    today's behavior (fast-path check, miss falls through to the cached upstream call);
    the ceiling and gate bound RECOVERY work, and the plain path's aggregate cost is
    today's shipped exposure, improved by caching — accepted and stated. Wave sets
    SCOPED per page and per arr side (series-detail recovery: series list, SONARR tag
    key, episodes for the OPEN series; library recovery: both lists + both tag keys).
    Boot and steady-state reads are plain. The scan engine's bypass IS a wave (start =
    its own read-begin): newest-write-wins, resets the floor clock; it does NOT pass
    the admission queue or budget. The tvdb→row /
    tmdb→row indexes derive once per entry refresh, INSIDE the entry value. TWO ENTRY
    POINTS (cached HTTP reads + the scan bypass); the
    poller's own longer-TTL exclude-tag cache STAYS.
  - Tests: N concurrent plain readers share ONE upstream call (the wrapper's
    singleflight); expiry; errors not cached; exclude-tag served from cache; sonarr and
    radarr tag sets NEVER share a cache entry (a series-detail recovery leaves the
    radarr entry untouched); a recovery read is never served by a pre-arrival flight
    (both orders); a marked read never JOINS a plain flight and a plain read never joins
    a wave (two upstream calls, both orders); the HELD-PERMIT join fixture: reader A
    creates a wave, the permit is withheld, reader B joins pre-start, release → ONE
    upstream call serves both (one transaction's own legs sharing a key coalesce this
    way — dispatched in one burst); two tabs 0.1 s apart with the first wave's read
    already begun cost TWO passes (stated); the PRIMED follow-up burst: W0 completes,
    upstream mutates, B and C arrive inside the floor → both join ONE follow-up and
    observe the mutation; a marked read arriving 1.9 s into a floor is answered after
    the follow-up, not before; boundary fixtures at floor − 1 ms (joins the follow-up)
    and floor + 1 ms (starts a wave); N arrivals (N ≥ 3 pinned) with OVERLAPPING
    outstanding waits during one initial-floor interval cost ≤ TWO read-begins (initial
    wave + one shared follow-up) and EVERY one observes a value fetched after its own
    arrival (truly SERIAL calls: one read-begin per elapsed floor interval — the honest
    ceiling, stated); the floor measures READ-BEGIN instants (a wave queued
    past its schedule licenses no extra wave); the SLOW-PLAIN-FETCH fixture: plain
    begins first, wave begins later and writes, plain lands last → the wave's snapshot
    survives (both orders, under the per-key mutex); the write-through wins as the
    newest write AND resets the floor clock; the CONTEXT fixtures: the leader's request
    context dies mid-flight, a joiner still gets the value; server SHUTDOWN cancels an
    admitted pass; a last-waiter cancel after read-begin does not;
    zero-waiters-before-read-begin discards unexecuted (timer and queued acquire
    cancelled; liveness re-checked after permit acquisition; a post-permit discard
    releases its permit and the FIFO advances); AGGREGATE, deadline + budgets: N same-key
    marked calls in one floor → one pass; N distinct VALID series keys → read
    concurrency ≤ `RECOVERY_MAX_CONCURRENT_WAVES`; EVERY marked request — gated reads
    included — settles (value or typed refusal) within `RECOVERY_REQUEST_DEADLINE_MS`,
    so none reaches the client's 30 s `API_TIMEOUT_MS`; ONE deadline per HTTP request
    (a marked summary whose TWO wrapper reads both stall answers the typed refusal at
    25 s, one deadline observed); every ADMITTED pass settles
    within the EXECUTION budget; the FAN-OUT fixture: an N-waiter same-key wave
    reaching the execution deadline → exactly ONE read-begin, EVERY waiter gets the
    typed failure, each transaction latches once, retries join ONE replacement wave;
    the SINGLE-ATTEMPT fixture: transient-first-then-success upstream → a marked pass
    issues exactly ONE request and fails typed, a plain pass keeps the shipped
    3-attempt policy; the RELOAD fixture: a config reload mid-wave → the old wave's
    write does not land, post-reload reads hit the new instance; the refusal sentinel
    (admission refusal AND request-deadline expiry alike) reaches the wire as 429
    (never 500);
    N arbitrary positive ids → ZERO upstream calls;
    the ORDERED-GATE fixtures: a series added upstream with the cached list stale → the
    marked episodes read awaits the list wave and SUCCEEDS; a fabricated id misses the
    fresh list and 404s with zero upstream calls; EXCLUDE-TAG failure: a fail-open nil
    is never cached; a MARKED exclude-tag failure or refusal → typed leg failure (no
    empty-exclusion 200); plain/scan paths keep fail-open; the HINT fixtures: a cache
    hit with one matched + one missing configured name logs the missing NAME verbatim
    (the `{ids, unmatched}` entry value), both arr sides.
  - _Requirements: 1.3, 2.3_

- [x] 2. gzip: a delayed-commit writer, scoped by content type (A8)
  - Wrapper INSIDE `Recoverer`, short-circuiting `/api/events` and exempt routes BEFORE
    wrapping. `WriteHeader` records status + Content-Type eligibility (JSON only) without
    committing; buffer to `GZIP_MIN_BYTES + 1`; crossing commits compressed (drop
    Content-Length, set Content-Encoding, stream); completion at/under commits identity;
    `Flush` forces identity. UNWIND: panic before commit discards + re-panics (Recoverer's
    500); panic after compressed commit closes the gzip stream best-effort + re-panics.
    Hygiene: `Vary`, `q=0`, HEAD/204/304, pre-encoded skipped.
  - Tests: 1025-byte body compresses (Content-Length removed); 1024-byte identity;
    `{"ok":true}` identity; multi-write crossing compresses whole; non-200 preserved both
    sides; panic at 1024 → JSON 500; panic after 1025 → complete gzip stream, no second
    response; SSE unbuffered; Range/206 + yaml identity; `q=0` identity; `Vary` present.
  - _Requirements: 2.2_

- [x] 3. Per-item coverage endpoints, numeric TVDB/TMDB keys (A2)
  - `GET /api/coverage/series/{tvdbId}/summary`, `GET /api/coverage/movies/{tmdbId}/summary`
    (no subs), `GET /api/coverage/movies/{tmdbId}/subs`. Wildcards are UNTYPED; the HANDLER
    validates: malformed (non-numeric) → 400, well-formed unknown → 404. `userConfigured`;
    coexists with the legacy trailing-slash prefix route under ServeMux specificity.
    Series recipe: tvdb→row index over the CACHED LIST, exclude-tag via task 1, one
    prefix-bounded `subtitle_files` scan. EXCLUSION PARITY, shared predicate, SCOPED TO
    THE TWO SUMMARIES: a summary 404s exactly where the collection omits — zero-episode
    series, file-less movie, vanished id, and (with task 4) any row without a POSITIVE
    canonical id. `/subs` is a STORE-ONLY read: that movie's rows or an empty list, no
    arr read (nothing to wave — it does not honor `?recovery=1`), 400 only on a
    malformed id. Wirespec + regen; the TWO SUMMARIES register with `Query: true` at
    birth (they honor `?recovery=1`); `/subs` does NOT.
  - Tests: series summary deep-equals the collection's item (R7.1 seed); movie summary
    equals modulo `Subs` (task 4 upgrades); exclusion boundary → 404 (summaries);
    `/subs` for an excluded/vanished movie → rows-or-empty, no 404 parity claim;
    malformed → 400; unknown summary → 404; unauthenticated → 401; prefix-bounded scan;
    `TestWirespec_matches_registerRoutes` green.
  - _Requirements: 1.2, 2.3_

## Phase 2 — Coverage path (Go + TS)

- [x] 4. Movies wire cut: collection slims + detail reads rows on demand (A3, A5 — one change-set)
  - Server: `MovieItem.Subs` REMOVED; collections stop serializing rows without a positive
    canonical id (closes the `tvdb-0`/`tmdb-0` key-collision class; UX-flagged; population
    ~0); wirespec + regen. Steady-state and boot reads are PLAIN; recovery legs signal
    task 1's `?recovery=1` query parameter. THIS TASK owns the `Query: true` addition
    for the three SHIPPED honoring endpoints (the two collections + episodes-by-series):
    wirespec edits + regen + the shifted call-site signatures in the same change-set —
    `coverageSeries`/`coverageMovies` (`coverage.ts:76-77`), `mediaEpisodes`
    (`detail.ts:262`), the two typed test mocks (`coverage.test.ts:21,32`), and the
    zero-arg `coverageMovies()` at `app.ts:76` (compiles unchanged; named here because
    task 7 deletes it). THE PIN, split by layer (eleven shipped endpoints already carry
    `Query: true` for unrelated query strings — `listState`, `stateIDs` among them —
    and keep it): the wirespec table test asserts `Query == true` ON the five honoring
    endpoints and that no OTHER endpoint's flag changed; the HONORING pin is
    handler-level — `?recovery=1` is interpreted by exactly the five and ignored
    everywhere else (`/subs` named). Client (same commit): `openMovieDetail` renders
    from the cached row and fetches `/movies/{tmdbId}/subs` behind a NEW skeleton
    controller (150/300 ms). Task 3's movie parity upgrades to full equality.
  - Characterization-first: movie-detail suite pinned.
  - Tests: no `subs` key; zero-canonical-id rows absent from collections and 404 from
    summaries; movie-detail one bounded fetch, skeleton timing honored; the wirespec
    table test (five have `Query: true`; no other flag changed) + the handler-level
    honoring test (`recovery=1` interpreted by exactly the five, ignored by `/subs` and
    a sample of others); both tsconfigs compile after regen (the shifted signatures);
    fixture size raw+gzip recorded (≥60% hypothesis, evidence not gate).
  - _Requirements: 2.1, 2.3, 9.1_

- [x] 5. Identity-preserving merge + full-row updater (B1, F1)
  - One canonical `coverageItemSignature` over EVERY rendered/keyed/filtered/sorted/
    actioned field (type, ids, title, year, dates, rule, tags, excluded, has_file,
    scene_name, audio_lang, episodes, targets vector), AUDITED against the structs. Merge
    keeps the CURRENT object on equal signatures. `updateCoverageRow` becomes a FULL-row
    in-place updater (fixes the stale-title bug), gated by `data-sig`. `--title-w` DELETED
    incl. its five assertions as named deliberate deletions.
  - Characterization-first: coverage.test.ts pinned.
  - Tests: no-op `setAll` repaints ZERO rows; one-row change repaints exactly that row
    incl. title-only and episode-count-only; focus survives; recompute count pinned; sort
    does not run on a no-op merge.
  - _Requirements: 8.1, 8.2, 7.1_

- [x] 6. Event heal through ONE per-id coalescer (A6)
  - Greenfield `events.ts` coverage handler. ONE PARSER, POSITIVE by construction:
    `tvdb-([1-9]\d*)(?:-s\d+e\d+)?`, `tmdb-([1-9]\d*)` → `{kind, numericID, rootKey}`;
    malformed, ZERO, LEADING-ZERO, type-mismatched, imdb-fallback → NO request. GATE:
    enqueue only when `libraryLoaded` or the affected detail is open (a deep-link insert
    does not open it); task 12's history trigger listens outside this gate. Trailing
    coalescer (`SUMMARY_COALESCE_MS = 300`); per-root serialized latest-wins GETs,
    aborting superseded; a 404 DELETES the row. One `batch()` per flush. FAILURE
    ESCALATION: a failed per-root GET re-enqueues ONCE; a second failure puts the root in
    the DIRTY SET — SCOPED (library-root entries persist until convergence or a
    committing transaction; detail-coupled entries clear on route leave), CAPPED at 64
    with drop-oldest (a dropped root converges via the next event, replay, or
    transaction), retried at each 60 s reconcile tick, which PAUSES WHEN HIDDEN
    (accepted: a hidden tab converges on return via replay or transaction). Detail
    coupling: series event → the REFRESH pair through task 7's guard (keeps `historySet`
    current); movie event with detail open → `/subs` refetch + one `state/ids?type=movie`
    read. THE RESET RULE (shared with task 9 AND the library route loader): abort
    in-flight per-root GETs and clear pending queues for rows about to be overwritten;
    task 12's pending-reload latch re-arms on abort. Full-pair collection reads are
    legal in
    exactly TWO places: transactions and the LIBRARY ROUTE LOADER — the navigation load is
    a plain read that ON LANDING sets `libraryLoaded`, opens this gate, registers the
    pair for later collection legs, and runs the reset rule; it does not touch the
    watermark (the transaction's own leg is task 9's failure-preserving variant; a
    loader arriving mid-transaction joins PER COLLECTION per task 9's ownership rule).
  - Tests: parser table (episode, series, movie, malformed, ZERO, LEADING-ZERO, mismatch,
    imdb → no request); synchronous 200-event burst across k roots ≤ k GETs, one flush;
    window-spanning ≤ one per root per window + one trailing heal; zero full-collection
    GETs; one derive per flush; delayed OLDER response aborted; 404 removes the row; a
    FAILED per-root GET (502) re-enqueues once, a second failure joins the dirty set and
    converges at a reconcile tick WITHOUT a reconnect (the live-stream fixture); event
    with gate closed makes NO request; a /history session navigating to the library loads
    the pair via the ROUTE LOADER, sets `libraryLoaded`, and opens the gate (the
    mid-session fixture); movie + series heal through the SAME path; detail couplings once
    per window; `libraryLoaded` stays false on upsert-into-incomplete; the R7.1 PARITY
    test.
  - _Requirements: 1.1, 1.2, 1.4, 7.1_

- [x] 7. `refreshCurrentPage` becomes the page-leg dispatcher; invalidate coalescer RETIRED (B2)
  - `refreshCurrentPage` gains AbortController + generation token PER ROUTE and becomes the
    ONE dispatcher for the transaction's PAGE leg (task 9's per-route enumeration); the
    movie-detail arm's full-collection `coverageMovies()` read (`app.ts:76`) is DELETED
    with the takeover (the leg is summary + `/subs` + movie `stateIDs` — R1.2's last
    illegal full-collection read on a detail path). The
    ROUTER's leave path ABORTS the departing route's controller (the detail refresh pair
    dies on leave, beside C2's dispose). The same generation guard makes a degraded
    boot's ungated fetch SUPERSEDED by the follow-up transaction's leg; a SUPERSEDING
    caller's AbortError is never treated as a failure. The trailing invalidate coalescer
    is RETIRED (seven emitters; tasks 9/12 delete six; config-save survives alone and is
    non-bursty); `INVALIDATE_COALESCE_MS` does not ship.
  - Tests: a stale response (older generation) is discarded — the fetch-races pin;
    config-save still refreshes once; a degraded-boot fetch superseded by a transaction
    leg under one generation; the leave path aborts the departing detail pair; the
    movie-detail arm performs NO full-collection read.
  - _Requirements: 1.2, 8.4_

- [x] 8. Item-grain deep links (A7)
  - `findCoverageItem` on a cold cache resolves via the routed type's summary (the route's
    numeric id IS the path parameter); renders directly; a 404 renders the NOT-FOUND empty
    state; does NOT mark the library complete and does NOT open task 6's gate.
  - Tests: series deep link → one summary GET; movie mirror; a 404 deep link renders the
    empty state (no crash, no collection fetch); cold-cache detail flips neither
    `libraryLoaded` nor the gate; back-nav from a LOADED pair zero-fetch; from an unloaded it
    fetches the full collection.
  - _Requirements: 2.3, 1.2, 9.1_

## Phase 3 — SSE lifecycle + status (Go + TS)

- [x] 9. Resumable SSE: boot epoch, commit-only watermark, ordered transactions (E3)
  - Server (wrapper IN `internal/server/events`): parse `?last_id=` (positive decimal ≤
    2^53−1; larger/zero/invalid/absent = no resume); header absent + query present →
    DERIVED request; header wins, on recreates too. Pre-check: pre-gap iff
    `lastID > 0 && (lastID > head || lastID+1 < floor || head − lastID > REPLAY_BUDGET)`
    — ordered disjuncts; the FOURTH is the server-side replay budget (covers hidden-window
    growth and native retries); THE STRIP
    FOLLOWS THE VERDICT whatever the cursor's source. AUTHORITATIVE verdict in
    `OnConnect(fn(w, b) error)`: exactly ONE `epoch` frame per connection, after any
    replay, before live delivery, `w.Event(0, "epoch", …)` — NO id field; payload
    `EpochEvent{boot_id, gap, head}`, a REGISTERED union variant (three edits), in the
    SAME `{type, data}` envelope `Publish` uses. Pins: `b.Head` ≥ every replayed id; a
    post-subscribe read covers every id ≤ head; TOPICLESS publish. `WithReplay(SSE_RING =
    1024)` at `events.New`; the pre-check's head is read before subscribe (the budget is
    a bound within one subscribe's publish rate — stated tolerance; fixtures control
    publish timing). Client (`events.ts` module state — a mid-latch reload boots
    fresh, safe; starts only in the configured, authenticated app): TWO BUFFERS — the
    `verdictBuffer` (pre-epoch; cap `2 × SSE_RING`, a stated heuristic; DISCARDED on
    condemnation, drained in order on a clean verdict; the `> head` classification arm
    is DEFENSIVE and unreachable by webhttp's ordering — replay precedes the epoch and
    `b.Head` covers every replayed id) and the `holdQueue` (post-epoch frames > head
    during a transaction; applied on commit after the snapshot and on abort).
    BOOT-CHANGE APPLICATION: every buffered frame carries its source `boot_id`; a
    boot_id-change abort applies the old queue BEFORE any new-boot frame is
    classified, with counter advancement DISABLED and ONLY the non-reconstructible
    payloads applied — `notify` toasts and `sync:done` dialog settlement (no read
    model / registry dropped at restart; held frames were never applied, so first
    delivery); STATE-BEARING old-boot frames (`coverage`, `activity`, `alert`,
    `provider`, the `scan:*` state halves and `scan:done`'s toast) are SKIPPED — the
    new transaction's legs are strictly newer authority; replay dedupe keys are
    `(boot_id, frame_id)`, the sets reset with the namespace; sync correlation is
    `job_id` WITHIN one boot — the dialog's match clears WITH the counters and the
    namespace, AFTER the old payloads apply (the held settlement lands first), then
    re-attaches via the jobs read. Post-epoch frames ≤ head apply IMMEDIATELY,
    with the transaction TOMBSTONE set over WRITERS: a heal 404-DELETE landing while a
    transaction is open records its root; while the transaction is open, EVERY
    full-pair snapshot application — the collection leg AND any concurrently-running
    route loader — DROPS tombstoned rows, through ONE shared `applyCoveragePair`
    application site this task EXTRACTS (both writers call it; the null-collapsing
    read keeps its fetch shape); tombstones clear at settle only when no covered
    writer is in flight (else when the last one lands); residual stated: outside a
    transaction there is no tombstone set, and a loader racing a heal delete converges
    via replay or the next transaction (a VANISHED root emits no next event —
    transaction-or-reload only). REFUSALS FAIL FAST: a non-200 fires `error` at once — the 401
    redirect and the degraded boot run immediately; `EPOCH_TIMEOUT_MS = 10 000` prices a
    SILENT open stream only; a timeout = undecodable handling; an epoch-less down period
    latches ONE precautionary refetch. The epoch handler MUST NOT read `lastEventId`;
    `boot_id` stores on epoch arrival. COMMIT-ONLY WATERMARK, scoped to one boot_id
    (trigger (2) resets it — the reset precedes any presentation opportunity); real frames
    never advance it. `appliedHigh`: advances by `max` over applied ids, RESETS on boot_id
    change, never a cursor; THE REPLAY TABLE, exhaustive over the union: `coverage`
    re-applies (coalescer); `activity` upsert/remove, `alert`, `provider` re-apply
    (idempotent); `scan:start`/`scan:done` re-apply STATE, `scan:done`'s toast half
    DEDUPES; `notify` dedupes; `sync:done` re-applies (idempotent per `job_id`); `epoch`
    is never replayed. THE ORDERED TRANSACTION (boot =
    no stored boot_id; the transaction FOLLOWING a degraded boot uses RECOVERY semantics):
    (1) connect, await the epoch — DEGRADING on refusal/failure/deadline, the ungated boot
    load superseded later under task 7's generation guard; (2) HOLD frames > head; (3) run
    the legs after this connection's subscribe, applying task 6's RESET RULE — LEGS APPLY
    ON LANDING (a leg applies its result the moment it lands; COMMIT = all legs LANDED,
    never a visibility gate): the
    COLLECTION leg = registeredCollections ∪ collectionsRequiredByCurrentRoute
    (cold `/` = the indivisible pair; the library route LOADER also registers the pair
    mid-session; `registeredCollections` is PAGE STATE and SURVIVES boot_id changes —
    trigger 2 resets the counters only), a TRANSACTION-OWNED FAILURE-PRESERVING
    operation: applies `setAll` on landing, settles `applied | superseded`, REJECTS on
    genuine transport failure — never the loader's null-collapsing read. The JOIN is PER
    COLLECTION: a loader arriving mid-transaction whose pair the leg COVERS awaits it
    (renders on landing; attaches its own error rendering without converting the
    transaction's settlement); a loader whose pair the leg does NOT cover (a /history or
    deep-link session's transaction has an EMPTY collection leg) runs its NORMAL
    navigation load concurrent with the transaction (plain read; on landing sets
    `libraryLoaded`, registers the pair, runs the reset rule; superseded by nothing —
    the drain stays idempotent over it; NOT aborted by a further navigation away — it
    applies on landing wherever the user now is, registration and `libraryLoaded` being
    tab state, not route state); a JOINED loader whose leg FAILS renders the
    route's normal loading/empty state (no error panel — the latch's forced transaction
    owns recovery) and still performs its non-fetch duties (mount, pair registration,
    reset rule). THE PAGE LEG dispatches via task 7's dispatcher (library:
    nothing extra; series detail: the triple, whose `mediaEpisodes(arrId)` is task 1's
    episodes-by-series consumer; movie detail: summary + `/subs` + movie `stateIDs`;
    history: `reloadHistoryForTransaction(): Promise<"applied" | "superseded">` — a
    REQUIRED EXTRACTION backed by the raw list read, NOT a wrapper (today's
    `reloadHistory` returns void, its catch terminates at `showError`, and `fetchPage`
    maps failure to empty — no wrapper can observe settlement or failure): non-2xx
    REJECTS and ABORTS the transaction (prior rows intact, watermark untouched); THE
    SETTLEMENT MODEL: every created or queued generation settles exactly once as
    applied | failed | superseded(next) | abandoned, published through a
    history-module-owned gen → settlement handle; a superseded run resolves
    `superseded` only by chaining through superseded(next) links to a generation that
    settles applied (`g !== gen` proves the superseder STARTED, not that it applied);
    a chain ending failed or abandoned leaves the leg REJECTING (abort + latch); a
    page-leg run aborted by the dispatcher's RE-ROUTE (route leave during a
    transaction) settles superseded(next = the new route's page leg) — no latch,
    commit lands when the re-routed leg and loader land; a route whose page leg is
    EMPTY settles its generation applied immediately on dispatch, so next always
    resolves; a route leave DROPS the departing route's pending E4 latch under the
    same re-route arm; abandoned is a DEFENSIVE terminal with NO shipped producer (a page-destroying
    redirect kills the transaction with the page): any settlement path not otherwise
    named rejects loudly rather than hanging;
    `reloadHistory(): void` survives as the UI adapter routing rejection to
    `showError`; task 12 replaces the extraction's INTERNALS behind the same seam and
    settlement contract; files: the file list),
    plus E2's status fetch; transaction leg fetches run on the RAW generated client
    with ZERO automatic retries (a 429 or any non-2xx surfaces on first receipt;
    retryNetwork-classified wrappers are out of bounds for legs); RECOVERY
    transactions send `?recovery=1` on the FIVE honoring
    endpoints, BOOT
    transactions read plain; (4) COMMIT (ALL legs landed — the page leg included, waiting
    behind an in-flight gesture where task 12's serializer holds one; anchor live):
    watermark := epoch.head; `libraryLoaded` is set by WHICHEVER caller LANDS the pair
    (the loader's plain read or the transaction's leg), never unset by an abort; drain
    the holdQueue
    ascending; clear EVERY pending latch (force + down-period COALESCE into this one
    transaction); (5) ABORT (anchor died, a leg GENUINELY failed, or a leg was REFUSED —
    the wave layer's typed 429 counts as a genuine leg failure; a superseding
    caller's AbortError is NOT a leg failure; NAVIGATION never aborts the collection leg,
    it re-routes the PAGE leg): results already applied STAY APPLIED (nothing rolls
    back) but the license is REVOKED for UNLANDED legs — a still-in-flight leg's
    eventual landing is a NO-OP (the forced transaction's own leg is the pair's next
    writer; an orphaned stale pair must not revert healed rows); apply the holdQueue
    (the boot_id-change counter-advancement-disabled arm
    above), set the
    FORCETRANSACTION LATCH, tear down via `close()` (no
    native retry can exist on a latched client) and recreate with NO cursor — the first
    valid epoch transacts UNCONDITIONALLY. LATCHED BACKOFF, pinned at the code site:
    `reconnectAttempt` resets ONLY on a non-latched `open` and on COMMIT; `disconnect()`
    and the latched teardown do NOT touch it (today's reset at `events.ts:149` moves);
    `SSE_MAX_RECONNECT_MS = 60 000` caps the ladder — a persistent outage costs at most
    one transaction per minute. DETERMINISTIC TRIGGER ORDER: reset counters (trigger 2) →
    classify verdictBuffer frames (≤ head apply, > head hold) → legs → commit → drain; on
    a CLEAN (gap-free) verdict the verdictBuffer classifies BEFORE triggers evaluate
    (replayed frames apply, `appliedHigh` advances first); an ABORT slots where its
    trigger fires — with the boot_id arm applying-without-advancement, reset-then-abort
    and abort-then-reset agree.
    SYNTHETIC CURSOR:
    recreate paths present `?last_id = watermark` only with a committed watermark for the
    CURRENT boot_id, the latch clear, and the client PRE-FILTER passing
    (`appliedHigh − watermark ≤ REPLAY_BUDGET`; the SERVER disjunct is authoritative;
    an UNEVALUABLE pre-filter PASSES — one counter unknown presents the cursor and the
    server decides; `max` over counters treats an unknown as absent). TRIGGERS: (1) epoch gap; (2) boot_id change
    (watermark + appliedHigh reset first); (3) cursor-less non-boot connect whose head
    exceeds `max(watermark, appliedHigh)` or finds BOTH unknown — `appliedHigh == head`
    after classification is the "nothing was missed" evidence, available without reading
    `lastEventId`; plus the latch. head == watermark triggers nothing; post-classification
    head == appliedHigh triggers nothing either (the native-retry-with-replay case).
    One transaction at a time; triggers and latches coalesce. The `open` handler's
    unconditional `DataInvalidate + pollStatus` is DELETED.
  - Tests (Go): derived-request injection, original unchanged; header wins on recreate;
    0/invalid/over-2^53 ignored; replay from mid-ring; gap truth table (floor-1 covered;
    below-floor gap; above-head gap; empty-ring gap; header-carried pre-gap STRIPPED);
    exactly one epoch per connection, after replay, before live, no `id:` field,
    `{type,data}` envelope, generated-decoder green; `ReplayBounds.Head` + topicless
    pins; ring 1024 at `events.New`. (TS): boot page loads epoch-gated on EVERY route,
    degrading on refusal/failure/timeout, refusals FAIL FAST (a 401 redirect does not
    wait the deadline), the degraded fetch superseded under one generation; the
    transaction after a DEGRADED boot sends `?recovery=1`; a post-epoch frame > head is
    HELD and drained after commit (V10/V11); frames ≤ head apply immediately; the
    TOMBSTONE fixtures, BOTH writers: frame ≤ head mid-transaction, arr deletes the root
    before the heal GET (the cache refreshed between the leg's read and the heal's), the
    full-pair response lands LAST — the row is ABSENT after commit; once with the
    collection leg landing last, once with the empty-leg arm's LOADER landing last;
    ABORT rolls nothing back (applied legs stay applied); the BOOT-CHANGE fixtures:
    restart mid-transaction with a held `notify` and a held `sync:done` → the toast
    shows once, the dialog settles its job, `appliedHigh` starts from the new boot's
    ids, the next reconnect presents a cursor; the SAME numeric frame id across two
    boots applies both distinct payloads once (dedupe keyed `(boot_id, frame_id)`, the
    sets reset with the namespace); a held old-boot `scan:done` followed by a new-boot
    `scan:start` leaves final state NEW-boot state (the old state half skipped);
    a failed OR 429-refused leg → the
    LATCH: recreate presents NO cursor, the first epoch transacts unconditionally, and
    `reconnectAttempt` is untouched by `disconnect()` and reset only on non-latched open
    or COMMIT (the persistent-502 ladder fixture climbs to the 60 s ceiling); the JOIN
    per collection: a loader whose pair the leg COVERS joins it (no abort, no false latch
    — the nav-mid-transaction fixture); a loader whose pair the leg does NOT cover runs
    its normal load (the /history-session transaction + mid-flight library navigation
    fixture: the library PAINTS and the gate OPENS; a further navigation away does not
    abort it — it applies on landing); a JOINED loader whose leg 502s:
    loading/empty state (no error panel), ONE request pair, transaction aborts, snapshot
    and watermark unchanged, latch set, the retry's commit paints with zero user action;
    the history-leg extraction, the settlement model in the task-9 window: a
    current-run 502 REJECTS (prior rows and watermark unchanged, latch set, no
    commit); a superseded run whose superseder APPLIES resolves `superseded` and the
    transaction COMMITS; the SUPERSEDED-CHAIN fixture — T lands, S starts (bumps gen),
    S 502s → the leg REJECTS, prior rows and watermark unchanged, latch set; the
    RE-ROUTE fixture — transaction on /history, navigation to the library mid-leg →
    the run settles superseded(next = the new route's page leg, EMPTY there → applied
    immediately on dispatch), NO latch, commit lands when the re-routed leg and loader
    land, and the departing route's pending E4 latch is dropped under the same arm; the
    abort REVOKE fixture: an aborting transaction's still-in-flight collection leg
    lands after the successor's fresher pair → NO revert (the orphan's landing is a
    no-op); a 429-refused
    leg's endpoint saw exactly ONE request before the latch set; navigation re-routes the PAGE leg
    without
    aborting
    the collection leg; latches coalesce into ONE transaction; DETERMINISTIC trigger
    order (counters reset → verdictBuffer classified → legs → commit → drain;
    clean-verdict classification precedes trigger evaluation); a replayed
    notify shows ONE toast and EVERY union variant behaves per the replay table;
    `appliedHigh` and the watermark reset on boot_id change while `registeredCollections`
    SURVIVES it (restart with a committed pair → the recovery transaction's collection
    leg includes the pair); the SERVER replay budget
    (a cursor with `head − lastID > 256` → gap epoch → one transaction — the
    hidden-scan-return fixture); the NATIVE-RETRY split: a native retry with a real id
    100 frames back → replay, `appliedHigh == head`, ZERO refetches, NO transaction; the
    same retry 400 frames back → gap epoch, exactly ONE transaction; the client
    pre-filter blocks only
    with both counters known (unevaluable PASSES — the cursor is presented, the server
    decides); synthetic truth rows
    (== head, in-ring within budget, native-retry in-budget, in-ring over budget
    (floor 100, head 500, cursor 200 → gap, one transaction, zero replay presentation),
    == floor-1, > head, below floor,
    latch set, and the R5.2 max rows: watermark 100 / appliedHigh 400 / head 400
    cursor-less → ZERO refetches, head 401 → exactly one transaction); head == watermark
    triggers nothing; post-classification head ==
    appliedHigh triggers nothing; the 10/11 residual recovers once;
    cold-`/` fetches the pair and a failed pair load sets neither `libraryLoaded` nor the
    watermark; recovery reads send `?recovery=1` and render wave-fresh data (the
    t=14 s fixture, stated single-reader: no wave in the preceding floor); boot reads are
    plain; verdictBuffer frames of a condemned connection
    never applied; overflow → latched recovery; the epoch handler never reads
    `lastEventId`.
  - _Requirements: 5.1, 5.2_

- [x] 10. Server status events: activity, alert, provider (E1)
  - `activity {op: upsert|remove, entry?}`: terminal transitions IMMEDIATE + FLUSH BARRIER
    (cancel pending timer, discard queued snapshot, one terminal upsert; only `remove`
    after); non-terminal progress coalesces per activity per `ACTIVITY_EVENT_MIN_MS =
    1000`. `remove` fires on ALL THREE removal paths (dismiss, prune, cap eviction)
    through the Log's `OnRemove` hook — removals COLLECTED under the lock, the hook FIRED
    AFTER UNLOCK with the under-lock entry snapshot (set by the SERVER; the Log cannot
    import the bus). Prune POLICY on `Log.PruneCompleted`; the GOROUTINE is the server's —
    `s.bgWg.Go(runActivityPrune)` on a 60 s ticker (the goroutine-discipline test sees
    it); the READ-PATH prune is REMOVED (one owner); prune timing "within [PruneAge,
    PruneAge + 60 s)". `alert {op, alert?}` on raise/dismiss (TTL expiry reconcile-only).
    `provider {op, entry?}` on raise/clear. Each variant = the three union edits + regen.
  - Tests: progress burst → one event per activity per second, LAST snapshot; terminal
    immediate; FAKE-CLOCK barrier (progress t=0, terminal t=100 ms, advance past 1 s, NO
    stale snapshot); `remove` observed from dismiss AND ticker-prune AND cap eviction,
    fired AFTER unlock with the under-lock snapshot; the read path no longer prunes;
    alert + provider round-trips; payloads decode through the generated decoders.
  - _Requirements: 4.1_

- [x] 11. Event-driven status; the poll becomes a floor (E2)
  - Status store fed by task 10's events. One fetch at connect/boot; 60 s reconcile while
    CONNECTED; 5 s ONLY while DOWN (post-CLOSED backoff; CONNECTING blips are not down);
    `stateStats` popup-gated; concurrent legs; offline gates result handling;
    pause-when-hidden pinned.
  - Tests: steady-state ≤1/60 s; degrade on backoff entry, recover on reconnect;
    CONNECTING blip does not trigger 5 s; buttons + chip update from events, poll silent;
    provider chip event-fresh; offline detection intact; hidden tab ZERO polls.
  - _Requirements: 4.1, 4.2, 4.3_

- [x] 12. Redundant refresh paths deleted; history: coverage-driven, foreground-priority, depth-preserving (E4)
  - `scan:done` drops `pollStatus` + blanket `DataInvalidate`. History's owner covers ALL
    acquisition paths incl. poller imports: while `currentPage === "history"`, coverage
    events — observed OUTSIDE task 6's gate — enqueue a trailing reload coalescer (the A6
    window); terminal `activity` events too. FOREGROUND PRIORITY, one serializer: a user
    gesture (`loadMore`) is NEVER cancelled by an event reload — the event latches ONE
    pending reload that runs at the NEW depth after the append commits; a FILTER change
    supersedes everything; a route LEAVE drops the pending latch, settling under task
    9's re-route arm; every dispatch captures `{filters, depth}` at dispatch time
    under one generation. The reload is DEPTH-PRESERVING with newest-window semantics: one
    fetch of `limit = loaded row count` (≤ the 10 000 depth cap — the server's LIMIT clamp
    is the rationale; the clamp stays silent and the client never exceeds it), keyed
    `setAll`: COUNT preserved, surviving rows keep identity, displaced rows drop off the
    bottom. "Show more" visibility = `hasMore && loaded < cap` (two named facts). The
    status-poll running→idle `DataInvalidate` is deleted; manual-download `DataInvalidate`
    dropped; per-download 2 s pollers DELETED (buttons carry `data-activity-id`; popup
    close/reopen re-derives; SSE-down covered by the degraded poll). Task 9's TRANSACTION
    uses this serializer for its history page leg — the leg latches like any event reload
    and COMMIT WAITS FOR IT (behind an in-flight gesture too, bounded by the gesture's
    seconds); task 9 ships the `reloadHistoryForTransaction` EXTRACTION (failure-
    preserving: non-2xx rejects, superseded resolves `superseded`) whose INTERNALS this
    task REPLACES with the foreground-priority depth-preserving version behind the SAME
    seam and settlement contract.
  - Characterization-first: the download button's optimistic flip + rollback pinned BEFORE
    the pollers are deleted.
  - Tests: scan completion → zero coverage fetches; coverage event with history OPEN
    reloads once per window; closed → nothing; a poller-import-shaped event reloads a
    FRESH tab at /history (no collection loaded); THE STORM ORACLE — 150 loaded, Show more
    in flight, N event triggers, append commits to 200, at most ONE pending reload at
    limit 200, all 200 rows keyed; a filter change supersedes both; depth pin under a
    storm asserts count + surviving-row identity (displaced rows drop); button hides at
    the cap while `hasMore` stays true internally; download completion flips the button
    via the event; TWO concurrent downloads resolve correctly; close-before-complete
    reopen re-derives; SSE-down completion still observed.
  - _Requirements: 5.2, 5.3, 4.4_

- [x] 13. Status precision (B5, B6, B7)
  - `runningScansByScope` publishes only on content change; `updateStatusButton`
    early-returns; scan buttons in a module registry; `updateLiveTimers` roots at the
    popup; the popup `reconcile` gains an `update` callback.
  - Tests: unchanged publish → ZERO DOM mutations; running→done updates the open popup row
    in place; registry add/remove on mount/unmount; timers tick only while open.
  - _Requirements: 8.5, 7.2_

## Phase 4 — Sync (Go + TS)

- [x] 14. Async single-file sync — server + client + functional suite, one change-set (D1, D3, D4)
  - Server: `POST /api/sync/audio` → `202 {activity_id, job_id}` after validation (the
    dialog matches `sync:done` on `job_id`, so the 202 hands it over; wirespec updated).
    Per-job `context.WithCancel` off the server lifetime context; tracked by the
    WaitGroup. BOUNDED DISPATCHER with an ADMISSION LEASE: an explicit FIFO owning the
    accepted count (`SYNC_JOBS_MAX = 8` top-level entries; overflow → typed 429 via
    `httpapi.TooManyRequestsC`; same-file dedupe answers 202 with the EXISTING ids even
    at cap), the dedupe map, and per-job cancels; enqueue non-blocking, atomically
    reserving a slot; the dispatcher runs ONE top-level entry at a time in FIFO order (a
    queued single behind a season batch waits for the batch — visible in the jobs list).
    THE TYPED CORE IS `run()` RESHAPED: `syncworker.Client.run` today maps cancellation
    and every failure to a no-change success (`client.go:90`); the reshaped core returns
    result (versioned JSON response, worker exit 0) | timeout (the budget timer fired —
    its own signal, not inferred from ctx.Err) | cancelled (job context cancelled) |
    crash (everything else: non-zero exit, protocol error, spawn failure), arms the
    budget UNCONDITIONALLY after admission — `client.go:94`'s conditional arm is DELETED
    with the reshape —
    and takes the ADMISSION HOOK — invoked at semaphore acquisition, returning ACCEPT or
    REFUSE: under the dispatcher lock it first REGISTERS the stop entry (single-file
    jobs), then publishes running (registry + activity); a cancel that won beforehand
    makes it refuse and the worker never spawns; a QUEUED DELETE that LOSES the lock race
    to admission CONVERTS to a running-cancel through the just-registered stop entry,
    answers 204, and the job settles `done(cancelled)` on worker exit; every terminal path
    unregisters before
    (or atomically with) publishing terminal state. `syncing.SyncExec` becomes a THIN
    PROJECTION mapping every non-result to the documented no-change degrade (one
    implementation, two projections; the scan-path pin holds). HTTP wiring, mechanical:
    `activityhandlers.Deps` gains an activity-id→dispatcher lookup; `DELETE
    /api/activity?id=` routes a QUEUED sync job through `dispatcher.Cancel` (slot released
    IMMEDIATELY; next 202 enters) before the legacy scan path;
    `POST /api/activity/{id}/cancel` stops RUNNING work via the stop registry; a TERMINAL
    row's dismissal never touches the registry. PINNED HTTP MATRIX: unknown POST 404;
    under-role 403; terminal/non-stoppable 409; repeated running stop 204; queued DELETE
    releases capacity; terminal DELETE dismisses only — and the stop-before-publish
    ordering makes a stop request between running-publish and registration
    unrepresentable. Shutdown cancels queued (settle, never
    execute) and waits for admitted workers. JOB RECORDS: `job_id` is a NUMERIC int64
    process sequence, distinct from the activity log's string ids (the record carries
    both); record `{job_id, activity_id, batch_activity_id?, series_id?, season?,
    ordinal?, file_ref, state: queued|running|done, outcome?, offset_ms?, confidence?,
    method?, applied?, dry_run?, error?, accepted_at, started_at?, ended_at?}`; total
    order `accepted_at DESC, job_id DESC` (numeric). `GET /api/sync/jobs`
    (`userConfigured`, filterable by `batch_activity_id`); `sync:done {job_id,
    batch_activity_id?, file_ref, offset_ms (CUMULATIVE), confidence, method, applied,
    dry_run, error?}`. Reload: prefer queued/running for the FileRef, else newest
    terminal by the total order. Retention = `activity.DefaultPruneAge` (ONE owner);
    queued/running never evicted; the cap evicts only past-retention; the registry is
    truth independent of the activity ring; restart drops it (documented).
    `readAndParseSRT` gains the caller's context. Client (same commit):
    `audioSyncAction` → 202 dispatch carrying NO retry (the shipped actions framework
    classifies 429 retryable; the cap refusal must be visible and never auto-retried).
    `error: false` STAYS — the framework has no per-outcome error spec
    (`NotificationSpec` is one definition-level value; `emitErrorToast` suppresses
    only on literal false, `actions/src/define.ts:657-678`) — and the DIALOG's inline
    result path renders the typed refusal, read through `DispatchHandle.outcome`
    (`ActionErrorLike.status`) and DISPLACING the shipped
    `notify.error("Audio sync failed")` toast at `sync.ts:485` for the 429 arm:
    exactly ONE visible surface, pinned. The
    dialog
    subscribes to `sync:done` matched on
    the 202's `job_id` + activity phases; reload re-attaches via the jobs list.
    Functional suite: `sections_ops_test.go:252` is a LOG-ONLY probe — the task report
    STATES its behavior change (4xx unchanged if validation still reads the file
    synchronously; 202-then-failed-activity if deferred).
  - Characterization-first: sync.test.ts pinned.
  - Tests (Go): slow analysis outlives request + disconnect; shutdown cancels queued
    (settled cancelled, never executed) and waits for admitted; the HOOK returns
    accept/refuse — a cancel that won refuses the run; stop registered BEFORE running
    publishes (a stop request in the window cannot 409 — pinned); terminal unregisters
    before terminal-publish; the HTTP MATRIX (unknown 404, under-role 403,
    terminal/non-stoppable 409, repeated stop 204, queued DELETE releases capacity,
    terminal DELETE dismisses only); with the semaphore held by an automatic sync, a
    dispatched job stays `queued` with NO budget running; the budget arms UNCONDITIONALLY
    at admission; typed outcomes discriminated by their NAMED signals (budget timer vs
    ctx cancel vs exit state — the `client.go:90` cancellation-as-success mapping is
    gone); SyncExec's degrade projection pinned on the scan path; duplicate dispatch →
    existing ids; CAP (N accepted, N+1 → 429, same-file at cap → 202 existing); FIFO — a
    single behind a batch waits, the automatic path interleaves at the semaphore; total
    order numeric; queued→done(cancelled) retained and reload-visible; retention/cap
    rules; unauthenticated → 401. (TS): dispatch resolves fast, result lands via event
    matched on the 202's `job_id`; job A completes, job B starts on the SAME file, a
    replayed `sync:done` for A settles only A; close-dialog analysis continues; reload
    shows queued state, running progress, AND a completed-while-away outcome; no
    timeout-retryable classification remains; the cap 429 renders a VISIBLE message
    through the dialog's inline result path — exactly ONE surface, no framework toast
    (`error: false` stays) — and dispatches exactly one request.
  - _Requirements: 3.1, 3.2, 3.4_

- [x] 15. Season sync: server-owned batch — parity first, then the cut-over (D2, D3)
  - PARITY TEST FIRST (gate for the deletion): fixture-driven equality with the client
    pool's selection contract (file-bearing episodes; resolved configured target pairs;
    EXTERNAL entries only, every matching ordinal; ASS/SSA gate per item; config snapshot
    at acceptance; vanished rows skipped and reported). Then: `POST /api/sync/season
    {series_id, season}` (episodes via task 1); ONE batch activity carrying the AGGREGATE
    only (`Current`/`Total`/detail — `activity.Entry` gains no per-item shape); EVERY
    per-item fact lives in the registry — ALL item records created at batch ACCEPTANCE
    (`state: queued`, `ordinal` set), so a reload BEFORE any `sync:done` renders the full
    item list — each item publishing its own `sync:done`; item job contexts derive from
    the BATCH context (itself off the server lifetime context), so the batch's stop
    cancels every item. The batch is ONE dispatcher entry; items
    submitted SEQUENTIALLY (item N+1 only after N terminates; the semaphore released
    between items — an automatic sync waits behind at most ONE item; dispatcher entries
    wait in FIFO). THE BATCH IS THE CANCELLATION UNIT and REGISTERS ONE STOP ENTRY AT
    BATCH START (its callback cancels the batch context; queued items settle cancelled;
    ITEM leases do not touch the stop registry — no overwrite/unregister window between
    items); item rows carry NO cancel affordance. Wirespec, `userConfigured`; the season
    202 returns `{activity_id}` (item job_ids arrive via the registry read and per-item
    events). Client (same
    commit): the season UI drives from the batch aggregate + the registry filtered by
    `batch_activity_id`; reload re-attaches; `detail-season-sync.ts` DELETED after parity
    passes — its tests die with it as named deletions.
  - Tests (Go): observed concurrency EXACTLY 1 through the REAL admission; between items
    an automatic sync can acquire the semaphore (interleaving observed); a TWO-ITEM
    season retains BOTH results, reload reconstructs by `batch_activity_id`, a replayed
    `sync:done` updates only its own `job_id`; partial failures per item, batch
    completes; a WEDGED item consumes its ceiling, batch proceeds; stop mid-batch —
    between items AND mid-item — cancels via the batch's SINGLE stop entry (no 409
    window) and settles
    remaining as cancelled; a reload BEFORE any `sync:done` lists all items queued;
    parity fixture green. (TS): reload re-attach shows batch
    progress; per-item results render from the registry; one POST, no client fan-out.
  - _Requirements: 3.2, 3.3, 3.4_

## Phase 5 — Detail + residue (TS/CSS)

- [x] 16. DESKTOP series episode table leaves the table formatting context; mobile + movie-detail unchanged; release on leave; hoists (C1, C2, C3)
  - Desktop conversion SCOPED to `.series-detail` (the shared row/cell rules must not
    disturb `.movie-detail`, which stays a table and joins the sweep as a no-change
    control). Named overrides: the generic `table-layout: fixed` as applied here,
    `table.series-detail { display: table }`, the tbody/colgroup display rules, the row
    reset (`06-table.css:84-86, 236-295`). Conversion: table → block; tbody → block;
    `<colgroup>` DELETED from `detail.ts` (widths → ONE shared desktop
    `grid-template-columns` custom property for episode/season-head/column-header/
    season-gap rows); `colSpan: 999` → `grid-column: 1 / -1`; the season-head label
    spans, not clips. MOBILE VERBATIM. Then `content-visibility: auto` +
    `contain-intrinsic-size: auto <estimate>` per breakpoint. ARIA: table +
    `aria-labelledby` (harness populates `lib-heading` first), rowgroup/row/columnheader/
    cell — desktop full set; mobile as rendered (accepted). `detail.test.ts:1365-1371`
    RETIRED, REPLACED by the shared-template assertion. Client: dispose from the ROUTER's
    leave path; sort hoisted once per rebuild; precomputed history membership; keyenc
    SHA-256 out of scope.
  - VISUAL PARITY HARD GATE (sidecar, both breakpoints + the movie-detail control):
    desktop column alignment, season-head rows identical and unclipped, hover/selection
    indistinguishable, MULTIPLE consecutive rows and season groups; mobile pixel-stable.
    ANY difference stops the task. Find-in-page: spec-guaranteed; MANUAL check at
    sign-off (task 20).
  - Tests (vitest browser): navigate-away releases row effects; derivation-count pin;
    ARIA per breakpoint incl. accessible name; shared-template assertion; containment
    TRANSITIONS on a driven scroll (skipped true away / false into view); scroll
    stability across a heal; existing detail suites re-run. Reduced-motion +
    view-transition characterization. (Sidecar): parity screenshots at both breakpoints +
    movie-detail control.
  - _Requirements: 6.1, 6.2, 6.3, 7.3_

- [x] 17. Batching + small residue (B3, B4; F2, F3 hygiene)
  - `history.loadMore` batches upserts; `filterCoverage` batches its two writes;
    toast/dismiss Sets prune on `remove` events with a size-cap belt (F2);
    `onBackdropClose` wired once per dialog at boot (F3).
  - Tests: reconcile-count spy; no double filter+sort; Sets shrink on prune, respect the
    cap; N dialog opens leave one close listener.
  - _Requirements: 8.3, 8.4_

## Phase 6 — Closure

- [x] 18. Steering documentation (same change-set rule; documentation task — no requirement tag)
  - `subflux.md` (identity events + parser + coalesced heals; boot epoch + watermark +
    ordered transactions + synthetic cursor; bounded sync dispatcher + admission lease +
    job registry; status events + poll floor + flush barrier + prune ticker),
    `subflux-api.md` (endpoints + numeric params + exclusion parity, the no-HTTP-bypass
    rule + recovery WAVES (read-after-arrival, the delay floor, the aggregate cap, the
    five honoring endpoints) and why,
    `EpochEvent` + status events + `sync:done` shapes, removed Subs, replaced sync
    handler, `GET /api/sync/jobs` + the job state machine, `SYNC_JOBS_MAX`, SSE_RING),
    `subflux-ui.md` (desktop conversion + shared template + ARIA, mobile unchanged,
    movie-detail on-demand rows + skeleton, sync UX on `job_id`, `libraryLoaded` +
    gate, history foreground priority + depth cap). Spec docs committed to .kiro
    alongside.

- [x] 19. Full verification battery + stress evidence
  - `go build ./... && go test ./... -count=1`; static-src: both tsconfigs, eslint,
    stylelint, prettier, full vitest. Stress lane against the deterministic reference
    fixture (500 series / ~10k episodes / 4,360 movies): 200-event synchronous burst
    (zero full-collection GETs, ≤k FIRST-ATTEMPT summary GETs — belt and dirty-set
    retries counted separately, one derive per flush, ≤1 row repaint per
    event, main-thread time); 1,100-row detail layout ON vs OFF containment (fixed
    browser/viewport/warmups/threshold); status steady-state rates SSE-up vs SSE-down;
    the history storm oracle at 3 loaded pages; movies payload raw+gzip before/after
    (≥60% hypothesis as evidence); the MEASURED full arr list fetch duration at the
    reference library (recorded as the wave-delay trade's stated basis — design A4).
    R7.3's battery half lives here.
  - _Requirements: 1.4, 2.1, 4.1, 6.1, 7.3, 8.1_

- [ ] 20. Final report
  - DELIVERED IN CHAT (2026-08), not written to a file: the outcome summary, the measured
    numbers and the two open user-eyes items live in `design.md`'s BUILD OUTCOME block.
  - Per-task evidence, review-loop summary, UX-flag sign-off list (design §UX-visible,
    all 10 items incl. the manual find-in-page check, the zero-canonical-id removal, the
    10 k cap, and the boot connect-before-fetch reorder), bugs fixed along the way
    (stale-title, open-handler double-fetch, readAndParseSRT context,
    sync failure-as-success mapping, the `tvdb-0`/`tmdb-0` key-collision class),
    deliberate test deletions/replacements, deferred items, push/PR follow-ups. No pushes
    without approval.

## Traceability

| Req | Tasks |
|---|---|
| R1 | 1, 3, 5, 6, 7 |
| R2 | 1, 2, 3, 4, 8 |
| R3 | 14, 15 |
| R4 | 10, 11, 12 |
| R5 | 9, 12 |
| R6 | 16 |
| R7 | 3, 5, 6, 12, 13, 16, 19 |
| R8 | 5, 7, 13, 17 |
| R9 | matrix above (5, 6, 7, 8, 11, 12, 16, 17 + battery) |
