// The action framework's notifier, recording what production's `initActions` hands to
// `actionNotice`: the staged subject and name, the sentence and the Retry button.
import { configure, type NotifierRetry } from "@cplieger/ui/actions";
import { takeSubject } from "../actions/subject.js";

export interface Notice {
  readonly level: "success" | "error";
  readonly subject: string;
  readonly name: string;
  readonly message: string;
  readonly retry: NotifierRetry | undefined;
}

/** Configure the notifier to record into the returned list. Call after `resetActionFramework`. */
export function recordNotices(): Notice[] {
  const seen: Notice[] = [];
  configure({
    success: (message) => {
      const { subject, name } = takeSubject();
      seen.push({ level: "success", subject, name, message, retry: undefined });
    },
    error: (message, retry) => {
      const { subject, name } = takeSubject();
      seen.push({ level: "error", subject, name, message, retry });
    },
  });
  return seen;
}
