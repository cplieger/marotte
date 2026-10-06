# Configuration

This page lists every environment variable marotte reads and explains the ones that guard access or change what the agent can do. Most people need none of them. Models, permissions, tools, notifications and chat retention are set on the page under **Settings**.

Variables go in the `environment:` block of `compose.yaml` and are read at start, so recreate the container after a change.

## All variables

| Variable | Description | Default |
| --- | --- | --- |
| `ALLOWED_HOSTS` | Hostnames and IPs you open marotte at, comma-separated. Any other address gets `403`. | _(unset)_ |
| `TRUSTED_PROXIES` | Address ranges of your reverse proxy, so the logs record the real client address. | _(unset)_ |
| `TRUSTED_INSTALL_UIDS` | User IDs that may write to `/config/tools` without marotte refusing to install kiro-cli. Only for shared or network volumes. | _(unset)_ |
| `MAROTTE_BROWSE_ROOTS` | Extra folders the file browser shows, colon-separated absolute paths. | _(unset)_ |
| `MAROTTE_ALLOW_AGENT_ENV` | Program-changing variables, such as `LD_PRELOAD`, the agent may set for its commands. | _(unset)_ |
| `MAROTTE_ALLOW_BRIDGE_ENV` | Variable names that look like credentials but should still reach kiro-cli, comma-separated. | _(unset)_ |
| `MAROTTE_KIRO_ACP_ARGS` | Extra `kiro-cli acp` flags for every chat. | _(unset)_ |
| `KIRO_WORK_DIR` | Folder chats and the terminal start in. It must exist, or startup fails. | `/workspace` |
| `KIRO_CONFIG_DIR` | Folder for chats, the kiro-cli home, installed tools and settings. It must exist and be writable. | `/config` |
| `KIRO_HOME` | Where marotte finds kiro-cli's own state: steering, settings and session files. | `$HOME/.kiro` |
| `MAROTTE_TOOLS_DIR` | Where the tools engine installs tools, on the persistent volume. | `<KIRO_CONFIG_DIR>/tools` |
| `MAROTTE_TOOL_CATALOG` | Tool catalog built into the image, used at first start and offline. | `/opt/marotte/tool-catalog.json` |
| `MAROTTE_TOOL_CATALOG_URL` | Where catalog updates come from. Point it at a fork or a mirror. | the [tool-catalog](https://github.com/cplieger/tool-catalog) latest release |
| `MAROTTE_BUNDLED_TOOLS` | File inside the image naming the tools marotte bundles. A wrong path leaves the suggested language servers unable to install. | `/opt/marotte/bundled-tools.json` |
| `VAPID_SUBJECT` | Contact address placed in the keys used for push notifications. | `mailto:marotte@noreply.invalid` |

## Host allowlist (`ALLOWED_HOSTS`)

Set it to the exact hostnames and IPs you open marotte at, for example `ALLOWED_HOSTS: "localhost,192.168.1.10,marotte.example.com"`. A request with any other `Host` header is answered with `403`.

Set it on any server that stays up. It blocks DNS rebinding, where a website you visit points its own hostname at your marotte address. The browser then treats your marotte as that website, so the usual same-origin check passes. The attack runs through your own browser, so it also reaches a marotte that only listens on your network. Requests from inside the container are always allowed, so the image's healthcheck keeps working. Left unset, marotte accepts every `Host` and logs a warning at startup.

## Behind a reverse proxy (`TRUSTED_PROXIES`)

The access log and the sign-in and sign-out logs record a `client_ip`. Leave `TRUSTED_PROXIES` unset when nothing sits in front of marotte. The address is then the connecting socket's, which a client cannot fake, and any `X-Forwarded-For` header is ignored.

Behind a reverse proxy, set it to the proxy's address, or a comma-separated list of every proxy hop, as [Telling the app about the proxy](https://github.com/cplieger/docs/blob/main/docs/reverse-proxy.md#telling-the-app-about-the-proxy) shows. A bare IP counts as one host, and a CIDR range trusts every address inside it.

marotte reads `X-Forwarded-For` only when the connecting address is inside that list. An empty, unset or malformed value therefore cannot be used to fake an address.

## Trusted install accounts (`TRUSTED_INSTALL_UIDS`)

Before it installs kiro-cli, marotte checks who can write to each folder on the way to `/config/tools`. It refuses the install when another account can, because the container later runs what lands there. The log line names the folder and the account that "can be modified by" it.

Leave this unset on almost every server. Set it only when the check refuses a volume you know is safe, usually a shared or network volume whose permissions include an account you control.

```yaml
environment:
  TRUSTED_INSTALL_UIDS: "1001"
```

Each number you list says that account already has at least the power of this server, so its write access gains it nothing. That is true of an administrator who already has root on the host. It is false of an ordinary account, and listing one gives it a way in. A malformed entry is skipped with a warning.

## Extra file-browser folders (`MAROTTE_BROWSE_ROOTS`)

The file browser shows `/workspace`, `/config` and `/uploads` and nothing else in the container. To show another mount, list its absolute path. Separate several with colons.

```yaml
environment:
  MAROTTE_BROWSE_ROOTS: "/tmp:/data"
```

Mount each folder with `volumes:` first. Credential and internal files under `/config` stay hidden whatever you add. That covers SSH keys, cloud tokens, forge credentials, the chat store and the MCP configuration.

## Agent-started workflow runs

Whether the chat agent can start and manage workflow runs is a setting, not a variable: **Workflows** under **Agent capabilities** on the **General** tab in **Settings**, on by default. With it on, a request like "run the publish workflow" starts the run instead of describing it. Runs you start yourself from **Workflows** on the `/docs` page work either way. A change reaches the next chat, and an open chat picks it up when it reloads.

## Variables the agent sets (`MAROTTE_ALLOW_AGENT_ENV`)

When the agent runs a command, it can ask for environment variables to be set for it. Most are ordinary, such as `CGO_ENABLED`, `GOFLAGS` or `TERM`. A few change which program runs instead: `LD_PRELOAD`, `GIT_SSH_COMMAND` and `BASH_ENV` each redirect execution. marotte refuses those, because approving a command must approve that exact command, and the agent's variables take precedence over marotte's own. A harmless value is still accepted, so `GIT_PAGER=cat` keeps working. The refusal names the variable, so the agent can retry without it.

If you need one, for example a profiler that preloads a library or a vendored `NODE_PATH`, name it. Separate several with commas. Only the names you list are allowed.

```yaml
environment:
  MAROTTE_ALLOW_AGENT_ENV: "LD_PRELOAD,NODE_PATH"
```

This applies to what the agent asks for, not to variables you set on the container yourself.

## Credentials in the container environment (`MAROTTE_ALLOW_BRIDGE_ENV`)

The shell commands the agent runs inherit the container's full `environment:`. A `GITHUB_TOKEN` added there for another reason is a credential those commands can read and use. The kiro-cli process gets a screened copy instead.

marotte drops credential-looking names from the kiro-cli process and logs which ones, by name only. That covers any name ending in `_TOKEN` or `_SECRET`, plus `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and `KIRO_API_KEY`. Other variables pass unchanged, `AWS_REGION` and `AWS_PROFILE` included. An allowlisted `KIRO_API_KEY` makes the agent run as that key's identity instead of the signed-in account, and marotte logs a warning at start when it does.

Keep credentials out of the container environment anyway. Forge credentials belong in the git panel's Sources tab, which keeps them in marotte's own store. If a variable only looks like a credential, name it:

```yaml
environment:
  MAROTTE_ALLOW_BRIDGE_ENV: "BUILDKITE_AGENT_TOKEN"
```

## Launch flags

`MAROTTE_KIRO_ACP_ARGS` has its own page, [Launch flags](launch-flags.md).

## Uploads

Files you attach to a message, by drag and drop, paste or the `+` menu, are written to `/uploads`, and the agent reads them from there. The image creates the folder itself, so attaching works with no volume. Without a volume, though, the files are lost when the container is recreated, and a saved draft that still lists them points at files that no longer exist. Mount a volume, owned by the same user as the other mounts, to keep them. The file browser shows `/uploads`, so you can rename and delete files there.

## What `/config` holds

`/config` is the one volume marotte keeps its state in. Back it up as a whole.

| Path | Contents |
| --- | --- |
| `/config/chats/` | Chat history, one folder per chat |
| `/config/home/` | The container's home folder: sign-ins, SSH keys, git settings, build caches |
| `/config/home/.kiro/` | kiro-cli's sessions, settings, steering, agents and logs |
| `/config/tools/` | Installed tools. `bin/` is on `PATH` |
| `/config/*.json` | marotte's settings, the tools list and its state, and the MCP server list |

## OS packages

The tools engine also installs Debian packages, so there is no separate variable for them. See [OS packages](os-packages.md).
