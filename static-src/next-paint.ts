/** Run `fn` in a task after the next frame paints, so what the current task wrote is on screen
 *  first. A hidden page fires no frame callbacks (a notification click can act on one), so it gets
 *  a bare task. */
export function afterNextPaint(fn: () => void): void {
  if (document.hidden) {
    postUrgent(fn);
    return;
  }
  requestAnimationFrame(() => {
    postUrgent(fn);
  });
}

type PostTask = (cb: () => void, opts: { priority: "user-blocking" }) => Promise<unknown>;

/** A user-blocking task where the engine has `postTask`: a `scheduler.yield` continuation runs
 *  ahead of a plain timeout (https://developer.chrome.com/blog/use-scheduler-yield). A throw is
 *  reported as a timeout's would be. */
function postUrgent(fn: () => void): void {
  const sched = (globalThis as { scheduler?: { postTask?: PostTask } }).scheduler;
  if (sched?.postTask === undefined) {
    setTimeout(fn, 0);
    return;
  }
  sched.postTask.call(sched, fn, { priority: "user-blocking" }).catch(reportError);
}
