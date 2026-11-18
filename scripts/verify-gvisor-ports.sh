#!/usr/bin/env bash
# Source regression checks; does not start containerd or change live cgroups.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
module="$root/src/code.cloudfoundry.org/guardian"
baseline=${BASELINE:-9359d6da4}
[[ $(uname -s) == Linux ]] || { echo 'Run on Linux; cross-compilation is not execution.' >&2; exit 2; }
cd "$module"

go test -mod=vendor ./rundmc/runcontainerd ./rundmc/peas -count=1
go test -mod=vendor ./rundmc/runcontainerd/nerd \
  -run '^Test(Nerd.+|Delete.+|Pea.+|SetKata.+|Parse.+|Select.+|Read.+|TaskProcessesGone)$' -count=1
go test -mod=vendor ./throttle -run '^TestRuntimeIOLimits$' -v -count=1

# The new regressions must reject the original implementation. Go overlays keep
# the checkout intact while retaining the new tests.
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
git show "$baseline:src/code.cloudfoundry.org/guardian/rundmc/runcontainerd/nerd/nerd.go" > "$tmp/nerd.go"
git show "$baseline:src/code.cloudfoundry.org/guardian/throttle/enforcer_linux.go" > "$tmp/enforcer_linux.go"
python3 - "$module" "$tmp" <<'PY'
import json, pathlib, sys
module, tmp = map(pathlib.Path, sys.argv[1:])
(tmp / 'overlay.json').write_text(json.dumps({'Replace': {
    str(module / 'rundmc/runcontainerd/nerd/nerd.go'): str(tmp / 'nerd.go'),
    str(module / 'throttle/enforcer_linux.go'): str(tmp / 'enforcer_linux.go'),
}}))
PY
if go test -mod=vendor -overlay="$tmp/overlay.json" ./rundmc/runcontainerd/nerd \
  -run '^TestNerd(RuntimePolicyCreatePayload|DeadlineRetries)$' -count=1 > "$tmp/nerd.log" 2>&1; then
  echo 'Negative control failed: original Nerd passed new regressions.' >&2
  exit 1
fi
cat "$tmp/nerd.log"
grep -q 'stats scope =' "$tmp/nerd.log"
grep -q -- '--- FAIL: TestNerdDeadlineRetries/load_recovers' "$tmp/nerd.log"
grep -q -- '--- FAIL: TestNerdDeadlineRetries/status_recovers' "$tmp/nerd.log"
if go test -mod=vendor -overlay="$tmp/overlay.json" ./throttle \
  -run '^TestRuntimeIOLimits$' -count=1 > "$tmp/io.log" 2>&1; then
  echo 'Negative control failed: original enforcer passed new regression.' >&2
  exit 1
fi
cat "$tmp/io.log"
grep -q -- '--- FAIL: TestRuntimeIOLimits' "$tmp/io.log"
grep -q 'io.max: no such file or directory' "$tmp/io.log"
echo 'PASS: source regressions pass; original implementations fail the negative controls.'
