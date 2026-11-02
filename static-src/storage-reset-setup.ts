// Browser Mode gives each file a fresh window on one shared origin, so Web Storage outlives the
// file that wrote it. A file that fails mid-test (each killed Stryker mutant ends one) leaves its
// blob behind, and the next file's absence assertions fail for a reason that is not theirs.
localStorage.clear();
sessionStorage.clear();
