# Known upstream issues

This page lists the upstream issues that affect marotte and is updated when one is reported, fixed or worked around. Each entry says what you see, links the upstream report with its state, and says what marotte does about it.

## Agent engine

### Chats in Plan, Autonomous, a spec mode or an agent wait for each other

Chats in Default and Default v2 run their replies side by side. A reply in Plan, Autonomous, a spec mode or an agent waits while any other such reply runs, in any chat. The waiting chat says which chat it waits for, and the wait can last minutes. A workflow step woken by a message can join the same wait.

Reported as [kirodotdev/Kiro#11995](https://github.com/kirodotdev/Kiro/issues/11995), closed once kiro-cli 2.37.0 let Default v2 replies run side by side. The other modes still wait. marotte has no workaround. Use Default or Default v2 for chats you want to run side by side.

### The agent stalls during a file search

kiro-cli runs its own ripgrep when the agent searches the workspace. When ripgrep fails, kiro-cli falls back to a slow listing with no time limit, and every chat and workflow its engine hosts waits while it runs, sometimes for over an hour. ripgrep fails on a dangling symbolic link or a link loop anywhere in the workspace, and on a tree too large to search in 30 seconds.

Reported as [kirodotdev/Kiro#11957](https://github.com/kirodotdev/Kiro/issues/11957), open. marotte runs the agent's `grep_search` and `file_search` itself, so kiro-cli's own search and its fallback never run. marotte searches the workspace and the additional directories with ripgrep, honours `.gitignore`, never follows symbolic links, and keeps ripgrep's results when it skips an unreadable path. A search that names a folder, such as `marotte/internal/**/*.go`, reads only that folder. Each search ends after 15 seconds, or when you stop the reply, and the agent receives what was found by then: at most 100 files for a text search.

### A reply hangs after a failed compaction

When kiro-cli's own automatic compaction fails, the reply can stay open with nothing more arriving. marotte turns kiro-cli's automatic compaction off in every chat and compacts at the end of a reply instead, so a chat no longer meets this. A workflow step, which kiro-cli still compacts by itself, can. marotte marks a step that shows no activity for an hour and notifies you, and stops the run instead when no chat started it.

Reported in the Kiro Crew tracker as [kirodotdev/KiroCrew#3583](https://github.com/kirodotdev/KiroCrew/issues/3583), closed after Kiro Crew added a time limit in its own client. kiro-cli itself still leaves the reply open. After a failed compaction, marotte ends a reply that shows no new activity for 60 seconds and marks it interrupted.

### Context use reads the figure from before a compaction

After a compaction, kiro-cli keeps reporting the context use from before it until the model's next reply measures it again. A chat at 96% before `/compact` can still read 96% at about 30% real use. A client that compacts or warns at a context limit then acts on a figure that is no longer true.

Reported as [kirodotdev/Kiro#12392](https://github.com/kirodotdev/Kiro/issues/12392), open. After a compaction, marotte clears the chat's context use and ignores the figure kiro-cli repeats until the model's next reply measures it. The **Compact at** point and the 95% stop act only on a figure a model reply measured. marotte therefore does not stop a turn on the old figure.

### Replies slow down when many chats and workflow steps run at once

kiro-cli's agent engine runs every chat, workflow step and subagent on one JavaScript thread. On Node.js 22, which kiro-cli ships, garbage collection and the tracking of asynchronous code fill that thread when many of them run at once. Replies and workflow steps then slow down, starting a chat or a step can time out, and the engine can fall far behind. The engine's own log, `kiro.log`, falls behind under the same load.

Reported as [kirodotdev/Kiro#12261](https://github.com/kirodotdev/Kiro/issues/12261), open, and the log delay as [kirodotdev/Kiro#12260](https://github.com/kirodotdev/Kiro/issues/12260), open. **High throughput optimizations**, under **Experimental features** in **Settings**, is on by default. It starts the engine's Node.js with `--experimental-async-context-frame` and `--max-semi-space-size=64`, which cut the time the thread spends on that work. [Configuration](configuration.md#high-throughput-optimizations) gives the measured effect.

While the engine's thread stays busy, marotte waits up to 2 minutes before it restarts the engine, so one long stop does not end the replies in every chat. kiro-cli itself still ends the engine, and the replies in every chat, when a very long message, such as a large pasted prompt, is sent to it during a stop of more than 15 seconds. A change you make to a chat during the stop, such as a new mode or effort, shows as still applying instead of failed. The chat shows the change once kiro-cli applies it.

### A command that finished can show as timed out

When the engine's thread stops for longer than a command's time limit, and the command finishes during that stop, kiro-cli reports the command as timed out. In a chat, the agent is told that the command timed out and gets only part of its output. A workflow watch step that polls a command can drop a result the command printed in time, and the next step never receives it.

Reported as [kirodotdev/Kiro#12269](https://github.com/kirodotdev/Kiro/issues/12269), open. marotte has no workaround, because kiro-cli decides the timeout before it reads that the command finished. Running fewer chats and workflow steps at once makes the stops in the entry above less frequent.

## Chats

### A chat is named with a sentence that is not a title

kiro-cli names a new chat from a model reply, and that reply is sometimes a question back, such as a request for more context.

Reported as [kirodotdev/Kiro#11343](https://github.com/kirodotdev/Kiro/issues/11343), open. kiro-cli 2.21.4 fixed most cases. marotte refuses a name that is cut off, holds more than one sentence, or runs over 80 characters or 12 words. A short one-sentence reply can still get through. Rename the chat with **Rename chat** in the composer's **Chat options** menu.

### A message sent after deleting a waiting one does not reach the reply

While a reply runs, deleting a message the agent has not read yet can leave the messages you send next unread until the reply ends.

Reported as [kirodotdev/Kiro#11449](https://github.com/kirodotdev/Kiro/issues/11449), open. When you delete one waiting message, marotte clears them all and sends the ones you kept again as one message. It reaches the running reply when the agent has read none of your messages in that reply yet. Otherwise it waits, and marotte sends it as your next message once the reply ends.

### Deleting or editing a waiting message as a reply ends can start an empty reply

When a reply ends while a message you sent is still unread, kiro-cli continues the reply so the agent reads it. If you delete or edit a waiting message within a fraction of a second of the reply ending, kiro-cli can empty its queue after it has decided to continue. The agent then starts a reply with none of your messages in it.

Reported as [kirodotdev/Kiro#12262](https://github.com/kirodotdev/Kiro/issues/12262), open. A fix needs kiro-cli to check for unread messages again before it continues, or to offer a way to withdraw one message without emptying the queue. Once marotte sees a reply end, it shows each message kiro-cli still holds as "Delivering", with no Edit or Delete. That lasts until the agent reads the message or kiro-cli drops it, and marotte refuses a delete sent during that time. A delete or edit that marotte sent just before it saw the reply end is not covered.

### A message sent during a reply reaches its subagents

The agent can hand work to subagents in a chat with **Workflows** switched off in Settings, in Plan, Autonomous and the spec modes whatever that switch says, and inside a workflow step. A message you send while it works then reaches every subagent that runs later in that reply. A subagent can stop its own task to answer it.

Reported as [kirodotdev/Kiro#11240](https://github.com/kirodotdev/Kiro/issues/11240), open. marotte has no workaround. With **Workflows** on, the default, a chat in any other mode has no subagent tool, and this does not happen. In the cases above, wait for the reply to finish before you send guidance meant only for the main agent.

### A failed hook shows as completed, and what a hook prints never shows

A hook whose command fails, or whose command does not exist, shows as completed on its card. Anything a hook's command prints, such as instructions a `PostToolUse` hook gives the agent, is missing from the chat, so that hook counts as a passing hook.

Reported as [kirodotdev/Kiro#11369](https://github.com/kirodotdev/Kiro/issues/11369), open. kiro-cli reports every hook command as completed and sends neither its output nor its exit code. marotte shows what it reports, so it cannot tell a failure or show the output today. It will turn the card red on a failed status or a non-zero exit code, and show the output as its own row, once a kiro-cli release sends them. To check a hook, run its command in the terminal.

### A hook shows on its tool group, not on the call it checked

A hook that checks a tool call, such as a guard that runs before every command, shows as a count on its tool group's header rather than on the command it ran for. Selecting the count opens a list of those hooks, each with how often it ran and when it fires. When a call in the group is refused or fails, every hook in the group stays a row, because marotte cannot tell which hook refused it.

Reported as [kirodotdev/Kiro#12284](https://github.com/kirodotdev/Kiro/issues/12284), open. kiro-cli's report of a hook names neither the tool call it ran for nor whether it blocked that call. marotte will show each hook on its own call once kiro-cli names the call.

### A message from another service reads to the agent as yours

A message another service sends through the [notify route](configuration.md#waking-a-chat-from-another-service-marotte_notify_token) reaches the agent as a message from you. kiro-cli presents a message steered into a running reply as a new message from the user, and a new message as one the user typed, so the agent can follow it as your instruction.

Reported as [kirodotdev/Kiro#12325](https://github.com/kirodotdev/Kiro/issues/12325), open. kiro-cli offers no way to name a message's sender. The chat shows "Sent by" and the caller's `source` on the message, but the agent does not read the `source`. Until kiro-cli supports a sender label, start the message's text by naming the service that sent it.

### A saved prompt shows as the command you typed

When you send a saved prompt, such as `/chat-extract`, your message in the chat shows the command you typed, not the prompt's text that the agent received. A reload shows the same.

Reported as [kirodotdev/Kiro#12398](https://github.com/kirodotdev/Kiro/issues/12398), open. kiro-cli sends the prompt's text to the model but reports only the typed command to marotte, live and on reload. marotte has no workaround, because it shows what kiro-cli reports rather than reading the prompt file itself. To see what the agent received, open the prompt on the `/docs` page.

### A forked or branched chat carries the ID of the chat it came from

The agent's shell commands carry the chat's session ID as `KIRO_SESSION_ID`. In a chat started with **Fork a tangent** or **Branch from here**, that variable holds the ID of the chat it was forked from, not the new chat's own. A [notify request](configuration.md#waking-a-chat-from-another-service-marotte_notify_token) or a [message between chats](configuration.md#messages-between-chats) addressed with it reaches the chat it came from. kiro-cli sets the variable from the conversation's root, which a fork keeps from its source chat.

Not reported upstream yet. marotte has no workaround, because kiro-cli sets the variable itself. Until it changes, a forked or branched chat cannot read its own ID, so address requests and messages only to chats that were not forked or branched.

## Skills

### A skill with a long description or an underscore in its name never loads

kiro-cli drops a skill whose description is over 1,024 characters, or whose name has an underscore. The skill is missing from chats, and the agent cannot use it by name. Nothing says why.

Reported as [kirodotdev/Kiro#11101](https://github.com/kirodotdev/Kiro/issues/11101), open, and [kirodotdev/Kiro#10678](https://github.com/kirodotdev/Kiro/issues/10678), open. A skill created with **+** on the `/docs` page follows both rules. For a skill you edit by hand, keep the description at 1,024 characters or less and the name to 64 characters or less of lowercase letters, numbers and single hyphens.

## Workflows

### A message from another service shows in a workflow step's request as something you said

Every step of a workflow starts with a block that quotes the messages you sent the chat, as the request it is working on. A message another service sends through the [notify route](configuration.md#waking-a-chat-from-another-service-marotte_notify_token) is quoted there as one of your messages, so a step can treat it as your instruction. The block keeps only the newest 12 messages, so many of these lines can push your own messages out of it. A message that was withdrawn before the agent read it is quoted too.

Reported as [kirodotdev/Kiro#12346](https://github.com/kirodotdev/Kiro/issues/12346), open, beside [kirodotdev/Kiro#12325](https://github.com/kirodotdev/Kiro/issues/12325) in the entry "A message from another service reads to the agent as yours". marotte has no workaround. kiro-cli quotes the text of the message and removes every marker a client can send to tell it the message is not yours. This lasts until kiro-cli fixes it.

### An answer to a waiting step can become an ordinary message

kiro-cli can take an answer to a step's question as an ordinary message to the step. The step reads the words, but the run keeps showing the question as open.

Reported as [kirodotdev/Kiro#11863](https://github.com/kirodotdev/Kiro/issues/11863), open. marotte sends an answer only while kiro-cli is ready to take it, and keeps the question marked as answered until the step starts again. When the step has not started within 10 seconds, the question can be answered again.

### A workflow result can be lost when a reply ends

When a step reports or a run finishes while the chat that launched it is replying, the result waits for the agent to read it. When the reply ends with an error, or kiro-cli stops carrying unread messages into it, before the agent reads the result, the agent never gets it and keeps waiting for it.

Reported as [kirodotdev/Kiro#11857](https://github.com/kirodotdev/Kiro/issues/11857), open. marotte labels the lost result "Workflow result not delivered" in the chat. While a workflow result waits beside your unread messages, marotte deletes them only all together, and that drops the result too. Ask the agent to check the run when it keeps waiting.

### A workflow step fails while marotte is signed out

When a step needs the Kiro account while marotte is not signed in, the step fails instead of waiting. This can happen when runs resume after a container restart, before you sign in. Signing in does not bring the run back.

Reported as [kirodotdev/Kiro#11978](https://github.com/kirodotdev/Kiro/issues/11978), open. marotte has no workaround. After you sign in, **Retry failed steps** on the run's tab starts the failed step over from the beginning.

### A watch step fails although its command succeeded

While the host is busy, a step that polls a command can fail even though the command printed a valid result. The error reads `exited 0 but its stdout is not a valid WatchPollResult`.

Reported as [kirodotdev/Kiro#11985](https://github.com/kirodotdev/Kiro/issues/11985), open. marotte has no workaround. **Retry failed steps** on the run's tab starts the watch again.

### A run paused for a model throttle can read "Workflow paused by user."

kiro-cli accepts a request to pause a workflow run only on behalf of the user, and its run details do not say who paused a run. Any pause sent to a run kiro-cli paused for a model throttle therefore reads "Workflow paused by user.", though you paused nothing.

Reported as [kirodotdev/Kiro#12274](https://github.com/kirodotdev/Kiro/issues/12274), open. When marotte stops resuming a run after repeated throttling, it sends no pause and records the stop itself: the run's tab reads "marotte stopped resuming this run after repeated model throttling" beside **Resume**, a **Workflow runs** notification says so, and the agent that started the run reads it on its next turn. While your Kiro sign-in cannot be answered, marotte still pauses running runs, and that pause must name you, since kiro-cli accepts nothing else. The run's tab shows marotte's sign-in reason for it instead of saying you paused it.

### A model throttle after an answered question pauses the step as a question

When a workflow step asks a question, gets its answer while its turn is still running, and then hits a model throttle in that same turn, kiro-cli pauses the run as if the step still waited for an answer. It skips its own retries of the throttle, and nothing in the run says a throttle stopped it.

Reported as [kirodotdev/Kiro#12326](https://github.com/kirodotdev/Kiro/issues/12326), open. When marotte's record of every waiting step shows that the step's question was answered and its turn then failed on a model error a retry can clear, such as a throttle, marotte answers the question itself with kiro-cli's own message for continuing an interrupted turn, and the step carries on. The answer shows in the step as sent by marotte. **Resume** on the run's tab sends the same answer in your name. marotte's own answers count toward its limit of 3 automatic resumes with no progress in between, after which it stops resuming the run as described in the entry above. A step whose turn failed before its question was answered keeps waiting for your answer.

### A step can end with kiro-cli's placeholder as its answer

When a turn fails on a model throttle before the model replies, kiro-cli puts a placeholder reply in its place in every later request to the model. The placeholder starts "(No assistant response was recorded for the preceding turn." Later in such a session, a model can answer with that exact text and end its turn. In a workflow step, kiro-cli then counts the step as completed with the placeholder as its output, and the run moves on.

Not tracked upstream: the report was withdrawn and its issue closed. kiro-cli 3.2.0 still inserts the placeholder. marotte marks a step failed when kiro-cli completes it with no completion signal and the step's answer after its last tool call is empty or is the placeholder. The run's tab shows the step as failed with the reason "The step ended with an empty model turn and no completion signal, so its work may be unfinished." A **Workflow runs** notification says the step failed, and the agent that started the run is told which step failed and how to run its work again. kiro-cli has already moved the run past the step, so marotte does not re-run it.

### A step that finished inside a parallel group holds up the steps after it

When a paused step that runs beside other steps finishes, because it reported that it is done or failed or because you chose **Mark step done** or **Mark step failed**, the steps after it in its group wait until every other step of the run stops. A step you mark done or failed shows as finished or failed at once and its group as paused. A step that reported it is done or failed stays paused until the other steps stop.

Reported as [kirodotdev/Kiro#12281](https://github.com/kirodotdev/Kiro/issues/12281), open, on kiro-cli 2.37.1, 3.0.0 and 3.1.0. It follows [kirodotdev/Kiro#11733](https://github.com/kirodotdev/Kiro/issues/11733), closed, whose fix in kiro-cli 2.32.0 finishes the step once the other steps stop but still holds its group until then. marotte labels a step that reported its end "Done, applies when the run's other steps finish" or "Failed, applies when the run's other steps finish". While the run keeps running, it labels the group of a step you marked "Done, moves on when the run's other steps finish" or "Failed, applies when the run's other steps finish". It also turns off **Send** for the step while another step of the run is running. To move the group on at once, pause and resume the run, which interrupts the other steps.

### A safety check that did not decide can look like one that passed

With the **Approve for me** profile, Kiro's safety check judges each tool request that no rule settles. When the check fails or times out, or keeps a request with a person on purpose, such as a write to a sensitive path, kiro-cli asks you with nothing to show that the check did not clear the request. In a chat, the request looks like any other approval request, and its card gives no reason from the safety check.

Reported as [kirodotdev/Kiro#12314](https://github.com/kirodotdev/Kiro/issues/12314), open. With **Approve tool requests in scheduled and triggered runs instead of refusing them** turned on, a scheduled or triggered run that started under **Approve for me** refuses, after 3 minutes, every tool request the safety check did not clear, whether kiro-cli marked it or not. The schedule's row says Kiro's safety check asked a person to approve the tool with nobody watching. The other profiles send no request to the safety check, so runs under them are not affected.

## Agent terminals

### Stopping a reply does not stop its command

When you stop a reply while the agent runs a command, kiro-cli ends the reply. The command carries on until its timeout, which can be up to 30 minutes.

Reported as [kirodotdev/Kiro#11819](https://github.com/kirodotdev/Kiro/issues/11819), open. When you stop a reply, marotte also stops the commands that reply started.

## Fixed upstream, workaround still in marotte

### A message sent just after a stop can run out of view

Before kiro-cli 2.33.1, when you stopped a reply while workflow messages waited for the agent, kiro-cli started a reply of its own to pass them on. A message that reached kiro-cli while that reply was still starting could run with none of its output in the chat. What it did still happened, such as a message to a workflow step or a new workflow, and the agent forgot it afterwards.

Reported as [kirodotdev/Kiro#12011](https://github.com/kirodotdev/Kiro/issues/12011), closed as fixed in kiro-cli 2.33.1. marotte still waits to send your next message while kiro-cli reports the chat busy, including while kiro-cli is passing the waiting workflow messages on. A message you send after a stop therefore waits if that busy report has arrived.

### A message from a workflow step can show as Cleared instead of delivered or dropped

kiro-cli can drop messages a workflow step sent to the chat before the agent reads them, for example when a reply ends. Before kiro-cli 3.1.0 it named the dropped messages by an id it never gave marotte, so marotte could not tell which of them were dropped.

Reported as [kirodotdev/Kiro#12209](https://github.com/kirodotdev/Kiro/issues/12209), after [kirodotdev/Kiro#12012](https://github.com/kirodotdev/Kiro/issues/12012) was closed automatically as a duplicate. kiro-cli 3.1.0 sends that id with each workflow message, and marotte matches on it: a message kiro-cli dropped at the end of a reply reads "not delivered", and a message it sent to the agent again after you stopped a reply reads delivered once that new reply starts. A message still shows as "Cleared", which reads "the agent's queue was emptied before this was confirmed read", in two cases: kiro-cli sent it without an id (a chat from before kiro-cli 3.1.0, or a message kiro-cli failed to save), or you stopped a reply and kiro-cli did not send the message again in the reply after. A message sent to a kiro-cli session the chat no longer uses keeps saying "not delivered yet".
