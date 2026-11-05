# Known upstream issues

This page lists the upstream issues that affect marotte and is updated when one is reported, fixed or worked around. Each entry says what you see, links the upstream report with its state, and says what marotte does about it.

## Agent engine

### Chats in Plan, Autonomous, a spec mode or an agent wait for each other

Chats in Default and Default v2 run their replies side by side. A reply in Plan, Autonomous, a spec mode or an agent waits while any other such reply runs, in any chat. The waiting chat says which chat it waits for, and the wait can last minutes. A workflow step woken by a message can join the same wait.

Reported as [kirodotdev/Kiro#11995](https://github.com/kirodotdev/Kiro/issues/11995), open. kiro-cli 2.37.0 lets Default v2 replies run side by side, and the other modes still wait. marotte has no workaround. Use Default or Default v2 for chats you want to run side by side.

### The agent stalls during a file search

kiro-cli runs its own ripgrep when the agent searches the workspace. When ripgrep fails, kiro-cli falls back to a slow listing with no time limit, and every chat and workflow its engine hosts waits while it runs, sometimes for over an hour. ripgrep fails on a dangling symbolic link or a link loop anywhere in the workspace, and on a tree too large to search in 30 seconds.

Reported as [kirodotdev/Kiro#11957](https://github.com/kirodotdev/Kiro/issues/11957), open. marotte runs the agent's `grep_search` and `file_search` itself, so kiro-cli's own search and its fallback never run. marotte searches the workspace and the additional directories with ripgrep, honours `.gitignore`, never follows symbolic links, and keeps ripgrep's results when it skips an unreadable path. A search that names a folder, such as `marotte/internal/**/*.go`, reads only that folder. Each search ends after 15 seconds, or when you stop the reply, and the agent receives what was found by then: at most 100 files for a text search.

### A reply hangs after a failed compaction

When an automatic compaction fails, the reply can stay open with nothing more arriving.

Reported in the Kiro Crew tracker as [kirodotdev/KiroCrew#3583](https://github.com/kirodotdev/KiroCrew/issues/3583), closed after Kiro Crew added a time limit in its own client. kiro-cli itself still leaves the reply open. After a failed compaction, marotte ends a reply that shows no new activity for 60 seconds and marks it interrupted.

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

### A message sent just after a stop can run out of view

When you stop a reply while workflow messages wait for the agent, kiro-cli starts a reply of its own to pass them on. A message that reaches kiro-cli while that reply is still starting can run with none of its output in the chat. What it does still happens, such as a message to a workflow step or a new workflow, and the agent forgets it afterwards.

Reported as [kirodotdev/Kiro#12011](https://github.com/kirodotdev/Kiro/issues/12011), open. marotte waits to send your next message while kiro-cli reports the chat busy, including while kiro-cli is passing the waiting workflow messages on. A message you send after a stop therefore waits if that busy report has arrived, but marotte cannot close the narrow gap before it arrives.

### A message sent during a reply reaches its subagents

The agent can hand work to subagents in a chat with **Workflows** switched off in Settings, in Plan, Autonomous and the spec modes whatever that switch says, and inside a workflow step. A message you send while it works then reaches every subagent that runs later in that reply. A subagent can stop its own task to answer it.

Reported as [kirodotdev/Kiro#11240](https://github.com/kirodotdev/Kiro/issues/11240), open. marotte has no workaround. With **Workflows** on, the default, a chat in any other mode has no subagent tool, and this does not happen. In the cases above, wait for the reply to finish before you send guidance meant only for the main agent.

### A failed hook shows as completed, and what a hook prints never shows

A hook whose command fails, or whose command does not exist, shows as completed on its card. Anything a hook's command prints, such as instructions a `PostToolUse` hook gives the agent, is missing from the chat, so that hook counts as a passing hook.

Reported as [kirodotdev/Kiro#11369](https://github.com/kirodotdev/Kiro/issues/11369), open. kiro-cli reports every hook command as completed and sends neither its output nor its exit code. marotte shows what it reports, so it cannot tell a failure or show the output today. It will turn the card red on a failed status or a non-zero exit code, and show the output as its own row, once a kiro-cli release sends them. To check a hook, run its command in the terminal.

### A hook shows on its tool group, not on the call it checked

A hook that checks a tool call, such as a guard that runs before every command, shows as a count on its tool group's header rather than on the command it ran for. Selecting the count opens a list of those hooks, each with how often it ran and when it fires. When a call in the group is refused or fails, every hook in the group stays a row, because marotte cannot tell which hook refused it.

Reported as [kirodotdev/Kiro#12284](https://github.com/kirodotdev/Kiro/issues/12284), open. kiro-cli's report of a hook names neither the tool call it ran for nor whether it blocked that call. marotte will show each hook on its own call once kiro-cli names the call.

## Skills

### A skill with a long description or an underscore in its name never loads

kiro-cli drops a skill whose description is over 1,024 characters, or whose name has an underscore. The skill is missing from chats, and the agent cannot use it by name. Nothing says why.

Reported as [kirodotdev/Kiro#11101](https://github.com/kirodotdev/Kiro/issues/11101), open, and [kirodotdev/Kiro#10678](https://github.com/kirodotdev/Kiro/issues/10678), open. A skill created with **+** on the `/docs` page follows both rules. For a skill you edit by hand, keep the description at 1,024 characters or less and the name to 64 characters or less of lowercase letters, numbers and single hyphens.

## Workflows

### An answer to a waiting step can become an ordinary message

kiro-cli can take an answer to a step's question as an ordinary message to the step. The step reads the words, but the run keeps showing the question as open.

Reported as [kirodotdev/Kiro#11863](https://github.com/kirodotdev/Kiro/issues/11863), open. marotte sends an answer only while kiro-cli is ready to take it, and keeps the question marked as answered until the step starts again. When the step has not started within 10 seconds, the question can be answered again.

### A workflow result can be lost when a reply ends

When a step reports or a run finishes while the chat that launched it is replying, the result waits for the agent to read it. When the reply ends with an error, or kiro-cli stops carrying unread messages into it, before the agent reads the result, the agent never gets it and keeps waiting for it.

Reported as [kirodotdev/Kiro#11857](https://github.com/kirodotdev/Kiro/issues/11857), open. marotte labels the lost result "Workflow result not delivered" in the chat. While a workflow result waits beside your unread messages, marotte deletes them only all together, and that drops the result too. Ask the agent to check the run when it keeps waiting.

### A message from a workflow step can show as Cleared instead of delivered or dropped

kiro-cli can drop messages a workflow step sent to the chat before the agent reads them, for example when a reply ends. It names the dropped messages by an id it never gave marotte, so marotte cannot always tell which of the messages it shows were dropped.

Reported as [kirodotdev/Kiro#12012](https://github.com/kirodotdev/Kiro/issues/12012), open. Until kiro-cli sends that id, marotte marks a message "delivered" only when it sees the agent read it or sees the turn the message woke. When kiro-cli empties its buffer, at the end of a reply or when you stop one, marotte marks every message it could not confirm as "Cleared", which reads "the agent's queue was emptied before this was confirmed read": kiro-cli may have sent it to the agent again or dropped it, and marotte cannot tell which. A message sent to a kiro-cli session the chat no longer uses keeps saying "not delivered yet".

### A workflow step fails while marotte is signed out

When a step needs the Kiro account while marotte is not signed in, the step fails instead of waiting. This can happen when runs resume after a container restart, before you sign in. Signing in does not bring the run back.

Reported as [kirodotdev/Kiro#11978](https://github.com/kirodotdev/Kiro/issues/11978), open. marotte has no workaround. After you sign in, **Retry failed steps** on the run's tab starts the failed step over from the beginning.

### A watch step fails although its command succeeded

While the host is busy, a step that polls a command can fail even though the command printed a valid result. The error reads `exited 0 but its stdout is not a valid WatchPollResult`.

Reported as [kirodotdev/Kiro#11985](https://github.com/kirodotdev/Kiro/issues/11985), open. marotte has no workaround. **Retry failed steps** on the run's tab starts the watch again.

### A run paused for a model throttle can read "Workflow paused by user."

kiro-cli accepts a request to pause a workflow run only on behalf of the user, and its run details do not say who paused a run. Any pause sent to a run kiro-cli paused for a model throttle therefore reads "Workflow paused by user.", though you paused nothing.

Reported as [kirodotdev/Kiro#12274](https://github.com/kirodotdev/Kiro/issues/12274), open. When marotte stops resuming a run after repeated throttling, it sends no pause and records the stop itself: the run's tab reads "marotte stopped resuming this run after repeated model throttling" beside **Resume**, a **Workflow runs** notification says so, and the agent that started the run reads it on its next turn. While your Kiro sign-in cannot be answered, marotte still pauses running runs, and that pause must name you, since kiro-cli accepts nothing else. The run's tab shows marotte's sign-in reason for it instead of saying you paused it.

## Agent terminals

### Stopping a reply does not stop its command

When you stop a reply while the agent runs a command, kiro-cli ends the reply. The command carries on until its timeout, which can be up to 30 minutes.

Reported as [kirodotdev/Kiro#11819](https://github.com/kirodotdev/Kiro/issues/11819), open. When you stop a reply, marotte also stops the commands that reply started.

## Fixed upstream, workaround still in marotte

### An answered step inside a parallel group keeps showing as waiting

kiro-cli 2.37.0 finishes a step that runs beside other steps as soon as the step reports that it is done, while the other steps keep running. Earlier releases kept such a step showing as paused until every other step in its group stopped.

Reported as [kirodotdev/Kiro#11733](https://github.com/kirodotdev/Kiro/issues/11733), closed. marotte still turns off **Send** on the run's tab while another step of the run is running.
