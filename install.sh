#!/usr/bin/env bash
set -euo pipefail
if [[ ${EUID} -ne 0 ]]; then
    echo '请使用 sudo ./install.sh --config gost.json' >&2
    exit 1
fi
source /etc/os-release
case "${ID}:${VERSION_ID}" in
    ubuntu:20.04|ubuntu:22.04|ubuntu:24.04|debian:12|debian:13) ;;
    *) echo '支持 Ubuntu 20.04/22.04/24.04、Debian 12/13。' >&2; exit 1 ;;
esac
if ! command -v python3 >/dev/null || ! command -v useradd >/dev/null || ! dpkg-query -W -f='${Status}' ca-certificates 2>/dev/null | grep -qx 'install ok installed'; then
    apt-get update
    apt-get install -y --no-install-recommends python3 ca-certificates passwd
fi
repo_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
exec python3 "${repo_dir}/gostctl" install "$@"
