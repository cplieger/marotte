# Event triggers

A workflow can start itself when something happens in a repository: an issue or pull request gets a label, an issue or pull request is opened by an account you list, or a CI run finishes. marotte watches the repository through a forge connection from the **Sources** tab, by polling it. It needs no webhook, so it works for a marotte the forge cannot reach.

## Adding a rule

On the **Workflows** panel of the `/docs` page, open **Run when…** on the workflow's row. The button counts the enabled rules when there is more than one, as in "Run when… 2". **Add a rule** offers **On a schedule**, the schedule picker, and **When something happens on a forge**, the event form:

1. **Connection**: one of the connections the **Sources** tab lists. GitHub, GitLab, Gitea, Forgejo and Codeberg connections work.
2. **Repository**: `owner/name`, or a GitLab group path.
3. **When**: "an issue gets a label", "a pull request gets a label", "an issue is opened by…", "a pull request is opened by…" or "a CI run finishes".
4. The kind's own fields: the label; the accounts, one login per line; or the workflow file path, the branch and the results (success, failure, neutral) of a CI run.
5. **Check every**: how often marotte reads the repository, from 2 to 60 minutes, 5 by default. Under **More**, how events wait while the workflow is busy (see [Held events](#held-events)).
6. **Inputs**: a fixed value for each input the workflow declares, and the list of inputs the event sets.
7. The model and effort picks, as for a schedule.

Saving checks that the connection can read the repository and the list the rule watches. When it cannot, the form shows the forge's reason and saves nothing. A new rule takes a baseline at its first check: what is already there when you save it never starts a run. An issue that already has the label, an item opened before the save, or a CI run that started before the save launches nothing.

Each rule is one line under **Run when…**, with a switch that pauses it, **Edit** and **Delete**. The line says what starts the run, the last event that started one and that run's outcome, and the rule's state, such as "watching, checked 13:55" or "1 event waiting".

## Who can start a run

Each kind rests on the forge's own permissions or on a list you write. None of them reads what an issue or pull request says.

- **A label**: the rule starts a run whichever account adds the label. On GitHub, adding a label takes the Triage role or higher. On GitLab it takes the Planner or Reporter role or higher, and GitLab drops the labels a Guest sets on a new issue. On Gitea and Forgejo it takes write access to the repository's issues or pull requests. Two things get around that permission. An issue template or issue form whose `labels:` key holds the label applies it for anyone who files through it, so keep a trigger label out of every template. And a bot or Actions workflow you gave the permission can add the label in answer to text a stranger wrote. Pick a label that a person applies, or that your automation applies only after its own check of who asked.
- **Opened by**: the rule starts a run only for an item opened by an account on its list, compared without regard to case. The list cannot be empty. A bot starts runs only when its login is on the list, such as `renovate[bot]`. Remove a person from every list when they leave: a renamed account matches nothing, and on GitHub a released login can be registered again by someone else.
- **A CI run**: the rule starts a run when one of the repository's own runs finishes with a result you picked, on the workflow and branch you named. By default only runs started by a push, the forge's schedule or a person count. **Also runs started by pull requests or comments** adds the runs that an outside pull request or comment can start. A run started by a pull request from a fork reports the fork's branch, which its author names and can call `main`. On Forgejo a pull request's run names the pull request's number as its branch, so a branch filter never matches it.

A triggered run starts with nobody watching, like a scheduled one. A tool request that needs approval is refused after 3 minutes and the run stops, unless **Approve tool requests in scheduled and triggered runs instead of refusing them** under **Unattended runs** in **Settings** is on. A triggered run can read text other people wrote, such as an issue body. With that setting on, a request that such text leads the run to make is approved too.

## What the run receives

A run receives ids and your own values, never an issue's or pull request's title, body or branch, and never a commit author. A workflow that needs the text reads it through its own tools, from the ids, and treats it as untrusted data.

Every event sets these inputs:

| Input | Value |
| --- | --- |
| `repo` | The repository, such as `owner/name` |
| `forge` | The connection, such as `github:github.com` |
| `trigger_kind` | The rule's kind: `issue_labeled`, `pr_labeled`, `issue_opened`, `pr_opened` or `run_concluded` |
| `trigger_rule` | The rule's id |
| `trigger_key` | The event's key |
| `event_id` | The event's id, which stays the same for every run marotte starts for that event. Compare it whole: a GitLab repository path can contain `/` |
| `issue_number` or `pr_number` | The item's number, for the label and opened kinds |
| `label` | The rule's label, as you typed it |
| `author` | The entry of the rule's list that matched, as you typed it |
| `run_id` | The CI run's id |
| `result` | `success`, `failure` or `neutral` |
| `workflow` | The rule's workflow file path |
| `head_sha` | The commit the CI run ran on, when it is a full commit id |

A workflow receives only the inputs it declares, so declare each one it should read. A label event's `event_id` includes the rule's id and the time marotte saw the label added, so a label removed and added again is a new event, and two label rules on one item never share an id. The other kinds' ids name the item or the run, so two rules on the same connection pass the same id for the same item or the same run ending with the same result. A fixed input cannot use a name the event sets.

## A run can start twice for one event

marotte starts each event's run at least once, and sometimes twice. A run counts as started only when it reports its start to marotte. If marotte restarts at the wrong moment, or the start report is lost, marotte cancels the run it cannot account for and starts the event again with the same inputs. The first run may already have done some of its work.

So a workflow whose work must not happen twice does this:

1. It declares the `event_id` input.
2. It records the `event_id` where its later runs can read it, such as a file in its own state folder, a label or a commit trailer. It records it only after the work that must not repeat has landed, in the same step as that work or the next one, never at its start. A workflow with several such effects records each one on its own (`<event_id>#push`, `<event_id>#comment`), or makes each effect safe to repeat.
3. A run that finds its `event_id` already recorded ends at once, successfully.

A run stopped before its record has recorded nothing, so the next run for the event, or your retry with the same inputs, does the work. A workflow whose work is safe to repeat, such as one that applies a diff a second run finds empty, needs none of this. The event form warns when the workflow declares no `event_id` input, and the rule's line shows "no event_id".

## While marotte is down

marotte keeps each rule's position on disk and catches up at its first check after a restart:

- **Labels and opened items**: an event on an item that is still open when marotte is back starts its run then, however long marotte was down. An item opened or labelled and then closed while marotte was down is not caught.
- **CI runs**: marotte reads back up to 24 hours before its last complete check, which catches a re-run of a run that started within that time. A run that is still running is watched for up to 72 hours after it started. A run that finishes more than 72 hours after it started is missed, and so may be a run that finishes while marotte goes more than 48 hours without a complete check. Each such case shows on the rule's line as a gap, such as "run 41414016210 was still running at its last check and was not seen to end", until you dismiss it with **Dismiss gap**.

A run that a restart stopped is not started again for its event. The rule's line says "stopped because the server restarted while it was running; its event is not launched again. Run it by hand if it is still needed".

## Held events

A workflow runs one run at a time, so an event that arrives while one is running waits. **More** on the form sets how:

- **Only the newest**, the default for CI runs, holds up to 10 events and starts only the newest of each workflow and branch, or of each item for the other kinds. The older ones are marked as replaced by it.
- **Each one, in order**, the default for labels and opened items, holds up to 20 events and starts every one, oldest first.

The number held goes up to 50. An event past it waits at the forge and is read again once the rule has room. A rule that matches more than 5,000 open items stops until fewer match.

Two settings under **Agent capabilities** on the **General** tab in **Settings** cap every rule together: **Triggered runs at once**, 3 by default, and **Triggered runs per hour**, 6 by default. An event past either cap waits. A run that holds the workflow and that marotte did not start is never cancelled. The rule's line names it, and after 5 minutes says that the run is blocking the workflow and that the rule has no record of launching it.

## When a launch fails

- A launch that fails is tried again after 1, 2, 4, 8, then every 15 minutes. A rule whose launches keep failing for 24 hours stops, and its line says why. Edit the rule, or switch it off and on, to start it again.
- A run that does not report its start within 5 minutes is cancelled and the event is started again. A second such run stops the rule, and its line says the run did not report its start twice: check the agent engine, then switch the rule off and on.
- A cancel the agent engine refuses is sent again at each check, and a run whose cancel has not landed holds the workflow until it does.
- A rule whose saved state does not load stops until you edit or delete it. A rule whose state cannot be written stops launching until a write lands, tried again at each check. Its line says which.

## Cost on the forge

marotte reads each repository once per check, for all the rules that watch it. On GitHub a page that has not changed since the last check costs nothing. Each connection spends at most 600 requests an hour of each of its rate-limit pools on trigger checks. A check waits when the pool has less than a fifth of its requests left, and when the forge answers that the limit is reached. The rule's line then says until when, for example "paused for the forge budget: core has 412 requests left, resumes 15:29". Saving a rule whose checks could spend more than that is refused with the estimate.

## Monitoring

The metrics page shows each enabled rule's state, and marotte ships alert rules for a rule that stopped, a full queue, a repository read that keeps failing and a catch-up gap. [Monitoring](monitoring.md) lists them.
