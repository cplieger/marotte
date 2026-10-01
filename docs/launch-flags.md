# Extra kiro-cli launch flags (`MAROTTE_KIRO_ACP_ARGS`)

An escape hatch for a `kiro-cli acp` flag marotte does not pass yet, without
waiting for a release. Whitespace-separated, appended to every chat's launch
command:

```yaml
environment:
  MAROTTE_KIRO_ACP_ARGS: "-v"
```

Only the values are appended; nothing is interpreted as a shell command.

Five flags are refused with a logged reason, because each one breaks a chat or
does nothing:

| Flag                                 | Why it is refused                                                                            |
| ------------------------------------ | -------------------------------------------------------------------------------------------- |
| `--agent-engine`                     | marotte speaks only the v3 wire                                                              |
| `--trust-all-tools`, `--trust-tools` | inert on v3, where tool approval is the policy you edit in **Settings → Permissions**        |
| `--model`, `--effort`                | kiro-cli rejects both and exits before the session opens; pick them per chat in the composer |

Anything else you set is a starting value the UI still overrides. Flags are
logged by count only, never by value, so a mistyped value cannot leak into the
logs.
