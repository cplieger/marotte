# Security

This page is for anyone deciding where to run marotte and who can reach it. It covers access, the user the container runs as, tool installs, what marotte sends out, and how kiro-cli is installed.

## Who can reach it

marotte has no login of its own. Anyone who can reach port 9847 can use the agent, which runs commands and reads and writes files under `/workspace` and any [additional directory](configuration.md#folders-the-agent-can-use-beyond-workspace) you add. They can also use the Kiro sign-in marotte keeps in `/config`. Keep the port on your private network, or put marotte behind a [reverse proxy](https://github.com/cplieger/docs/blob/main/docs/reverse-proxy.md) that asks for a login. Caddy forward-auth, oauth2-proxy and Authentik all work. Doing both is better.

Signing in on the page gives the agent your Kiro account. That is the agent's identity, and it does not protect marotte itself. marotte keeps the sign-in in `/config/signin.json`, which only the container's user may read or write. marotte tightens a file that others can read, and refuses to use one it cannot tighten. It never reads the sign-in of a `kiro-cli` you run in the terminal.

Set `ALLOWED_HOSTS` on any server that stays up, and set `TRUSTED_PROXIES` when a proxy sits in front. [Configuration](configuration.md) explains both. Requests that change state from another website are refused with `403`.

The file browser shows only the folders it is given, `/workspace`, `/config`, `/uploads` and the agent's additional directories by default. Credential and internal files under `/config`, such as SSH keys, cloud tokens, forge credentials, the Kiro sign-in, the chat store and the MCP configuration, stay hidden. The preview tab follows the same list. It refuses a page whose folder holds those files or sits among them.

## The container user

The example `compose.yaml` runs the container as user 1000, or as the `PUID` and `PGID` from `.env`. The files it writes on the host then belong to you. You can run it as root instead with `user: "0:0"`. You then no longer give the host folders to user 1000, and you can install Debian packages from [OS packages](os-packages.md), which install only as root. It also makes the agent's terminal a root shell inside the container.

The image is built on Debian, which has the shell the terminal needs and runs kiro-cli as its own program.

## Tool installs

Installing a tool from **Settings** runs as the container's own user. Anyone who can reach the port already has that user's shell, so the Add tool button gives no extra access.

The two kinds of source differ in what backs them. An `apt:` entry can only be a literal Debian package name, and Debian's signed package lists vouch for it. A `release:` entry downloads a file from a project's releases. Most of the catalog's release-based entries publish no checksum, so those installs are an unverified download, with the file picked by matching its name. Three things limit the risk. The owner and repository come from the pinned upstream registry, not from anything typed into the box. Nothing is reported installed until the tool has run. The row says `no checksum`.

## What marotte sends out

marotte sends no telemetry. Its outbound requests are these:

- the AI provider of the Kiro account you signed in with
- AWS's sign-in service in your account's region, to sign in and to renew the sign-in, and Kiro's account service, to read the account's Kiro profile and email at sign-in
- any MCP server you add
- the API of each forge you connect in the git panel, never on a private address unless that connection allows it
- the public MCP registry when you search it
- the kiro-cli download on first start, the tools you install, and the tool catalog refresh, once a day by default. When a github.com account is connected, its token goes with the tools' requests to GitHub's API and to no other host.

kiro-cli's own telemetry is on by default, as in kiro-cli. The **kiro-cli telemetry** switch under **Data sharing** in **Settings** covers both kinds: its usage telemetry, which sends usage data linked to your signed-in account, and its legacy toolkit telemetry. Turned off, it stops both. On an enterprise account, the organization's usage-analytics setting decides it instead. Push notifications go only to the browser vendors' push services, over HTTPS on port 443. Each push connection is checked again after the name is resolved.

## How kiro-cli is installed

kiro-cli is downloaded on first start, not built into the image, because the AWS Customer Agreement governs passing it on. You accept that agreement by starting the container. A newer kiro-cli arrives with a newer image tag. kiro-cli's own self-update is switched off.

The download is checked against a SHA-256 checksum pinned for your architecture before anything is installed. Each version goes into its own folder under `/config/tools/kiro-cli-versions/<version>/`, and it is checked again on every start. A replaced or half-restored install is refused rather than run. Before installing, marotte checks who can write to the folders on the way. It refuses when any account other than the container's own or root can, because the container runs what lands there. `TRUSTED_INSTALL_UIDS` in [Configuration](configuration.md) is for a volume where that is expected.

## The image

Images are published with cosign signatures and SBOM attestations, which [Checking a signature](https://github.com/cplieger/docs/blob/main/docs/images.md#checking-a-signature) and [Reading the software bill of materials](https://github.com/cplieger/docs/blob/main/docs/images.md#reading-the-software-bill-of-materials) show how to check. The image carries the license text of every bundled component under `/usr/share/licenses/`.
