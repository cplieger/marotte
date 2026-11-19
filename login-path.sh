# shellcheck shell=sh
# Installed as /etc/profile.d/10-marotte-path.sh. Debian's /etc/profile assigns PATH for every uid,
# dropping the tools folder, and sources this file after it. MAROTTE_LOGIN_PATH is the boot's PATH,
# which entrypoint.sh's load_boot_paths hands the server's tree; a shell started outside that tree,
# such as `docker exec`, gets the image's ENV PATH.
PATH=${MAROTTE_LOGIN_PATH:-/config/tools/bin:/config/tools/go/bin:/config/home/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin}
export PATH
# The image ENV's self-update switch, for a login shell started from a cleared environment.
KIRO_NO_AUTO_UPDATE=1
export KIRO_NO_AUTO_UPDATE
