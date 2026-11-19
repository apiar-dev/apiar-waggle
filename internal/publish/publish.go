// Package publish is what a server tells its workstations through its VPN DNS: the domains it has served, with their
// mode, and its legacy ports, as TXT records under Name (ADR-0003, decision 9). A workstation reads them at `waggle up`,
// and relays those domains without listing them.
package publish

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/apiar-dev/apiar-waggle/internal/config"
)

// Zone and Name are where the server's DNS answers: one TXT record per entry.
const (
	Zone = "waggle."
	Name = "relay." + Zone
)

// List is what a server publishes.
type List struct {
	Domains []string // "https://dev.example.com", "tls://dev.legacy.com", "http://localhost", sorted
	Ports   []int    // legacy ports, sorted
}

// Records are the list's TXT entries: "https dev.example.com", and "ports 3000 3001".
func (l List) Records() []string {
	var records []string
	for _, d := range l.Domains {
		mode, domain, _ := strings.Cut(d, "://")
		records = append(records, mode+" "+domain)
	}
	if len(l.Ports) > 0 {
		var ports []string
		for _, p := range l.Ports {
			ports = append(ports, strconv.Itoa(p))
		}
		records = append(records, "ports "+strings.Join(ports, " "))
	}
	return records
}

// Parse reads TXT entries back, skipping the ones it does not understand: a newer server may publish more.
func Parse(records []string) List {
	var l List
	seen := map[string]bool{}
	for _, r := range records {
		fields := strings.Fields(r)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "ports" {
			for _, f := range fields[1:] {
				if p, err := strconv.Atoi(f); err == nil && p > 0 && p < 65536 && !seen["port "+f] {
					seen["port "+f] = true
					l.Ports = append(l.Ports, p)
				}
			}
			continue
		}
		if len(fields) != 2 {
			continue
		}
		text := fields[0] + "://" + fields[1]
		if _, err := config.ParseRelayedDomain(text); err == nil && !seen[text] {
			seen[text] = true
			l.Domains = append(l.Domains, text)
		}
	}
	sort.Strings(l.Domains)
	sort.Ints(l.Ports)
	return l
}

// Lookup asks a server's VPN DNS for its list, over TCP: Docker Desktop, where a Mac's waggle runs, does not carry UDP
// to an address on the VPN, while TCP goes through. The server's DNS answers both. A server that published nothing yet
// answers an empty list.
func Lookup(ctx context.Context, server string, port int) (List, error) {
	address := net.JoinHostPort(server, strconv.Itoa(port))
	// Given a stream rather than a packet connection, Go's resolver speaks DNS over TCP
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", address)
	}}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	records, err := resolver.LookupTXT(ctx, Name)
	if err != nil {
		reason := err.Error()
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) {
			switch {
			case dnsErr.IsNotFound:
				return List{}, nil
			case dnsErr.IsTimeout:
				reason = "timeout"
			default:
				reason = dnsErr.Err
			}
		}
		return List{}, fmt.Errorf("its DNS, %s, does not answer (%s)", address, reason)
	}
	return Parse(records), nil
}
