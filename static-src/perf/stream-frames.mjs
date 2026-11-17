/**
 * How many of sse-capture.py's records are frames the server sent the page: the hub's own `sse:`
 * frames (hello, reset) and the keepalive the hello names are left out, since the keepalive's
 * count measures the window's length, not the page's work.
 */
export function streamFrames(records) {
  return records.filter((f) => !f.event.startsWith("sse:") && !f.keepalive).length;
}
