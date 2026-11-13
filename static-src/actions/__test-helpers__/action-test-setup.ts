/** Shared action-test setup over @cplieger/reactive/actions' internals. `vi.mock()` calls stay at the top
 *  of each test file (hoisting); pass these factories as the implementation argument. */
import { configure, configureTransport } from "@cplieger/reactive/actions";
import { resetActionFramework as resetFramework } from "@cplieger/reactive/actions/testing";
import { error as toastError, success as toastSuccess } from "../../toast.js";
import { send as transportSend } from "../../transport.js";
import { forAction } from "../transport-result.js";

export function resetActionFramework(): void {
  resetFramework();
  configure({
    success: (msg) => {
      toastSuccess(msg);
    },
    error: (msg, retry) => {
      toastError(msg, retry);
    },
  });
  configureTransport(async (cmd, { signal }) =>
    forAction(
      await transportSend(cmd as Parameters<typeof transportSend>[0], {
        signal,
        reportSendState: false,
      }),
    ),
  );
}

/** A header from a mocked `fetch` call's RequestInit, case-insensitive, whatever the headers'
 *  shape: since actions 2.0.7 the request core passes a `Headers` instance, so a bracket lookup
 *  reads `undefined`. */
export function headerValue(init: RequestInit | undefined, name: string): string | undefined {
  const h = init?.headers;
  if (h === undefined) {
    return undefined;
  }
  if (h instanceof Headers) {
    return h.get(name) ?? undefined;
  }
  const lower = name.toLowerCase();
  const entries = Array.isArray(h) ? h : Object.entries(h);
  for (const [k, v] of entries) {
    if (k.toLowerCase() === lower) {
      return v;
    }
  }
  return undefined;
}
