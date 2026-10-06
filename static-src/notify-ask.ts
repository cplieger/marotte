// When the app may ask for notification permission. Firefox 72+ and Safari refuse
// `Notification.requestPermission()` outside user activation
// (https://developer.mozilla.org/en-US/docs/Web/API/Notification/requestPermission_static),
// so the moment a notification would fire ARMS the ask and the next click SPENDS it.
// DOM-free: every capability arrives through NotifyAskEnv; notify.ts binds globals.

/** Injected capabilities. `permission` is a plain string: an unrecognised newer
 *  value must degrade rather than be asserted. */
export interface NotifyAskEnv {
  /** Does this browser have the Notification API at all? False on iOS Safari
   *  outside an installed web app, and in a non-secure context. */
  supported: () => boolean;
  /** `"default"` | `"granted"` | `"denied"`, or anything else. */
  permission: () => string;
  /** Raise the prompt. Must be called from a user gesture. Resolves to the
   *  permission the browser settled on. */
  request: () => Promise<string>;
  /** Has this DEVICE already had its automatic ask? */
  spent: () => boolean;
  /** Record that it has. */
  markSpent: () => void;
  /** The browser granted permission: adopt it (turn the switch on, subscribe). */
  granted: () => void;
}

export interface NotifyAsk {
  /** Note that the app wanted to notify and could not; only an armed ask may prompt. */
  arm(): void;
  /** Note a user gesture: the ONLY moment a prompt may be raised. */
  gesture(): void;
  /** Record the ask as answered without raising anything: the Settings toggle
   *  going on (it prompts itself) or off (a refusal). */
  spend(): void;
}

/** ONE ASK PER DEVICE, because Settings is a standing door to the same prompt.
 *  Spending the ask when the switch goes OFF is what stops a later grant
 *  overriding that refusal: the wire's resolved boolean cannot tell "never opted
 *  in" from "switched off". */
export function createNotifyAsk(env: NotifyAskEnv): NotifyAsk {
  let armed = false;
  let requested = false;

  /** Re-read at both ends: a sibling tab can be granted or denied while this one
   *  waits for a click. */
  function askable(): boolean {
    return env.supported() && env.permission() === "default" && !env.spent();
  }

  return {
    arm(): void {
      if (armed || requested || !askable()) {
        return;
      }
      armed = true;
    },
    gesture(): void {
      if (!armed || requested) {
        return;
      }
      if (!askable()) {
        armed = false;
        return;
      }
      // Both flags before the call: a raised prompt is spent whatever comes back, a throw included.
      requested = true;
      armed = false;
      env.markSpent();
      void (async () => {
        let answer: string;
        try {
          answer = await env.request();
        } catch {
          return; // a browser that refuses to be asked is a badge-and-title browser
        }
        // Outside the try, so a throw from the adoption is not read as a refused prompt.
        if (answer === "granted") {
          env.granted();
        }
      })();
    },
    spend(): void {
      armed = false;
      requested = true;
      env.markSpent();
    },
  };
}
