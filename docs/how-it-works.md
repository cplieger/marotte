# How marotte works

This page explains how chats stay in sync, how marotte talks to kiro-cli, and how kiro-cli is installed and repaired. Read it when something behaves unexpectedly or before you change the volumes.

## One copy of each chat, on the server

marotte runs `kiro-cli acp` as a separate program for each open chat. It talks to it over the Agent Client Protocol (ACP), the same protocol code editors use to drive coding agents. Several tabs or devices on the same chat share that one program.

The server keeps the only copy of each chat, under `/config/chats/`. Every action goes to the server first, is saved, and is then sent back to every connected page over a live stream (Server-Sent Events). A page shows a message only after the server has saved it. That is why a chat open on a phone and a desktop shows the same thing, and why a reconnecting page catches up instead of guessing.

## How kiro-cli is installed

kiro-cli is not built into the image. On first start the server downloads the version the image pins, about 530 MB, and checks it against the SHA-256 checksum for your architecture. It installs it under `/config/tools/kiro-cli-versions/<version>/`. The server starts listening before the download, so the page works while it runs and chats wait.

On every start, marotte runs the installed kiro-cli to confirm it is the expected version, and switches off kiro-cli's own self-update. A replaced or half-restored install is refused rather than run. The previous version stays on the volume as the fallback for a broken new one, and older versions are removed.

`docker exec marotte kiro-cli --version` keeps working, because `/config/tools/bin/kiro-cli` points at the active version.

## Health and repair

`GET /api/health` reports healthy once the server is up and the pinned kiro-cli is installed, runs at that exact version, and has its self-update switched off. Anything short of that answers `503` with a reason:

| Reason | Meaning |
| --- | --- |
| `kiro-cli installing` | The download or install is running. |
| `kiro-cli install retrying` | An attempt failed and the next one is waiting. |
| `kiro-cli unavailable` | Four attempts failed. A restart starts again. |
| `kiro-cli required settings not enforced` | marotte could not switch off kiro-cli's self-update. |

Attempts wait 30 seconds, then double each time up to 10 minutes. The page shows a banner in every one of these states and only chats wait.

To repair an install by hand, fix `/config/tools/kiro-cli-versions` inside the container, then run `curl -X POST localhost:9847/api/kiro-cli/rescan` from inside the container. marotte picks it up without a restart. The command works only from inside the container.

## Language servers and code intelligence

Turning on a language server in **Settings** also turns on kiro-cli code intelligence for the workspace, in live chats too. The first activation saves the detected languages to `/workspace/.kiro/settings/lsp.json`. After you add a language to the workspace, delete that file, and marotte writes it again on the next start.

## Leftover processes

The agent's commands start programs such as git, language servers and build tools, and some of those leave child processes behind when they exit. Those leftovers are handed to the container's first process. The image makes that first process tini, a small init program that cleans them up, so finished processes cannot pile up until the container is unable to start new ones. Because the image already runs tini, the compose example needs no `init: true` line of its own.
