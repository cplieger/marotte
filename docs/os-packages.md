# OS packages

This page is for anyone who needs a Debian package the image does not ship. The tools engine installs OS packages, so there is no separate variable for them. Add one from the **Tools** tab in **Settings** the way you add anything else, or by name with an explicit source:

```json
{ "tools": { "gcc": { "source": "apt:gcc" }, "libc6-dev": { "source": "apt:libc6-dev" } } }
```

Debian packages install only when the container runs as root, with `user: "0:0"` in `compose.yaml`. As user 1000, the example's default, the engine reports apt as unavailable.

Two cases need this. Go work that runs `go test -race` needs a C compiler the image does not ship. And a runtime the engine installs can link a shared library the image lacks. The tool then installs and refuses to start, and the tools panel names the missing library on that runtime's row.

An `apt:` entry does more than `apt-get install` in the terminal, because the entry is kept. It lives on the `/config` volume, so recreating the container reinstalls the package instead of losing it, and the row reports the installed version. Removing the entry logs a message and uninstalls nothing, because apt packages are shared.

Use plain package names only. Each of these is refused with the reason:

- a version pin, such as `pkg=1.2`
- an architecture or a release, such as `pkg:arch` or `pkg/release`
- a trailing `-`, which apt reads as a removal
- a name absent from the package index
- a purely virtual package such as `awk`, where you name a real provider such as `mawk` instead

Pinning an entry keeps the installed version and marks it held in dpkg.

An `apt:` entry can only ever be a literal Debian package name, and Debian's signed package lists vouch for it. [Security](security.md#tool-installs) compares that with a `release:` entry.
