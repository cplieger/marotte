# The HTTP API

This page is for a developer who calls marotte's HTTP API from a script or another service. The browser app uses the same API. Every endpoint, with its parameters, request and reply bodies, error codes and statuses, is listed in [`openapi.json`](openapi.json), an OpenAPI 3.1 document generated from the server's endpoint table. This page explains what the document does not.

## Where the reference is

- [`docs/openapi.json`](openapi.json) in this repository is the document for the code at that commit.
- A running server serves its own copy at `GET /api/openapi.json`.

Each operation lists the query arguments it reads and the error codes it can answer, each code with its one status. A code that is not listed for an operation is never sent by it, with two exceptions. `POST /api/sync` also answers the library codes that [Errors](#errors) lists, and any operation answers `500` with the code `internal_error` when its handler fails unexpectedly.

## Authentication

marotte has no login of its own yet. Anyone who can reach port 9847 can call every endpoint, as [Security](../README.md#security) describes, so keep the port on a private network or behind a reverse proxy that asks for a login.

Two exceptions take a check of their own:

- `POST /api/sessions/{session}/notify` takes the bearer token in `MAROTTE_NOTIFY_TOKEN`, sent as `Authorization: Bearer <token>`. The server refuses that token on every other path. [Configuration](configuration.md#waking-a-chat-from-another-service-marotte_notify_token) shows how to set it.
- `POST /api/kiro-cli/rescan` and `/debug/pprof/` answer only a caller on loopback inside the container, with no proxy header. The document gives the rescan operation a `servers` entry of `http://127.0.0.1:9847`. `/debug/pprof/` has no operation in the document.

## Requests

A request body is JSON unless the operation says otherwise. `POST /api/file/upload` takes a multipart form with a `dir` field and one or more `files` parts.

A `POST`, `PUT`, `PATCH` or `DELETE` sent with an `Idempotency-Key` header is run once per key, method and path. When its reply has a status below 500 and a body of at most 1 MiB, a retry with the same key within five minutes gets that reply again. A 5xx reply or a larger body is not stored, so a retry runs the request again. A second request that arrives while the first still runs gets `409`. An empty key, a key over 256 bytes or one with a control character is ignored, and the request is not deduplicated.

A path no endpoint claims under `/api/` gets `404` with the message `unknown endpoint`. A request whose method the path does not serve gets one of two answers, depending on how the server routes the path:

- `405` with the code `method_not_allowed` and an `Allow` header listing the methods the path serves. A path that serves `GET` also serves `HEAD`.
- `404` with the message `unknown endpoint` and no `Allow` header, the same answer as an unclaimed path. `POST /api/kiro-cli/rescan` answers a `GET` this way.

A `404` with the message `unknown endpoint` on a path the document lists means the method is wrong, or the server does not have the feature behind the path. Two features can be missing: the `/api/launch-rules` routes while their store could not be opened, and `POST /api/kiro-cli/rescan` while marotte manages no kiro-cli install. Any other `404` names what was not found.

## Errors

An error reply is JSON with these fields:

| Field | Meaning |
| --- | --- |
| `error` | A message for a person. Its wording can change in any release. |
| `code` | The machine-readable refusal, when the endpoint has one. Branch on this field. |
| `request_id` | The id of the request in the server's log, when the server set one. |

`POST /api/command` names its refusal in `reason` instead of `code`, and a refused tab close adds `impact`, what the close would stop.

Some replies carry more fields beside `error` and `code`:

- `POST /api/kiro-cli/rescan` answers `503` with its own body when no kiro-cli version is usable: `status` is `unready` and `reason` says why. That reply has no `error` field.
- `/api/file` and `/api/file/download` add `file_id`, `size`, `content_kind` and, for a stale save, `content`, so a client can show what is on disk.
- The `/api/forges` routes add `kind`, the forge's own class of failure, and, when the forge sent them, `retry_after_s`, `diag_id`, `successor` (a renamed or moved repository) and `capability` (what the forge could not do). A relayed forge code is answered with one status whatever path the forge took to it.

Four codes on `POST /api/sync` come from the event-stream library, not marotte, and are not in the document yet: `digest_method` (405), `digest_content_type` (415), `digest_too_large` (413) and `digest_invalid` (400). A digest that takes too long answers `timeout` (503).

## Live updates and the terminal

`GET /api/events` is a server-sent event stream. `POST /api/sync` tells a client that reconnects which of the subjects it holds changed.

The Shell panel's terminal is a WebSocket at `/api/shell/ws`. OpenAPI cannot describe a socket's frames, so the document has no operation for it.

## Versioning

marotte is at v0.x. Any release can change the API, and its release notes list each change. The document's `info.version` stays `0` until the API is stable.
