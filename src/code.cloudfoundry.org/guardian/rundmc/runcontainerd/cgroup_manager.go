package runcontainerd

import (
	"encoding/json"
	"os"
	"path/filepath"

	"code.cloudfoundry.org/guardian/rundmc/goci"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate
//counterfeiter:generate . CgroupManager
type CgroupManager interface {
	SetUseMemoryHierarchy(handle string) error
	SetUnifiedResources(bundle goci.Bndl) error
	AddCgroupBindMount(bundle *goci.Bndl)
}

type cgroupManager struct {
	runcRoot  string
	namespace string
	policy    RuntimePolicy
}

// KataSandboxMemoryOverhead is added to the memory limit of a Kata
// container's cgroup. Kata runs QEMU and the shim inside that cgroup and
// boots the VM with the full app limit plus a 32 MiB guest reserve, so the
// cgroup must also cover the reserve and the VMM's own memory (pod-overhead
// style). The app keeps its full limit; the overhead is host cost per app.
const KataSandboxMemoryOverhead int64 = 128 * 1024 * 1024

type containerState struct {
	CgroupPaths cgroupPaths `json:"cgroup_paths"`
}

type cgroupPaths struct {
	Memory string
}

func NewCgroupManager(runcRoot, namespace string) CgroupManager {
	return cgroupManager{
		runcRoot:  runcRoot,
		namespace: namespace,
	}
}

func NewCgroupManagerWithPolicy(runcRoot, namespace string, policy RuntimePolicy) CgroupManager {
	return cgroupManager{
		runcRoot:  runcRoot,
		namespace: namespace,
		policy:    policy,
	}
}

func (m cgroupManager) SetUseMemoryHierarchy(handle string) error {
	statePath := filepath.Join(m.runcRoot, m.namespace, handle, "state.json")
	if _, statErr := os.Stat(statePath); os.IsNotExist(statErr) {
		return nil
	}
	stateFile, err := os.Open(statePath)
	if err != nil {
		return err
	}
	defer stateFile.Close()

	var state containerState
	err = json.NewDecoder(stateFile).Decode(&state)
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(state.CgroupPaths.Memory, "memory.use_hierarchy"), []byte("1"), os.ModePerm)
}

func (m cgroupManager) SetUnifiedResources(bundle goci.Bndl) error {
	return m.setUnifiedResources(bundle)
}

func (m cgroupManager) AddCgroupBindMount(bundle *goci.Bndl) {
	m.addCgroupBindMount(bundle)
}
