# Extra kiro-cli launch flags (`MAROTTE_KIRO_ACP_ARGS`)

This page is for anyone who needs a `kiro-cli acp` flag that marotte does not pass yet, without waiting for a release. List the flags separated by spaces, and marotte adds them to the agent engine's launch command:

```yaml
environment:
  MAROTTE_KIRO_ACP_ARGS: "-v"
```

Only the values are added. Nothing is read as a shell command.

marotte runs one agent engine for everything, so the flags reach every session on it: chats, the sessions that host workflow runs, and the short sessions marotte opens for its own reads, such as the permissions page. A flag kiro-cli rejects keeps the engine from starting, so every chat and workflow waits behind the restart banner until you remove the flag and recreate the container.

These flags are refused with a logged reason, because each one breaks the engine or does nothing:

| Flag | Why it is refused |
| --- | --- |
| `--agent-engine` | marotte speaks only the v3 protocol |
| `--auth-method`, `--authMethod` | marotte owns the Kiro sign-in and sets the sign-in method itself |
| `--agent` | kiro-cli rejects it and exits at startup, so no chat could open. Pick the role per chat with the mode pill |
| `--trust-all-tools`, `-a`, `--trust-tools` | kiro-cli rejects them and exits at startup, so no chat could open. Tool approval is the policy you edit on the **Permissions** tab in **Settings** |
| `--model`, `--effort` | kiro-cli rejects both and exits at startup, so no chat could open. Pick them per chat in the composer |

Anything else you set is a starting value the page can still change. Flags are logged by count only, never by value, so a mistyped value cannot leak into the logs.
