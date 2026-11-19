#!/usr/bin/env bash
# Boots the image the way the example compose runs it (uid 1000, read-only root) and checks the PATH
# a login shell gets there. Usage: tests/image-test.sh [IMAGE] (default: ci-smoke:latest)
set -euo pipefail

IMAGE="${1:-ci-smoke:latest}"
NAME="smoke-marotte-login-$$"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  docker logs "$NAME" 2>&1 | tail -25 >&2 || true
  exit 1
}
cleanup() {
  docker rm -f "$NAME" >/dev/null 2>&1 || true
}
trap 'exit 143' TERM HUP
trap cleanup EXIT

command -v docker >/dev/null 2>&1 || fail "docker is required"
docker image inspect "$IMAGE" >/dev/null 2>&1 || fail "image $IMAGE not found"
image_path=$(docker image inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$IMAGE" | sed -n 's/^PATH=//p')
[ -n "$image_path" ] || fail "image $IMAGE sets no PATH"

# No network, so the first boot's kiro-cli download fails at once rather than fetching it. The
# moved tools folder makes the boot's PATH differ from the image's.
docker run -d --name "$NAME" --network none --user 1000:1000 --read-only \
  --tmpfs /tmp:rw,exec,nosuid,nodev,size=64m,mode=1777 \
  --tmpfs /config:rw,uid=1000,gid=1000,mode=755 \
  --tmpfs /workspace:rw,uid=1000,gid=1000,mode=755 \
  --tmpfs /uploads:rw,uid=1000,gid=1000,mode=755 \
  -e MAROTTE_TOOLS_DIR=/config/moved \
  "$IMAGE" >/dev/null || fail "docker run failed"

# Starts the browser Shell panel's `bash --login` the way the server does, with the server's own
# environment, and prints the server's PATH, a line feed, then the login shell's. The server is
# tini's child; a healthcheck's /app/marotte is not.
# shellcheck disable=SC2016 # expanded inside the container
as_server_child='
for p in /proc/[0-9]*; do
  [ "$(readlink "$p/exe" 2>/dev/null)" = /app/marotte ] || continue
  { read -r _ _ _ ppid _ <"$p/stat"; } 2>/dev/null || continue
  [ "$(readlink "/proc/$ppid/exe" 2>/dev/null)" = /usr/bin/tini ] || continue
  mapfile -d "" env <"$p/environ"
  for kv in "${env[@]}"; do
    case "$kv" in PATH=*) printf "%s\n" "${kv#PATH=}" ;; esac
  done
  exec env -i "${env[@]}" bash --login -c "printf %s \"\$PATH\"" </dev/null
done
exit 3
'
deadline=$(($(date +%s) + 60))
until out=$(docker exec -u 1000:1000 "$NAME" bash -c "$as_server_child" 2>/dev/null); do
  [ "$(docker inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null)" = true ] || fail "the container exited during boot"
  [ "$(date +%s)" -lt "$deadline" ] || fail "no server process within 60 s"
  sleep 1
done
server_path=${out%%$'\n'*}
login_path=${out#*$'\n'}
case ":$server_path:" in
  *:/config/moved/bin:*) ;;
  *) fail "the server's PATH does not search the moved tools folder: $server_path" ;;
esac
[ "$login_path" = "$server_path" ] \
  || fail "a uid 1000 login shell the server starts has PATH '$login_path', want the boot's '$server_path'"
printf 'ok   a uid 1000 login shell the server starts on a read-only root gets the boot PATH, moved tools folder included\n'

got=$(docker exec -u 1000:1000 "$NAME" bash -lc 'printf %s "$PATH"' </dev/null) || fail "docker exec bash -l failed"
[ "$got" = "$image_path" ] || fail "a uid 1000 docker exec login shell has PATH '$got', want the image's '$image_path'"
printf 'ok   a uid 1000 docker exec login shell gets the image PATH\n'

printf 'marotte image smoke: ok\n'
