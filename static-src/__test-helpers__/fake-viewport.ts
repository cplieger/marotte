// A stand-in for `window.visualViewport`, whose offset and height headless Chromium cannot move:
// install it with `vi.stubGlobal("visualViewport", fake)` and fire its events by hand.

type Listener = () => void;

export interface FakeViewport {
  offsetLeft: number;
  offsetTop: number;
  width: number;
  height: number;
  addEventListener(type: string, fn: Listener): void;
  removeEventListener(type: string, fn: Listener): void;
  fire(type: string): void;
  listeners(): number;
}

export function fakeViewport(
  height: number,
  offsetTop = 0,
  width = 0,
  offsetLeft = 0,
): FakeViewport {
  const map = new Map<string, Set<Listener>>();
  return {
    offsetLeft,
    offsetTop,
    width,
    height,
    addEventListener(type, fn): void {
      let set = map.get(type);
      if (set === undefined) {
        set = new Set();
        map.set(type, set);
      }
      set.add(fn);
    },
    removeEventListener(type, fn): void {
      map.get(type)?.delete(fn);
    },
    fire(type): void {
      for (const fn of [...(map.get(type) ?? [])]) {
        fn();
      }
    },
    listeners(): number {
      let n = 0;
      for (const set of map.values()) {
        n += set.size;
      }
      return n;
    },
  };
}
