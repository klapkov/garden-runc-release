#!/usr/bin/env python3
"""Package a successful native build with pinned BOSH job/package templates."""
import argparse
import hashlib
import json
import pathlib
import platform
import shutil
import subprocess
import tarfile

p = argparse.ArgumentParser()
p.add_argument("--build", type=pathlib.Path, required=True)
p.add_argument("--templates", type=pathlib.Path, required=True)
p.add_argument("--templates-sha256", required=True)
p.add_argument("--templates-revision", required=True)
p.add_argument("--source-revision", required=True)
p.add_argument("--version", required=True)
p.add_argument("--output", type=pathlib.Path, required=True)
args = p.parse_args()
if platform.system() != "Linux" or platform.machine() != "x86_64":
    raise SystemExit("Package on native Linux amd64")
if (args.build / "exit-status").read_text().strip() != "0":
    raise SystemExit("Native build or proc tests did not pass")
record = json.loads((args.build / "artifacts.json").read_text())
if record["source"] != args.source_revision:
    raise SystemExit("Build source does not match requested revision")
if hashlib.sha256(args.templates.read_bytes()).hexdigest() != args.templates_sha256:
    raise SystemExit("BOSH template archive checksum mismatch")

# This is the BOSH package adapter for the pinned release-files layout.
blobs = {
    "runsc": "runsc/runsc",
    "containerd-shim-runsc-v1": "shim/containerd-shim-runsc-v1",
    "gvisor-bin/gvisor_sentry": "sentry/gvisor_sentry",
    "gvisor-bin/gvisor-sentry-prewarmer": "sentry/gvisor-sentry-prewarmer",
    "gvisor-bin/checkpointgofer": "sidecars/checkpointgofer",
    "gvisor-bin/runsc-metric-server": "sidecars/runsc-metric-server",
    "gvisor-bin/runsc-fd-parking": "sidecars/runsc-fd-parking",
    "gvisor-bin/gvisor-proc-umounter": "sidecars/gvisor-proc-umounter",
}
if set(record["artifacts"]) != set(blobs):
    raise SystemExit("Build fileset differs from the pinned package adapter")
for name, metadata in record["artifacts"].items():
    path = args.build / "release" / name
    if path.is_symlink() or not path.is_file():
        raise SystemExit(f"Missing or indirect build artifact: {name}")
    if path.stat().st_size != metadata["size"] or hashlib.sha256(path.read_bytes()).hexdigest() != metadata["sha256"]:
        raise SystemExit(f"Build artifact mismatch: {name}")

args.output.mkdir(parents=True, exist_ok=False)
out = args.output.resolve()
release = out / "source"
subprocess.run(["bosh", "init-release", "--dir", str(release)], check=True)
with tarfile.open(args.templates) as archive:
    for member in archive:
        name = pathlib.PurePosixPath(member.name)
        if name.is_absolute() or ".." in name.parts:
            raise SystemExit("Unsafe template archive path")
        if not name.parts or name.parts[0] not in {"jobs", "packages"}:
            continue
        if member.isdir():
            continue
        if not member.isfile():
            raise SystemExit("Template links are not permitted")
        target = release / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(archive.extractfile(member).read())
        target.chmod(member.mode & 0o777)
for name, blob in blobs.items():
    subprocess.run(["bosh", "add-blob", str((args.build / "release" / name).resolve()), blob], cwd=release, check=True)
archive = out / f"runtime-gvisor-{args.version}.tgz"
subprocess.run(["bosh", "create-release", "--name", "runtime-gvisor", "--version", args.version,
                "--force", "--tarball", str(archive)], cwd=release, check=True)
record.update({"release": {"name": "runtime-gvisor", "version": args.version},
               "source_repository": "https://github.com/klapkov/gvisor.git",
               "templates_revision": args.templates_revision,
               "templates_sha256": args.templates_sha256,
               "archive_sha256": hashlib.sha256(archive.read_bytes()).hexdigest()})
(out / "BUILD-PROVENANCE.json").write_text(json.dumps(record, indent=2) + "\n")
shutil.copyfile(args.build / "runsc-version.txt", out / "runsc-version.txt")
print(f"PACKAGED {archive} sha256={record['archive_sha256']}")
