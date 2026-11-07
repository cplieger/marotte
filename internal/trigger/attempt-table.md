# The attempt table

Every launch attempt of an event rule is one record, and `Confirmer.step` is the one function that changes a record or writes an event rule's state file. A change from outside the table (a poll's cursor and queue, an outcome on the rule's row, a dismissed gap) is applied in memory by `Confirmer.update` and then written by row E10. This file names the rows the code and its tests cite: A0 to A38, W1 and W2 for a record, E1 to E10 for a rule, and the two rules R and Q. `TestAttemptTable` runs the cases built from it against `step`.

`step` takes the rule's mutex, applies the row for the record's state and the event (or, for a rule event, the rule row, which applies a named record row to each record it holds), writes the rule's whole value at most once, and returns the effects. No caller holds one rule's mutex while it calls `step` on another rule.

## States

| State | Persisted as | Meaning |
| --- | --- | --- |
| `invoking` | `pending` | Named after `workflow/new`, before the lease and the `invoke`; its `LaunchTriggered` call has not returned. Its bound is armed. |
| `invoked-unconfirmed` | `pending` | The call returned a nil reply or a transport error, and the bound decides. |
| `boot-held` | `pending` | Loaded at boot with a lease. `BootHeld` answers true, and its bound starts when the engine is ready. |
| `abandoned-cancelling` | `abandoned`, no `cancel_landed_at` | No longer the entry's attempt; a cancel is owed until one lands. |
| `abandoned-cancelled` | `abandoned` with `cancel_landed_at` | KAS took this table's own cancel; the record stays until a run list shows the run gone, the TTL passes or a person retries the run. |
| none | no record | An id the rule holds no record for: confirmed, dropped, or never named by this rule. |

The three `pending` states are the live ones, and only a live record is its queue entry's current attempt; `step` keeps at most one per key. A confirmed attempt has no record: its `launched` entry carries the workflow id. In memory a record also carries its bound timer, `in_flight` (from A1 until the call that named it returns), `owes_release` (abandoned while in flight), `unpersisted` (its rule is gone, so it writes nothing) and `cancels_out` (cancels queued whose answer has not come back).

## Events

Record events carry a workflow id:

| Event | Delivered by |
| --- | --- |
| `named` | `NameAttempt`, after `workflow/new` and before the lease and the `invoke` |
| `invoke ok` | `LaunchReturned` with a nil error, only for a call that created a run |
| `invoke error` | `LaunchReturned` with an error proving KAS did not take the `invoke`: a `*marotte.RPCError` (`cause=invoke_error`), `ErrLeaseNotGranted` (`cause=lease_not_granted`), or an error the table already decided (`errAttemptGone`, `errAttemptNotWritten`) |
| `invoke reply lost` | `LaunchReturned` with any other error: a deadline, the bridge ending, another transport error |
| `run_start` | `ConfirmEvent` |
| `bound reached` | the record's `launchConfirmBound` timer |
| `engine ready` | the engine-ready channel closing, delivered only to `boot-held` records |
| `cancel ok`, `cancel error` | the effect goroutine's `CancelAbandoned` answer: ok only when its own cancel verb returned nil |
| `busy` | the launcher, when `LaunchTriggered` answers a `*schedule.BusyError` naming the run. When another rule Y holds the record, `step(Y, busy)` runs first and the launching rule's `busy` then carries Y's name and whether Y's record is live (A38) |
| `retried` | `Retried`, from a person's retry, only on the rule holding the id |
| `write failed` | the write a row made returning an error |

Rule events carry none:

| Event | Delivered by |
| --- | --- |
| `edit (filter)`, `edit (rebaseline)`, `delete`, `pause`, `resume` | `RuleChanged`, once the definitions write lands. A save that changes only `enabled` is a pause or a resume; one that changes the connection, repo or kind is a rebaseline; any other is a filter edit |
| `run gone` | `PruneAbandoned`, with a triggered launch's run list; a list that failed delivers nothing |
| `ttl` | the hourly TTL ticker |
| `launch ended` | the launcher clearing a key's launching mark, with the call's error when it created no run |
| `write retry` | `update`, and the write-failure tick of a rule a write-failure block holds |
| `boot` | the Confirmer's load, once per state file |

## Record rows

"Live" is any `pending` state and "abandoned" either abandoned state. A cancel is queued to the effect goroutine and adds one to `cancels_out`. A failed attempt on the 24-hour clock sets `failing_since` when it is zero, and blocks the rule once it is 24 hours old.

| Row | State | Event | Next state | Write | Effects |
| --- | --- | --- | --- | --- | --- |
| A0 | any | a cell no other row names | unchanged | no | ERROR `attempt table: impossible transition`; the call answers an error. |
| A1 | none | `named`, the key queued with no live record | `invoking`, `in_flight` | yes, before `NameAttempt` returns | Arm the bound. A failed write is W1. |
| A2 | none | `named`, the key no longer queued or already holding a live record | `abandoned-cancelling`, `cause=name_failed`; `unpersisted` when the rule is gone or its file unreadable | yes, unless `unpersisted` | Cancel; `NameAttempt` answers `errAttemptGone`. Nothing counted. |
| A3 | live | `run_start` | confirmed: no record | yes | The key moves from the queue to `launched`; superseded keys naming it gain the workflow id; `last_run_id` and `last_run_at` set; the bound stops; the id leaves the boot-held set; the failure counts reset. |
| A4 | `invoking` | `invoke ok`, `invoke reply lost` | `invoked-unconfirmed`, not `in_flight` | no | None: the bound decides. |
| A5 | `invoking` | `invoke error` | `abandoned-cancelling`, its cause, not `in_flight` | yes | Cancel; Q; the backoff. The error's class decides the rest: blocked blocks the rule with KAS's sentence, counted is a failed attempt on the 24-hour clock, retryable counts nothing. |
| A6 | `invoking`, `invoked-unconfirmed` | `bound reached` | `abandoned-cancelling`, `cause=bound`; `owes_release` if `in_flight` | yes | Cancel; Q; the backoff; a failed attempt; `bound_abandons` + 1, and the second blocks the rule. |
| A7 | `boot-held` | `engine ready` | unchanged | no | Arm the bound. |
| A8 | `boot-held` | `bound reached` | `abandoned-cancelling`, `cause=bound` | yes | As A6, except `bound_abandons` does not grow: a restart is not the rule's fault. |
| A9 | live | E2's part | `abandoned-cancelling`, `cause=edit`; `owes_release` if `in_flight` | in E2's write | Cancel. Nothing counted. |
| A10 | live | E3's part | `abandoned-cancelling`, `cause=delete`, `unpersisted`; `owes_release` if `in_flight` | none | Cancel; it leaves by A32. |
| A11 | live | E1's part | unchanged | in E1's write | None: the bound still decides. |
| A12 | live | `busy` | unchanged | no | None: the bound decides. |
| A13 | `abandoned-cancelling` | `cancel ok` | `abandoned-cancelled`, `cancel_landed_at` set | yes, unless `unpersisted` | `cancels_out` - 1; R; A32. |
| A14 | `abandoned-cancelling` | `cancel error` | unchanged | no | `cancels_out` - 1; R; A32. |
| A15 | abandoned | any `LaunchReturned` event | unchanged, not `in_flight` | no | R; A32. Nothing abandoned again, nothing counted. |
| A16 | `abandoned-cancelling` | `busy` | unchanged | only when the clock starts or the rule blocks | Cancel again; a failed attempt; the row says the cancel has not landed. |
| A17 | `abandoned-cancelled` | `busy` | unchanged | as A16 | A failed attempt; the row says the cancelled run has not ended. No cancel. |
| A18 | abandoned, nothing waiting on it (neither `in_flight` nor `owes_release`), its run not `Active()` in the list | E5's part | dropped | in E5's write | None. |
| A19 | `abandoned-cancelled`, nothing waiting on it, `cancel_landed_at` older than `abandonedTTL` | E6's part | dropped | in E6's write | None. |
| A20 | abandoned | E1's or E2's part | unchanged | in the edit's write | None: the run may still be live. |
| A21 | abandoned | E3's part | `abandoned-cancelling`: `unpersisted`, cancelled again. `abandoned-cancelled`: dropped, or `unpersisted` while a call, a release or a cancel is out | none | An `unpersisted` record leaves by A32. |
| A22 | abandoned | `run_start`, `bound reached`; a second cancel's answer | unchanged | no | A cancel's answer: `cancels_out` - 1, R, A32. Otherwise none. |
| A23 | none | `run_start`, any `LaunchReturned` event, `bound reached`, a cancel's answer, `retried`, a `busy` for another rule's launch | none | no | None: a resumed confirmed run, a late return, a stale timer, or a record that left meanwhile. |
| A24 | none, no rule's record holds the id | `busy` | none | no | Not counted, never cancelled. A lease of any origin or `last_run_id` naming the run: the row says the recipe's run is running. Otherwise the run is the rule's blocker and the row says it waits for it; once it has blocked for `launchConfirmBound` the row says no record names it and one WARN is logged. |
| A25 | any | E4's or E8's part | unchanged | in E4's write | None: a live record can still confirm. |
| A26 | live | a cancel's answer; E5's and E6's part | unchanged | no | None. |
| A27 | `pending` on disk, a lease held | E7's part | `boot-held` | none of its own | Joins the boot-held set; A7 arms the bound. |
| A28 | `pending` on disk, no lease | E7's part | `abandoned-cancelling`, `cause=boot_unleased` | in E7's write | Cancel once the engine is ready; Q; the backoff. Nothing counted. |
| A29 | `abandoned` on disk, no `cancel_landed_at` | E7's part | `abandoned-cancelling` | none of its own | Cancel once the engine is ready. |
| A30 | `abandoned` on disk with `cancel_landed_at` | E7's part | `abandoned-cancelled` | none of its own | None. |
| A31 | none: a triggered lease naming the rule and a queued key, no record for its id | E7's part | `abandoned-cancelling`, `cause=boot_stale` | in E7's write | Cancel once the engine is ready. |
| A32 | abandoned, `unpersisted`, nothing in flight, owed or out | the end of A13, A14, A15, A22 or R | dropped | no | None; a deleted rule's value goes once it holds no record. |
| A33 | abandoned | `retried` | dropped, committed only once its write lands or it is `unpersisted` | yes, unless `unpersisted` | `Retried` answers handed back, and the retry takes a manual lease. A failed write keeps the record, refuses the retry with the error, and W2 blocks the rule. |
| A34 | the rule's blocker | E5's part, the blocker not `Active()` in the list | blocker cleared | no | The row text A24 set clears. |
| A35 | live | `retried` | unchanged, the bound restarted from the retry | no | `Retried` answers the record's event, so a retry finding no lease re-leases the run as that event's. A `boot-held` record A7 has not armed keeps waiting for A7. |
| A36 | none: a triggered lease naming the rule whose key is in `launched` | E7's part | none | no | Not boot-held, no cancel. |
| A37 | abandoned | E5's or E6's part where A18 or A19 does not apply | unchanged | no | None. A record whose cancel never landed is never dropped by age. |
| A38 | none, the id held by another rule Y's record | `busy`, after `step(Y, busy)` | none | no | Not counted, no WARN, the blocker cleared; the row says the run is Y's, live or abandoned. |
| W1 | none | `write failed` on A1's write | `abandoned-cancelling`, `cause=name_failed`, no bound | with the next write that lands | The write-failure block; ERROR `launch rules not written`; cancel; Q; the backoff; `NameAttempt` answers `errAttemptNotWritten`. Nothing counted. |
| W2 | any | `write failed` on any other write | the row's next state, in memory; for A33, the state before it | E10, at each write-failure tick and the next `update` | The write-failure block and the ERROR line. The block lifts when any write of the rule's whole value lands. |

## Rule rows

Each applies the named record row to every record the rule holds, then its own changes, in one call.

| Row | Event | Per record | Then | Write |
| --- | --- | --- | --- | --- |
| E1 | `edit (filter)` | A11, A20 | Failure counts reset and the block lifted, except an unreadable file's; a `filter.label` change resets the labeled cursor; a change to `newest` runs Q for every queued key with no live record | one |
| E2 | `edit (rebaseline)`, or a stored population that is not the rule's at boot | A9, A20 | Cursor, queue, `launched` and `superseded` reset; failure counts reset and the block lifted, an unreadable file's included; the population set to the rule's | one |
| E3 | `delete`, or a state file whose rule is gone at boot | A10, A21 | The state file removed; the write-failure block cleared. A removal that fails logs ERROR and is retried by the next boot's E3 | none |
| E4 | `resume` | A25 | As E1's reset | one |
| E5 | `run gone` | A18 or A37, A26, A34 | none | only if A18 dropped a record |
| E6 | `ttl` | A19 or A37, A26 | none | only if A19 dropped a record |
| E7 | `boot` | A27 to A31, A36 | Q for every queued key with no live record; E2 when the stored population is non-empty and not the rule's. A file that does not load, or whose cursor, queue or attempts do not decode, leaves the rule blocked with the read error and holding nothing, and nothing writes over it until E2 replaces it or E3 removes it | one per rule changed |
| E8 | `pause` | A25 | none: the counts, the block and the clock are kept | none |
| E9 | `launch ended` | none | Q for the key when it is still queued with no live record. An error from a call that created no run is classed: blocked blocks the rule, counted is a failed attempt, retryable counts nothing, and a busy recipe waits for the rule's next poll. A call the launcher's own shutdown ended is not classed | only if Q moved the key or the rule's failure state changed |
| E10 | `write retry` | none | Write the whole value; a landed write lifts the write-failure block and wakes the launcher, a failed one is W2 | one, unless the rule is gone |

## R and Q

**R, the release check.** A record abandoned while its launch was in flight (`owes_release`) may have had its cancel land before its lease was written, so that cancel's terminal frame released nothing. R runs once the record is neither in flight nor waiting on a cancel's answer: an `abandoned-cancelled` record queues `ReleaseTriggered`, an `abandoned-cancelling` one leaves the lease as the run's floor, and either way `owes_release` clears.

**Q, the queue rule.** Q runs for a key whose flight or live record ended while it stays queued (A5, A6, A8, A28, W1, E9, and E1 and E7 for every queued key with no live record). The entry keeps its place and launches again on the backoff, unless the rule's hold policy is `newest` and a newer entry of the same coalesce key is queued: then the key moves to `superseded`, naming the newest entry, and is not launched again. A key in `launched` is never superseded.
