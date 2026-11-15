// Package config reads and validates a machine's configuration file: its instances, their addresses, ports, projects,
// relays and DNS (ADR-0001, decisions 1, 4 and 7; ADR-0003). An address that is neither loopback nor a private address
// of a VPN interface — or of an interface the configuration names — is refused: a dev URL is never on the internet.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// DefaultRelayPort is the port of a server's `relay` entry point, where workstations relay over HTTP the domains that
// waggle terminates (ADR-0002, decision 3).
const DefaultRelayPort = 8080

// File is a machine's configuration.
type File struct {
	Instances map[string]*Instance `yaml:"instances"`
}

// Instance is one Traefik, its controller, its network, and, depending on the configuration, a DNS server and a
// firewall rule.
type Instance struct {
	Name          string         `yaml:"-" json:"name"`
	Address       string         `yaml:"address" json:"address"`
	Loopback      bool           `yaml:"loopback,omitempty" json:"loopback,omitempty"`
	Interface     string         `yaml:"interface,omitempty" json:"interface,omitempty"`
	Projects      []string       `yaml:"projects,omitempty" json:"projects,omitempty"`
	Ports         []int          `yaml:"ports,omitempty" json:"ports,omitempty"`
	Relays        []Relay        `yaml:"relays,omitempty" json:"relays,omitempty"`
	DNS           *bool          `yaml:"dns,omitempty" json:"dns,omitempty"`
	RelayListener *RelayListener `yaml:"relay_listener,omitempty" json:"relay_listener,omitempty"`
	PortOffset    int            `yaml:"port_offset,omitempty" json:"port_offset,omitempty"`
}

// Relay is a server to which this workstation relays the domains it does not serve. Without domains, they are learned
// from the server, at each `waggle up` (ADR-0003, decision 9).
type Relay struct {
	To         string   `yaml:"to" json:"to"`
	Domains    []string `yaml:"domains,omitempty" json:"domains,omitempty"`
	RelayPort  int      `yaml:"relay_port,omitempty" json:"relay_port,omitempty"`
	PortOffset int      `yaml:"port_offset,omitempty" json:"port_offset,omitempty"`
	Learned    bool     `yaml:"-" json:"-"` // the domains come from the server
}

// RelayListener is a server's HTTP entry point for workstation relays, and the networks whose X-Forwarded-* headers it
// trusts.
type RelayListener struct {
	Port       int      `yaml:"port" json:"port"`
	TrustedIPs []string `yaml:"trusted_ips" json:"trusted_ips"`
}

// RelayedDomain is one domain of a relay, read from "https://dev.example.com". The scheme gives the mode, as for routes.
type RelayedDomain struct {
	Mode   string // https, tls or http
	Domain string // "*": everything else, http only
}

// Link is a network interface of the machine.
type Link struct {
	Name     string
	Kind     string // WireGuard, TUN, or "" for any other interface
	Networks []*net.IPNet
}

// The kinds of tunnel interfaces, those of a VPN (ADR-0003, decision 7).
const (
	WireGuard = "wireguard" // DEVTYPE=wireguard: WireGuard, NetBird in kernel mode
	TUN       = "tun"       // a TUN or TAP device: Tailscale, ZeroTier, Nebula, OpenVPN…
)

// Tunnel tells whether the interface is a VPN's.
func (l Link) Tunnel() bool { return l.Kind != "" }

// VPN names the VPN behind a tunnel interface, from the name its software gives it.
func (l Link) VPN() string {
	switch {
	case !l.Tunnel():
		return ""
	case strings.HasPrefix(l.Name, "tailscale"):
		return "Tailscale"
	case strings.HasPrefix(l.Name, "zt"):
		return "ZeroTier"
	case strings.HasPrefix(l.Name, "wt"):
		return "NetBird"
	case strings.HasPrefix(l.Name, "nebula"):
		return "Nebula"
	case l.Kind == WireGuard:
		return "WireGuard"
	}
	return "a tunnel"
}

// IPv4 is the interface's first IPv4 address, or nil.
func (l Link) IPv4() net.IP {
	for _, n := range l.Networks {
		if ip := n.IP.To4(); ip != nil {
			return ip
		}
	}
	return nil
}

// Shared is the range of Tailscale, headscale and NetBird: they put a single address on their interface, a /32 that says
// nothing of the VPN's network.
var Shared = mustCIDR("100.64.0.0/10")

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

// Private tells whether an address can carry dev URLs: a private address (RFC 1918), or one of the shared range.
func Private(ip net.IP) bool { return ip.IsPrivate() || Shared.Contains(ip) }

// Host is what validation needs to know about the machine.
type Host struct {
	OS    string // "Linux", "Darwin"…
	Links func() ([]Link, error)
}

var (
	reName   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	reDomain = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// Read reads a configuration file, refusing unknown keys: a typo must not pass for a setting.
func Read(path string) (*File, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("configuration: %w", err)
	}
	return Decode(content)
}

// Decode reads a YAML configuration.
func Decode(content []byte) (*File, error) {
	dec := yaml.NewDecoder(bytes.NewReader(content))
	dec.KnownFields(true)
	var f File
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("configuration cannot be read: %w", err)
	}
	return &f, nil
}

// Validate checks the configuration, fills in what can be deduced — the interface of a VPN address, its relay listener
// and the network it trusts — and returns the instances sorted by name.
func Validate(f *File, h Host) ([]*Instance, error) {
	if f == nil || len(f.Instances) == 0 {
		return nil, errors.New("the configuration describes no instance (key \"instances\")")
	}
	var links []Link
	if h.OS == "Linux" && h.Links != nil {
		var err error
		if links, err = h.Links(); err != nil {
			return nil, fmt.Errorf("machine interfaces: %w", err)
		}
	}
	var instances []*Instance
	var errs []error
	for name, inst := range f.Instances {
		if inst == nil {
			inst = &Instance{}
		}
		inst.Name = name
		for _, err := range validateInstance(inst, h.OS, links) {
			errs = append(errs, fmt.Errorf("instance %s: %w", name, err))
		}
		instances = append(instances, inst)
	}
	sort.Slice(instances, func(i, j int) bool { return instances[i].Name < instances[j].Name })
	errs = append(errs, validateTogether(instances)...)
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return instances, nil
}

func validateInstance(inst *Instance, osName string, links []Link) []error {
	var errs []error
	fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if !reName.MatchString(inst.Name) {
		fail("the name must be lowercase letters, digits and dashes")
	}
	ip := net.ParseIP(inst.Address).To4()
	switch {
	case inst.Address == "":
		fail("address is missing (127.0.0.1 on a workstation, its VPN address on a server)")
	case ip == nil:
		fail("address %q is not an IPv4 address", inst.Address)
	case ip.IsUnspecified():
		fail("waggle refuses %s: a dev URL is never on the internet", inst.Address)
	case ip.IsLoopback():
		if inst.Interface != "" {
			fail("a loopback address has no interface to name")
		}
		if inst.Loopback {
			fail("loopback: true is for a server instance; %s already is loopback", inst.Address)
		}
	case osName != "Linux":
		fail("on %s, waggle only listens on loopback: %s refused", osLabel(osName), inst.Address)
	case !Private(ip):
		fail("waggle refuses %s, a public address: a dev URL is never on the internet", inst.Address)
	default:
		link, network := linkOf(ip, links)
		switch {
		case link == nil:
			fail("no interface of this machine carries %s", inst.Address)
		case inst.Interface != "" && inst.Interface != link.Name:
			fail("%s is on %s, not on %s", inst.Address, link.Name, inst.Interface)
		case !link.Tunnel() && inst.Interface == "":
			// Another interface — IPsec, a local network, a provider's private network — serves only when named: whatever
			// reaches the address through it reaches the stacks
			fail("%s is on %s, which is not a VPN interface (WireGuard, TUN or TAP): to share the stacks with whatever reaches it, name it (interface: %s)",
				inst.Address, link.Name, link.Name)
		default:
			inst.Interface = link.Name
			if inst.RelayListener == nil {
				if trusted, ok := trustedNetwork(ip, network); ok {
					inst.RelayListener = &RelayListener{Port: DefaultRelayPort, TrustedIPs: []string{trusted}}
				} else {
					fail("%s is alone on %s (/32), which says nothing of its VPN's network: write it in relay_listener.trusted_ips", inst.Address, link.Name)
				}
			}
		}
	}

	seen := map[int]bool{}
	for _, p := range inst.Ports {
		switch {
		case p < 1 || p > 65535:
			fail("port %d does not exist", p)
		case p == 80 || p == 443:
			fail("port %d is already in the catalog: list legacy ports only", p)
		case p == 53:
			fail("port 53 belongs to the VPN DNS")
		case seen[p]:
			fail("port %d is listed twice", p)
		}
		seen[p] = true
	}
	sort.Ints(inst.Ports)

	if inst.PortOffset < 0 || inst.PortOffset > 60000 {
		fail("port_offset must be between 0 and 60000")
	}
	for _, p := range inst.PublishedPorts() {
		if p+inst.PortOffset > 65535 {
			fail("port %d, offset by %d, is above 65535", p, inst.PortOffset)
		}
	}

	for _, project := range inst.Projects {
		if !reName.MatchString(project) {
			fail("project %q must be lowercase letters, digits and dashes", project)
		}
	}

	if rl := inst.RelayListener; rl != nil {
		if rl.Port < 1 || rl.Port > 65535 || rl.Port == 80 || rl.Port == 443 || seen[rl.Port] {
			fail("relay listener port %d is invalid or already taken", rl.Port)
		}
		if len(rl.TrustedIPs) == 0 {
			fail("the relay listener must say which networks it trusts (trusted_ips)")
		}
		for _, c := range rl.TrustedIPs {
			if _, _, err := net.ParseCIDR(c); err != nil {
				fail("relay listener trusted_ips entry %q is not a network (10.8.0.0/24)", c)
			}
		}
	}

	for i := range inst.Relays {
		r := &inst.Relays[i]
		if r.To == "" {
			fail("relay %d does not say where to (to)", i+1)
		}
		if r.RelayPort == 0 {
			r.RelayPort = DefaultRelayPort
		}
		if r.PortOffset < 0 || r.PortOffset > 60000 {
			fail("port_offset of relay %d must be between 0 and 60000", i+1)
		}
		for _, d := range r.Domains {
			if _, err := ParseRelayedDomain(d); err != nil {
				fail("relay to %s: %v", r.To, err)
			}
		}
	}

	if inst.DNS != nil && *inst.DNS && !inst.Shares() {
		fail("dns: true is for an instance that shares its stacks, on a VPN")
	}
	return errs
}

// trustedNetwork is the network whose X-Forwarded-* headers the relay listener trusts: the interface's, or the shared
// range for a single address in it.
func trustedNetwork(ip net.IP, network *net.IPNet) (string, bool) {
	if ones, bits := network.Mask.Size(); ones < bits {
		return network.String(), true
	}
	if Shared.Contains(ip) {
		return Shared.String(), true
	}
	return "", false
}

func validateTogether(instances []*Instance) []error {
	var errs []error
	owner := map[string]string{}
	withoutProjects := 0
	addresses := map[string]string{}
	for _, inst := range instances {
		if len(inst.Projects) == 0 {
			withoutProjects++
		}
		for _, p := range inst.Projects {
			if other, ok := owner[p]; ok && other != inst.Name {
				errs = append(errs, fmt.Errorf("project %s is read by two instances, %s and %s: isolation forbids it", p, other, inst.Name))
			}
			owner[p] = inst.Name
		}
		key := fmt.Sprintf("%s+%d", inst.Address, inst.PortOffset)
		if other, ok := addresses[key]; ok {
			errs = append(errs, fmt.Errorf("instances %s and %s listen on the same address, %s", other, inst.Name, inst.Address))
		}
		addresses[key] = inst.Name
		if inst.Loopback {
			key := fmt.Sprintf("%s+%d", LoopbackAddress, inst.PortOffset)
			if other, ok := addresses[key]; ok {
				errs = append(errs, fmt.Errorf("instances %s and %s both listen on %s: one instance per machine serves its loopback", other, inst.Name, LoopbackAddress))
			}
			addresses[key] = inst.Name
		}
	}
	if withoutProjects > 0 && len(instances) > 1 {
		errs = append(errs, errors.New("with several instances, each one lists its projects (projects): an instance without a list would read them all"))
	}
	return errs
}

// ParseRelayedDomain reads "https://dev.example.com", "tls://dev.legacy.com", "http://localhost" or "http://*".
func ParseRelayedDomain(text string) (RelayedDomain, error) {
	mode, domain, ok := strings.Cut(text, "://")
	if !ok {
		return RelayedDomain{}, fmt.Errorf("%q: a scheme is needed, https://, tls:// or http://", text)
	}
	domain = strings.TrimSuffix(strings.ToLower(domain), "/")
	switch mode {
	case "https", "tls":
		if !reDomain.MatchString(domain) || !strings.Contains(domain, ".") {
			return RelayedDomain{}, fmt.Errorf("%q: a %s domain has at least two labels", text, mode)
		}
	case "http":
		if domain != "*" && !reDomain.MatchString(domain) {
			return RelayedDomain{}, fmt.Errorf("%q is not a domain name", text)
		}
	default:
		return RelayedDomain{}, fmt.Errorf("%q: the scheme must be https://, tls:// or http://", text)
	}
	return RelayedDomain{Mode: mode, Domain: domain}, nil
}

// Shares tells whether the instance shares its stacks with other devices: a server's, which has a relay listener.
func (inst *Instance) Shares() bool { return inst.RelayListener != nil }

// ServesDNS tells whether the instance runs the VPN DNS: an instance that shares, unless `dns: false` (ADR-0003,
// decision 8).
func (inst *Instance) ServesDNS() bool { return inst.Shares() && (inst.DNS == nil || *inst.DNS) }

// AddPorts adds legacy ports read from the projects' routes or learned from a server (ADR-0003, decisions 6 and 9), and
// says why it skips the ones it cannot publish.
func (inst *Instance) AddPorts(ports []int) (skipped []string) {
	for _, p := range ports {
		switch {
		case p == 80 || inst.InCatalog(p):
		case p < 1 || p+inst.PortOffset > 65535:
			skipped = append(skipped, fmt.Sprintf("port %d does not exist", p))
		case p == 53 && inst.ServesDNS():
			skipped = append(skipped, "port 53 belongs to the VPN DNS")
		case inst.RelayListener != nil && p == inst.RelayListener.Port:
			skipped = append(skipped, fmt.Sprintf("port %d is the relay listener's", p))
		default:
			inst.Ports = append(inst.Ports, p)
		}
	}
	sort.Ints(inst.Ports)
	return skipped
}

// ServesProject tells whether this instance serves the project.
func (inst *Instance) ServesProject(project string) bool {
	if len(inst.Projects) == 0 {
		return true
	}
	for _, p := range inst.Projects {
		if p == project {
			return true
		}
	}
	return false
}

// TLSPorts are the TLS entry points: 443 and the legacy ports.
func (inst *Instance) TLSPorts() []int {
	return append([]int{443}, inst.Ports...)
}

// PublishedPorts are the TCP ports of Traefik's entry points, published on the instance address. The DNS server also
// publishes 53, over UDP and TCP.
func (inst *Instance) PublishedPorts() []int {
	ports := append([]int{80}, inst.TLSPorts()...)
	if inst.RelayListener != nil {
		ports = append(ports, inst.RelayListener.Port)
	}
	return ports
}

// LoopbackAddress is where a machine's own browsers and agents reach its stacks.
const LoopbackAddress = "127.0.0.1"

// LoopbackPorts are the ports a server instance with loopback: true also publishes on 127.0.0.1, so that the machine's own
// agents reach its stacks under their URLs (ADR-0003): the stacks' entry points, not the relay listener, which only
// workstations reach, through the VPN.
func (inst *Instance) LoopbackPorts() []int {
	if !inst.Loopback {
		return nil
	}
	return append([]int{80}, inst.TLSPorts()...)
}

// InCatalog tells whether a TLS port is an entry point of the instance.
func (inst *Instance) InCatalog(port int) bool {
	for _, p := range inst.TLSPorts() {
		if p == port {
			return true
		}
	}
	return false
}

func linkOf(ip net.IP, links []Link) (*Link, *net.IPNet) {
	for i := range links {
		for _, n := range links[i].Networks {
			if n.IP.Equal(ip) {
				return &links[i], &net.IPNet{IP: n.IP.Mask(n.Mask), Mask: n.Mask}
			}
		}
	}
	return nil, nil
}

func osLabel(s string) string {
	switch s {
	case "Darwin":
		return "macOS"
	case "":
		return "this system"
	}
	return s
}

// HostLinks reads the machine's interfaces, and recognizes a VPN's: WireGuard by its uevent (DEVTYPE=wireguard), TUN
// and TAP by their tun_flags. Run in a container, it needs `--network host` to see the host's.
func HostLinks() ([]Link, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var links []Link
	for _, iface := range ifaces {
		l := Link{Name: iface.Name}
		sys := "/sys/class/net/" + iface.Name
		if uevent, err := os.ReadFile(sys + "/uevent"); err == nil && bytes.Contains(uevent, []byte("DEVTYPE=wireguard")) {
			l.Kind = WireGuard
		} else if _, err := os.Stat(sys + "/tun_flags"); err == nil {
			l.Kind = TUN
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				l.Networks = append(l.Networks, n)
			}
		}
		links = append(links, l)
	}
	return links, nil
}
