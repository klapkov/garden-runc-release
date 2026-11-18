# gVisor benchmark source ports

Base: `kata/runtime-spike` at `9359d6da4`. This branch preserves the Kata runtime,
VM memory overhead, network-before-create ordering, and peer-address handling.

## Changes

| Change | Source reference | Behavior |
| --- | --- | --- |
| Sandbox statistics scope | cp-runtime/garden-runc-release `53c419f6` | Runsc app parents request aggregate statistics; peas cannot inherit aggregate scope. Kata and runc do not receive this annotation. |
| Deadline retries | cp-runtime/garden-runc-release `b9b4ce4f85` | Container lookup and task status retry transient deadline errors, with bounded attempts and cancellation-aware waits. |
| I/O limits during CPU throttling | cp-runtime/garden-runc-release `68dea1e47` | Copy `io.max` before moving PIDs; retain it after release for outstanding writeback. |

Image-backed peas and public TaskService deletion for CREATED runsc tasks already
exist in the base. The stale runtime-policy test that rejected Kata now tests an
unsupported runtime instead. New payload tests cover both Kata and runsc.

## Validation

`bash scripts/verify-gvisor-ports.sh` passes on native Linux amd64. It runs
containerd/pea unit suites, selected lifecycle fixtures, and simulated cgroup I/O
tests. Go overlays verify that the new regressions fail against `9359d6da4`.
The cgroup fixture checks enforcer control flow, not kernel writeback behavior.
Privileged cgroup tests and live runtime acceptance have not run.

## Companion sources

- Diego v2.138.0: `klapkov/diego-release`, local branch
  `cfar-1567-runtime-memory-v2138`. Its `patches/cfar-1567/` directory contains
  required Rep/Executor source patches. The property defaults to zero. A gVisor
  deployment can select 130 MiB runtime allowance plus its existing 50 MiB proxy
  allowance. Kata keeps zero runtime allowance to avoid changing its existing
  Garden-side overhead policy.
- gVisor: local branch `gvisor/benchmark-shim-cleanup`, based on `41b2f2aec8`.
  It includes the later stdout/stderr writer cleanup from `cfe5385688` and
  regression coverage. The source needs a new matched runtime build; existing
  runtime release `0.9.0` does not contain this cleanup fix.

No BOSH release identity or artifact digest is changed by these source ports.
Release packaging and deployment remain separate steps.
