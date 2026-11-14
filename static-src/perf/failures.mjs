// What fails one measured window of view.mjs, each message naming the window. A window's
// `tabSwitch` and `settle` are settle() results, and its `drive` a driver's.

const where = (st) =>
  `at ${String(st.path)}, ready ${String(st.ready)}, ${String(st.inflight)} reads in flight`;

export function windowFailures(name, w) {
  const out = [];
  if (w.tabSwitch && !w.tabSwitch.settled) {
    out.push(`${name}: the tab switch did not settle (${where(w.tabSwitch)})`);
  }
  if (w.settle && !w.settle.settled) {
    out.push(`${name}: did not settle (${where(w.settle)})`);
  }
  if (w.drive && !w.drive.done) {
    out.push(`${name}: the driver did not finish`);
  }
  return out;
}
