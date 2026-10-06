// URL scheme guard for the MCP OAuth pill (mcp-ui.ts) and the open_external_url banner: a
// server-supplied URL becomes a link only when http/https, never file:, javascript:, data: or an
// app scheme. The server applies the same check; this is the client-side second layer.

/** Returns true when `url` parses and uses the http or https scheme. */
export function isSafeURL(url: string): boolean {
  try {
    const u = new URL(url);
    return u.protocol === "http:" || u.protocol === "https:";
  } catch {
    return false;
  }
}
