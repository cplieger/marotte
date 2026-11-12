# Configuration

This page lists every environment variable marotte reads and explains the ones that guard access or change what the agent can do. Most people need none of them. Models, permissions, tools, notifications and chat retention are set on the page under **Settings**.

Variables go in the `environment:` block of `compose.yaml` and are read at start, so recreate the container after a change.

## All variables

| Variable | Description | Default |
| --- | --- | --- |
| `LISTEN_ADDR` | Address the server listens on, as `host:port`. The host may be empty, `0.0.0.0` or `::`, which listen on every address, or `localhost` or a loopback address such as `127.0.0.1` or `::1` (without a `%zone`, and not `127.255.255.255`); any other host stops startup, because the healthcheck and the in-container API reach the server on loopback. Change it on a host-network install (`network_mode: host`) when another service on the host holds port 9847; `ports:` does not apply there. On the default network, change the host side of `ports:` instead. A value without a port number from 1 to 65535 stops startup. | `:9847` |
| `ALLOWED_HOSTS` | Hostnames and IPs you open marotte at, comma-separated. Any other address gets `403`. | _(unset)_ |
| `TRUSTED_PROXIES` | Address ranges of your reverse proxy, so the logs record the real client address. | _(unset)_ |
| `TRUSTED_INSTALL_UIDS` | User IDs that may write to the tools folder (`MAROTTE_TOOLS_DIR`, by default `/config/tools`) without marotte refusing to install kiro-cli or start the tools engine. Only for shared or network volumes. | _(unset)_ |
| `MAROTTE_BROWSE_ROOTS` | Extra folders the file browser shows, colon-separated absolute paths. | _(unset)_ |
| `MAROTTE_ALLOW_BRIDGE_ENV` | Variable names that look like credentials but should still reach kiro-cli, comma-separated. | _(unset)_ |
| `MAROTTE_KIRO_ACP_ARGS` | Extra `kiro-cli acp` flags for the agent engine. | _(unset)_ |
| `KIRO_WORK_DIR` | Folder chats and the terminal start in. It must exist and must not be or contain `KIRO_CONFIG_DIR`, or startup fails. Startup also fails when its path holds a backtick, a line break, a tab or another control or invisible character, because marotte writes it into the agent's instructions. | `/workspace` |
| `KIRO_CONFIG_DIR` | Folder for chats, the Kiro sign-in, settings and the tools list, and by default the installed tools. The container creates it when it is missing, and it must be writable. Its path follows the same character rule as `KIRO_WORK_DIR`, or startup fails. The home folder stays `/config/home` unless you also set `HOME`. Each marotte needs its own: a second one started on the same folder refuses to start. | `/config` |
| `KIRO_HOME` | Where marotte and kiro-cli keep kiro-cli's own state: steering, settings, permissions and session files. A relative path, `/`, or a path that is `HOME` or holds `HOME` inside it is ignored with a warning at startup, as kiro-cli ignores it. kiro-cli's test for "inside" does not count a `HOME` whose path relative to `KIRO_HOME` starts with `..`, such as a `..home` folder directly below it, and marotte follows it. Each marotte needs its own: a second one started on the same folder refuses to start. | `$HOME/.kiro` |
| `KIRO_RUNTIME_DIR` | Where kiro-cli unpacks the programs it runs on: node, its agent server and its terminal UI. kiro-cli keeps only the version it runs there and deletes the others each time it starts, so run any other kiro-cli version with this variable unset. It must be an absolute path. A relative one is ignored, and the programs go to kiro-cli's data folder under `HOME`. | `/config/home/.cache/kiro-cli-runtime` |
| `MAROTTE_TOOLS_DIR` | Where the tools engine installs tools, on the persistent volume. Its `bin/` and `go/bin/` take the place of `/config/tools/bin` and `/config/tools/go/bin` on `PATH`, or go in front when you set a `PATH` without them, and `GOPATH` and `GOBIN` follow it unless you set them to another folder. When its path holds a colon, which `PATH` and `GOPATH` cannot carry, or a character `KIRO_WORK_DIR` refuses, marotte still starts but uses nothing in that folder: the tools engine stays off, kiro-cli is not installed, so chats stay unavailable, and the folder's `bin/` and `go/bin/`, spelled as you set them or as absolute paths, are removed from `PATH`, `GOPATH` and `GOBIN`, also from a `GOPATH` that lists other folders, which stay. Where the colon splits them into the image's system folders, such as `/usr/bin`, those stay. Settings > Tools and the server log name the variable and the character. Each marotte needs its own: a second one started on the same folder refuses to start. | `<KIRO_CONFIG_DIR>/tools` |
| `MAROTTE_TOOL_CATALOG` | Tool catalog built into the image, used at first start and offline. | `/opt/marotte/tool-catalog.json` |
| `MAROTTE_TOOL_CATALOG_URL` | Where catalog updates come from. Point it at a fork or a mirror. | the [tool-catalog](https://github.com/cplieger/tool-catalog) latest release |
| `MAROTTE_BUNDLED_TOOLS` | File inside the image naming the tools marotte bundles. A wrong path leaves the suggested language servers unable to install. | `/opt/marotte/bundled-tools.json` |
| `VAPID_SUBJECT` | Contact address placed in the keys used for push notifications. | `mailto:marotte@noreply.invalid` |
| `MAROTTE_NOTIFY_TOKEN` | Bearer token another service sends to [wake a chat](#waking-a-chat-from-another-service-marotte_notify_token). `MAROTTE_NOTIFY_TOKEN_FILE` reads it from a file instead. | _(unset: off)_ |

## Host allowlist (`ALLOWED_HOSTS`)

Set it to the exact hostnames and IPs you open marotte at, for example `ALLOWED_HOSTS: "localhost,192.0.2.10,marotte.example.com"`. A request with any other `Host` header is answered with `403`.

Set it on any server that stays up. It blocks DNS rebinding, where a website you visit points its own hostname at your marotte address. The browser then treats your marotte as that website, so the usual same-origin check passes. The attack runs through your own browser, so it also reaches a marotte that only listens on your network. Requests from inside the container are always allowed, so the image's healthcheck keeps working. Left unset, marotte accepts every `Host` and logs a warning at startup.

A metrics scraper or an uptime prober outside the container must call marotte by a name in the list too, or it gets `403`. A Docker network name such as `marotte` works once you add it. [Monitoring and alerts](monitoring.md) covers both.

## Behind a reverse proxy (`TRUSTED_PROXIES`)

The access log and the sign-in and sign-out logs record a `client_ip`. Leave `TRUSTED_PROXIES` unset when nothing sits in front of marotte. The address is then the connecting socket's, which a client cannot fake, and any `X-Forwarded-For` header is ignored.

Behind a reverse proxy, set it to the proxy's address, or a comma-separated list of every proxy hop, as [Telling the app about the proxy](https://github.com/cplieger/docs/blob/main/docs/reverse-proxy.md#telling-the-app-about-the-proxy) shows. A bare IP counts as one host, and a CIDR range trusts every address inside it.

marotte reads `X-Forwarded-For` only when the connecting address is inside that list. An empty, unset or malformed value therefore cannot be used to fake an address. The first time an address outside the list forwards a public client address, marotte logs one warning, because that usually means a proxy is missing from the list.

## Trusted install accounts (`TRUSTED_INSTALL_UIDS`)

Before it installs kiro-cli, marotte checks who can write to each folder on the way to the tools folder. It refuses the install when another account can, because the container later runs what lands there. The log line names the folder and the account that "can be modified by" it.

Leave this unset on almost every server. Set it only when the check refuses a volume you know is safe, usually a shared or network volume whose permissions include an account you control.

```yaml
environment:
  TRUSTED_INSTALL_UIDS: "1001"
```

Each number you list says that account already has at least the power of this server, so its write access gains it nothing. That is true of an administrator who already has root on the host. It is false of an ordinary account, and listing one gives it a way in. A malformed entry is skipped with a warning.

The tools engine makes the same check, with the same exception: it does not start while an account other than the container's own, root or one listed here owns or can write to the config folder, the tools folder, a folder it installs into or a folder above them, and the **Tools** tab in **Settings** then says "Tools are off", with the server log naming each folder. So at every start, before the tools engine starts, marotte removes group and other write from those folders and logs a warning naming each one it changed and the mode it had. Anything another account wrote into such a folder before stays, so check what it holds if that account is not one you trust. marotte never follows a symlink when it changes these folders. A symlink at one of them is left as it is, with the folders below it and a warning, and the tools engine names it too. Only the folders above the config and tools folders may be symlinks. Naming the home folder or `KIRO_HOME` through a second path into the config folder, such as a symlink elsewhere that points into it, is not supported: marotte cannot tell that path leads into the config folder, so a symlink planted there could redirect a mode change. marotte needs permission to enter a folder, not to list it, to change its mode. In the same pass marotte sets the home folder to 755 and its `.ssh` to 700. It removes group and other write from `KIRO_HOME`, from `kiro-cli-versions` in the tools folder, and from `.local`, `.cache` and `.docker` in the home folder with the folders below them that kiro-cli, Go and Docker use. A home folder, `KIRO_HOME` or folder below them that is a symlink keeps its mode, with a warning.

## Extra file-browser folders (`MAROTTE_BROWSE_ROOTS`)

The file browser shows `/workspace`, `/config`, `/uploads` and the [folders the agent can use](#folders-the-agent-can-use-beyond-workspace), and nothing else in the container. To show another mount, list its absolute path. Separate several with colons.

```yaml
environment:
  MAROTTE_BROWSE_ROOTS: "/tmp:/data"
```

Mount each folder with `volumes:` first. Credential and internal files under `/config` stay hidden whatever you add. That covers SSH keys, cloud tokens, forge credentials, the chat store and the MCP configuration.

## Folders the agent can use beyond `/workspace`

The agent's file tools read and write `/workspace` only, and read `/uploads`. To give the agent another folder, add it under **Additional directories** on the **Permissions** tab in **Settings**. The agent can then read, write and delete files there and start shell commands in it, and the file browser shows it. Mount the folder with `volumes:` first.

Each entry is the absolute path of an existing folder, written without a symbolic link. The filesystem root, `/config`, the workspace, and any folder inside or holding either of them are refused, with the reason beside the field. A listed folder that later stops qualifying, because it was deleted or `config.json` was edited by hand, is marked **not granted** with its reason and gives the agent nothing until it qualifies again.

A change reaches the agent's file tools, the folder a shell command may start in, and the file browser at once. The file tools refuse a removed folder from their next operation in every chat and run. Shell commands themselves are not confined to these folders, so one can still `cd` into a removed folder or write to it. kiro-cli takes the current list whenever marotte opens or reloads a chat's session: a new chat with its first message, an open or resumed chat at its next message, and a chat you fork or rewind as you do it. A workflow run keeps the list it started with for every step, even after an engine restart.

## Agent-started workflow runs

Whether the chat agent can start and manage workflow runs is a setting, not a variable: **Workflows** under **Agent capabilities** on the **General** tab in **Settings**, on by default. With it on, a request like "run the publish workflow" starts the run instead of describing it. On: the agent can start workflows that run in the background while you keep chatting. Off: it uses subagents instead and waits for each to finish before it continues. Runs you start yourself from **Workflows** on the `/docs` page work either way.

**Workflow use**, below the switch and shown while it is on, says when the agent starts a workflow on its own. With **Only when asked**, the default, the agent does the work in the chat unless you ask for a workflow. With **Only for large tasks**, it starts one for multi-step work or work that would fill the chat's context. With **As much as possible**, it hands reading code, writing it, testing and review to workflows. A chat started on an older kiro-cli, one without this choice, keeps handing all work to workflows whatever you pick, because kiro-cli does not offer the choice on it. A change to either setting reaches new chats, and an open chat picks it up at its next message.

When a loop in a run the agent started stops at its pass limit, kiro-cli tells the agent, which then asks you whether to give the loop more passes or end it.

A workflow can also start itself on a schedule or when something happens on a forge, through **Run when…** on its row on the **Workflows** panel. [Event triggers](triggers.md) describes the forge rules and the two settings that cap the runs they start.

## Messages between chats

Whether the agent in one chat can send a message to another chat is a setting, not a variable: **Messages between chats** under **Agent capabilities** on the **General** tab in **Settings**, off by default, as in kiro-cli. With it on, the agent can message another chat, which shows the message with the chat it came from, while the sending chat shows it with the chat it went to. The agent names the other chat by its session ID, which it cannot look up, so tell it which chat to reach; each chat's agent knows its own ID as `KIRO_SESSION_ID`. Both chats need the setting in force, and a workflow step cannot receive a message. Sending also needs **Workflows** on, because kiro-cli gives a chat the tool that sends only with workflows on; a chat with **Workflows** off can still receive. A change reaches new chats, and any other chat takes it at its next message or when you open it. So after turning it on, send each chat you want to reach one message first; after turning it off, two chats that have not had a message or been opened since can still exchange messages until they do.

## kiro-cli telemetry

kiro-cli's telemetry is a setting, not a variable: **kiro-cli telemetry** under **Data sharing** on the **General** tab in **Settings**. It is on by default, as in kiro-cli. One switch covers both kinds kiro-cli sends: its usage telemetry and its legacy toolkit telemetry. Off stops both. An existing install keeps the value it already has, so one that earlier versions started with telemetry off stays off. On an enterprise account, the organization's usage-analytics setting decides it instead, and the switch is locked. A change applies after the agent engine restarts, which it does by itself once no reply and no workflow is running.

## Agent engine memory limit

The agent engine's memory limit is a setting, not a variable: **Agent engine memory limit** under **Agent engine** on the **General** tab in **Settings**. Empty keeps kiro-cli's default, about 4 GB. A value is in MiB, from 1024 to 65536, and at most the container's memory limit minus 512 when the container has one. It bounds the engine's JavaScript heap only, not its native memory. A change applies after the agent engine restarts.

With **High throughput optimizations** in use, marotte puts the limit on the command line of kiro-cli's own Node.js, beside the high throughput flags, and leaves `NODE_OPTIONS` as you set it. The MCP servers, hooks and workflow watch commands the engine starts then keep their own heap size. A heap-size flag you set in `NODE_OPTIONS` gives way to the setting.

Without the optimizations, kiro-cli starts Node.js itself, so marotte applies the limit through `NODE_OPTIONS`. It does the same when your `NODE_OPTIONS` sets `--max-old-space-size-percentage`, which Node.js ranks above any heap size. The MCP servers, hooks and workflow watch commands the engine starts inherit `NODE_OPTIONS`, so a Node.js one then runs under the engine's limit too. Agent shell commands do not inherit it. In `NODE_OPTIONS`, a heap-size flag you set gives way to the setting, and every other option is kept as written. A `NODE_OPTIONS` marotte cannot read, such as one with an unterminated quote, makes **Settings** refuse a limit until you fix the variable.

marotte checks the engine's memory use once a minute. The first time it reaches 80% of the limit in an engine start, marotte's log shows `engine memory high` with the figures. When the engine stops, its `engine exited` line shows `rss_peak_mib`, the most memory the engine used, and `heap_limit_mib`, the limit it ran with. With the setting empty, that is kiro-cli's default. If marotte cannot read that default, `heap_limit_mib` is 0, marotte's log says the default is unknown, and it does not log `engine memory high`.

## Debug logs

**Debug logs** under **Experimental features** on the **General** tab in **Settings** writes more detailed logs for troubleshooting. marotte's own log, which `docker logs marotte` shows, switches at once. The agent engine's log, `kiro.log` in a folder per engine start under `/config/home/.kiro/logs/`, switches after the agent engine restarts. So does kiro-cli's own log, `/tmp/kiro-log/kiro-chat.log`. The engine restarts by itself once no reply and no workflow is running.

`/tmp/kiro-log/kiro-chat.log` also holds the agent engine's startup and console lines, Node.js warnings included, whatever **Debug logs** is set to; `docker logs marotte` does not show them. The lines that say why the engine died, such as a JavaScript heap out of memory, still reach `docker logs marotte`.

## High throughput optimizations

**High throughput optimizations** under **Experimental features** on the **General** tab in **Settings** is on by default. It starts the agent engine's Node.js with `--experimental-async-context-frame` and `--max-semi-space-size=64`, which let one engine carry many chats and workflow steps at once. Under a load of 32 concurrent chats on an engine holding 4 GB of session state, the two flags raised throughput from about 3 to 10 prompts a second and cut the time spent in garbage collection from about 40% to 12%. Turn it off for kiro-cli's stock engine. A change applies after the agent engine restarts.

The flags go on the command line of kiro-cli's own Node.js, not in `NODE_OPTIONS`, so the programs the agent runs never see them. marotte names its own binary in `KIRO_KAS_NODE_PATH`, and that binary starts kiro-cli's Node.js with the flags. It does so only when the Node.js kiro-cli extracted matches the one the running kiro-cli bundles and reports the version the flags were tested on, v22.23.4. Otherwise the engine starts without them, and marotte's log names why in one `starting without the high throughput optimizations` warning. After a kiro-cli version change that happens once, while kiro-cli extracts its new Node.js. If that Node.js is still v22.23.4, the next engine start uses the flags again. A `KIRO_KAS_NODE_PATH` you set yourself also turns them off.

## Credentials in the container environment (`MAROTTE_ALLOW_BRIDGE_ENV`)

The shell commands the agent runs inherit the container's full `environment:`. A `GITHUB_TOKEN` added there for another reason is a credential those commands can read and use. The kiro-cli process gets a screened copy instead.

marotte drops credential-looking names from the kiro-cli process and logs which ones, by name only. That covers any name ending in `_TOKEN` or `_SECRET`, plus `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and `KIRO_API_KEY`. Other variables pass unchanged, `AWS_REGION` and `AWS_PROFILE` included. An allowlisted `KIRO_API_KEY` makes the agent run as that key's identity instead of the account signed in on the page, because kiro-cli prefers the key, and marotte logs a warning at start when it does. While it does, the account menu shows the API key, and signing in or out on the page is refused. marotte keeps the saved sign-in, and it is used again once you remove the variable and recreate the container.

Keep credentials out of the container environment anyway. Forge credentials belong in the git panel's Sources tab, which keeps them in marotte's own store. If a variable only looks like a credential, name it:

```yaml
environment:
  MAROTTE_ALLOW_BRIDGE_ENV: "BUILDKITE_AGENT_TOKEN"
```

## Waking a chat from another service (`MAROTTE_NOTIFY_TOKEN`)

A service on your network, such as a mail or job server for your agents, can hand a chat a message with one request. marotte places the message the way **Send** on the page does: into the chat's running reply, or as a new message. A new message waits in the chat's queue while a compaction runs, or while a reply runs that it cannot join or that **Queue messages** keeps it out of, and goes when what runs ends. The message box, its draft and a question the chat is waiting on you for are left as they are. The delivered message shows "Sent by" and the caller's `source`.

The agent is not told who sent the message. kiro-cli presents a message steered into a running reply as a new message from the user, and a new message as one the user typed. marotte also gives kiro-cli the `source` with a new message, and kiro-cli keeps it with that message in the session's history, but the agent does not read it there. So start `text` by naming the sender, as the example below does.

The route is off until you give it a token. Generate a random value of 32 to 512 characters, for example with `openssl rand -base64 48`, and give the same value to the calling service. Prefer a Docker secret, read through the `_FILE` variant:

```yaml
environment:
  MAROTTE_NOTIFY_TOKEN_FILE: /run/secrets/marotte_notify_token
secrets:
  - marotte_notify_token
```

`MAROTTE_NOTIFY_TOKEN` set to the value itself works too. A token outside 32 to 512 of the characters `A-Z a-z 0-9 - . _ ~ + /`, with optional trailing `=`, or a `_FILE` that is blank or cannot be read, stops startup with a message naming the variable. Unset, startup logs one line saying the route is off. marotte keeps only a hash of the token and never logs it. kiro-cli does not get `MAROTTE_NOTIFY_TOKEN`, because its name ends in `_TOKEN`, but the shell commands the agent runs [inherit the container's environment](#credentials-in-the-container-environment-marotte_allow_bridge_env), so the `_FILE` form keeps the value out of their reach unless they open the file.

The request names the chat by its session ID, which the agent's shell commands carry as `KIRO_SESSION_ID`:

```sh
curl -X POST http://marotte:9847/api/sessions/$SESSION/notify \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"text":"mail (automated, not the user): You have new mail.","source":"mail","dedupe_key":"mail-1234"}'
```

| Field | Rule |
| --- | --- |
| `text` | The message. Required, not only whitespace, at most 64 KiB. It may not start with `[notification/...]` or hold a `#[[kind:query]]` context reference. |
| `source` | Who sent it, shown on the message. 1 to 32 characters of `A-Z a-z 0-9 . _ -`. |
| `dedupe_key` | 1 to 128 printable ASCII characters. A second request for the same chat with the same key within 10 minutes of the first delivery is not delivered again. |

| Answer | Meaning |
| --- | --- |
| `200 {"delivered":"steer"}` or `200 {"delivered":"prompt"}` | Delivered into the running reply, or as a new message, now or once what runs ends. |
| `200 {"duplicate":true}` | The same `dedupe_key` was delivered to this chat in the last 10 minutes. |
| `400` | A field is missing or breaks its rule, and the error names it, or `body` when the body is not a JSON object. |
| `401` | The token is missing or wrong, or the route is off. The off answer names `MAROTTE_NOTIFY_TOKEN`. |
| `404` | No chat holds this session. |
| `413` | The body is over 512 KiB. |
| `503` with `Retry-After` | The agent engine is starting, stopping or cannot take a message now, or a request with the same key is still being delivered. Retry with the same `dedupe_key`. |

The token opens this route and nothing else: a request that carries it to any other address is answered `403`. This one route also skips the `ALLOWED_HOSTS` check, so a service in the same compose network can call `http://marotte:9847` without adding `marotte` to the list. Every other route keeps the check.

## Launch flags

`MAROTTE_KIRO_ACP_ARGS` has its own page, [Launch flags](launch-flags.md).

## Uploads

Files you attach to a message, by drag and drop, paste or the composer's **Chat options** menu, are written to `/uploads`, and the agent reads them from there. The image creates the folder itself, so attaching works with no volume. Without a volume, though, the files are lost when the container is recreated, and a saved draft that still lists them points at files that no longer exist. Mount a volume, owned by the same user as the other mounts, to keep them. The file browser shows `/uploads`, so you can rename and delete files there.

## What `/config` holds

`/config` is the one volume marotte keeps its state in. Back it up as a whole. The paths below are the defaults; `KIRO_CONFIG_DIR`, `MAROTTE_TOOLS_DIR`, `HOME` and `KIRO_HOME` move them.

| Path | Contents |
| --- | --- |
| `/config/chats/` | Chat history, one folder per chat |
| `/config/signin.json` | marotte's Kiro sign-in, readable only by the container's user |
| `/config/home/` | The container's home folder: SSH keys, git settings, build caches, and the separate sign-in of a `kiro-cli` you run in the terminal |
| `/config/home/.kiro/` | kiro-cli's sessions, settings, steering, agents and logs |
| `/config/tools/` | Installed tools. `bin/` and `go/bin/` are on the `PATH` of the server and the agent, so link any other binary into `bin/`. A login shell searches them only when the container runs as root and marotte's `/etc/profile.d` file holds that start's PATH ([How kiro-cli is installed](how-it-works.md#how-kiro-cli-is-installed)) |
| `/config/*.json` | marotte's settings, the tools list and its state, and the MCP server list |
| `marotte.lock` in `/config`, `/config/home/.kiro` and `/config/tools` | Holds the process ID of the marotte using the folder. marotte locks the folder itself as well as this file, so deleting the file does not let a second marotte start on the same host, but deleting or replacing the whole folder does. A marotte that crashed leaves no lock behind. Between two hosts sharing a network folder only the lock on this file counts, so leave it in place, and the share must support file locks |

## OS packages

The tools engine also installs Debian packages, so there is no separate variable for them. See [OS packages](os-packages.md).
