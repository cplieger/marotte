// vitest's browser orchestrator reads any `pagehide` on the tester window as the
// page unloading and, 5 s later, fails the still-running file with "Cannot connect
// to the iframe" (`watchUnload`, added by https://github.com/vitest-dev/vitest/pull/11525).
// Tests drive the page lifecycle with synthetic events, so this window's pagehide
// and pageshow listeners, and the events a test dispatches on it, go through a
// private target the orchestrator never listens on.
// page-lifecycle-setup.node.test.ts fails once vitest ignores an untrusted pagehide.

const LIFECYCLE: ReadonlySet<string> = new Set(["pagehide", "pageshow"]);
const lifecycle = new EventTarget();
const add = window.addEventListener.bind(window);
const remove = window.removeEventListener.bind(window);
const dispatch = window.dispatchEvent.bind(window);
const own = (value: unknown): PropertyDescriptor => ({
  value,
  configurable: true,
  writable: true,
});

Object.defineProperties(window, {
  addEventListener: own(
    (
      type: string,
      listener: EventListenerOrEventListenerObject | null,
      options?: boolean | AddEventListenerOptions,
    ): void => {
      (LIFECYCLE.has(type) ? lifecycle.addEventListener.bind(lifecycle) : add)(
        type,
        listener,
        options,
      );
    },
  ),
  removeEventListener: own(
    (
      type: string,
      listener: EventListenerOrEventListenerObject | null,
      options?: boolean | EventListenerOptions,
    ): void => {
      (LIFECYCLE.has(type) ? lifecycle.removeEventListener.bind(lifecycle) : remove)(
        type,
        listener,
        options,
      );
    },
  ),
  dispatchEvent: own((event: Event): boolean =>
    LIFECYCLE.has(event.type) ? lifecycle.dispatchEvent(event) : dispatch(event),
  ),
});

// A module, so the bindings above stay out of the global scope of every other file.
export {};
