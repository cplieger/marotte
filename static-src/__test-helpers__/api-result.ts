// The status-bearing GET served over a test's collapsed `apiGetTyped` fake, as `apiGetTyped`
// collapses that envelope: a case written against `data | null` drives both doors.

/** `apiGetTypedOrError` over `typed`: a null answer is a 503 with no body, anything else a 200. */
export function orErrorOver(
  typed: (...args: unknown[]) => unknown,
): (...args: unknown[]) => Promise<unknown> {
  return async (...args) => {
    const data: unknown = await typed(...args);
    return data === null
      ? { ok: false, status: 503, data: null, error: "unavailable" }
      : { ok: true, status: 200, data, error: "" };
  };
}
