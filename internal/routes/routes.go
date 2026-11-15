// Package routes parses the routes a service declares in its `waggle.routes` label (ADR-0002, decision 1): one URL per
// line, where `{stack}` stands for the stack name, followed by the service port. The scheme gives the mode:
//
//	https://{stack}.dev.example.com/          8080   waggle terminates TLS, and routes by host and path
//	https://{stack}.dev.example.com:3000/     5173   a legacy port, terminated by waggle
//	https://*.{stack}.dev.example.com/api     8000   a family of hosts, under a path
//	http://admin.{stack}.localhost/           5173   plain HTTP, on p80
//	tls://{stack}.dev.legacy.com:3000         3000   passthrough: the stack terminates its own TLS
//
// A route whose host does not carry `{stack}` is refused: a stack cannot take another one's name.
package routes

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Mode is how waggle serves a route.
type Mode string

const (
	HTTPS Mode = "https" // waggle terminates TLS
	HTTP  Mode = "http"  // plain text, on p80
	TLS   Mode = "tls"   // passthrough, routed by SNI
)

// Label is where a service declares its routes.
const Label = "waggle.routes"

// Placeholder stands for the stack name in a route.
const Placeholder = "{stack}"

// Route is a parsed route, with `{stack}` replaced.
type Route struct {
	Mode   Mode
	Host   string // a.dev.example.com, or *.a.dev.example.com
	Port   int    // the entry point: 80, 443, or a legacy port
	Path   string // "/", or a prefix without trailing slash: "/api"
	Target int    // the service port
	Stack  string
	Prefix string // labels before the stack name: "", "api", "*"
	Domain string // labels after the stack name: "dev.example.com"
	Text   string // the original line, for logs
}

var (
	reLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	rePath  = regexp.MustCompile(`^(/[A-Za-z0-9._~!$&'()*+,;=:@%-]+)+$`)
)

// Parse reads a service's label value. `exposed` lists the ports the image exposes, for a route that does not name its
// own. Each faulty line gives an error, and the others remain.
func Parse(value, stack string, exposed []int) ([]Route, []error) {
	if !reLabel.MatchString(stack) {
		return nil, []error{fmt.Errorf("the stack name, %q, does not fit in a DNS label (lowercase, digits, dashes)", stack)}
	}
	var routes []Route
	var errs []error
	for _, line := range strings.Split(value, "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		r, err := parseLine(line, stack, exposed)
		if err != nil {
			errs = append(errs, fmt.Errorf("%q: %w", line, err))
			continue
		}
		routes = append(routes, r)
	}
	return routes, errs
}

func parseLine(line, stack string, exposed []int) (Route, error) {
	fields := strings.Fields(line)
	if len(fields) > 2 {
		return Route{}, fmt.Errorf("a route is a URL, then the service port")
	}
	r := Route{Text: line, Stack: stack}
	scheme, rest, ok := strings.Cut(fields[0], "://")
	if !ok {
		return Route{}, fmt.Errorf("a scheme is needed: https://, http:// or tls://")
	}
	r.Mode = Mode(scheme)
	switch r.Mode {
	case HTTPS, TLS:
		r.Port = 443
	case HTTP:
		r.Port = 80
	default:
		return Route{}, fmt.Errorf("scheme %q is not https, http or tls", scheme)
	}

	hostPort, path := rest, "/"
	if i := strings.Index(rest, "/"); i >= 0 {
		hostPort, path = rest[:i], rest[i:]
	}
	host := hostPort
	if h, p, ok := strings.Cut(hostPort, ":"); ok {
		port, err := strconv.Atoi(p)
		if err != nil || port < 1 || port > 65535 {
			return Route{}, fmt.Errorf("port %q does not exist", p)
		}
		host, r.Port = h, port
	}
	if r.Mode == HTTP && r.Port != 80 {
		return Route{}, fmt.Errorf("an http:// route goes through port 80 only")
	}

	if path != "/" {
		path = strings.TrimSuffix(path, "/")
		if r.Mode == TLS {
			return Route{}, fmt.Errorf("a tls:// route has no path: SNI says nothing about it")
		}
		if !rePath.MatchString(path) {
			return Route{}, fmt.Errorf("path %q is not a simple URL path", path)
		}
	}
	r.Path = path

	labels := strings.Split(strings.ToLower(host), ".")
	position := -1
	for i, l := range labels {
		if l == Placeholder {
			if position >= 0 {
				return Route{}, fmt.Errorf("%s appears only once", Placeholder)
			}
			position = i
		}
	}
	if position < 0 {
		return Route{}, fmt.Errorf("the host does not carry %s: a stack does not take another one's name", Placeholder)
	}
	prefix, domain := labels[:position], labels[position+1:]
	if len(domain) == 0 {
		return Route{}, fmt.Errorf("a domain is needed after %s", Placeholder)
	}
	for i, l := range prefix {
		if l == "*" && i == 0 {
			continue
		}
		if !reLabel.MatchString(l) {
			return Route{}, fmt.Errorf("label %q is not a DNS label (only the first one may be *)", l)
		}
	}
	for _, l := range domain {
		if !reLabel.MatchString(l) {
			return Route{}, fmt.Errorf("label %q is not a DNS label", l)
		}
	}
	r.Prefix = strings.Join(prefix, ".")
	r.Domain = strings.Join(domain, ".")
	r.Host = strings.Join(append(append(append([]string{}, prefix...), stack), domain...), ".")

	switch {
	case len(fields) == 2:
		target, err := strconv.Atoi(fields[1])
		if err != nil || target < 1 || target > 65535 {
			return Route{}, fmt.Errorf("service port %q does not exist", fields[1])
		}
		r.Target = target
	case len(exposed) == 1:
		r.Target = exposed[0]
	default:
		return Route{}, fmt.Errorf("the service port is missing, and the image does not expose exactly one")
	}
	return r, nil
}

// DeclaredPorts are the TLS entry ports a label's routes need, besides 443: the legacy ports an instance publishes
// (ADR-0003, decision 6). Faulty lines say nothing.
func DeclaredPorts(value string) []int {
	// Neither the stack name nor the service port matter here
	parsed, _ := Parse(value, "x", []int{1})
	var ports []int
	for _, r := range parsed {
		if r.Mode != HTTP && r.Port != 443 {
			ports = append(ports, r.Port)
		}
	}
	return ports
}

// Wildcard tells whether the host is a family (*.a.dev.example.com).
func (r Route) Wildcard() bool { return strings.HasPrefix(r.Host, "*.") }

// Key identifies what a route takes: two routes with the same key compete for the same requests.
func (r Route) Key() string {
	return fmt.Sprintf("%s %s:%d%s", r.Mode, r.Host, r.Port, r.Path)
}

// URL is the route as a browser writes it.
func (r Route) URL() string {
	port := ""
	if !(r.Mode == HTTP && r.Port == 80) && !(r.Mode != HTTP && r.Port == 443) {
		port = fmt.Sprintf(":%d", r.Port)
	}
	path := r.Path
	if r.Mode == TLS {
		path = ""
	}
	return fmt.Sprintf("%s://%s%s%s", r.Mode, r.Host, port, path)
}
