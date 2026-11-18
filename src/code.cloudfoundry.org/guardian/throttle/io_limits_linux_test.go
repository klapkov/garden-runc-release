package throttle_test

import (
	"os"
	"path/filepath"
	"testing"

	"code.cloudfoundry.org/guardian/throttle"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/opencontainers/cgroups"
)

// Exercise the enforcer's control flow without moving real processes. Kernel
// controller/writeback behavior still needs the privileged cgroup suite.
func TestRuntimeIOLimits(t *testing.T) {
	if !cgroups.IsCgroup2UnifiedMode() {
		t.Skip("requires cgroup v2")
	}
	previous := cgroups.TestMode
	cgroups.TestMode = true
	t.Cleanup(func() { cgroups.TestMode = previous })
	for _, initChild := range []bool{false, true} {
		name := "container"
		if initChild {
			name = "init"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			good := filepath.Join(root, "good", "app")
			bad := filepath.Join(root, "bad", "app")
			source := good
			if initChild {
				source = filepath.Join(good, "init")
			}
			for _, dir := range []string{source, bad} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path, content string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			read := func(path string) string {
				t.Helper()
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				return string(b)
			}
			const limits = "259:0 rbps=59768832 wbps=59768832 riops=900 wiops=900\n259:3 rbps=1234 wbps=5678 riops=90 wiops=100"
			write(filepath.Join(source, "io.max"), limits)
			write(filepath.Join(source, "cpu.weight"), "100")
			write(filepath.Join(source, "cgroup.procs"), "")
			write(filepath.Join(bad, "cgroup.procs"), "")
			if initChild {
				write(filepath.Join(good, "io.max"), "259:0 rbps=1")
			}
			log := lagertest.NewTestLogger("io-limits")
			e := throttle.NewEnforcer(root, t.TempDir(), "test")
			if err := e.Punish(log, "app"); err != nil {
				t.Fatal(err)
			}
			if got := read(filepath.Join(bad, "io.max")); got != limits {
				t.Fatalf("lost limits: %q", got)
			}
			if err := e.Release(log, "app"); err != nil {
				t.Fatal(err)
			}
			if got := read(filepath.Join(bad, "io.max")); got != limits {
				t.Fatalf("Release cleared writeback limits: %q", got)
			}
			if err := e.Punish(log, "app"); err != nil {
				t.Fatal(err)
			}
			if got := read(filepath.Join(bad, "io.max")); got != limits {
				t.Fatalf("re-Punish lost limits: %q", got)
			}

			// A write failure must stop before CPU shares or process placement change.
			if err := os.Remove(filepath.Join(bad, "io.max")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(bad, "io.max"), 0700); err != nil {
				t.Fatal(err)
			}
			write(filepath.Join(bad, "cpu.weight"), "77")
			if err := e.Punish(log, "app"); err == nil {
				t.Fatal("Punish ignored io.max write failure")
			}
			if got := read(filepath.Join(bad, "cpu.weight")); got != "77" {
				t.Fatalf("Punish continued after failed I/O enforcement: %q", got)
			}
		})
	}
}
