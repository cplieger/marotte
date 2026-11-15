// When a shell driver's command ended: emit.py's last line, `emit.py done <pid>`, arriving with a
// pid no earlier frame carried. Bytes in are no proof of it: a socket that drops and resumes gets
// only the engine's bounded tail of the scrollback (web-terminal-engine terminal.replayStart).

const DONE = /emit\.py done (\d+)/g;

/** The emit.py pids one shell socket frame names in a done line. A run's text is plain UTF-8 in
 *  the binary frame (web-terminal-engine docs/wire-protocol.md "Row payload"). */
export function donePids(payload) {
  const text = typeof payload === "string" ? payload : Buffer.from(payload).toString("latin1");
  return Array.from(text.matchAll(DONE), (m) => m[1]);
}
