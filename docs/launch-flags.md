# Extra kiro-cli launch flags (`MAROTTE_KIRO_ACP_ARGS`)

This page is for anyone who needs a `kiro-cli acp` flag that marotte does not pass yet, without waiting for a release. List the flags separated by spaces, and marotte adds them to every chat's launch command:

```yaml
environment:
  MAROTTE_KIRO_ACP_ARGS: "-v"
```

Only the values are added. Nothing is read as a shell command.

These flags are refused with a logged reason, because each one breaks a chat or does nothing:

| Flag | Why it is refused |
| --- | --- |
| `--agent-engine` | marotte speaks only the v3 protocol |
| `--auth-method`, `--authMethod` | marotte sets the sign-in method itself, and a wrong value makes kiro-cli exit before the chat starts |
| `--agent` | kiro-cli rejects it and exits before the session opens. Pick the role per chat with the mode pill |
| `--trust-all-tools`, `-a`, `--trust-tools` | They have no effect on v3, where tool approval is the policy you edit on the **Permissions** tab in **Settings** |
| `--model`, `--effort` | kiro-cli rejects both and exits before the session opens. Pick them per chat in the composer |

Anything else you set is a starting value the page can still change. Flags are logged by count only, never by value, so a mistyped value cannot leak into the logs.
