# Contributing to marotte

The [shared rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) for commits, releases, synced files and checks apply here.

## Scope

A change to how chats or settings are stored adds no migration code, compatibility layer or reader for files an older build left behind.

## Rules

- Every change a user makes to chat state goes through `POST /api/command`, is written to disk, and reaches every open tab over `/api/events`. The transcript draws a user message only after `turn_opened` arrives, so two devices never show different chats.
- A chat's record is removed only by `delete_chat`, by closing its tab while chat retention is off, and by the retention purge. A bridge exit, model switch, restart or error never removes one.
- kiro-cli's ACP notifications become marotte entries and events in `internal/translate` before anything reaches a client. Never forward a raw ACP frame. The client has no type or decoder for one, so it would drop or misread it.
- A failed user action shows its action's toast. A form validation error shows beside the field that failed, and a condition the user must see on every view is a banner in `banner-stack.ts`. Background polls and stream errors only log.
- An action whose failure a form or a banner already shows sets `error: false`, or the user sees the same failure twice.
- A control in the prompt-pill row opens through `pill-expand.ts`, never a floating popup. A popup outside it breaks the one-pill-open-at-a-time rule the other pills share.
- Each `@cplieger/*` package pinned in `static-src/package.json` is fetched again by a Dockerfile `ARG` at the same exact version. Change both together, or the image bundles a different version from the one you tested.

## Checks

The census tests in `internal/kascap` compare the capability table with kiro-cli's own agent-server bundle under `~/.local/share/kiro-cli/kas/`. CI has no bundle and skips them.

After a change to that package, run `go test -v ./internal/kascap/` on a machine with the pinned kiro-cli, and check that no census test reports SKIP. A failure prints the `-update` command, and every line it adds is a capability to judge.

The `e2e-sse` test project runs against a real server binary and skips unless `SSE_FIXTURE` names one, which CI never sets. After a change to the SSE stream or the sync digest, build that binary and run the project:

```sh
go build -tags marotte_test -o /tmp/marotte-test .
cd static-src && SSE_FIXTURE=/tmp/marotte-test npx vitest --run --project e2e-sse
```
