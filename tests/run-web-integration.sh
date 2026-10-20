#!/usr/bin/env bash
set -euo pipefail
repo_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "${repo_dir}"
test_container=${1:?isolated container name required}
if [[ $(docker exec "${test_container}" printenv GOST_DEPLOY_TEST) != 1 ]]; then
    echo 'Refusing a container without the isolated test guard' >&2; exit 1
fi
umask 077
mkdir -p test-results
web_tmp=$(mktemp -d "${repo_dir}/.cache/web-browser.XXXXXX")
web_pid=''
web_port=$(docker port "${test_container}" 18081/tcp)
web_url="http://${web_port}"
cleanup() {
    if [[ -f ${web_tmp}/token ]]; then
        GOST_WEB_URL="${web_url}" GOST_WEB_TOKEN_FILE="${web_tmp}/token" node --input-type=module - <<'JS' || true
import fs from 'node:fs';
await fetch(process.env.GOST_WEB_URL+'/_fixture/done', {method:'POST',headers:{Authorization:'Bearer '+fs.readFileSync(process.env.GOST_WEB_TOKEN_FILE,'utf8').trim()}});
JS
    fi
    if [[ -n ${web_pid} ]]; then
        for ((cleanup_attempt=0; cleanup_attempt<50; cleanup_attempt++)); do
            if ! kill -0 "${web_pid}" 2>/dev/null; then break; fi
            sleep 0.1
        done
        kill "${web_pid}" 2>/dev/null || true
        wait "${web_pid}" || true
    fi
    rm -f "${web_tmp}/token"; rmdir "${web_tmp}"
}
trap cleanup EXIT
docker exec -e GOST_WEB_TEST=1 -e GOST_ARCHIVE=/artifacts/gost_3.3.0_linux_amd64.tar.gz "${test_container}" \
    /fixtures/integration.test -test.v -test.run '^TestBrowserSession$' >test-results/browser-systemd.log 2>&1 &
web_pid=$!
web_ready=false
for ((attempt=0; attempt<90; attempt++)); do
    if curl -fsS "${web_url}/" >/dev/null 2>&1; then web_ready=true; break; fi
    if ! kill -0 "${web_pid}" 2>/dev/null; then cat test-results/browser-systemd.log; exit 1; fi
    sleep 1
done
if [[ ${web_ready} != true ]]; then cat test-results/browser-systemd.log; exit 1; fi
docker exec "${test_container}" cat /var/lib/gost-deploy/api-token >"${web_tmp}/token"
GOST_WEB_URL="${web_url}" GOST_WEB_TOKEN_FILE="${web_tmp}/token" npm --prefix web run test:e2e -- --project=real | tee test-results/browser-real.log
# Tell the fixture to finish and verify its exit status, including connection checks.
GOST_WEB_URL="${web_url}" GOST_WEB_TOKEN_FILE="${web_tmp}/token" node --input-type=module - <<'JS'
import fs from 'node:fs';
const r=await fetch(process.env.GOST_WEB_URL+'/_fixture/done',{method:'POST',headers:{Authorization:'Bearer '+fs.readFileSync(process.env.GOST_WEB_TOKEN_FILE,'utf8').trim()}});
if(!r.ok)process.exit(1);
JS
wait "${web_pid}"
web_pid=''
rm -f "${web_tmp}/token"
cat test-results/browser-systemd.log
echo 'PASS browser end-to-end against real systemd and GOST'
