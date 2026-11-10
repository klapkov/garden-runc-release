package bundlerules

import (
	spec "code.cloudfoundry.org/guardian/gardener/container-spec"
	"code.cloudfoundry.org/guardian/rundmc/goci"
	"github.com/mitchellh/copystructure"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

type Base struct {
	PrivilegedBase   goci.Bndl
	UnprivilegedBase goci.Bndl
}

func (r Base) Apply(bndl goci.Bndl, spec spec.DesiredContainerSpec) (goci.Bndl, error) {
	if spec.Privileged {
		copiedBndl, err := copystructure.Copy(r.PrivilegedBase)
		if err != nil {
			return goci.Bndl{}, err
		}
		return copiedBndl.(goci.Bndl), nil
	} else {
		copiedBndl, err := copystructure.Copy(r.UnprivilegedBase)
		if err != nil {
			return goci.Bndl{}, err
		}
		b := copiedBndl.(goci.Bndl)
		if spec.NoUserNamespace {
			b = withoutUserNamespace(b)
		}
		return b, nil
	}
}

func withoutUserNamespace(b goci.Bndl) goci.Bndl {
	linux := b.CloneLinux().Spec.Linux
	namespaces := linux.Namespaces[:0:0]
	for _, ns := range linux.Namespaces {
		if ns.Type != specs.UserNamespace {
			namespaces = append(namespaces, ns)
		}
	}
	linux.Namespaces = namespaces
	linux.UIDMappings = nil
	linux.GIDMappings = nil
	return b
}
