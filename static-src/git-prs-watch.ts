// Whether this tab shows the PR list: the git view on screen, its PRs panel active, the page visible. The `hidden`
// class is the one state every way in and out writes, so it is observed rather than mirrored.

/** One poll interval, the cadence the watch holds the poller at. */
const WATCH_RENEW_MS = 60_000;

/**
 * This page's name in every watch. The SSE-Client tag is the profile's, shared by sibling pages, so the server keys a
 * viewer on the tag and this name together.
 */
export const VIEW_PAGE = Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) =>
  b.toString(16).padStart(2, "0"),
).join("");

/** Call `post` with true when the list comes on screen and false when it leaves,
 *  and with true every `WATCH_RENEW_MS` while it stays. Answers the teardown. A
 *  page without the git view observes nothing. */
export function observePRView(post: (watching: boolean) => void): () => void {
  const view = document.getElementById("git-view");
  const panel = document.querySelector<HTMLElement>('[data-git-panel="prs"]');
  if (view === null || panel === null) {
    return () => undefined;
  }
  let shown = false;
  let renew: ReturnType<typeof setInterval> | undefined;
  const sync = (): void => {
    const now =
      !view.classList.contains("hidden") &&
      !panel.classList.contains("hidden") &&
      document.visibilityState === "visible";
    if (now === shown) {
      return;
    }
    shown = now;
    clearInterval(renew);
    renew = now
      ? setInterval(() => {
          post(true);
        }, WATCH_RENEW_MS)
      : undefined;
    post(now);
  };
  const observer = new MutationObserver(sync);
  for (const node of [view, panel]) {
    observer.observe(node, { attributes: true, attributeFilter: ["class"] });
  }
  document.addEventListener("visibilitychange", sync);
  sync();
  return () => {
    observer.disconnect();
    document.removeEventListener("visibilitychange", sync);
    clearInterval(renew);
  };
}
