// Package firewall puts, on a server, the rule that closes a VPN address to anything that does not come in through its
// interface (ADR-0001, decision 4):
//
//	iptables -I DOCKER-USER -m conntrack --ctorigdst <address> --ctdir ORIGINAL ! -i <interface> -j DROP
//
// `--ctdir ORIGINAL` is essential: a stack's replies enter FORWARD through Docker's bridge and carry the same original
// destination; without it, the rule drops them too, and cuts off the tunnel members.
package firewall

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Runner runs a command and returns its output.
type Runner func(name string, args ...string) (string, error)

// System runs the command on the machine.
func System(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Rule is the rule, without its verb (-I, -C, -D).
func Rule(address, iface string) []string {
	return []string{"DOCKER-USER", "-m", "conntrack", "--ctorigdst", address, "--ctdir", "ORIGINAL", "!", "-i", iface, "-j", "DROP"}
}

// Binaries are the two firewalls a host may run. Docker writes its rules into one of them; the rule goes where its
// DOCKER-USER chain lives.
var Binaries = []string{"iptables-nft", "iptables-legacy"}

// Ensure puts the rule into the first firewall that carries the DOCKER-USER chain, unless it is already there, and
// returns the binary where it just put it. It stops at the first one: probing iptables-legacy on an nftables host would
// create empty tables there.
func Ensure(run Runner, address, iface string) ([]string, error) {
	for _, b := range Binaries {
		if _, err := run(b, "-n", "-L", "DOCKER-USER"); err != nil {
			continue
		}
		if _, err := run(b, append([]string{"-C"}, Rule(address, iface)...)...); err == nil {
			return nil, nil
		}
		if out, err := run(b, append([]string{"-I"}, Rule(address, iface)...)...); err != nil {
			return nil, fmt.Errorf("%s did not put the rule: %v %s", b, err, out)
		}
		return []string{b}, nil
	}
	return nil, errors.New("no DOCKER-USER chain on this machine: does Docker run with its iptables firewall?")
}

// Command is the rule as one would read it in a terminal.
func Command(address, iface string) string {
	return "iptables -I " + strings.Join(Rule(address, iface), " ")
}
