#!/usr/bin/env bash
# Native-only build. Does not register emulators or modify the host runtime.
set -euo pipefail
source_dir=${1:?source checkout required}
output_dir=${2:?new output directory required}
revision=${3:?exact source commit required}
[[ $(uname -s) == Linux && $(uname -m) == x86_64 ]] || { echo 'Requires native Linux amd64' >&2; exit 2; }
for compiler in /usr/bin/x86_64-linux-gnu-gcc /usr/bin/aarch64-linux-gnu-gcc /usr/bin/aarch64-linux-gnu-g++; do
  [[ -x "$compiler" ]] || { echo "Missing build compiler: $compiler" >&2; exit 2; }
done
source_dir=$(cd "$source_dir" && pwd)
[[ $(git -C "$source_dir" rev-parse HEAD) == "$revision" ]]
[[ -z $(git -C "$source_dir" status --porcelain) ]]
[[ ! -e "$output_dir" ]] || { echo 'Output directory already exists' >&2; exit 2; }
mkdir -p "$output_dir"
output_dir=$(cd "$output_dir" && pwd)
exec > >(tee "$output_dir/build.log") 2>&1
trap 'rc=$?; printf "%s\n" "$rc" > "$output_dir/exit-status"' EXIT
mkdir "$output_dir/tools"
bazel="$output_dir/tools/bazel"
curl --fail --location --proto '=https' --connect-timeout 20 --max-time 180 \
  https://github.com/bazelbuild/bazel/releases/download/8.8.1/bazel-8.8.1-linux-x86_64 -o "$bazel"
printf '%s  %s\n' 5b5bab2095065817d3416ca9a0678bd39c10c9ede432dbe8187bfe74de285160 "$bazel" | sha256sum --check
chmod 700 "$bazel"
cd "$source_dir"
printf 'source=%s\narch=%s\n' "$revision" "$(uname -m)" > "$output_dir/source.txt"
"$bazel" --host_jvm_args=-Xmx2048m version
timeout --kill-after=30s 1800 "$bazel" --host_jvm_args=-Xmx2048m test \
  --jobs=2 --local_ram_resources=4096 --test_output=errors --test_timeout=180 \
  //pkg/shim/v1/proc:proc_test
timeout --kill-after=30s 1800 "$bazel" --host_jvm_args=-Xmx2048m build \
  --jobs=2 --local_ram_resources=4096 //:release
cp -aL bazel-bin/release "$output_dir/release"
[[ -z $(git status --porcelain) ]] || { echo 'Source tree changed during build' >&2; git status --short; exit 1; }
"$output_dir/release/runsc" --version > "$output_dir/runsc-version.txt"
python3 - "$output_dir/release" "$revision" <<'PY'
import hashlib, json, pathlib, sys
root = pathlib.Path(sys.argv[1])
artifacts = {str(p.relative_to(root)): {'size': p.stat().st_size,
    'sha256': hashlib.sha256(p.read_bytes()).hexdigest()}
    for p in sorted(root.rglob('*')) if p.is_file()}
(root.parent / 'artifacts.json').write_text(json.dumps(
    {'source': sys.argv[2], 'artifacts': artifacts}, indent=2) + '\n')
PY
echo 'BUILD_AND_PROC_TEST_PASS'
