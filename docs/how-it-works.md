# How marotte works

This page explains how chats stay in sync, how marotte talks to kiro-cli, and how kiro-cli is installed and repaired. Read it when something behaves unexpectedly or before you change the volumes.

## One copy of each chat, on the server

marotte runs one `kiro-cli-chat acp` program, the agent engine, and each open chat is a session inside it. marotte talks to it over the Agent Client Protocol (ACP), the same protocol code editors use to drive coding agents. Several tabs or devices on the same chat share that chat's session.

The server keeps the only copy of each chat, under `/config/chats/`. Every action goes to the server first, is saved, and is then sent back to every connected page over a live stream (Server-Sent Events). A page shows a message only after the server has saved it. That is why a chat open on a phone and a desktop shows the same thing, and why a reconnecting page catches up instead of guessing.

## The agent engine

One engine serves every chat, every workflow run and the short sessions marotte opens for its own reads, such as the permissions page. A new or reopened chat opens a session on the running engine instead of starting a program. Closing a tab answers at once: marotte stops the closed chats' replies and releases their sessions in the background, and a chat reopened meanwhile waits for that to finish. When the engine needs the Kiro account, it asks marotte, which answers from its own sign-in (see [Features](features.md#kiro-account)).

A few settings apply only when the engine restarts. Their rows on the **Settings** page say "Applies after the agent engine restarts", and after a change a banner says so too. The engine then restarts by itself as soon as no reply and no workflow is running. **Restart now**, in the banner and under **Agent engine** in **Settings**, restarts it at once. Its confirmation names the chats whose reply would stop and the workflows that would pause. Running replies stop and are marked interrupted, open permission cards close, and the workflows resume by themselves once the engine is back. A newly activated kiro-cli version, and a few account switches, wait for a restart the same way.

If the engine stops on its own, every running reply in every chat stops at once and is marked interrupted. A banner says the agent engine is restarting, marotte starts it again, waiting longer after each failed start, up to a minute, and the running workflows resume once it is back. After any restart, each open chat's next message first waits while marotte reloads that chat's session, as when you reopen a chat.

## How kiro-cli is installed

kiro-cli is not built into the image. On first start the server downloads the version the image pins, about 530 MB, and checks it against the SHA-256 checksum for your architecture. It installs it under `kiro-cli-versions/<version>/` in the tools folder, which is `/config/tools` unless `MAROTTE_TOOLS_DIR` in [Configuration](configuration.md) moves it. The server starts listening before the download, so the page works while it runs and chats wait.

On every start, marotte runs the installed kiro-cli to confirm it is the expected version, and switches off kiro-cli's own self-update. A replaced or half-restored install is refused rather than run. The previous version stays on the volume as the fallback for a broken new one, and older versions are removed.

`kiro-cli` and `kiro-cli-chat` in the tools folder's `bin/` point at the active version, and kiro-cli finds its chat helper through the same directory. A tools folder whose path holds a colon or a refused character gets no install, so chats stay unavailable (`MAROTTE_TOOLS_DIR` in [Configuration](configuration.md)). A login shell, such as the browser **Shell** panel or `docker exec marotte bash -l`, searches it only while `/etc/profile.d/10-marotte-path.sh` holds that start's PATH. marotte writes it at every start when the container runs as root, so `docker exec marotte bash -lc 'kiro-cli --version'` then works for a moved tools folder too. As any other user, the quick start's 1000 included, Debian's `/etc/profile` gives a login shell a PATH without the tools folder, and the log warns about it at every start. When the write fails and the copy an earlier start left already holds this start's PATH, marotte keeps it and logs nothing. Otherwise the log warns, and marotte tries to delete that copy. When the deletion fails too, the log warns again and a login shell may still source the earlier PATH. Either way a login shell may not find the tools folder. Run an installed tool there by its full path, such as `/config/tools/bin/kiro-cli`. A plain `docker exec marotte kiro-cli --version` searches the image's PATH instead, so it finds kiro-cli only while the tools folder is the default `/config/tools`.

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

To repair an install by hand, fix `kiro-cli-versions` in the tools folder (by default `/config/tools`) inside the container, then run `curl -X POST localhost:9847/api/kiro-cli/rescan` from inside the container. marotte picks it up without a container restart. When the rescan activates another version, the agent engine moves to it at its next restart. The command works only from inside the container.

## Language servers and code intelligence

Turning on a language server in **Settings** also turns on kiro-cli code intelligence for the workspace, in live chats too. The first activation saves the detected languages to `/workspace/.kiro/settings/lsp.json`. After you add a language to the workspace, delete that file, and marotte writes it again on the next start.

## Leftover processes

The agent's commands start programs such as git, language servers and build tools, and some of those leave child processes behind when they exit. Those leftovers are handed to the container's first process. The image makes that first process tini, a small init program that cleans them up, so finished processes cannot pile up until the container is unable to start new ones. Because the image already runs tini, the compose example needs no `init: true` line of its own.
