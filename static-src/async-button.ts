import {
  withAsyncFeedback as feedback,
  type AsyncFeedbackOptions,
} from "@cplieger/reactive/actions";

import { spinnerEl } from "./spinner.js";

/** `@cplieger/reactive`'s `withAsyncFeedback`, its pending face the small `.spinner` ring unless `opts` names one. */
export function withAsyncFeedback(
  btn: HTMLButtonElement,
  fn: () => Promise<unknown>,
  opts: AsyncFeedbackOptions = {},
): Promise<void> {
  return feedback(btn, fn, {
    renderPending: () => spinnerEl("spinner-sm btn-async-spinner"),
    ...opts,
  });
}
