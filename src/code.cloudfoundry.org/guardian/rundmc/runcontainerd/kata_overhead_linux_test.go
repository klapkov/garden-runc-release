package runcontainerd

import (
	"testing"

	"github.com/opencontainers/cgroups"
)

func TestAddKataSandboxOverhead(t *testing.T) {
	r := &cgroups.Resources{Memory: 306 << 20, MemorySwap: 306 << 20}
	addKataSandboxOverhead(r)
	want := int64(306<<20) + KataSandboxMemoryOverhead
	if r.Memory != want || r.MemorySwap != want {
		t.Errorf("memory=%d swap=%d, want %d", r.Memory, r.MemorySwap, want)
	}

	unlimited := &cgroups.Resources{}
	addKataSandboxOverhead(unlimited)
	if unlimited.Memory != 0 || unlimited.MemorySwap != 0 {
		t.Errorf("unlimited container got a limit: %+v", unlimited)
	}
}
