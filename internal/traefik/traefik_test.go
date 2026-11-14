package traefik

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/apiar-dev/apiar-waggle/internal/config"
	"github.com/apiar-dev/apiar-waggle/internal/routes"
)

func served(t *testing.T, value, stack, container, ip string) []ServedRoute {
	t.Helper()
	rs, errs := routes.Parse(value, stack, nil)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	var out []ServedRoute
	for _, r := range rs {
		out = append(out, ServedRoute{Route: r, Container: container, IP: ip})
	}
	return out
}

// at reads a value in the configuration, by its keys.
func at(t *testing.T, cfg map[string]any, keys ...string) any {
	t.Helper()
	b, _ := json.Marshal(cfg)
	var v any
	json.Unmarshal(b, &v)
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("%v: no object at %q", keys, k)
		}
		v = m[k]
	}
	return v
}

func TestRoutes(t *testing.T) {
	inst := &config.Instance{Name: "workstation", Ports: []int{3000}}
	rs := served(t, `
https://{stack}.test.example/ 8080
https://{stack}.test.example/api 9000
https://*.{stack}.test.example:3000/ 5173
http://admin.{stack}.localhost/ 8080
tls://{stack}.legacy.example 8443
`, "a", "/a-front-1", "172.30.0.5")
	c := Routes(inst, rs)
	if got := at(t, c, "http", "routers", "a-front-1-1", "rule"); got != "Host(`a.test.example`)" {
		t.Errorf("https route: %v", got)
	}
	if got := at(t, c, "http", "routers", "a-front-1-1", "tls"); !reflect.DeepEqual(got, map[string]any{}) {
		t.Errorf("waggle terminates TLS: %v", got)
	}
	if got := at(t, c, "http", "routers", "a-front-1-2", "rule"); got != "Host(`a.test.example`) && (Path(`/api`) || PathPrefix(`/api/`))" {
		t.Errorf("route with a path: %v", got)
	}
	if got := at(t, c, "http", "routers", "a-front-1-3", "rule"); got != "HostRegexp(`^.+\\.a\\.test\\.example$`)" {
		t.Errorf("host family: %v", got)
	}
	if got := at(t, c, "http", "routers", "a-front-1-3", "entryPoints"); !reflect.DeepEqual(got, []any{"p3000"}) {
		t.Errorf("legacy port: %v", got)
	}
	if got := at(t, c, "http", "routers", "a-front-1-4", "entryPoints"); !reflect.DeepEqual(got, []any{"p80"}) {
		t.Errorf("http route: %v", got)
	}
	if got := at(t, c, "http", "services", "a-front-1-2", "loadBalancer", "servers"); !reflect.DeepEqual(got, []any{map[string]any{"url": "http://172.30.0.5:9000"}}) {
		t.Errorf("service: %v", got)
	}
	if got := at(t, c, "tcp", "routers", "a-front-1-5", "tls", "passthrough"); got != true {
		t.Errorf("passthrough route: %v", got)
	}
	if got := at(t, c, "tcp", "services", "a-front-1-5", "loadBalancer", "servers"); !reflect.DeepEqual(got, []any{map[string]any{"address": "172.30.0.5:8443"}}) {
		t.Errorf("passthrough service: %v", got)
	}
	if at(t, c, "http", "routers", "a-front-1-1-relay") != nil {
		t.Error("a workstation has no relay entry point")
	}
}

func TestServerRoutes(t *testing.T) {
	inst := &config.Instance{Name: "team", RelayListener: &config.RelayListener{Port: 8080, TrustedIPs: []string{"10.8.0.0/24"}}}
	c := Routes(inst, served(t, "https://{stack}.test.example:443/ 8080", "r", "r-app-1", "172.30.0.9"))
	got := at(t, c, "http", "routers", "r-app-1-1-relay")
	want := map[string]any{
		"entryPoints": []any{"relay"},
		"rule":        "(Host(`r.test.example`)) && Header(`X-Waggle-Port`, `443`)",
		"service":     "r-app-1-1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("a server's relay listener: %v", got)
	}
}

func TestRelays(t *testing.T) {
	inst := &config.Instance{Name: "workstation", Ports: []int{3000}, Relays: []config.Relay{{
		To: "10.8.0.1", RelayPort: 8080,
		Domains: []string{"https://test.example", "tls://legacy.example", "http://localhost", "http://*"},
	}}}
	c := Relays(inst)
	for _, p := range []string{"p443", "p3000"} {
		r := at(t, c, "http", "routers", "relay-1-test-example-https-"+p).(map[string]any)
		if r["rule"] != "HostRegexp(`^(.+\\.)?test\\.example$`)" || r["priority"] != float64(-1) || r["service"] != "relay-1-https" {
			t.Errorf("https relay on %s: %v", p, r)
		}
		if got := at(t, c, "http", "middlewares", "relay-port-"+p, "headers", "customRequestHeaders", "X-Waggle-Port"); got != strings.TrimPrefix(p, "p") {
			t.Errorf("the relay names its entry port, %s: %v", p, got)
		}
		tr := at(t, c, "tcp", "routers", "relay-1-legacy-example-tls-"+p).(map[string]any)
		if tr["rule"] != "HostSNIRegexp(`^(.+\\.)?legacy\\.example$`)" || tr["priority"] != float64(-1) {
			t.Errorf("tls relay on %s: %v", p, tr)
		}
	}
	if got := at(t, c, "http", "services", "relay-1-https", "loadBalancer", "servers"); !reflect.DeepEqual(got, []any{map[string]any{"url": "http://10.8.0.1:8080"}}) {
		t.Errorf("the https relay targets the server's relay entry point: %v", got)
	}
	if got := at(t, c, "tcp", "services", "relay-1-tls-p3000", "loadBalancer", "servers"); !reflect.DeepEqual(got, []any{map[string]any{"address": "10.8.0.1:3000"}}) {
		t.Errorf("the tls relay targets the same port: %v", got)
	}
	all := at(t, c, "http", "routers", "relay-1-wildcard-http").(map[string]any)
	if all["rule"] != "PathPrefix(`/`)" || all["priority"] != float64(-2) {
		t.Errorf("the http relay for everything else comes after the others: %v", all)
	}
	if got := at(t, c, "http", "serversTransports", "relay", "forwardingTimeouts", "dialTimeout"); got != RelayDialTimeout {
		t.Errorf("relay timeout: %v", got)
	}
	if got := at(t, c, "tcp", "serversTransports", "relay", "dialTimeout"); got != RelayDialTimeout {
		t.Errorf("tcp relay timeout: %v", got)
	}
}

func TestWantedCertificates(t *testing.T) {
	inst := &config.Instance{Relays: []config.Relay{{To: "x", Domains: []string{"https://server.example", "tls://legacy.example"}}}}
	rs := served(t, `
https://{stack}.test.example/ 80
https://api.{stack}.test.example/ 80
https://*.{stack}.test.example/ 80
https://{stack}.localhost/ 80
tls://{stack}.legacy.example 443
http://{stack}.localhost/ 80
`, "a", "a-1", "1.1.1.1")
	got := WantedCertificates(inst, rs)
	want := map[string][]string{
		"wildcard.test.example":   {"*.test.example", "test.example"},
		"stack.a.test.example":    {"*.a.test.example", "a.test.example"},
		"stack.a.localhost":       {"a.localhost"},
		"wildcard.server.example": {"*.server.example", "server.example"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wanted certificates: %v", got)
	}
}

func TestSanitize(t *testing.T) {
	if got := Sanitize("/My_Project-a.front.1"); got != "my-project-a-front-1" || strings.ContainsAny(got, "_./") {
		t.Errorf("%q", got)
	}
}
