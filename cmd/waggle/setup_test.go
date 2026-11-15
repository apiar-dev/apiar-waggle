package main

import (
	"bufio"
	"net"
	"strings"
	"testing"

	"github.com/apiar-dev/apiar-waggle/internal/config"
)

func link(name, kind, cidr string) config.Link {
	ip, network, _ := net.ParseCIDR(cidr)
	network.IP = ip
	return config.Link{Name: name, Kind: kind, Networks: []*net.IPNet{network}}
}

var vps = []config.Link{
	link("lo", "", "127.0.0.1/8"),
	link("eth0", "", "51.15.0.1/24"),
	link("ens10", "", "10.0.0.2/16"),
	link("docker0", "", "172.17.0.1/16"),
	link("tailscale0", config.TUN, "100.101.102.103/32"),
}

func yamlOf(t *testing.T, f *config.File) string {
	t.Helper()
	b, err := marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(string(b), header)
}

// Without a terminal, setup guesses nothing on Linux: a VPN interface is no proof that its devices should reach the
// stacks (ADR-0003, decision 6).
func TestSetupWithoutTerminal(t *testing.T) {
	a := answers{}
	err := a.ask(nil, "Linux", nil, vps)
	if err == nil || !strings.Contains(err.Error(), "--share") || !strings.Contains(err.Error(), "tailscale0 (Tailscale), 100.101.102.103") {
		t.Fatalf("it asks for --share, and lists what it detected: %v", err)
	}
	a = answers{}
	if err := a.ask(nil, "Darwin", nil, nil); err != nil || a.share != none || a.relay != none {
		t.Fatalf("on a Mac, the default is harmless: a workstation, without relay: %+v %v", a, err)
	}
	current := &config.File{Instances: map[string]*config.Instance{"vpn": {Address: "100.101.102.103", Loopback: true}}}
	a = answers{}
	if err := a.ask(nil, "Linux", current, vps); err != nil || a.share != "tailscale0" {
		t.Fatalf("run again without a terminal, it keeps the current answers: %+v %v", a, err)
	}
}

func TestSetupPlan(t *testing.T) {
	f, err := plan(nil, answers{share: "tailscale0", relay: none}, vps)
	if err != nil {
		t.Fatal(err)
	}
	if got := yamlOf(t, f); got != "instances:\n  vpn:\n    address: 100.101.102.103\n    loopback: true\n" {
		t.Errorf("a server, on its VPN address, with loopback:\n%s", got)
	}
	if _, err := config.Validate(f, config.Host{OS: "Linux", Links: func() ([]config.Link, error) { return vps, nil }}); err != nil {
		t.Errorf("it validates: %v", err)
	}

	f, _ = plan(nil, answers{share: "ens10", relay: none}, vps)
	if inst := f.Instances["vpn"]; inst.Interface != "ens10" {
		t.Errorf("another interface is named, so that it serves: %+v", inst)
	}

	// A workstation keeps what setup does not ask, and the relay it had, with its settings
	current := &config.File{Instances: map[string]*config.Instance{"mac": {
		Address: "127.0.0.1", Ports: []int{3000}, Projects: []string{"p"},
		Relays: []config.Relay{{To: "10.8.0.1", PortOffset: 5}},
	}}}
	f, _ = plan(current, answers{share: none, relay: "10.8.0.1"}, nil)
	if got := yamlOf(t, f); !strings.Contains(got, "mac:") || !strings.Contains(got, "ports:") || !strings.Contains(got, "port_offset: 5") || strings.Contains(got, "domains") {
		t.Errorf("the current settings stay, and the relay learns its domains:\n%s", got)
	}
	f, _ = plan(current, answers{share: none, relay: none}, nil)
	if len(f.Instances["mac"].Relays) != 0 {
		t.Error("relay none removes the relay")
	}
	if _, err := plan(nil, answers{share: "wg9", relay: none}, vps); err == nil {
		t.Error("an interface the machine does not have is refused")
	}
}

func answer(input string) (*prompt, *strings.Builder) {
	out := &strings.Builder{}
	return &prompt{in: bufio.NewReader(strings.NewReader(input)), out: out}, out
}

func TestSetupQuestions(t *testing.T) {
	tunnels := vpnLinks(vps)
	p, out := answer("\n")
	if got := p.shareQuestion(tunnels, vps, "tailscale0"); got != "tailscale0" {
		t.Errorf("the detected VPN, by default: %q", got)
	}
	for _, want := range []string{"1. No: it serves its own browsers and agents only", "2. Yes, through tailscale0 (Tailscale), 100.101.102.103", "3. Yes, through another interface", "[2]"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the question shows %q:\n%s", want, out)
		}
	}
	if strings.Contains(out.String(), "docker0") || strings.Contains(out.String(), "eth0") {
		t.Errorf("neither Docker's bridge nor a public address is offered:\n%s", out)
	}
	p, out = answer("9\n3\n\n")
	if got := p.shareQuestion(tunnels, vps, none); got != "ens10" {
		t.Errorf("another interface, after a wrong answer: %q", got)
	}
	if !strings.Contains(out.String(), "reachable from the internet") {
		t.Errorf("another interface comes with a warning:\n%s", out)
	}
	p, _ = answer("none\n")
	if got := p.relayQuestion("10.8.0.1"); got != none {
		t.Errorf("none removes the relay: %q", got)
	}
	p, _ = answer("maybe\nn\n")
	if p.confirm("Write it?", true) {
		t.Error("n says no, after an answer it does not understand")
	}
}
