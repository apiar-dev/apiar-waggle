// Package traefik translates the routes of the stacks and the relays of an instance into Traefik dynamic configuration,
// which the file provider reloads (ADR-0002, decisions 1 to 3). Everything is written as JSON, which Traefik's YAML
// reader takes as is.
package traefik

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/apiar-dev/apiar-waggle/internal/config"
	"github.com/apiar-dev/apiar-waggle/internal/routes"
)

// RelayDialTimeout is the dial timeout of a relay. With the VPN down, a relayed name fails in two seconds rather than
// thirty (ADR-0001, decision 5).
const RelayDialTimeout = "2s"

// PortHeader is where a workstation's relay names the entry port of a request. A server's `relay` entry point is only
// open to the VPN.
const PortHeader = "X-Waggle-Port"

// ServedRoute is a route, and the service that serves it on the instance network.
type ServedRoute struct {
	routes.Route
	Container string
	IP        string
}

// Certificate is a certificate's files, as Traefik reads them.
type Certificate struct {
	CertFile string
	KeyFile  string
}

type section = map[string]any

// EntryPoint is the name of a port's entry point, p<port>.
func EntryPoint(port int) string { return fmt.Sprintf("p%d", port) }

// Routes are the routers and services of the stacks.
func Routes(inst *config.Instance, rs []ServedRoute) section {
	httpR, httpS, tcpR, tcpS := section{}, section{}, section{}, section{}
	count := map[string]int{}
	for _, r := range rs {
		count[r.Container]++
		name := fmt.Sprintf("%s-%d", Sanitize(r.Container), count[r.Container])
		switch r.Mode {
		case routes.HTTPS:
			rule := httpRule(r.Route)
			httpR[name] = section{"entryPoints": []string{EntryPoint(r.Port)}, "rule": rule, "tls": section{}, "service": name}
			if inst.RelayListener != nil {
				// A workstation's relay arrives over HTTP, and names in PortHeader the entry point the request reached on its
				// side: a route on port 3000 only answers a request that came in on port 3000
				httpR[name+"-relay"] = section{
					"entryPoints": []string{"relay"},
					"rule":        fmt.Sprintf("(%s) && Header(`%s`, `%d`)", rule, PortHeader, r.Port),
					"service":     name,
				}
			}
			httpS[name] = httpService(fmt.Sprintf("http://%s:%d", r.IP, r.Target), false)
		case routes.HTTP:
			httpR[name] = section{"entryPoints": []string{"p80"}, "rule": httpRule(r.Route), "service": name}
			httpS[name] = httpService(fmt.Sprintf("http://%s:%d", r.IP, r.Target), false)
		case routes.TLS:
			tcpR[name] = section{
				"entryPoints": []string{EntryPoint(r.Port)},
				"rule":        sniRule(r.Route),
				"tls":         section{"passthrough": true},
				"service":     name,
			}
			tcpS[name] = tcpService(fmt.Sprintf("%s:%d", r.IP, r.Target), false)
		}
	}
	return assemble(httpR, httpS, nil, tcpR, tcpS, false)
}

// Relays are a workstation's relay routes, one per domain, in the domain's mode, with a negative priority: every stack
// route goes first (ADR-0001, decision 5; ADR-0002, decision 3).
func Relays(inst *config.Instance) section {
	httpR, httpS, httpM, tcpR, tcpS := section{}, section{}, section{}, section{}, section{}
	for i, rel := range inst.Relays {
		n := i + 1
		for _, text := range rel.Domains {
			d, err := config.ParseRelayedDomain(text)
			if err != nil {
				continue // validated configuration has none
			}
			base := fmt.Sprintf("relay-%d-%s", n, Sanitize(d.Domain))
			switch d.Mode {
			case "https":
				// Terminated here, relayed over HTTP to the server's `relay` entry point, request by request
				service := fmt.Sprintf("relay-%d-https", n)
				httpS[service] = httpService(fmt.Sprintf("http://%s:%d", rel.To, rel.RelayPort+rel.PortOffset), true)
				for _, p := range inst.TLSPorts() {
					// The server finds the original entry point through this header, not X-Forwarded-Port, which takes
					// the port of the Host header
					mark := fmt.Sprintf("relay-port-p%d", p)
					httpM[mark] = section{"headers": section{"customRequestHeaders": section{PortHeader: fmt.Sprint(p)}}}
					httpR[fmt.Sprintf("%s-https-p%d", base, p)] = section{
						"entryPoints": []string{EntryPoint(p)}, "rule": domainRule("HostRegexp", d.Domain),
						"priority": -1, "tls": section{}, "middlewares": []string{mark}, "service": service,
					}
				}
			case "tls":
				// Passthrough, relayed over TCP to the same server port; never a HostSNI(`*`) catch-all, which receives
				// nothing next to HTTPS routes
				for _, p := range inst.TLSPorts() {
					service := fmt.Sprintf("relay-%d-tls-p%d", n, p)
					tcpS[service] = tcpService(fmt.Sprintf("%s:%d", rel.To, p+rel.PortOffset), true)
					tcpR[fmt.Sprintf("%s-tls-p%d", base, p)] = section{
						"entryPoints": []string{EntryPoint(p)}, "rule": domainRule("HostSNIRegexp", d.Domain),
						"priority": -1, "tls": section{"passthrough": true}, "service": service,
					}
				}
			case "http":
				service := fmt.Sprintf("relay-%d-http", n)
				httpS[service] = httpService(fmt.Sprintf("http://%s:%d", rel.To, 80+rel.PortOffset), true)
				rule, priority := domainRule("HostRegexp", d.Domain), -1
				if d.Domain == "*" {
					rule, priority = "PathPrefix(`/`)", -2
				}
				httpR[base+"-http"] = section{"entryPoints": []string{"p80"}, "rule": rule, "priority": priority, "service": service}
			}
		}
	}
	return assemble(httpR, httpS, httpM, tcpR, tcpS, len(inst.Relays) > 0)
}

// TLS lists the certificates Traefik presents, chosen by SNI.
func TLS(certs []Certificate) section {
	if len(certs) == 0 {
		return section{}
	}
	var list []any
	for _, c := range certs {
		list = append(list, section{"certFile": c.CertFile, "keyFile": c.KeyFile})
	}
	return section{"tls": section{"certificates": list}}
}

// WantedCertificates are the certificates needed by HTTPS routes and by domains relayed over HTTPS, by file name.
//   - A host one label under the domain (<stack>.<domain>): the domain's wildcard, *.<domain>.
//   - A host with more labels (api.<stack>.<domain>): the stack's own certificate, which covers <stack>.<domain> and the
//     parent of each of its hosts, as a wildcard. That is Upsun's choice, one certificate per environment.
//   - A single-label domain (localhost) has no wildcard: the stack's certificate names its hosts.
func WantedCertificates(inst *config.Instance, rs []ServedRoute) map[string][]string {
	sets := map[string]map[string]bool{}
	add := func(file string, names ...string) {
		if sets[file] == nil {
			sets[file] = map[string]bool{}
		}
		for _, n := range names {
			sets[file][n] = true
		}
	}
	for _, r := range rs {
		if r.Mode != routes.HTTPS {
			continue
		}
		stack := r.Stack + "." + r.Domain
		switch {
		case r.Prefix == "" && strings.Contains(r.Domain, "."):
			add("wildcard."+r.Domain, "*."+r.Domain, r.Domain)
		case r.Prefix == "":
			add("stack."+stack, stack)
		default:
			_, parent, _ := strings.Cut(r.Host, ".")
			add("stack."+stack, stack, "*."+parent)
		}
	}
	for _, rel := range inst.Relays {
		for _, text := range rel.Domains {
			if d, err := config.ParseRelayedDomain(text); err == nil && d.Mode == "https" {
				add("wildcard."+d.Domain, "*."+d.Domain, d.Domain)
			}
		}
	}
	wanted := map[string][]string{}
	for file, names := range sets {
		var list []string
		for n := range names {
			list = append(list, n)
		}
		sort.Strings(list)
		wanted[file] = list
	}
	return wanted
}

// Sanitize makes a name usable as a router or file name.
func Sanitize(s string) string {
	s = strings.ToLower(strings.TrimPrefix(s, "/"))
	s = strings.ReplaceAll(s, "*", "wildcard")
	return strings.Trim(reNotName.ReplaceAllString(s, "-"), "-")
}

var reNotName = regexp.MustCompile(`[^a-z0-9-]+`)

func hostRule(r routes.Route) string {
	if r.Wildcard() {
		return fmt.Sprintf("HostRegexp(`^.+\\.%s$`)", regexp.QuoteMeta(strings.TrimPrefix(r.Host, "*.")))
	}
	return fmt.Sprintf("Host(`%s`)", r.Host)
}

func httpRule(r routes.Route) string {
	rule := hostRule(r)
	if r.Path != "/" {
		// Path(`/api`) || PathPrefix(`/api/`): /api and what is below it, but not /apiv2
		rule += fmt.Sprintf(" && (Path(`%s`) || PathPrefix(`%s/`))", r.Path, r.Path)
	}
	return rule
}

func sniRule(r routes.Route) string {
	if r.Wildcard() {
		return fmt.Sprintf("HostSNIRegexp(`^.+\\.%s$`)", regexp.QuoteMeta(strings.TrimPrefix(r.Host, "*.")))
	}
	return fmt.Sprintf("HostSNI(`%s`)", r.Host)
}

// domainRule matches a domain and everything below it.
func domainRule(matcher, domain string) string {
	return fmt.Sprintf("%s(`^(.+\\.)?%s$`)", matcher, regexp.QuoteMeta(domain))
}

func httpService(url string, relay bool) section {
	lb := section{"passHostHeader": true, "servers": []any{section{"url": url}}}
	if relay {
		lb["serversTransport"] = "relay"
	}
	return section{"loadBalancer": lb}
}

func tcpService(address string, relay bool) section {
	lb := section{"servers": []any{section{"address": address}}}
	if relay {
		lb["serversTransport"] = "relay"
	}
	return section{"loadBalancer": lb}
}

func assemble(httpR, httpS, httpM, tcpR, tcpS section, transports bool) section {
	cfg := section{}
	if len(httpR) > 0 || transports {
		h := section{}
		if len(httpR) > 0 {
			h["routers"], h["services"] = httpR, httpS
		}
		if len(httpM) > 0 {
			h["middlewares"] = httpM
		}
		if transports {
			h["serversTransports"] = section{"relay": section{"forwardingTimeouts": section{"dialTimeout": RelayDialTimeout}}}
		}
		cfg["http"] = h
	}
	if len(tcpR) > 0 || transports {
		t := section{}
		if len(tcpR) > 0 {
			t["routers"], t["services"] = tcpR, tcpS
		}
		if transports {
			t["serversTransports"] = section{"relay": section{"dialTimeout": RelayDialTimeout}}
		}
		cfg["tcp"] = t
	}
	return cfg
}
