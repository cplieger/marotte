# Security

This page is for anyone deciding where to run marotte and who can reach it. It covers access, the user the container runs as, a read-only root, tool installs, what marotte sends out, and how kiro-cli is installed.

## Who can reach it

marotte has no login of its own. Anyone who can reach port 9847 (or the port `LISTEN_ADDR` names) can use the agent, which runs commands and reads and writes files under `/workspace` and any [additional directory](configuration.md#folders-the-agent-can-use-beyond-workspace) you add. They can also use the Kiro sign-in marotte keeps in `/config`. Keep the port on your private network, or put marotte behind a [reverse proxy](https://github.com/cplieger/docs/blob/main/docs/reverse-proxy.md) that asks for a login. Caddy forward-auth, oauth2-proxy and Authentik all work. Doing both is better.

Signing in on the page gives the agent your Kiro account. That is the agent's identity, and it does not protect marotte itself. marotte keeps the sign-in in `/config/signin.json`, which only the container's user may read or write. marotte tightens a file that others can read, and refuses to use one it cannot tighten. It never reads the sign-in of a `kiro-cli` you run in the terminal.

The metrics page at `/metrics` answers with no login. It shows whether the agent engine is ready and how much memory it uses, and counts failed prompts by class. It also counts each schedule's slots under the schedule's random id, and the outcomes of unattended runs and of the permission requests marotte answered in automatic Bot turns. It shows no chat text, workflow name, path, command or credential. Behind a login proxy, exempt `/metrics` there, or scrape the container only from a network you already trust with the agent. [Monitoring and alerts](monitoring.md) lists every series.

Set `ALLOWED_HOSTS` on any server that stays up, and set `TRUSTED_PROXIES` when a proxy sits in front. [Configuration](configuration.md) explains both. Requests that change state from another website are refused with `403`. The one route that skips `ALLOWED_HOSTS`, the [notify route](configuration.md#waking-a-chat-from-another-service-marotte_notify_token), answers only a request carrying `MAROTTE_NOTIFY_TOKEN`, and that token opens no other route.

The file browser shows only the folders it is given, `/workspace`, `/config`, `/uploads` and the agent's additional directories by default. Credential and internal files under `/config`, such as SSH keys, cloud tokens, forge credentials, the Kiro sign-in, the chat store and the MCP configuration, stay hidden. The preview tab follows the same list. It refuses a page whose folder holds those files or sits among them.

## The container user

The example `compose.yaml` runs the container as user 1000, or as the `PUID` and `PGID` from `.env`. The files it writes on the host then belong to you. You can run it as root instead with `user: "0:0"`. You then no longer give the host folders to user 1000, and you can install Debian packages from [OS packages](os-packages.md), which install only as root. It also makes the agent's terminal a root shell inside the container.

The image is built on Debian, which has the shell the terminal needs and runs kiro-cli as its own program.

## Read-only root

marotte runs with `read_only: true`, which stops anything inside the container from changing the image's own files. As user 1000, the example's default, it needs one `tmpfs` beside the three folders the example already mounts. A `tmpfs` is a folder kept in memory that is emptied when the container stops. Add `read_only: true` and the `tmpfs:` lines to the service:

```yaml
services:
  marotte:
    image: ghcr.io/cplieger/marotte:latest
    user: "${PUID:-1000}:${PGID:-1000}"
    read_only: true
    tmpfs:
      - /tmp:rw,exec,nosuid,nodev,size=512m,mode=1777
      # when the container runs as root, also:
      # - /etc/profile.d:rw,noexec,nosuid,nodev,size=1m,mode=755
    volumes:
      - "/opt/appdata/marotte/config:/config"
      - "/opt/appdata/marotte/workspace:/workspace"
      - "/opt/appdata/marotte/uploads:/uploads"
```

Each folder holds this:

- `/tmp` holds kiro-cli's logs in `/tmp/kiro-log` and its update check folder. It also holds what the **Shell** panel and the agent's commands write there, such as build files and test binaries. marotte itself uses less than 200 KB, so the 512 MB are room for those programs. Docker mounts a `tmpfs` with `noexec` by default, so the line adds `exec` for test runners that start programs from `/tmp`. Mode 1777 matches the image's own `/tmp`.
- `/etc/profile.d` is needed only when the container runs as root, with `user: "0:0"`. marotte writes `10-marotte-path.sh` there at each start, which tells the **Shell** panel's login shell where kiro-cli and the installed tools are. Without it, the log warns that marotte could not write that file, and the **Shell** panel no longer finds those programs by name. The folder is empty in the image, so the mount hides nothing.
- `/config` holds the chats, the Kiro sign-in, the kiro-cli install with its unpacked runtime, and the tools you install.
- `/workspace` holds your repositories.
- `/uploads` holds the files you attach. Under `read_only: true` it must stay a mount, or attaching a file fails.

Each `tmpfs` counts against the container's memory limit. A program in the **Shell** panel that writes anywhere else, such as `/var/tmp`, gets `Read-only file system`.

Debian packages from [OS packages](os-packages.md) do not install on a read-only root. apt and dpkg write the package database in `/var/lib/dpkg`, and they unpack each package into `/usr`, `/etc` and `/lib`, which are the image's own folders. A `tmpfs` on `/var/lib/dpkg` would hide the image's record of its installed packages, and one on `/usr` would hide its programs. Leave out `read_only: true` when you install Debian packages.

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

AWS may use what marotte sends the model, which is your chats and workflow runs, to improve its service. The **Share content with AWS for service improvement** switch under **Data sharing** in **Settings** decides it. It is on by default, as in kiro-cli, and a switch you turned off stays off. On an enterprise account, the organization's setting decides it instead. kiro-cli run in the Shell panel follows its own setting, which [Content sharing with AWS](configuration.md#content-sharing-with-aws) covers.

kiro-cli's own telemetry is on by default, as in kiro-cli. The **kiro-cli telemetry** switch under **Data sharing** in **Settings** covers both kinds: its usage telemetry, which sends usage data linked to your signed-in account, and its legacy toolkit telemetry. Turned off, it stops both. On an enterprise account, the organization's usage-analytics setting decides it instead. Push notifications go only to the browser vendors' push services, over HTTPS on port 443. Each push connection is checked again after the name is resolved.

## How kiro-cli is installed

kiro-cli is downloaded on first start, not built into the image, because the AWS Customer Agreement governs passing it on. You accept that agreement by starting the container. A newer kiro-cli arrives with a newer image tag. kiro-cli's own self-update is switched off.

The download is checked against a SHA-256 checksum pinned for your architecture before anything is installed. Each version goes into its own folder under `kiro-cli-versions/<version>/` in the tools folder (by default `/config/tools`, moved by `MAROTTE_TOOLS_DIR`), and it is checked again on every start. A replaced or half-restored install is refused rather than run. Before installing, marotte checks who can write to the folders on the way. It refuses when any account other than the container's own or root can, because the container runs what lands there. `TRUSTED_INSTALL_UIDS` in [Configuration](configuration.md) is for a volume where that is expected, and the tools engine honours the same list. The tools engine also refuses a folder it manages that another account owns or can write through an access-control list. marotte repairs only mode bits at start. Fix an owner by hand with `chown` inside the container, and remove a list grant on the host, because the image has no ACL tools: `setfacl -b` for a POSIX list, or the host's NFSv4 ACL tool on ZFS or NFS. Then restart.

## The image

Images are published with cosign signatures and SBOM attestations, which [Checking a signature](https://github.com/cplieger/docs/blob/main/docs/images.md#checking-a-signature) and [Reading the software bill of materials](https://github.com/cplieger/docs/blob/main/docs/images.md#reading-the-software-bill-of-materials) show how to check. The image carries the license text of every bundled component under `/usr/share/licenses/`.
