/** Resolves after `n` zero-delay timer tasks, each queued behind every task already waiting. A
 *  negative assertion over a chain of promises and zero-delay tasks (a mocked round trip, a
 *  yielded build) waits on the queue that chain runs on, so its verdict does not depend on how
 *  fast the runner is. Needs a real `setTimeout`. */
export async function taskTurns(n = 3): Promise<void> {
  for (let i = 0; i < n; i++) {
    await new Promise<void>((resolve) => {
      setTimeout(resolve, 0);
    });
  }
}
