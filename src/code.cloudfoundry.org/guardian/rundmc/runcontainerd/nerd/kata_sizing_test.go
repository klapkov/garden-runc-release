//go:build !windows

package nerd

import (
	"testing"

	specs "github.com/opencontainers/runtime-spec/specs-go"
)

func TestSetKataSandboxMemory(t *testing.T) {
	limit := int64(320864256)
	withLimit := func(containerType string) *specs.Spec {
		return &specs.Spec{
			Annotations: map[string]string{"io.kubernetes.cri.container-type": containerType},
			Linux:       &specs.Linux{Resources: &specs.LinuxResources{Memory: &specs.LinuxMemory{Limit: &limit}}},
		}
	}

	sandbox := withLimit("sandbox")
	setKataSandboxMemory(sandbox)
	if got := sandbox.Annotations["io.kubernetes.cri.sandbox-memory"]; got != "320864256" {
		t.Errorf("sandbox-memory = %q, want 320864256", got)
	}

	pea := withLimit("container")
	setKataSandboxMemory(pea)
	if _, ok := pea.Annotations["io.kubernetes.cri.sandbox-memory"]; ok {
		t.Error("sandbox-memory set on a pea")
	}

	unlimited := &specs.Spec{Annotations: map[string]string{"io.kubernetes.cri.container-type": "sandbox"}}
	setKataSandboxMemory(unlimited)
	if _, ok := unlimited.Annotations["io.kubernetes.cri.sandbox-memory"]; ok {
		t.Error("sandbox-memory set without a memory limit")
	}
}
