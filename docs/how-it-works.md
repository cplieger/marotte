# How marotte works

This page explains how chats stay in sync, how marotte talks to kiro-cli, and how kiro-cli is installed and repaired. Read it when something behaves unexpectedly or before you change the volumes.

## One copy of each chat, on the server

marotte runs one `kiro-cli-chat acp` program, the agent engine, and each open chat is a session inside it. marotte talks to it over the Agent Client Protocol (ACP), the same protocol code editors use to drive coding agents. Several tabs or devices on the same chat share that chat's session.

The server keeps the only copy of each chat, under `/config/chats/`. Every action goes to the server first, is saved, and is then sent back to every connected page over a live stream (Server-Sent Events). A page shows a message only after the server has saved it. That is why a chat open on a phone and a desktop shows the same thing, and why a reconnecting page catches up instead of guessing.

## The agent engine

One engine serves every chat, every workflow run and the short sessions marotte opens for its own reads, such as the permissions page. A new or reopened chat opens a session on the running engine instead of starting a program. When the engine needs the Kiro account, it asks marotte, which answers from its own sign-in (see [Features](features.md#kiro-account)).

A few settings apply only when the engine restarts. Their rows on the **Settings** page say "Applies after the agent engine restarts", and after a change a banner says so too. The engine then restarts by itself as soon as no reply and no workflow is running. **Restart now**, in the banner and under **Agent engine** in **Settings**, restarts it at once. Its confirmation names the chats whose reply would stop and the workflows that would pause. Running replies stop and are marked interrupted, open permission cards close, and the workflows resume by themselves once the engine is back. A newly activated kiro-cli version, and a few account switches, wait for a restart the same way.

If the engine stops on its own, every running reply in every chat stops at once and is marked interrupted. A banner says the agent engine is restarting, marotte starts it again, waiting longer after each failed start, up to a minute, and the running workflows resume once it is back. After any restart, each open chat's next message first waits while marotte reloads that chat's session, as when you reopen a chat.

## How kiro-cli is installed

kiro-cli is not built into the image. On first start the server downloads the version the image pins, about 530 MB, and checks it against the SHA-256 checksum for your architecture. It installs it under `/config/tools/kiro-cli-versions/<version>/`. The server starts listening before the download, so the page works while it runs and chats wait.

On every start, marotte runs the installed kiro-cli to confirm it is the expected version, and switches off kiro-cli's own self-update. A replaced or half-restored install is refused rather than run. The previous version stays on the volume as the fallback for a broken new one, and older versions are removed.

`docker exec marotte kiro-cli --version` keeps working, because `/config/tools/bin/kiro-cli` and `/config/tools/bin/kiro-cli-chat` point at the active version. kiro-cli finds its chat helper through the same directory.

## Health and repair

`GET /api/health` reports healthy once the server is up and the pinned kiro-cli is installed, runs at that exact version, and has its self-update switched off. Anything short of that answers `503` with a reason:

| Reason | Meaning |
| --- | --- |
| `kiro-cli installing` | The download or install is running. |
| `kiro-cli install retrying` | An attempt failed and the next one is waiting. |
| `kiro-cli unavailable` | Four attempts failed. A container restart starts again. |
| `kiro-cli required settings not enforced` | marotte could not switch off kiro-cli's self-update. |
| `sign-in required` | A chat's last turn failed because marotte is not signed in to Kiro, or Kiro refused the sign-in. The next turn that succeeds clears it. |

Attempts wait 30 seconds, then double each time up to 10 minutes. The page shows a banner in every one of these states and only chats wait. The `sign-in required` banner has a **Sign in** button.

To repair an install by hand, fix `/config/tools/kiro-cli-versions` inside the container, then run `curl -X POST localhost:9847/api/kiro-cli/rescan` from inside the container. marotte picks it up without a container restart. When the rescan activates another version, the agent engine moves to it at its next restart. The command works only from inside the container.

## Language servers and code intelligence

Turning on a language server in **Settings** also turns on kiro-cli code intelligence for the workspace, in live chats too. The first activation saves the detected languages to `/workspace/.kiro/settings/lsp.json`. After you add a language to the workspace, delete that file, and marotte writes it again on the next start.

## Leftover processes

The agent's commands start programs such as git, language servers and build tools, and some of those leave child processes behind when they exit. Those leftovers are handed to the container's first process. The image makes that first process tini, a small init program that cleans them up, so finished processes cannot pile up until the container is unable to start new ones. Because the image already runs tini, the compose example needs no `init: true` line of its own.
