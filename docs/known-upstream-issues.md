# Known upstream issues

This page lists the upstream issues that affect marotte and is updated when one is reported, fixed or worked around. Each entry says what you see, links the upstream report with its state, and says what marotte does about it.

## Agent engine

### Chats in modes other than Default wait for each other

Only chats in Default mode run their replies side by side. A reply in Default v2, Plan, Autonomous, a spec mode or an agent waits while any other reply outside Default mode runs, in any chat. The chat shows it is working, and the wait can last minutes. A workflow step woken by a message joins the same wait. If Kiro routes your Default chats to Default v2 for your account, they wait too, and marotte cannot tell.

Reported as [kirodotdev/Kiro#11995](https://github.com/kirodotdev/Kiro/issues/11995), open. marotte has no workaround. Try Default mode for chats you want to run side by side.

### Every reply stops at once while the engine is busy

When the agent engine takes more than 2 seconds to read one large message from marotte, kiro-cli stops the engine. Every running reply stops and is marked interrupted, and a banner says the agent engine is restarting.

Reported as [kirodotdev/Kiro#11956](https://github.com/kirodotdev/Kiro/issues/11956), open. marotte starts the engine again and resumes the running workflows, as [How marotte works](how-it-works.md#the-agent-engine) describes for any engine stop. The slow file search in the next entry can make it more likely. Linux also makes it more likely once an account has used up its pipe budget, because new pipes then get 8 KiB. Running `sysctl -w fs.pipe-user-pages-soft=0` on the host lifts that budget for every account on the host.

### The agent stalls during a file search

kiro-cli lists the workspace with ripgrep when the agent searches for files. When ripgrep fails, kiro-cli falls back to a slow listing with no time limit. Every chat and workflow waits while it runs, sometimes for minutes. ripgrep fails on a dangling symbolic link or a link loop anywhere in the workspace. It also fails on a tree too large to list in 30 seconds.

Reported as [kirodotdev/Kiro#11957](https://github.com/kirodotdev/Kiro/issues/11957), open. marotte has no workaround. Remove dangling links and link loops under `/workspace`, which `find /workspace -xtype l` lists, and keep very large trees out of it.

### A reply hangs after a failed compaction

When an automatic compaction fails, the reply can stay open with nothing more arriving.

Reported in the Kiro Crew tracker as [kirodotdev/KiroCrew#3583](https://github.com/kirodotdev/KiroCrew/issues/3583), closed after Kiro Crew added a time limit in its own client. kiro-cli itself still leaves the reply open. After a failed compaction, marotte ends a reply that shows no new activity for 60 seconds and marks it interrupted.

## Chats

### A chat is named with a sentence that is not a title

kiro-cli names a new chat from a model reply, and that reply is sometimes a question back, such as a request for more context.

Reported as [kirodotdev/Kiro#11343](https://github.com/kirodotdev/Kiro/issues/11343), open. kiro-cli 2.21.4 fixed most cases. marotte refuses a name that is cut off, holds more than one sentence, or runs over 80 characters or 12 words. A short one-sentence reply can still get through. Rename the chat with **Rename chat** in the composer's **Chat options** menu.

### A message sent after deleting a waiting one does not reach the reply

While a reply runs, deleting a message the agent has not read yet can leave the messages you send next unread until the reply ends.

Reported as [kirodotdev/Kiro#11449](https://github.com/kirodotdev/Kiro/issues/11449), open. When you delete one waiting message, marotte clears them all and sends the ones you kept again as one message. It reaches the running reply when the agent has read none of your messages in that reply yet. Otherwise it waits, and marotte sends it as your next message once the reply ends.

### A message sent just after a stop can run out of view

When you stop a reply while workflow messages wait for the agent, kiro-cli starts a reply of its own to pass them on. A message that reaches kiro-cli while that reply is still starting can run with none of its output in the chat. What it does still happens, such as a message to a workflow step or a new workflow, and the agent forgets it afterwards.

Reported as [kirodotdev/Kiro#12011](https://github.com/kirodotdev/Kiro/issues/12011), open. marotte waits to send your next message while kiro-cli reports the chat busy, including while kiro-cli is passing the waiting workflow messages on. A message you send after a stop therefore waits if that busy report has arrived, but marotte cannot close the narrow gap before it arrives.

### A message sent during a reply reaches its subagents

The agent can hand work to subagents in a chat with **Workflows** switched off in Settings, in Plan, Autonomous and the spec modes whatever that switch says, and inside a workflow step. A message you send while it works then reaches every subagent that runs later in that reply. A subagent can stop its own task to answer it.

Reported as [kirodotdev/Kiro#11240](https://github.com/kirodotdev/Kiro/issues/11240), open. marotte has no workaround. With **Workflows** on, the default, a chat in any other mode hands work to workflows instead of subagents, and this does not happen. In the cases above, wait for the reply to finish before you send guidance meant only for the main agent.

### A failed hook shows as completed

A hook whose command fails, or whose command does not exist, shows as completed on its card.

Reported as [kirodotdev/Kiro#11369](https://github.com/kirodotdev/Kiro/issues/11369), open. kiro-cli reports every hook command as completed, and marotte shows what it reports, so it cannot tell a failure today. It will turn the card red if a future kiro-cli release sends a failed status. To check a hook, run its command in the terminal.

## Skills

### A skill with a long description or an underscore in its name never loads

kiro-cli drops a skill whose description is over 1,024 characters, or whose name has an underscore. The skill is missing from chats, and the agent cannot use it by name. Nothing says why.

Reported as [kirodotdev/Kiro#11101](https://github.com/kirodotdev/Kiro/issues/11101), open, and [kirodotdev/Kiro#10678](https://github.com/kirodotdev/Kiro/issues/10678), open. A skill created with **+** on the `/docs` page follows both rules. For a skill you edit by hand, keep the description at 1,024 characters or less and the name to 64 characters or less of lowercase letters, numbers and single hyphens.

## Workflows

### An answered step inside a parallel group keeps showing as waiting

A step that runs beside other steps can ask a question. After it gets its answer, it keeps showing as paused until every other step in its group stops, even after it has finished its work.

Reported as [kirodotdev/Kiro#11733](https://github.com/kirodotdev/Kiro/issues/11733), open. kiro-cli refuses an answer while another step of the run is running, so marotte turns off **Send** on the run's tab until then.

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

## Agent terminals

### Stopping a reply does not stop its command

When you stop a reply while the agent runs a command, kiro-cli ends the reply. The command carries on until its timeout, which can be up to 30 minutes.

Reported as [kirodotdev/Kiro#11819](https://github.com/kirodotdev/Kiro/issues/11819), open. When you stop a reply, marotte also stops the commands that reply started.
