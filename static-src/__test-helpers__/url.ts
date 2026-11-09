/** A request URL as the server reads it: its encoded path and its decoded query, so a test pins
 *  what is asked rather than how the query is spelled (`+` or `%20` for a space). */
export function asked(url: string): { path: string; query: Record<string, string> } {
  const u = new URL(url, "http://h");
  return { path: u.pathname, query: Object.fromEntries(u.searchParams) };
}
