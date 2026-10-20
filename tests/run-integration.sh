#!/usr/bin/env bash
# Trusted test code only. No host /etc, /run or cgroup directory is mounted.
set -euo pipefail
repo_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "${repo_dir}"
GO=${GO:-go}
qemu_bin=$(realpath -- "${GOST_TEST_QEMU_BIN:-$(command -v qemu-aarch64-static)}")
qemu_version=$("${qemu_bin}" --version)
if [[ ! ${qemu_version} =~ version[[:space:]]([0-9]+)\.([0-9]+) ]] || \
   (( BASH_REMATCH[1] < 8 || (BASH_REMATCH[1] == 8 && BASH_REMATCH[2] < 2) )); then
    echo "ARM64 AnyTLS acceptance requires QEMU >= 8.2; set GOST_TEST_QEMU_BIN to its path." >&2
    exit 1
fi
export GOST_TEST_QEMU_BIN="${qemu_bin}"
mkdir -p test-results .cache/test-bin
"${GO}" run ./cmd/testctl evidence start
make GO="${GO}" build
"${GO}" run ./cmd/testctl fetch
CGO_ENABLED=0 "${GO}" build -trimpath -buildvcs=false -o .cache/test-bin/testctl ./cmd/testctl
for arch in amd64 arm64; do
    CGO_ENABLED=0 GOOS=linux GOARCH=${arch} "${GO}" test -c -o ".cache/test-bin/runtime-${arch}.test" ./internal/supervisor
    CGO_ENABLED=0 GOOS=linux GOARCH=${arch} "${GO}" test -c -o ".cache/test-bin/subscription-${arch}.test" ./internal/subscription
    CGO_ENABLED=0 GOOS=linux GOARCH=${arch} "${GO}" build -trimpath -buildvcs=false -o ".cache/test-bin/gostctl-${arch}" ./cmd/gostctl
done
CGO_ENABLED=0 "${GO}" test -c -o .cache/test-bin/integration.test ./tests/integration
CGO_ENABLED=0 "${GO}" build -trimpath -buildvcs=false \
    -ldflags '-X github.com/ShaohongDong/relayharbor/internal/cli.Version=previous-fixture' \
    -o .cache/test-bin/gostctl-previous ./cmd/gostctl
test_container=''
fixture_container=''
cleanup() {
    if [[ -n ${test_container} ]]; then docker rm -f "${test_container}" >/dev/null 2>&1 || true; fi
    if [[ -n ${fixture_container} ]]; then docker rm -f "${fixture_container}" >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT
pebble_image='ghcr.io/letsencrypt/pebble:2.10.1@sha256:ddf230642b1a584f519f32e347de1b05a6e4c1f6c35c1863b33effeab5f78199'
docker pull "${pebble_image}"
fixture_container=$(docker create "${pebble_image}")
docker cp "${fixture_container}:/app" .cache/test-bin/pebble
docker rm "${fixture_container}" >/dev/null
fixture_container=''
tar -xOzf .cache/releases/lego_v5.5.2_linux_arm64.tar.gz lego > .cache/test-bin/lego-arm64
chmod 700 .cache/test-bin/lego-arm64
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
    image="gost-deploy-go-test:${spec//[:.]/-}"
    test_container="gost-deploy-go-test-${spec//[:.]/-}-$$"
    log="test-results/${spec//[:.]/-}.log"
    docker build --build-arg "BASE=${spec}" \
        --build-arg "http_proxy=${GOST_TEST_APT_PROXY:-}" --build-arg "https_proxy=${GOST_TEST_APT_PROXY:-}" \
        -f tests/systemd.Dockerfile -t "${image}" . >"${log}.build" 2>&1
    docker run -d --name "${test_container}" --privileged --cgroupns=private \
        --tmpfs /run --tmpfs /run/lock --add-host gost-target.test:127.0.0.1 --add-host gost-panel.test:127.0.0.1 \
        -e GOST_DEPLOY_TEST=1 -e GOST_CTL=/src/bin/gostctl -e GOST_TEST_REAL=1 -p 127.0.0.1::18081 \
        -v "${repo_dir}:/src:ro" -v "${repo_dir}/.cache/releases:/artifacts:ro" \
        -v "${repo_dir}/.cache/test-bin:/fixtures:ro" "${image}" >/dev/null
    if [[ -n ${GOST_TEST_APT_PROXY:-} ]]; then
        printf 'Acquire::http::Proxy "%s";\nAcquire::https::Proxy "%s";\n' \
            "${GOST_TEST_APT_PROXY}" "${GOST_TEST_APT_PROXY}" \
            | docker exec -i "${test_container}" tee /etc/apt/apt.conf.d/99gost-test-proxy >/dev/null
    fi
    wait_systemd
    if ! docker exec "${test_container}" /fixtures/integration.test -test.v -test.run '^TestLifecycle$' >"${log}" 2>&1; then cat "${log}"; exit 1; fi
    docker exec -e GOST_ARCHIVE=/artifacts/gost_3.3.0_linux_amd64.tar.gz "${test_container}" \
        /fixtures/runtime-amd64.test -test.v >>"${log}" 2>&1
    docker exec -e GOST_ARTIFACTS=/artifacts -e GOST_ARCHIVE=/artifacts/gost_3.3.0_linux_amd64.tar.gz "${test_container}" \
        /fixtures/subscription-amd64.test -test.v >>"${log}" 2>&1
    if [[ ${spec} == ubuntu:24.04 ]]; then
        bash tests/run-web-integration.sh "${test_container}"
    fi
    if ! docker exec -e GOST_TLS_BOOT=prepare "${test_container}" /fixtures/integration.test -test.v -test.run '^TestTLSDeployment$' >>"${log}" 2>&1; then cat "${log}"; exit 1; fi
    docker restart "${test_container}" >/dev/null
    wait_systemd
    if ! docker exec -e GOST_TLS_BOOT=verify "${test_container}" /fixtures/integration.test -test.v -test.run '^TestTLSBootVerify$' >>"${log}" 2>&1; then cat "${log}"; exit 1; fi
    docker exec -e GOST_BOOT=prepare "${test_container}" /fixtures/integration.test -test.v -test.run '^TestBootPrepare$' >>"${log}" 2>&1
    docker restart "${test_container}" >/dev/null
    wait_systemd
    docker exec -e GOST_BOOT=verify "${test_container}" /fixtures/integration.test -test.v -test.run '^TestBootVerify$' >>"${log}" 2>&1
    cat "${log}"
    cleanup; test_container=''
    echo "PASS ${spec} amd64 systemd integration"
done
for spec in ubuntu:24.04 debian:12-slim; do
    image="gost-deploy-go-test:${spec//[:.]/-}"
    docker run --rm \
        -e GOST_TEST_REAL=1 -e GOST_TEST_ARCH=arm64 -e GOST_TEST_QEMU=/qemu-aarch64-static -e QEMU_LD_PREFIX=/usr/aarch64-linux-gnu \
        -e GOST_ARCHIVE=/artifacts/gost_3.3.0_linux_arm64.tar.gz \
        -v "${repo_dir}/.cache/releases:/artifacts:ro" -v "${repo_dir}/.cache/test-bin:/fixtures:ro" \
        -v "${qemu_bin}:/qemu-aarch64-static:ro" "${image}" \
        /qemu-aarch64-static /fixtures/runtime-arm64.test -test.v \
        | tee "test-results/${spec//[:.]/-}-arm64-qemu.log"
    docker run --rm \
        -e GOST_TEST_REAL=1 -e GOST_TEST_ARCH=arm64 -e GOST_TEST_QEMU=/qemu-aarch64-static -e QEMU_LD_PREFIX=/usr/aarch64-linux-gnu \
        -e GOST_ARTIFACTS=/artifacts -e GOST_ARCHIVE=/artifacts/gost_3.3.0_linux_arm64.tar.gz \
        -v "${repo_dir}/.cache/releases:/artifacts:ro" -v "${repo_dir}/.cache/test-bin:/fixtures:ro" \
        -v "${qemu_bin}:/qemu-aarch64-static:ro" "${image}" \
        /qemu-aarch64-static /fixtures/subscription-arm64.test -test.v \
        | tee -a "test-results/${spec//[:.]/-}-arm64-qemu.log"
    docker run --rm -v "${repo_dir}/.cache/test-bin:/fixtures:ro" \
        -v "${qemu_bin}:/qemu-aarch64-static:ro" "${image}" \
        /qemu-aarch64-static /fixtures/gostctl-arm64 version \
        | tee -a "test-results/${spec//[:.]/-}-arm64-qemu.log"
    docker run --rm -v "${repo_dir}/.cache/test-bin:/fixtures:ro" \
        -v "${qemu_bin}:/qemu-aarch64-static:ro" "${image}" \
        /qemu-aarch64-static /fixtures/lego-arm64 --version \
        | tee -a "test-results/${spec//[:.]/-}-arm64-qemu.log"
done
.cache/test-bin/testctl evidence finish
