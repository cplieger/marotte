#!/usr/bin/env bash
# Runs every entrypoint.sh unit test here, each as its own process, all even after a failure. The
# filename is the contract: cplieger/ci's shell-ci.yaml runs it via `bash` when it exists. The
# REFUSALS no healthy image takes are asserted on disk and by warning line, never by exit status.
set -u

cd -- "$(dirname -- "$0")" || exit 1

failed=0
ran=0
for t in ./*_test.sh; do
  # An unmatched glob expands to itself: an empty suite is a harness fault, not a green run.
  if [ ! -f "$t" ]; then
    printf 'harness error: no *_test.sh found in %s\n' "$PWD" >&2
    exit 1
  fi
  printf '=== %s\n' "$(basename "$t")"
  if bash "$t"; then
    ran=$((ran + 1))
  else
    ran=$((ran + 1))
    failed=$((failed + 1))
  fi
  printf '\n'
done

if [ "$failed" -ne 0 ]; then
  printf 'FAILED: %d of %d entrypoint test files failed\n' "$failed" "$ran" >&2
  exit 1
fi
printf 'all %d entrypoint test files passed\n' "$ran"
