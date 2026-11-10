package netplugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"code.cloudfoundry.org/commandrunner"
	"code.cloudfoundry.org/garden"
	"code.cloudfoundry.org/guardian/gardener"
	"code.cloudfoundry.org/guardian/kawasaki"
	"code.cloudfoundry.org/lager/v3"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

const NetworkPropertyPrefix = "network."

var AllowableProperties = map[string]struct{}{"log_config": {}}

type externalBinaryNetworker struct {
	commandRunner         commandrunner.CommandRunner
	configStore           kawasaki.ConfigStore
	externalIP            net.IP
	operatorNameservers   []net.IP
	additionalNameservers []net.IP
	resolvConfigurer      kawasaki.DnsResolvConfigurer
	path                  string
	extraArg              []string
	networkDepot          kawasaki.NetworkDepot
	netnsDir              string
}

// Option configures optional externalBinaryNetworker behaviour.
type Option func(*externalBinaryNetworker)

// WithNetworkBeforeCreate makes the networker create and configure each
// container's network namespace before the container task exists. The
// namespace is bind-mounted under netnsDir/<handle>. Runtimes that boot a VM
// at task create (Kata) need this, because they snapshot the namespace then.
func WithNetworkBeforeCreate(netnsDir string) Option {
	return func(p *externalBinaryNetworker) { p.netnsDir = netnsDir }
}

func New(
	commandRunner commandrunner.CommandRunner,
	configStore kawasaki.ConfigStore,
	externalIP net.IP,
	operatorNameServers []net.IP,
	additionalNameservers []net.IP,
	resolvConfigurer kawasaki.DnsResolvConfigurer,
	path string,
	extraArg []string,
	networkDepot kawasaki.NetworkDepot,
	opts ...Option,
) ExternalNetworker {
	p := &externalBinaryNetworker{
		commandRunner:         commandRunner,
		configStore:           configStore,
		externalIP:            externalIP,
		operatorNameservers:   operatorNameServers,
		additionalNameservers: additionalNameservers,
		resolvConfigurer:      resolvConfigurer,
		path:                  path,
		extraArg:              extraArg,
		networkDepot:          networkDepot,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

type ExternalNetworker interface {
	gardener.Networker
	gardener.Starter
}

func (p *externalBinaryNetworker) Start() error { return nil }

func networkProperties(containerProperties garden.Properties) garden.Properties {
	properties := garden.Properties{}

	for k, value := range containerProperties {
		if strings.HasPrefix(k, NetworkPropertyPrefix) {
			key := strings.TrimPrefix(k, NetworkPropertyPrefix)
			properties[key] = value
		} else if _, ok := AllowableProperties[k]; ok {
			properties[k] = value
		}
	}

	return properties
}

type UpInputs struct {
	Pid        int
	Properties map[string]string
	NetOut     []garden.NetOutRule `json:"netout_rules,omitempty"`
	NetIn      []garden.NetIn      `json:"netin,omitempty"`
}

type UpOutputs struct {
	Properties    map[string]string
	DNSServers    []string `json:"dns_servers,omitempty"`
	SearchDomains []string `json:"search_domains,omitempty"`
}

func (p *externalBinaryNetworker) SetupBindMounts(log lager.Logger, handle string, privileged bool, rootfsPath string) ([]garden.BindMount, error) {
	return p.networkDepot.SetupBindMounts(log, handle, privileged, rootfsPath)
}

func (p *externalBinaryNetworker) Network(log lager.Logger, containerSpec garden.ContainerSpec, pid int) error {
	return p.network(log, containerSpec, pid, nil, fmt.Sprintf("/proc/%d/ns/net", pid))
}

// NetworksBeforeCreate reports whether WithNetworkBeforeCreate is active.
func (p *externalBinaryNetworker) NetworksBeforeCreate() bool {
	return p.netnsDir != ""
}

// NetworkNamespace creates a bind-mounted network namespace for the
// container, configures it through the external networker (which receives the
// namespace as inherited fd 3 with pid 0) and returns its path for the OCI spec.
func (p *externalBinaryNetworker) NetworkNamespace(log lager.Logger, containerSpec garden.ContainerSpec) (string, error) {
	log = log.Session("network-before-create")
	nsPath := filepath.Join(p.netnsDir, containerSpec.Handle)

	if err := createBoundNetns(nsPath); err != nil {
		return "", err
	}

	nsFile, err := os.Open(nsPath)
	if err != nil {
		return "", fmt.Errorf("opening netns %s: %w", nsPath, err)
	}
	defer nsFile.Close()

	if err := p.network(log, containerSpec, 0, nsFile, nsPath); err != nil {
		return "", err
	}

	if err := flattenPeerAddresses(nsPath); err != nil {
		return "", fmt.Errorf("flattening peer addresses: %w", err)
	}

	log.Info("configured", lager.Data{"netns": nsPath})
	return nsPath, nil
}

func (p *externalBinaryNetworker) network(log lager.Logger, containerSpec garden.ContainerSpec, pid int, nsFile *os.File, nsPath string) error {
	p.configStore.Set(containerSpec.Handle, gardener.ExternalIPKey, p.externalIP.String())

	inputs := UpInputs{
		Pid:        pid,
		Properties: networkProperties(containerSpec.Properties),
		NetOut:     containerSpec.NetOut,
		NetIn:      containerSpec.NetIn,
	}

	var extraFiles []*os.File
	if nsFile != nil {
		extraFiles = []*os.File{nsFile}
	}

	outputs := UpOutputs{}
	err := p.exec(log, "up", containerSpec.Handle, inputs, &outputs, extraFiles...)
	if err != nil {
		return err
	}

	// Ensure loopback interface is up in the container's network namespace.
	// External network plugins (e.g. silk CNI) typically only create a veth pair.
	// Runtimes like gVisor that build their own netstack by scraping the netns
	// need lo to be present for applications binding to 127.0.0.1.
	if err := setupLoopback(log, nsPath); err != nil {
		log.Error("setup-loopback", err)
		// Non-fatal: runc brings up lo on its own, only gVisor needs this.
	}

	for k, v := range outputs.Properties {
		p.configStore.Set(containerSpec.Handle, k, v)
	}

	var pluginNameservers []net.IP
	if outputs.DNSServers != nil {
		pluginNameservers = []net.IP{}
		for _, dnsServer := range outputs.DNSServers {
			pluginNameservers = append(pluginNameservers, net.ParseIP(dnsServer))
		}
	}

	containerIP, ok := p.configStore.Get(containerSpec.Handle, gardener.ContainerIPKey)

	// ipv6 address is "" if not returned by external networker
	containerIPv6, _ := p.configStore.Get(containerSpec.Handle, gardener.ContainerIPv6Key)
	if ok {
		log.Info("external-binary-write-dns-to-config", lager.Data{
			"dnsServers": pluginNameservers,
		})
		cfg := kawasaki.NetworkConfig{
			ContainerIP:           net.ParseIP(containerIP),
			ContainerIPv6:         net.ParseIP(containerIPv6),
			BridgeIP:              net.ParseIP(containerIP),
			ContainerHandle:       containerSpec.Handle,
			OperatorNameservers:   p.operatorNameservers,
			AdditionalNameservers: p.additionalNameservers,
			PluginNameservers:     pluginNameservers,
			PluginSearchDomains:   outputs.SearchDomains,
		}

		err = p.resolvConfigurer.Configure(log, cfg, pid)
		if err != nil {
			return err
		}
	}

	return nil
}

// setupLoopback enters the container's network namespace and brings up the
// loopback interface. This is needed for runtimes like gVisor that scrape the
// netns for interfaces rather than creating lo themselves.
func setupLoopback(log lager.Logger, nsPath string) error {
	containerNS, err := netns.GetFromPath(nsPath)
	if err != nil {
		return fmt.Errorf("getting netns %s: %w", nsPath, err)
	}
	defer containerNS.Close()

	// Create a netlink handle in the container's network namespace.
	nlh, err := netlink.NewHandleAt(containerNS)
	if err != nil {
		return fmt.Errorf("creating netlink handle in container netns: %w", err)
	}
	defer nlh.Close()

	lo, err := nlh.LinkByName("lo")
	if err != nil {
		return fmt.Errorf("finding lo interface: %w", err)
	}

	if err := nlh.LinkSetUp(lo); err != nil {
		return fmt.Errorf("bringing up lo: %w", err)
	}

	log.Info("setup-loopback-success", lager.Data{"netns": nsPath})
	return nil
}

func (p *externalBinaryNetworker) Destroy(log lager.Logger, handle string) error {
	err := p.exec(log, "down", handle, nil, nil)

	if p.netnsDir != "" {
		if nsErr := removeBoundNetns(filepath.Join(p.netnsDir, handle)); nsErr != nil && err == nil {
			err = nsErr
		}
	}

	if err != nil {
		return err
	}

	return p.networkDepot.Destroy(log, handle)
}

func (p *externalBinaryNetworker) Restore(log lager.Logger, handle string) error {
	return nil
}

func (p *externalBinaryNetworker) Capacity() (m uint64) {
	return math.MaxUint64
}

type NetInInputs struct {
	HostIP        string
	HostPort      uint32
	ContainerIP   string
	ContainerPort uint32
}

type NetInOutputs struct {
	HostPort      uint32 `json:"host_port"`
	ContainerPort uint32 `json:"container_port"`
}

func (p *externalBinaryNetworker) NetIn(log lager.Logger, handle string, hostPort, containerPort uint32) (uint32, uint32, error) {
	containerIP, ok := p.configStore.Get(handle, gardener.ContainerIPKey)
	if !ok {
		return 0, 0, fmt.Errorf("cannot find container [%s]\n", handle)
	}

	inputs := NetInInputs{
		HostIP:        p.externalIP.String(),
		ContainerIP:   containerIP,
		HostPort:      hostPort,
		ContainerPort: containerPort,
	}
	outputs := NetInOutputs{}

	err := p.exec(log, "net-in", handle, inputs, &outputs)
	if err != nil {
		return 0, 0, err
	}

	err = kawasaki.AddPortMapping(log, p.configStore, handle, garden.PortMapping{
		HostPort:      outputs.HostPort,
		ContainerPort: outputs.ContainerPort,
	})
	if err != nil {
		return 0, 0, err
	}

	return outputs.HostPort, outputs.ContainerPort, err
}

type NetOutInputs struct {
	ContainerIP string            `json:"container_ip"`
	NetOutRule  garden.NetOutRule `json:"netout_rule"`
}

func (p *externalBinaryNetworker) NetOut(log lager.Logger, handle string, rule garden.NetOutRule) error {
	containerIP, ok := p.configStore.Get(handle, gardener.ContainerIPKey)
	if !ok {
		return fmt.Errorf("cannot find container [%s]\n", handle)
	}

	inputs := NetOutInputs{
		ContainerIP: containerIP,
		NetOutRule:  rule,
	}

	err := p.exec(log, "net-out", handle, inputs, nil)
	if err != nil {
		return err
	}

	return nil
}

type BulkNetOutInputs struct {
	ContainerIP string              `json:"container_ip"`
	NetOutRules []garden.NetOutRule `json:"netout_rules"`
}

func (p *externalBinaryNetworker) BulkNetOut(log lager.Logger, handle string, rules []garden.NetOutRule) error {
	containerIP, ok := p.configStore.Get(handle, gardener.ContainerIPKey)
	if !ok {
		return fmt.Errorf("cannot find container [%s]\n", handle)
	}

	inputs := BulkNetOutInputs{
		ContainerIP: containerIP,
		NetOutRules: rules,
	}

	return p.exec(log, "bulk-net-out", handle, inputs, nil)
}

func (p *externalBinaryNetworker) exec(log lager.Logger, action, handle string,
	inputData interface{}, outputData interface{}, extraFiles ...*os.File) error {

	stdinBytes, err := json.Marshal(inputData)
	if err != nil {
		return err
	}

	args := append(p.extraArg, "--action", action, "--handle", handle)
	cmd := exec.Command(p.path, args...)
	cmd.ExtraFiles = extraFiles
	stdout := &bytes.Buffer{}
	cmd.Stdout = stdout
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	cmd.Stdin = bytes.NewReader(stdinBytes)

	err = p.commandRunner.Run(cmd)

	logData := lager.Data{"action": action, "stdin": string(stdinBytes), "stderr": stderr.String(), "stdout": stdout.String()}
	if err != nil {
		log.Error("external-networker-result", err, logData)
		return fmt.Errorf("external networker encountered an error running '%s' action: %s", action, err)
	}

	if outputData != nil && stdout.Len() > 0 {
		err = json.Unmarshal(stdout.Bytes(), outputData)
		if err != nil {
			log.Error("external-networker-result", err, logData)
			return fmt.Errorf("unmarshaling result from external networker: %s", err)
		}
	}

	if stderr.Len() > 0 {
		log.Info("external-networker-result", lager.Data{"stderr": stderr.String()})
	}

	log.Debug("external-networker-result", logData)

	return nil
}
