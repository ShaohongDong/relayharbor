#!/usr/bin/env bash
set -euo pipefail
umask 077
if [[ ${EUID} -ne 0 ]]; then
    echo '请使用 sudo bash install.sh（可加 --non-interactive --setup-host IP）' >&2
    exit 1
fi
source /etc/os-release
case "${ID}:${VERSION_ID}" in
    ubuntu:20.04|ubuntu:22.04|ubuntu:24.04|debian:12|debian:13) ;;
    *) echo '支持 Ubuntu 20.04/22.04/24.04、Debian 12/13。' >&2; exit 1 ;;
esac
repo_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
release_version=''
setup_host=''
setup_port=18443
interactive=true
forward=()
while (($#)); do
    case "$1" in
        --release-version|--setup-host|--setup-port)
            if (($# < 2)); then echo "$1 缺少参数" >&2; exit 1; fi
            case "$1" in
                --release-version) release_version=$2 ;;
                --setup-host) setup_host=$2 ;;
                --setup-port) setup_port=$2 ;;
            esac
            shift 2 ;;
        --non-interactive) interactive=false; shift ;;
        --accept-tos) forward+=("$1"); shift ;;
        --config|--version|--archive|--checksums|--domain|--email|--lego-archive|--lego-checksums)
            if (($# < 2)); then echo "$1 缺少参数" >&2; exit 1; fi
            forward+=("$1" "$2"); shift 2 ;;
        --help|-h)
            echo '用法：sudo bash install.sh [--release-version latest|X.Y.Z] [--setup-host IP或域名] [--setup-port 18443] [--non-interactive]'
            echo '可选：--config FILE、--version GOST_VERSION、--archive FILE --checksums FILE；域名 HTTPS 也可在网页设置。'
            exit 0 ;;
        *) echo "未知参数：$1" >&2; exit 1 ;;
    esac
done
case "$(uname -m)" in x86_64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) echo '仅支持 amd64/arm64' >&2; exit 1 ;; esac
local_bundle=false
if [[ -x ${repo_dir}/bin/gostctl && -f ${repo_dir}/bin/SHA256SUMS && -z ${release_version} ]]; then local_bundle=true; fi
if [[ ${interactive} == true ]]; then
    if ! { exec 3<>/dev/tty; } 2>/dev/null; then
        echo '无可用终端；请使用 --non-interactive --setup-host IP。' >&2; exit 1
    fi
    if [[ ${local_bundle} == false && -z ${release_version} ]]; then
        printf 'RelayHarbor 发行版本 [latest]：' >&3
        IFS= read -r release_version <&3
    fi
    if [[ -z ${setup_host} ]]; then
        read -r -a local_addresses <<< "$(hostname -I)"
        suggested_host=${local_addresses[0]:-localhost}
        printf '浏览器访问的 IP/域名（NAT 环境填写公网地址）[%s]：' "${suggested_host}" >&3
        IFS= read -r setup_host <&3
        setup_host=${setup_host:-${suggested_host}}
    fi
    printf '管理 HTTPS 端口 [%s]：' "${setup_port}" >&3
    IFS= read -r port_answer <&3
    setup_port=${port_answer:-${setup_port}}
    exec 3>&-
fi
if [[ -z ${setup_host} || ! ${setup_host} =~ ^[a-zA-Z0-9.:-]+$ ]]; then echo '需要有效的 --setup-host IP或域名' >&2; exit 1; fi
if [[ ! ${setup_port} =~ ^[0-9]{4,5}$ ]] || ((10#${setup_port} < 1024 || 10#${setup_port} > 65535 || 10#${setup_port} == 18080)); then
    echo '管理端口必须为 1024–65535，不能使用 18080。' >&2; exit 1
fi
if ! command -v useradd >/dev/null || ! command -v curl >/dev/null || ! dpkg-query -W -f='${Status}' ca-certificates 2>/dev/null | grep -qx 'install ok installed'; then
    apt-get update
    apt-get install -y --no-install-recommends ca-certificates passwd curl
fi
work_dir=''
cleanup() { if [[ -n ${work_dir} ]]; then rm -rf -- "${work_dir}"; fi; }
trap cleanup EXIT
fetch() { curl --fail --show-error --silent --location --proto '=https' --proto-redir '=https' --connect-timeout 20 --max-time 600 --retry 3 --output "$2.part" "$1"; mv -- "$2.part" "$2"; }
if [[ ${local_bundle} == false ]]; then
    release_version=${release_version:-latest}
    if [[ ${release_version} == latest ]]; then
        latest_url=$(curl --fail --show-error --silent --location --proto '=https' --proto-redir '=https' --connect-timeout 20 --max-time 60 --output /dev/null --write-out '%{url_effective}' 'https://github.com/ShaohongDong/relayharbor/releases/latest') || { echo '尚无可用稳定发行版，或 GitHub 不可达。' >&2; exit 1; }
        release_version=${latest_url##*/}
    fi
    release_version=${release_version#v}
    if [[ ! ${release_version} =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then echo '发行版本必须为 X.Y.Z 或 latest' >&2; exit 1; fi
    work_dir=$(mktemp -d)
    package="relayharbor_${release_version}_linux_${arch}"
    base_url="https://github.com/ShaohongDong/relayharbor/releases/download/v${release_version}"
    echo "下载 RelayHarbor ${release_version} (${arch})…"
    fetch "${base_url}/${package}.tar.gz" "${work_dir}/${package}.tar.gz"
    fetch "${base_url}/SHA256SUMS" "${work_dir}/SHA256SUMS"
    awk -v filename="${package}.tar.gz" '$2 == filename && $1 ~ /^[a-f0-9]+$/ && length($1) == 64 { print }' "${work_dir}/SHA256SUMS" >"${work_dir}/selected.sha256"
    if [[ $(wc -l <"${work_dir}/selected.sha256") != 1 ]]; then echo '发行包校验记录缺失或重复' >&2; exit 1; fi
    (cd "${work_dir}" && sha256sum --check --status selected.sha256)
    tar -tzf "${work_dir}/${package}.tar.gz" >"${work_dir}/members"
    while IFS= read -r member; do
        if [[ ${member} != "${package}/"* || ${member} == *'/../'* || ${member} == *'/./'* ]]; then echo '归档路径无效' >&2; exit 1; fi
    done <"${work_dir}/members"
    if tar -tvzf "${work_dir}/${package}.tar.gz" | awk 'substr($0,1,1) != "-" && substr($0,1,1) != "d" { found=1 } END { exit !found }'; then echo '发行包含异常链接或文件类型' >&2; exit 1; fi
    tar -xzf "${work_dir}/${package}.tar.gz" -C "${work_dir}" --no-same-owner --no-same-permissions
    repo_dir="${work_dir}/${package}"
fi
if [[ ! -x ${repo_dir}/bin/gostctl || ! -f ${repo_dir}/bin/SHA256SUMS ]]; then echo '缺少预编译程序或校验清单' >&2; exit 1; fi
(cd "${repo_dir}/bin" && sha256sum --check --status SHA256SUMS)
"${repo_dir}/bin/gostctl" install "${forward[@]}" --setup-host "${setup_host}" --setup-port "${setup_port}"
echo "浏览器需要接受自签名证书；请核对上方指纹。放行 TCP ${setup_port}；域名证书另需 TCP 80/443。"
echo '后续 HTTPS、AnyTLS 和代理池配置均可在页面完成。脚本不会修改 DNS、防火墙或云安全组。'
