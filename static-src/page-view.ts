// What this page tells the server about itself: whether it is visible and focused, and the chat or
// run tab it shows, so the server's notice ledger offers each notice to one page per profile.

const page = crypto.randomUUID();

/** This page load's id, the one `show_in` names. */
export function pageID(): string {
  return page;
}

/** Start reporting this page's view; returns the teardown. */
export function installPageView(): () => void {
  return () => {
    /* noop */
  };
}
