package config

import (
	"net"
	"strings"
	"testing"
)

func validate(t *testing.T, yaml string, h Host) ([]*Instance, error) {
	t.Helper()
	f, err := Decode([]byte(yaml))
	if err != nil {
		return nil, err
	}
	return Validate(f, h)
}

var mac = Host{OS: "Darwin"}

func server(links ...Link) Host {
	return Host{OS: "Linux", Links: func() ([]Link, error) { return links, nil }}
}

func link(name string, wg bool, cidr string) Link {
	kind := ""
	if wg {
		kind = WireGuard
	}
	return tunnel(name, kind, cidr)
}

func tunnel(name, kind, cidr string) Link {
	ip, network, _ := net.ParseCIDR(cidr)
	network.IP = ip
	return Link{Name: name, Kind: kind, Networks: []*net.IPNet{network}}
}

func (h Host) links() []Link {
	l, _ := h.Links()
	return l
}

func wantError(t *testing.T, err error, part string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), part) {
		t.Fatalf("expected an error containing %q, got: %v", part, err)
	}
}

func TestWorkstation(t *testing.T) {
	insts, err := validate(t, `
instances:
  workstation:
    address: 127.0.0.1
    ports: [3001, 3000]
    relays:
      - to: 10.8.0.1
        domains: [https://dev.example.com, tls://dev.legacy.com, http://localhost]
`, mac)
	if err != nil {
		t.Fatal(err)
	}
	w := insts[0]
	if w.Name != "workstation" || w.Interface != "" || w.RelayListener != nil {
		t.Fatalf("a workstation has neither interface nor relay listener: %+v", w)
	}
	if got := w.TLSPorts(); len(got) != 3 || got[0] != 443 || got[1] != 3000 {
		t.Fatalf("TLS ports, sorted after 443: %v", got)
	}
	if w.Relays[0].RelayPort != DefaultRelayPort {
		t.Fatalf("default relay port: %d", w.Relays[0].RelayPort)
	}
	if !w.ServesProject("anything") {
		t.Fatal("an instance without projects serves them all")
	}
}

func TestNeverOnTheInternet(t *testing.T) {
	_, err := validate(t, "instances:\n  workstation:\n    address: 0.0.0.0\n", mac)
	wantError(t, err, "never on the internet")
	_, err = validate(t, "instances:\n  workstation:\n    address: 10.8.0.2\n", mac)
	wantError(t, err, "only listens on loopback")
	_, err = validate(t, "instances:\n  vps:\n    address: 51.15.0.1\n", server(link("eth0", false, "51.15.0.1/24")))
	wantError(t, err, "a public address")
	_, err = validate(t, "instances:\n  vps:\n    address: 51.15.0.1\n    interface: eth0\n", server(link("eth0", false, "51.15.0.1/24")))
	wantError(t, err, "a public address")
	_, err = validate(t, "instances:\n  vps:\n    address: 172.31.0.5\n", server(link("eth0", false, "172.31.0.5/20")))
	wantError(t, err, "not a VPN interface")
	_, err = validate(t, "instances:\n  vps:\n    address: 10.9.0.1\n", server(link("wg0", true, "10.8.0.1/24")))
	wantError(t, err, "no interface")
}

func TestServer(t *testing.T) {
	insts, err := validate(t, `
instances:
  team:
    address: 10.8.0.1
    projects: [my-project]
  clients:
    address: 10.9.0.1
    projects: [other]
`, server(link("eth0", false, "51.15.0.1/24"), link("wg0", true, "10.8.0.1/24"), link("wg1", true, "10.9.0.1/24")))
	if err != nil {
		t.Fatal(err)
	}
	c, tm := insts[0], insts[1]
	if c.Name != "clients" || tm.Name != "team" {
		t.Fatalf("instances, sorted: %s, %s", c.Name, tm.Name)
	}
	if tm.Interface != "wg0" || c.Interface != "wg1" {
		t.Fatalf("the interface is deduced from the address: %s, %s", tm.Interface, c.Interface)
	}
	if tm.RelayListener == nil || tm.RelayListener.Port != DefaultRelayPort || tm.RelayListener.TrustedIPs[0] != "10.8.0.0/24" {
		t.Fatalf("the relay listener trusts the VPN network: %+v", tm.RelayListener)
	}
	if tm.ServesProject("other") || !tm.ServesProject("my-project") {
		t.Fatal("an instance only serves its projects")
	}
	if !tm.Shares() || !tm.ServesDNS() {
		t.Fatal("a server instance shares its stacks, and runs the VPN DNS")
	}
}

// Any VPN's interface serves, not only WireGuard's; another interface, only when named (ADR-0003, decision 7).
func TestAnyVPN(t *testing.T) {
	h := server(
		tunnel("tailscale0", TUN, "100.101.102.103/32"),
		tunnel("zt0", TUN, "10.147.17.4/24"),
		tunnel("tun0", TUN, "10.20.0.1/32"),
		tunnel("eth1", "", "192.168.1.10/24"),
	)
	insts, err := validate(t, "instances:\n  vpn:\n    address: 100.101.102.103\n", h)
	if err != nil {
		t.Fatal(err)
	}
	if v := insts[0]; v.Interface != "tailscale0" || v.RelayListener.TrustedIPs[0] != "100.64.0.0/10" {
		t.Errorf("a /32 of the shared range trusts the range: %+v %+v", v, v.RelayListener)
	}
	insts, err = validate(t, "instances:\n  vpn:\n    address: 10.147.17.4\n", h)
	if err != nil || insts[0].RelayListener.TrustedIPs[0] != "10.147.17.0/24" {
		t.Errorf("ZeroTier: the interface's network: %v %+v", err, insts)
	}
	_, err = validate(t, "instances:\n  vpn:\n    address: 10.20.0.1\n", h)
	wantError(t, err, "relay_listener.trusted_ips")
	_, err = validate(t, "instances:\n  lan:\n    address: 192.168.1.10\n", h)
	wantError(t, err, "name it (interface: eth1)")
	insts, err = validate(t, "instances:\n  lan:\n    address: 192.168.1.10\n    interface: eth1\n", h)
	if err != nil || insts[0].Interface != "eth1" {
		t.Errorf("a named interface serves: %v", err)
	}
	for l, want := range map[string]string{"tailscale0": "Tailscale", "zt0": "ZeroTier", "tun0": "a tunnel", "eth1": ""} {
		for _, x := range h.links() {
			if x.Name == l && x.VPN() != want {
				t.Errorf("%s: %q, want %q", l, x.VPN(), want)
			}
		}
	}
}

func TestDNS(t *testing.T) {
	h := server(link("wg0", true, "10.8.0.1/24"))
	insts, err := validate(t, "instances:\n  team:\n    address: 10.8.0.1\n    dns: false\n", h)
	if err != nil || insts[0].ServesDNS() {
		t.Errorf("dns: false turns the VPN DNS off: %v", err)
	}
	_, err = validate(t, "instances:\n  w:\n    address: 127.0.0.1\n    dns: true\n", mac)
	wantError(t, err, "shares its stacks")
	_, err = validate(t, "instances:\n  team:\n    address: 10.8.0.1\n    dns: [dev.example.com]\n", h)
	if err == nil {
		t.Error("the former list of domains is refused: the DNS reads them from the routes")
	}
}

func TestAddPorts(t *testing.T) {
	h := server(link("wg0", true, "10.8.0.1/24"))
	insts, err := validate(t, "instances:\n  team:\n    address: 10.8.0.1\n    ports: [3000]\n", h)
	if err != nil {
		t.Fatal(err)
	}
	team := insts[0]
	skipped := team.AddPorts([]int{3001, 443, 80, 3000, 53, 8080, 3001})
	if got := team.Ports; len(got) != 2 || got[0] != 3000 || got[1] != 3001 {
		t.Errorf("new ports only, once, sorted: %v", got)
	}
	if len(skipped) != 2 {
		t.Errorf("53 and the relay listener's port are skipped, and said: %v", skipped)
	}
}

// A server instance also serves its own machine on loopback, for its agents (ADR-0003). One instance per machine does.
func TestLoopback(t *testing.T) {
	h := server(link("wg0", true, "10.8.0.1/24"), link("wg1", true, "10.9.0.1/24"))
	insts, err := validate(t, "instances:\n  team:\n    address: 10.8.0.1\n    loopback: true\n", h)
	if err != nil {
		t.Fatal(err)
	}
	if got := insts[0].LoopbackPorts(); len(got) != 2 || got[0] != 80 || got[1] != 443 {
		t.Errorf("on loopback, the stacks' entry points only, not the relay listener: %v", got)
	}
	_, err = validate(t, "instances:\n  workstation:\n    address: 127.0.0.1\n    loopback: true\n", mac)
	wantError(t, err, "for a server instance")
	_, err = validate(t, `
instances:
  team: {address: 10.8.0.1, loopback: true, projects: [p]}
  clients: {address: 10.9.0.1, loopback: true, projects: [q]}
`, h)
	wantError(t, err, "one instance per machine serves its loopback")
	_, err = validate(t, `
instances:
  team: {address: 10.8.0.1, loopback: true, projects: [p]}
  local: {address: 127.0.0.1, projects: [q]}
`, h)
	wantError(t, err, "one instance per machine serves its loopback")
}

func TestIsolation(t *testing.T) {
	h := server(link("wg0", true, "10.8.0.1/24"), link("wg1", true, "10.9.0.1/24"))
	_, err := validate(t, `
instances:
  a: {address: 10.8.0.1, projects: [p]}
  b: {address: 10.9.0.1, projects: [p]}
`, h)
	wantError(t, err, "read by two instances")
	_, err = validate(t, `
instances:
  a: {address: 10.8.0.1}
  b: {address: 10.9.0.1, projects: [q]}
`, h)
	wantError(t, err, "each one lists its projects")
}

func TestRefusals(t *testing.T) {
	cases := map[string]string{
		"instances:\n  w: {address: 127.0.0.1, ports: [443]}\n":                                            "already in the catalog",
		"instances:\n  w: {address: 127.0.0.1, ports: [3000, 3000]}\n":                                     "listed twice",
		"instances:\n  w: {address: 127.0.0.1, port: [3000]}\n":                                            "field port not found",
		"instances:\n  W: {address: 127.0.0.1}\n":                                                          "lowercase",
		"instances:\n  w: {address: 127.0.0.1, relays: [{to: x, domains: [ftp://a.b]}]}\n":                 "scheme",
		"instances:\n  w: {address: 127.0.0.1, relays: [{to: x, domains: [https://localhost]}]}\n":         "two labels",
		"instances:\n  w: {address: 127.0.0.1, relays: [{domains: [http://localhost]}]}\n":                 "where to",
		"instances:\n  a: {address: 127.0.0.1, projects: [x]}\n  b: {address: 127.0.0.1, projects: [y]}\n": "same address",
	}
	for yaml, want := range cases {
		_, err := validate(t, yaml, mac)
		wantError(t, err, want)
	}
}

func TestPortOffset(t *testing.T) {
	insts, err := validate(t, `
instances:
  a: {address: 127.0.0.1, projects: [x], port_offset: 20000}
  b: {address: 127.0.0.1, projects: [y], port_offset: 30000, relay_listener: {port: 8080, trusted_ips: [0.0.0.0/0]}}
`, mac)
	if err != nil {
		t.Fatal(err)
	}
	if got := insts[1].PublishedPorts(); got[len(got)-1] != 8080 {
		t.Fatalf("the relay listener is published: %v", got)
	}
}

func TestTheExampleValidates(t *testing.T) {
	f, err := Read("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(f, mac); err != nil {
		t.Fatalf("the repository's example validates on a workstation: %v", err)
	}
}

func TestParseRelayedDomain(t *testing.T) {
	for text, want := range map[string]RelayedDomain{
		"https://Dev.Example.com/": {Mode: "https", Domain: "dev.example.com"},
		"tls://dev.legacy.com":     {Mode: "tls", Domain: "dev.legacy.com"},
		"http://localhost":         {Mode: "http", Domain: "localhost"},
		"http://*":                 {Mode: "http", Domain: "*"},
	} {
		got, err := ParseRelayedDomain(text)
		if err != nil || got != want {
			t.Errorf("%s: %+v, %v", text, got, err)
		}
	}
	if _, err := ParseRelayedDomain("https://*"); err == nil {
		t.Error("an https relay does not take everything: it needs a certificate per domain")
	}
}
