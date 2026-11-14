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
	ip, network, _ := net.ParseCIDR(cidr)
	network.IP = ip
	return Link{Name: name, WireGuard: wg, Networks: []*net.IPNet{network}}
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
	wantError(t, err, "not a WireGuard interface")
	_, err = validate(t, "instances:\n  vps:\n    address: 10.9.0.1\n", server(link("wg0", true, "10.8.0.1/24")))
	wantError(t, err, "no interface")
}

func TestServer(t *testing.T) {
	insts, err := validate(t, `
instances:
  team:
    address: 10.8.0.1
    projects: [my-project]
    dns: [dev.example.com]
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
