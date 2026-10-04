# Features

This page describes each part of the marotte page. Everything here works from any device that opens the same server.

## Chat

Conversations stay in sync across devices, so a chat open on a phone and a desktop shows the same messages. Replies stream in as formatted text, and the agent's reasoning shows in blocks you can fold.

- Send a message while the agent is working and it joins the running turn.
- Start a message with `!` to run a shell command.
- `/compact` compacts the context now. `/drop` ends a turn that is stuck. `/goal` sets an objective the agent works toward across turns, until it meets the goal or runs out of attempts.
- Attach files by drag and drop, paste or the composer's `+` menu. PDF, CSV and Office documents reach the agent as documents and images as images. Any other file reaches it as a path it opens with its file tools.

You can also search inside a chat, export a chat, search across chats, and browse past conversations and workflow runs in **History**. Settings decides how long closed chats are kept.

## Agent control

- Switch modes: Default, Spec, Quick Spec, Bug Fix, Plan and Autonomous, plus your own agents from `.kiro/agents/` in the workspace.
- Switch models during a conversation, and set reasoning effort from low to max.
- Answer permission requests, the agent's questions and the forms an MCP server asks you to fill in.
- Fork a tangent, a new chat that starts with this chat's context and leaves the original untouched.

Work handed to a subagent shows as a card you can open on its own page. The agent's own terminals show their output on the tool card that started them.

## Files, editor and terminal

- A file browser, and a search over file contents with include and exclude patterns.
- An editor with syntax highlighting. A link can point at one line with `#L<n>`.
- A preview tab that shows an HTML page from the workspace in a sandbox, at phone, tablet, desktop or full width. The **Open preview** button in the editor and in the file browser opens one for an `.html` file, and the tab reloads when the page's files change.
- Diffs against the last save or against git `HEAD`.
- A merge-conflict view that takes ours, theirs or both for each hunk.
- A terminal, built on [web-terminal-engine](https://github.com/cplieger/web-terminal-engine), that survives sleep and network drops.

## Git and forges

marotte works with GitHub, GitLab, Codeberg, and Gitea or Forgejo. You can stage, commit, diff and switch branches, and list, create, merge and close pull requests. The agent can write commit messages and pull request descriptions. Accounts connect with a sign-in code or a token, and the work runs through the `gh`, `glab` and `tea` command-line tools.

## Reviewing the agent's edits

- Rewind sends the conversation back to an earlier message of yours. The agent's file edits roll back with it, from snapshots kiro-cli takes separately from git.
- Supervised mode holds a whole turn for review instead of asking for each write. One approval lists every file the turn touched, renames and deletes included, and you keep or discard each one.
- The permissions page has a policy editor that sets allow, deny or ask for each capability, with path rules, and a box that explains what the policy would decide. One policy governs every tool call, shell commands included.

Rewind and supervised review cover the agent's file-editing tool. Changes made through shell commands or terminals still go through the policy, but nothing snapshots them, so use git for those. A held write is already on disk while you review it, so a file watcher or a dev server sees it before you decide.

## MCP servers

Add, edit and remove servers, local ones or remote ones over HTTP or SSE. Each server has its own auto-approve setting, reconnects live, and lets you browse its prompts and resources. For a server that needs OAuth, marotte registers itself automatically or takes a client ID and secret you already have, then shows a sign-in link.

## Tools

The **Tools** tab in **Settings** installs runtimes, language servers and command-line tools from a catalog of about 700, built from the mise and aqua registries by [tool-catalog](https://github.com/cplieger/tool-catalog). Installs run in the background. A tool can be pinned to a version. For a tool outside the catalog, install it from the terminal. Debian packages are covered in [OS packages](os-packages.md).

Turning on a language server also turns on kiro-cli [code intelligence](https://kiro.dev/docs/cli/code-intelligence/) for the workspace. The agent then gets navigation, rename and error checking from the language server, in live chats too, with no restart. The first activation saves the detected languages to `/workspace/.kiro/settings/lsp.json`. After you add a language to the workspace, delete that file, and marotte writes it again on the next start.

## Workspace configuration

The `/docs` page lists every steering doc, skill, agent, spec and hook under `.kiro`, with its header fields. It is also where you turn hooks on and off, and start, pause, resume, cancel or schedule workflow runs.

Instructions for every chat and knowledge bases are under **Custom instructions** in **Settings**, and **Settings** also has a diagnostics report you can copy. The sidebar shows your account's usage and switches between light, dark and system themes. The prompt bar shows the current chat's context use and credits. Open tabs follow you to every device, while each device keeps its own active tab, shell panel height and sidebar width.

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

The turn, pull request and workflow run notifications each have a switch on the **General** tab in **Settings**. Turns and workflow runs are on by default. Pull requests are off by default, because the forge already shows their checks. The permission notice has no switch, because nothing else tells you off-screen that a turn is waiting on you.

A device still receiving the live stream gets no notification. A request raised within about 45 seconds of locking a phone is held and sent once the stream goes quiet. It is dropped if someone answers it elsewhere or its time runs out first.
