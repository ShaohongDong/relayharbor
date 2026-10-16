#!/usr/bin/env bash
# Privileged containers run their own systemd with private cgroup and mount namespaces.
# Never mount the host's /etc, /run, or /sys/fs/cgroup into these containers.
set -euo pipefail
repo_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "${repo_dir}"
mkdir -p test-results
python3 tests/evidence.py start
python3 tests/fetch_releases.py
test_container=''
cleanup() {
    if [[ -n ${test_container} ]]; then
        docker rm -f "${test_container}" >/dev/null 2>&1 || true
    fi
}
trap cleanup EXIT
wait_systemd() {
    local state attempt
    for ((attempt=0; attempt<60; attempt++)); do
        state=$(docker exec "${test_container}" systemctl is-system-running 2>/dev/null || true)
        if [[ ${state} == running || ${state} == degraded ]]; then return 0; fi
        sleep 1
    done
    docker logs "${test_container}" >&2
    return 1
}
for spec in ubuntu:20.04 ubuntu:22.04 ubuntu:24.04 debian:12-slim debian:13-slim; do
    image="gost-deploy-test:${spec//[:.]/-}"
    name="gost-deploy-test-${spec//[:.]/-}-$$"
    log="test-results/${spec//[:.]/-}.log"
    docker build --build-arg "BASE=${spec}" -f tests/systemd.Dockerfile -t "${image}" . >"${log}.build" 2>&1
    test_container=${name}
    docker run -d --name "${name}" --privileged --cgroupns=private \
        --tmpfs /run --tmpfs /run/lock \
        --add-host gost-target.test:127.0.0.1 \
        -e GOST_DEPLOY_TEST=1 -e PYTHONDONTWRITEBYTECODE=1 \
        -v "${repo_dir}:/src:ro" -v "${repo_dir}/.cache/releases:/artifacts:ro" \
        "${image}" >/dev/null
    wait_systemd
    if ! docker exec "${name}" python3 /src/tests/integration.py >"${log}" 2>&1; then
        cat "${log}"
        exit 1
    fi
    docker exec "${name}" python3 /src/tests/boot.py prepare >>"${log}" 2>&1
    docker restart "${name}" >/dev/null
    wait_systemd
    docker exec "${name}" python3 /src/tests/boot.py verify >>"${log}" 2>&1
    cat "${log}"
    cleanup
    test_container=''
    test_arch=$(docker image inspect "${image}" --format '{{.Architecture}}')
    echo "PASS ${spec} ${test_arch} systemd integration"
done
qemu_bin=$(command -v qemu-aarch64-static || true)
if [[ -z ${qemu_bin} ]]; then
    echo 'ARM64 验证要求宿主机提供 qemu-aarch64-static。' >&2
    exit 1
fi
for spec in ubuntu:24.04 debian:12-slim; do
    image="gost-deploy-test:${spec//[:.]/-}"
    docker run --rm --add-host gost-target.test:127.0.0.1 \
        -e GOST_DEPLOY_TEST=1 -e PYTHONDONTWRITEBYTECODE=1 \
        -v "${repo_dir}:/src:ro" -v "${repo_dir}/.cache/releases:/artifacts:ro" \
        -v "${qemu_bin}:/qemu-aarch64-static:ro" \
        "${image}" python3 /src/tests/smoke_arm64.py \
        | tee "test-results/${spec//[:.]/-}-arm64-qemu.log"
done
python3 tests/evidence.py finish
