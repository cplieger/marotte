/**
 * roving-focus.ts — WAI-ARIA roving-tabindex keyboard navigation for
 * composite widgets (menus, listboxes, pickers, toolbars): one Tab stop,
 * arrow keys move focus. Items are queried live each keystroke; call
 * `refresh()` after a bulk re-render to restore the single-Tab-stop invariant.
 *
 * Pairs with popover for the WAI-ARIA menu pattern:
 *
 * ```ts
 * const pop = createPopover(button, panel, { haspopup: "menu" });
 * const nav = rovingFocus(panel, "[role=menuitem]");
 * // on open: nav.focusFirst();
 * ```
 *
 * @module
 */

import { untracked } from "@cplieger/reactive";

import { createDriver, ownedBinding } from "./owner.js";

/** Keyboard behavior of a roving-tabindex container: which arrow pair moves
 *  focus, whether the ends wrap, whether Home/End jump, and whether
 *  Enter/Space activate the focused item. The defaults are the WAI-ARIA
 *  vertical-menu shape. */
interface RovingFocusOptions {
  /** Which arrow pair moves focus: `"vertical"` (Up/Down, default) or
   *  `"horizontal"` (Left/Right). */
  orientation?: "vertical" | "horizontal";
  /** Wrap from the last item to the first and vice versa. Default `true`. */
  wrap?: boolean;
  /** Home/End jump to the first/last item. Default `true`. */
  homeEnd?: boolean;
  /** Enter and Space activate (click) the focused item. Default `true`.
   *  Disable when items are inputs or handle their own keys. */
  activate?: boolean;
}

/** Handle on a wired container. Items are re-queried on every keystroke, so
 *  adding or removing one needs no call here; `refresh()` exists for a bulk
 *  re-render, which discards the element carrying the single `tabindex=0` Tab
 *  stop. */
export interface RovingFocusController {
  /** Focus the first item (e.g. when a menu opens). */
  focusFirst(): void;
  /** Re-apply the roving tabindex after a bulk re-render: the current item
   *  (focused, or the previous Tab stop if still present) keeps `tabindex=0`,
   *  everything else gets `-1`. */
  refresh(): void;
  /** Remove the listeners. Tabindex attributes are left as-is, and
   *  `focusFirst()` and `refresh()` do nothing from then on. */
  dispose(): void;
}

/** Wire roving-tabindex arrow-key navigation over `container`'s descendants
 *  matching `selector`. Returns a controller with `focusFirst` / `refresh` /
 *  `dispose`. */
export function rovingFocus(
  container: HTMLElement,
  selector: string,
  opts?: RovingFocusOptions,
): RovingFocusController {
  const o = untracked(() => ({
    vertical: (opts?.orientation ?? "vertical") !== "horizontal",
    wrap: opts?.wrap ?? true,
    homeEnd: opts?.homeEnd ?? true,
    activate: opts?.activate ?? true,
  }));
  const prevKey = o.vertical ? "ArrowUp" : "ArrowLeft";
  const nextKey = o.vertical ? "ArrowDown" : "ArrowRight";

  interface State {
    /** The committed tab stop. */
    readonly stop: HTMLElement | null;
    /** Moves at every refresh(), whose job is to re-apply even an unchanged stop. */
    readonly sync: number;
  }

  return ownedBinding((binding) => {
    const items = (): HTMLElement[] => [...container.querySelectorAll<HTMLElement>(selector)];
    let listening = false;

    const onKeyDown = (e: KeyboardEvent): void => {
      driver.act((fx) => {
        const list = items();
        if (list.length === 0) {
          return;
        }
        const active = document.activeElement;
        const current = active instanceof HTMLElement ? list.indexOf(active) : -1;
        let next: number;
        switch (e.key) {
          case nextKey:
            next = current < list.length - 1 ? current + 1 : o.wrap ? 0 : list.length - 1;
            break;
          case prevKey:
            next = current > 0 ? current - 1 : o.wrap ? list.length - 1 : 0;
            break;
          case "Home":
            if (!o.homeEnd) {
              return;
            }
            next = 0;
            break;
          case "End":
            if (!o.homeEnd) {
              return;
            }
            next = list.length - 1;
            break;
          case "Enter":
          case " ":
          case "Spacebar": {
            const item = list[current];
            if (o.activate && item !== undefined) {
              // Suppress the native Enter/Space activation and fire exactly one click.
              e.preventDefault();
              fx.click(item);
            }
            return;
          }
          default:
            return;
        }
        e.preventDefault();
        const target = list[next];
        if (target !== undefined) {
          fx.focus(target);
        }
      });
    };
    const onFocusIn = (e: FocusEvent): void => {
      const target = e.target;
      if (binding.ended || !(target instanceof HTMLElement) || !target.matches(selector)) {
        return;
      }
      driver.commit(
        driver.state.stop === target ? driver.state : { ...driver.state, stop: target },
      );
    };
    const listen = (on: boolean): void => {
      if (listening !== on) {
        listening = on;
        const method = on ? "addEventListener" : "removeEventListener";
        container[method]("keydown", onKeyDown as EventListener);
        container[method]("focusin", onFocusIn as EventListener);
      }
    };

    const driver = createDriver<State>({
      initial: { stop: null, sync: 0 },
      realize(fx, d) {
        listen(true);
        const list = items();
        const target = d.stop !== null && list.includes(d.stop) ? d.stop : (list[0] ?? null);
        for (const item of list) {
          fx.setAttr(item, "tabindex", item === target ? "0" : "-1");
        }
      },
      terminal() {
        listen(false);
      },
    });

    return {
      controller: {
        focusFirst(): void {
          driver.act((fx) => {
            const first = items()[0];
            if (first !== undefined) {
              fx.focus(first);
            }
          });
        },
        refresh(): void {
          if (binding.ended) {
            return;
          }
          // Re-deriving the stop from the DOM is this method's contract.
          const list = items();
          const active = document.activeElement;
          const stop =
            active instanceof HTMLElement && list.includes(active)
              ? active
              : (list.find((i) => i.getAttribute("tabindex") === "0") ?? null);
          driver.commit({ stop, sync: driver.state.sync + 1 });
        },
        dispose: binding.dispose,
      },
      start: () => {
        driver.flush();
      },
      release: () => {
        driver.release();
      },
    };
  });
}
