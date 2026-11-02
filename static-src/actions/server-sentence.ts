/** The server's error sentence, or null. `@cplieger/fetch` falls back to the literal
 *  `HTTP <status>` for an empty body, and a transport failure (`status === 0`) carries a browser
 *  sentence, so both are compared exactly. */
export function serverSentence(err: {
  readonly message: string;
  readonly status?: number;
}): string | null {
  const status = err.status ?? 0;
  if (status < 400 || err.message === "" || err.message === `HTTP ${String(status)}`) {
    return null;
  }
  return err.message;
}
