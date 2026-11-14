// The security profile vocabulary a person reads, shared by the Settings picker and the composer's pill. The ladder
// itself is the server's (policyfile.Profiles); only the words and the confirm live here.

import { confirm } from "./confirm.js";

/** The Custom profile's id, the one rung the client knows by name: the policy table is editable only there. */
export const CUSTOM_PROFILE = "custom";

/** The loosest profile's id, read only to decide which pick earns the extra confirm; a rename upstream costs the
 *  confirm, never what the server grants. */
export const LOOSEST_PROFILE_HINT = "unrestricted";

/** An id the client has no label for falls back to the id, which is ugly but true. */
export function profileLabel(id: string): string {
  switch (id) {
    case "guarded":
      return "Guarded";
    case "read-only":
      return "Read-only";
    case "trusted":
      return "Trusted";
    case "delegated":
      return "Approve for me";
    case "unrestricted":
      return "Unrestricted";
    case CUSTOM_PROFILE:
      return "Custom";
    default:
      return id;
  }
}

/** Each rung names what it reaches with no prompt: Read-only, files OUTSIDE the workspace; a rung that allows shell
 *  commands, any file through a redirect. */
export function profileDescription(id: string): string {
  switch (id) {
    case "guarded":
      return "Reads files in this workspace. Everything else asks.";
    case "read-only":
      return "Also reads any file on this machine and reaches the web. That includes files outside this workspace, so an SSH key or a sibling project is readable with no prompt. Every shell command and every change asks.";
    case "trusted":
      return "Also edits files in this workspace and runs everyday development commands. Destructive and irreversible ones still ask, including git push, reset and clean. An allowed shell command can write any file through a redirect, including files outside this workspace and in .kiro/agents, with no prompt.";
    case "delegated":
      return "Grants what Trusted grants, and Kiro's safety check answers the requests no rule settles: it approves a request it judges safe, blocks one it judges unsafe, and asks you when it is unsure. A request a rule sends to you, such as a file-tool write to .git or .kiro/agents, still asks. Each check is a model call. An allowed shell command can write any file through a redirect, including files outside this workspace and in .kiro/agents, with no prompt.";
    case "unrestricted":
      return "Never asks, including before installing a power. Kiro still protects its own settings and asks before its file tool writes .git, .kiro/agents and .kiro/hooks. A shell command can write any of these, Kiro's settings included, with no prompt.";
    case CUSTOM_PROFILE:
      return "Your own rules, edited in the Active policy table in Settings. Nothing is granted that you do not add.";
    default:
      return "";
  }
}

/** Whether the user's or workspace's saved grants reach past `id`: a session's presets only add to the files, and
 *  Unrestricted already grants everything while Custom is those rules. */
export function savedRulesStillApply(id: string, savedGrants: boolean): boolean {
  return savedGrants && id !== LOOSEST_PROFILE_HINT && id !== CUSTOM_PROFILE;
}

/** It is the one that grants `power`, so a power installed afterwards runs its author's code at this privilege with
 *  nothing asking, and it is also the one whose name invites a click. */
export function confirmLoosest(): Promise<boolean> {
  return confirm({
    title: "Allow every capability without asking?",
    body:
      `This includes "power", so a power you ` +
      `install runs its author's code at your privilege with no prompt. It does NOT ` +
      `silence every prompt: Kiro still refuses file-tool writes to its own settings ` +
      `directories and still asks before that tool writes .git, .kiro/agents, ` +
      `.kiro/hooks and .vscode. A shell command can write any of them with no prompt.`,
    confirmLabel: "Allow everything",
    variant: "destructive",
  });
}
