package firewall

import (
	"errors"
	"strings"
	"testing"
)

// fake is a host whose chains and rules live in memory.
type fake struct {
	chains map[string]bool
	rules  map[string]bool
	calls  []string
}

func (f *fake) run(name string, args ...string) (string, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if !f.chains[name] {
		return "", errors.New("no chain")
	}
	rule := name + " " + strings.Join(args[1:], " ")
	switch args[0] {
	case "-n":
		return "", nil
	case "-C":
		if f.rules[rule] {
			return "", nil
		}
		return "", errors.New("absent")
	case "-I":
		f.rules[rule] = true
		return "", nil
	}
	return "", errors.New("unexpected")
}

func TestEnsure(t *testing.T) {
	f := &fake{chains: map[string]bool{"iptables-nft": true}, rules: map[string]bool{}}
	put, err := Ensure(f.run, "10.8.0.1", "wg0")
	if err != nil || len(put) != 1 || put[0] != "iptables-nft" {
		t.Fatalf("the rule goes where the chain lives: %v, %v", put, err)
	}
	want := "iptables-nft -I DOCKER-USER -m conntrack --ctorigdst 10.8.0.1 --ctdir ORIGINAL ! -i wg0 -j DROP"
	if !strings.Contains(strings.Join(f.calls, "\n"), want) {
		t.Fatalf("the rule, --ctdir ORIGINAL included:\n%s", strings.Join(f.calls, "\n"))
	}
	put, err = Ensure(f.run, "10.8.0.1", "wg0")
	if err != nil || len(put) != 0 {
		t.Fatalf("already there, nothing changes: %v, %v", put, err)
	}
}

func TestOneFirewallOnly(t *testing.T) {
	f := &fake{chains: map[string]bool{"iptables-nft": true, "iptables-legacy": true}, rules: map[string]bool{}}
	if _, err := Ensure(f.run, "10.8.0.1", "wg0"); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "iptables-legacy") {
			t.Fatalf("the first firewall carrying the chain is enough: %v", f.calls)
		}
	}
}

func TestNoChain(t *testing.T) {
	f := &fake{chains: map[string]bool{}, rules: map[string]bool{}}
	if _, err := Ensure(f.run, "10.8.0.1", "wg0"); err == nil || !strings.Contains(err.Error(), "DOCKER-USER") {
		t.Fatalf("without a DOCKER-USER chain, waggle says so: %v", err)
	}
}
