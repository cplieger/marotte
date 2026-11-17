/** Runs `repaint`. When focus sat inside `scope` and the repaint took it out (it removed or
 *  re-seated the focused node), focus moves to `successor()`, so it never falls to `<body>`. */
export function repaintKeepingFocus(
  scope: HTMLElement,
  successor: () => HTMLElement | null,
  repaint: () => void,
): void {
  const held = scope.contains(document.activeElement);
  repaint();
  if (held && !scope.contains(document.activeElement)) {
    successor()?.focus({ preventScroll: true });
  }
}
