# Features

This page describes each part of the marotte page. Everything here works from any device that opens the same server.

## Chat

Conversations stay in sync across devices, so a chat open on a phone and a desktop shows the same messages. Replies stream in as formatted text, and the agent's reasoning shows in blocks you can fold.

- Send a message while the agent is working and it joins the running turn, or with **Queue messages** switched on in the composer's **Chat options** menu, runs after it. Queued messages send one per turn, in order, and each row above the composer says how many turns it waits. On a touch screen, Return adds a new line, so send it with the **Steer** or **Queue** button beside Cancel.
- Start a message with `!` to run a shell command.
- `/compact` compacts the context now. `/drop` ends a turn that is stuck. `/goal` sets an objective the agent works toward across turns, until it meets the goal or runs out of attempts.
- A Mermaid diagram in a reply (a fenced `mermaid` block) is drawn as a picture. Its buttons switch to the source, copy it, download the SVG, or open the diagram full screen, where a pinch, the mouse wheel or the zoom buttons enlarge it. A diagram that cannot be drawn shows its source with a note saying why.
- Attach files by drag and drop, paste or the composer's **Chat options** menu. PDF, CSV and Office documents reach the agent as documents and images as images. Any other file reaches it as a path it opens with its file tools.

You can also search inside a chat, export a chat, search across chats, and browse past conversations and workflow runs in **History**. Settings decides how long closed chats are kept.

## Agent control

- Switch modes: Default, Spec, Quick Spec, Bug Fix, Plan and Autonomous, or the bundled Semantic Reviewer agent, plus your own agents from `.kiro/agents/` in the workspace or in your home folder.
- Switch models during a conversation, and set reasoning effort from low to max.
- Answer permission requests, the agent's questions and the forms an MCP server asks you to fill in.
- Fork a tangent, a new chat that starts with this chat's context and leaves the original untouched. **Merge into parent chat** in the composer's **Chat options** menu, or `/tangent merge`, sends a summary of the tangent's findings back to the chat it came from.

Pressing Stop on a reply answers that reply's open permission requests as cancelled, and their cards close as part of the stop. A card from a workflow step running in the same chat stays open, because Stop does not stop the workflow.

The **Workflows** switch in Settings decides how the agent hands work off. On: the agent hands work to workflows that run in the background while you keep chatting. Off: it uses subagents instead and waits for each to finish before it continues. Either kind of handed-off work shows as a card you can open on its own page. A question a workflow step asks goes to the agent that launched the workflow, not to you: the chat shows that the run is waiting for an answer, and to answer the step yourself, open the run's tab. A step's permission requests still come to you in the chat. The agent's own terminals show their output on the tool card that started them.

## Kiro account

The agent uses the Kiro account you sign in with on the page, with AWS Builder ID or IAM Identity Center: marotte shows a code, and you approve it in your browser. The sign-in is marotte's own. marotte never reads the sign-in of a `kiro-cli` you start in the terminal, and the two can be different accounts. If you used marotte before it kept its own sign-in, sign in once more after updating.

An IAM Identity Center account needs a Kiro profile, and without one the sign-in says "This account has no Kiro profile marotte can use". A Builder ID account signs in with or without one. When a sign-in fails after the browser step, the dialog says why instead of waiting. marotte reports the account's class the way kiro-cli does, so an organization's governance applies as it does in kiro-cli.

The account menu shows the sign-in method, start URL and region, and the account's email when Kiro's account service returns one at sign-in.

Signing in as another account reaches every open chat and running workflow with no restart. kiro-cli asks marotte for the account again at its first model call a minute or more after its last ask. That call can still use the previous account while kiro-cli renews in the background, and the call after it uses the new one. A call made after the previous account's token expired, about four minutes after the last ask, waits for the renewal and uses the new account.

Two kinds of switch wait for the agent engine to restart, because kiro-cli would keep part of the old account: a switch from an account with a Kiro profile to one without, and a switch between account classes, such as Builder ID to IAM Identity Center. The restart banner then says "The new Kiro account applies after the agent engine restarts". Until the restart, chats keep using the previous account whole, and signing in to the previous account again clears the banner.

Signing out stops the account within about four minutes, because kiro-cli keeps a token it already holds until it expires. **Restart now** under **Agent engine** in **Settings** stops it at once. If marotte cannot remove the saved sign-in, signing out says so and you stay signed in.

## The agent engine

One agent engine runs every chat and workflow. **Agent engine** on the **General** tab in **Settings** holds **Restart now** and **Agent engine memory limit**, which caps the engine's JavaScript heap and is empty by default, leaving kiro-cli's own limit of about 4 GB.

These settings apply only when the engine restarts, and their rows say so: **Knowledge base search**, **Model fallback**, **Steering reminders**, **Auto model routing**, **Smart helper models**, **AWS CloudFormation safety check**, **kiro-cli telemetry** and **Agent engine memory limit**. After a change, a banner names them, and the engine restarts by itself as soon as no reply and no workflow is running. **Restart now**, in the banner or in **Settings**, asks first and names the chats whose reply would stop and the workflows that would pause. Those workflows resume by themselves once the engine is back. [How marotte works](how-it-works.md#the-agent-engine) describes restarts and crashes.

## Files, editor and terminal

- A file browser with a search that finds files by name, or inside files when you ask. One Files filter narrows either search (`*.css`, `src/**`, `!node_modules`), and files your `.gitignore` excludes stay out unless you include them.
- An editor with syntax highlighting. A link can point at one line with `#L<n>`.
- A preview tab that shows an HTML page from the workspace in a sandbox, at phone, tablet, desktop or full width. Clicking an `.html` or `.htm` page that sits in its own folder in the file browser opens it there, and so does a link to one in the chat. **Edit** in the tab's bottom bar opens the page's source in the editor, and the editor's **Open preview** button goes back to the page. The tab reloads when the page's files change.
- Diffs against the last save or against git `HEAD`.
- A merge-conflict view that takes ours, theirs or both for each hunk.
- A terminal, built on [web-terminal-engine](https://github.com/cplieger/web-terminal-engine), that survives sleep and network drops.

## Git and forges

marotte works with GitHub, GitLab, Codeberg, and Gitea or Forgejo. You can stage, commit, diff and switch branches, and list, create, merge and close pull requests. A new pull request's description starts as the list of the branch's commits, which you can edit before you open it.

An account connects by signing in through your browser on GitHub or GitLab, or with an access token on any of them. marotte keeps the credential in its own store, uses it for the forge's API, and answers git over HTTPS with it through its own credential helper, so no forge command-line tool is needed. SSH remotes are not covered. A forge on a private or loopback address, such as one on your own network, needs **Allow private addresses** under **Connection options** when you connect it.

A gitlab.com sign-in token lasts two hours. marotte renews it with the refresh token GitLab issues with it, so the account stays connected. If GitLab issued no refresh token or refuses a renewal, the account shows **Reconnect** once the token expires.

marotte checks each connected account in the background about once a minute, whether or not a page is open. A network failure, a rate limit or a forge outage does not disconnect the account. Sources shows a note under it, and the note goes once a later check succeeds.

When the forge rejects the token, the account shows **Reconnect**, and only a new sign-in brings it back. When the forge refuses the account instead, for example for a missing permission or an IP allowlist, the account shows the forge's reason and **Check**. Press **Check** once you have fixed it on the forge.

In both cases marotte stops giving git that account's credential, so git uses your other credential helpers or asks you. Both states survive a restart, and the **Pull requests** tab lists the account with what to do. After a restart a refused account shows a general reason in place of the forge's, and **Check** asks the forge again.

marotte 0.15.0 does not keep the forge accounts that 0.14 or earlier saved. After that update, Sources lists no accounts. Add and sign in to each one again, including the address and **Connection options** of a self-hosted forge.

Signing in to GitHub Enterprise or a self-managed GitLab uses an OAuth application registered on that server, and the connect form asks for its client ID. On GitLab, an administrator registers it on the **Applications** page. Select the `api` scope, enter any HTTPS redirect URI, leave **Confidential** unticked and tick **Device authorization grant**. Without that last box, GitLab refuses the sign-in.

## Reviewing the agent's edits

- Rewind sends the conversation back to an earlier message of yours. The agent's file edits roll back with it, from snapshots kiro-cli takes separately from git.
- Supervised mode holds a whole turn for review instead of asking for each write. One approval lists every file the turn touched, renames and deletes included, and you keep or discard each one.
- The permissions page has a policy editor that sets allow, deny or ask for each capability, with path rules, and a box that explains what the policy would decide. One policy governs every tool call, shell commands included.

Rewind and supervised review cover the agent's file-editing tool. Changes made through shell commands or terminals still go through the policy, but nothing snapshots them, so use git for those. A held write is already on disk while you review it, so a file watcher or a dev server sees it before you decide.

## MCP servers

Add, edit and remove servers, local ones or remote ones over HTTP or SSE. Each server can block individual tools, reconnects live, and lets you browse its prompts and resources. For a server that needs OAuth, marotte registers itself automatically or takes a client ID you already have, then shows a sign-in link. The sign-in is kept for workflow steps too, so a step does not ask again on every run. A server that kiro-cli loaded but another layer replaced or switched off shows the same badge as a steering doc, and a server that failed to start shows its error on its card.

marotte writes this list into kiro-cli's `~/.kiro/settings/mcp.json`. If anything else changes the servers there, marotte puts them back within 30 seconds, or when a chat opens, and logs a warning naming them. Edit servers here, not in that file and not with `kiro-cli mcp add`. Switching a server on that is already on puts it back in that file if it was missing or changed.

## Tools

The **Tools** tab in **Settings** installs runtimes, language servers and command-line tools from a catalog of about 900, built from the mise and aqua registries by [tool-catalog](https://github.com/cplieger/tool-catalog). Installs run in the background. A tool can be pinned to a version. For a tool outside the catalog, install it from the terminal. Debian packages are covered in [OS packages](os-packages.md).

Version checks and some installs read GitHub's API. Without an account, GitHub allows 60 of those requests an hour from your address. Connect a github.com account on the **Sources** tab of the git panel, and marotte sends its token with them, which raises the limit. A GitHub Enterprise account is not used for this. When the limit is reached, the **Tools** tab says so, and offers to connect an account when none is connected. GitHub also limits bursts of requests, and an account does not raise that limit. For a burst, the tab gives the time to try again instead.

Turning on a language server also turns on kiro-cli [code intelligence](https://kiro.dev/docs/cli/code-intelligence/) for the workspace. The agent then gets navigation, rename and error checking from the language server, in live chats too, with no restart. The first activation saves the detected languages to `/workspace/.kiro/settings/lsp.json`. After you add a language to the workspace, delete that file, and marotte writes it again on the next start.

## Workspace configuration

The `/docs` page lists every steering doc, skill, agent, spec and hook under `.kiro`, with its header fields. It is also where you turn hooks on and off, and start, pause, resume, cancel or schedule workflow runs. A run's tab has its own composer: send the selected step a message while it runs, or answer its question, and each message shows when it was sent and when the step read it. The **+** at the top right creates a new steering doc, skill, prompt, agent or hook in the workspace's `.kiro`. Steering docs, prompts, agents and hooks start from the template the matching Kiro client writes. A skill has no Kiro template, so it starts from the name and description Kiro's skill format requires. The new file opens in the editor. On **Specs** it starts a spec chat that drafts the requirements, as `/spec new` does in kiro-cli.

A steering doc or agent that kiro-cli loaded but does not use carries a badge naming the layer that decided it: **Replaced by** a layer with an item that replaces it, or **Off by** a layer whose setting switched it off. Its tooltip gives the detail. An agent kiro-cli could not load shows the error on its row, with or without a badge. A row on the **Workflows** panel gets the same badge when kiro-cli reports its workflow as not in use. kiro-cli 2.31 reports no such workflow, so on that version no workflow row shows one.

**Run** opens a form under the workflow with its inputs and a **Model** and **Effort** choice. The choice applies to every step that does not name its own model or effort. **Default** sends neither, so the workflow's own settings apply. A schedule keeps its own choice for every run it starts. When you launch or save, marotte refuses a model or effort level your account does not offer. Without that check, kiro-cli would accept it and fail the step later.

Instructions for every chat and knowledge bases are under **Custom instructions** in **Settings**, and **Settings** also has a diagnostics report you can copy. **Data sharing** on its **General** tab holds the **kiro-cli telemetry** switch, one switch for kiro-cli's usage telemetry and its legacy toolkit telemetry. It is on by default, as in kiro-cli, and an enterprise organization's usage-analytics setting overrides it. The sidebar shows your account's usage and switches between light, dark and system themes. The prompt bar shows the current chat's context use and credits. Open tabs follow you to every device, while each device keeps its own active tab, shell panel height and sidebar width.

## Knowledge bases

The agent can search local folders you index for it under **Knowledge bases** on the **Custom instructions** tab in **Settings**. The field takes a folder path, never a URL, either absolute or relative to `/workspace`. Clone the repository first, then index the folder that holds its markdown.

```sh
git clone --depth 1 https://github.com/rust-lang/book /workspace/refs/rust-book
```

Then add `refs/rust-book/src`. Trees like these index well, because every page is a plain markdown file.

| Project | Folder | Pages |
| --- | --- | --- |
| [rust-lang/book](https://github.com/rust-lang/book) | `src` | 112 |
| [astral-sh/uv](https://github.com/astral-sh/uv) | `docs` | 81 |
| [prometheus/docs](https://github.com/prometheus/docs) | `docs` | 71 |
| [reactjs/react.dev](https://github.com/reactjs/react.dev) | `src/content` | 223 |
| [kubernetes/website](https://github.com/kubernetes/website) | `content/en/docs` | 1709 |

Index a subfolder, not a repository root, so the index skips code and assets. Indexing runs in the background and the row shows a percentage. A few hundred pages take minutes, and a tree the size of [mdn/content](https://github.com/mdn/content), with 14,000 pages, takes far longer.

## Notifications

marotte can be installed as an app from the browser. It then sends notifications when a turn finishes, a pull request's checks settle, a workflow run ends, or the agent needs permission, even with the tab closed. Browsers allow this only over HTTPS or on `localhost`.

The turn, pull request and workflow run notifications each have a switch on the **General** tab in **Settings**. Turns and workflow runs are on by default. Pull requests are off by default, because the forge already shows their checks. Pull request notifications work on GitHub, GitLab, Gitea, Forgejo and Codeberg, once the pull request's CI reports a result. The permission notice has no switch, because nothing else tells you off-screen that a turn is waiting on you.

A device still receiving the live stream gets no push. Its open page shows the notification itself, unless the chat or run it is about is the tab on screen. A request raised within about 30 seconds of locking a phone is held and sent once the stream goes quiet. It is dropped if someone answers it elsewhere or its time runs out first.

The number on the browser tab's title and the installed app's icon counts what wants you: an open question or permission request, a paused workflow run, and finished chats and runs you have not looked at. An open question counts until you answer it, even on the chat you are reading. marotte remembers what you have looked at, so reading a chat on one device clears it on your others. A device with marotte open updates when the server's next update reaches it. A backgrounded or closed app updates the next time you open it. A retried workflow run counts again when it finishes, unless the retry only re-ran terminal watches or the run was launched outside marotte.
