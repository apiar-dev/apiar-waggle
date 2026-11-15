package routes

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	value := `
https://{stack}.dev.example.com/          8080
https://{stack}.dev.example.com:3000/     5173   # Vite, on its legacy port
https://*.{stack}.dev.example.com/api/    8000
http://admin.{stack}.localhost/           5173
tls://{stack}.dev.legacy.com:3000         3000
`
	rs, errs := Parse(value, "go-1234", nil)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	want := []struct {
		mode                       Mode
		host, prefix, domain, path string
		port, target               int
		url                        string
	}{
		{HTTPS, "go-1234.dev.example.com", "", "dev.example.com", "/", 443, 8080, "https://go-1234.dev.example.com/"},
		{HTTPS, "go-1234.dev.example.com", "", "dev.example.com", "/", 3000, 5173, "https://go-1234.dev.example.com:3000/"},
		{HTTPS, "*.go-1234.dev.example.com", "*", "dev.example.com", "/api", 443, 8000, "https://*.go-1234.dev.example.com/api"},
		{HTTP, "admin.go-1234.localhost", "admin", "localhost", "/", 80, 5173, "http://admin.go-1234.localhost/"},
		{TLS, "go-1234.dev.legacy.com", "", "dev.legacy.com", "/", 3000, 3000, "tls://go-1234.dev.legacy.com:3000"},
	}
	if len(rs) != len(want) {
		t.Fatalf("%d routes parsed: %+v", len(rs), rs)
	}
	for i, w := range want {
		r := rs[i]
		if r.Mode != w.mode || r.Host != w.host || r.Prefix != w.prefix || r.Domain != w.domain || r.Path != w.path ||
			r.Port != w.port || r.Target != w.target || r.URL() != w.url {
			t.Errorf("route %d: %+v, want %+v", i, r, w)
		}
	}
	if !rs[2].Wildcard() || rs[0].Wildcard() {
		t.Error("only a *. host is a family")
	}
}

func TestExposedPort(t *testing.T) {
	rs, errs := Parse("https://{stack}.dev.example.com/", "a", []int{8080})
	if len(errs) > 0 || rs[0].Target != 8080 {
		t.Fatalf("the only exposed port is the target: %+v %v", rs, errs)
	}
	_, errs = Parse("https://{stack}.dev.example.com/", "a", []int{80, 443})
	if len(errs) != 1 {
		t.Fatal("two exposed ports: the route must name its own")
	}
}

func TestRefusals(t *testing.T) {
	cases := map[string]string{
		"https://intruder.dev.example.com/ 80":  "does not carry {stack}",
		"https://{stack}.{stack}.com/ 80":       "only once",
		"http://{stack}.localhost:3000/ 80":     "port 80 only",
		"tls://{stack}.dev.legacy.com/api 3000": "no path",
		"ftp://{stack}.dev.example.com/ 21":     "is not https",
		"{stack}.dev.example.com 80":            "scheme",
		"https://{stack}/ 80":                   "domain is needed",
		"https://{stack}.dev.example.com/ 0":    "does not exist",
		"https://a.*.{stack}.com/ 80":           "only the first",
	}
	for line, want := range cases {
		_, errs := Parse(line, "a", nil)
		if len(errs) != 1 || !strings.Contains(errs[0].Error(), want) {
			t.Errorf("%q: want %q, got %v", line, want, errs)
		}
	}
	if _, errs := Parse("https://{stack}.dev.example.com/ 80", "My_Stack", nil); len(errs) != 1 {
		t.Error("a stack name outside a DNS label is refused")
	}
}

func TestOneFaultyLineTakesOnlyItself(t *testing.T) {
	rs, errs := Parse("https://intruder.com/ 80\nhttps://{stack}.dev.example.com/ 80", "a", nil)
	if len(rs) != 1 || len(errs) != 1 {
		t.Fatalf("%d routes, %d errors", len(rs), len(errs))
	}
}

func TestDeclaredPorts(t *testing.T) {
	got := DeclaredPorts("https://{stack}.dev.example.com/ 80\nhttps://{stack}.dev.example.com:3000/ 5173\ntls://{stack}.legacy.com:3001\nhttp://{stack}.localhost/ 80\nnonsense")
	if len(got) != 2 || got[0] != 3000 || got[1] != 3001 {
		t.Fatalf("the legacy TLS ports only: %v", got)
	}
}
