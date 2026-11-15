package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/apiar-dev/apiar-waggle/internal/ca"
	"github.com/apiar-dev/apiar-waggle/internal/config"
	"github.com/apiar-dev/apiar-waggle/internal/docker"
)

type endpoint = struct {
	IPAddress string `json:"IPAddress"`
}

// fakeDocker keeps containers in memory; Connect connects them to the network.
type fakeDocker struct {
	containers map[string]*docker.Container
	created    map[string]int64
	connected  []string
}

func (f *fakeDocker) add(id, name, project, stack, routesValue string, created int64) {
	ct := &docker.Container{ID: id, Name: "/" + name}
	ct.State.Running = true
	ct.Config.Labels = map[string]string{ProjectLabel: project, StackLabel: stack, "waggle.routes": routesValue}
	ct.NetworkSettings.Networks = map[string]endpoint{stack + "_default": {IPAddress: "172.18.0.2"}}
	f.containers[id] = ct
	f.created[id] = created
}

func (f *fakeDocker) Containers(_ context.Context, _ string) ([]docker.Summary, error) {
	var l []docker.Summary
	for id, ct := range f.containers {
		l = append(l, docker.Summary{ID: id, Labels: ct.Config.Labels, Created: f.created[id]})
	}
	return l, nil
}

func (f *fakeDocker) Inspect(_ context.Context, id string) (*docker.Container, error) {
	return f.containers[id], nil
}

func (f *fakeDocker) Connect(_ context.Context, network, id string) error {
	f.connected = append(f.connected, id)
	f.containers[id].NetworkSettings.Networks[network] = endpoint{IPAddress: "172.30.0." + id}
	return nil
}

func (f *fakeDocker) Events(ctx context.Context, _ time.Time, _ string, _ []string, _ func(docker.Event)) error {
	<-ctx.Done()
	return ctx.Err()
}

func setup(t *testing.T, inst *config.Instance) (*Controller, *fakeDocker, *bytes.Buffer) {
	t.Helper()
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	cert, key, err := ca.Create("test", now)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := ca.Load(cert, key)
	f := &fakeDocker{containers: map[string]*docker.Container{}, created: map[string]int64{}}
	logs := &bytes.Buffer{}
	c := &Controller{Instance: inst, CA: a, Docker: f, Dir: t.TempDir(), Log: log.New(logs, "", 0),
		Now: func() time.Time { return now }}
	return c, f, logs
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(content, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func routers(t *testing.T, c *Controller) map[string]any {
	t.Helper()
	v := readJSON(t, filepath.Join(c.Dir, "routes.yaml"))
	h, _ := v["http"].(map[string]any)
	r, _ := h["routers"].(map[string]any)
	return r
}

func TestRebuild(t *testing.T) {
	inst := &config.Instance{Name: "workstation", Address: "127.0.0.1", Projects: []string{"p"}}
	c, f, logs := setup(t, inst)
	f.add("1", "a-front-1", "p", "a", "https://{stack}.test.example/ 8080\nhttps://intruder.test.example/ 80", 10)
	f.add("2", "b-front-1", "p", "b", "https://{stack}.test.example/ 8080\nhttps://api.{stack}.test.example/ 8080", 20)
	f.add("3", "x-front-1", "other", "x", "https://{stack}.test.example/ 8080", 5)
	if err := c.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The controller connects them in no particular order
	if connected := slices.Sorted(slices.Values(f.connected)); strings.Join(connected, ",") != "1,2" {
		t.Errorf("only the services of its projects are connected: %v", connected)
	}
	r := routers(t, c)
	if len(r) != 3 {
		t.Fatalf("three routes: %v", r)
	}
	if r["a-front-1-1"].(map[string]any)["rule"] != "Host(`a.test.example`)" {
		t.Errorf("a's route: %v", r["a-front-1-1"])
	}
	if !strings.Contains(logs.String(), "does not carry {stack}") {
		t.Errorf("the intruder route is refused, and reported:\n%s", logs)
	}

	tls := readJSON(t, filepath.Join(c.Dir, "tls.yaml"))
	certs := tls["tls"].(map[string]any)["certificates"].([]any)
	if len(certs) != 2 {
		t.Fatalf("the domain's wildcard and stack b's own certificate: %v", certs)
	}
	for _, e := range certs {
		file := e.(map[string]any)["certFile"].(string)
		if _, err := os.Stat(file); err != nil {
			t.Errorf("the certificate is written before Traefik reads it: %v", err)
		}
	}

	// Nothing changed: nothing is rewritten, and no certificate is reissued
	before, _ := os.Stat(filepath.Join(c.Dir, "routes.yaml"))
	entries, _ := os.ReadDir(filepath.Join(c.Dir, "certs"))
	time.Sleep(10 * time.Millisecond)
	if err := c.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(filepath.Join(c.Dir, "routes.yaml"))
	entries2, _ := os.ReadDir(filepath.Join(c.Dir, "certs"))
	if !before.ModTime().Equal(after.ModTime()) || len(entries) != len(entries2) || entries[0].Name() != entries2[0].Name() {
		t.Error("a rebuild without changes rewrites nothing")
	}

	// Stack b stops: its route goes, and its certificate with it
	delete(f.containers, "2")
	if err := c.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(routers(t, c)) != 1 || !strings.Contains(logs.String(), "route removed") {
		t.Errorf("a stopped stack loses its routes:\n%s", logs)
	}
	if entries3, _ := os.ReadDir(filepath.Join(c.Dir, "certs")); len(entries3) != 2 {
		t.Errorf("b's certificate is removed: %d files", len(entries3))
	}
}

func TestSettle(t *testing.T) {
	inst := &config.Instance{Name: "workstation", Address: "127.0.0.1",
		Relays: []config.Relay{{To: "10.8.0.1", Domains: []string{"tls://legacy.example"}}}}
	c, f, logs := setup(t, inst)
	f.add("1", "young-1", "p", "a", "https://{stack}.test.example/ 80", 20)
	f.add("2", "old-1", "p", "a", "https://{stack}.test.example/ 80", 10)
	f.add("3", "passthrough-1", "p", "c", "tls://{stack}.test.example 443", 30)
	f.add("4", "mixed-1", "p", "d", "https://{stack}.legacy.example/ 80", 40)
	if err := c.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := routers(t, c)
	if _, ok := r["old-1-1"]; !ok || len(r) != 1 {
		t.Errorf("the oldest stack keeps the contested route: %v", r)
	}
	for _, want := range []string{"already served by old-1", "domain test.example is already https", "domain legacy.example is already tls"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the log says %q:\n%s", want, logs)
		}
	}
}

func TestCatalog(t *testing.T) {
	c, f, logs := setup(t, &config.Instance{Name: "workstation", Address: "127.0.0.1", Ports: []int{3000}})
	f.add("1", "a-1", "p", "a", "https://{stack}.test.example:3000/ 80\nhttps://{stack}.test.example:4000/ 80", 1)
	if err := c.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(routers(t, c)) != 1 || !strings.Contains(logs.String(), "does not publish port 4000 yet: run waggle up") {
		t.Errorf("a port outside the catalog is refused:\n%s", logs)
	}
}

// A server's controller writes the VPN DNS: a zone per stack name under a real domain, never one for .localhost, and the
// list of the domains it has served, which only grows (ADR-0003, decisions 8 and 9).
func TestZones(t *testing.T) {
	inst := &config.Instance{Name: "team", Address: "10.8.0.1", Ports: []int{3000},
		RelayListener: &config.RelayListener{Port: 8080, TrustedIPs: []string{"10.8.0.0/24"}}}
	c, f, _ := setup(t, inst)
	c.Zones = t.TempDir()
	f.add("1", "a-front-1", "p", "a", "https://{stack}.dev.example.com/ 80\nhttp://admin.{stack}.localhost/ 80", 1)
	f.add("2", "b-front-1", "p", "b", "tls://{stack}.dev.legacy.com:3000 443", 2)
	if err := c.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	names := func() []string {
		var l []string
		entries, _ := os.ReadDir(c.Zones)
		for _, e := range entries {
			l = append(l, e.Name())
		}
		return l
	}
	if got := names(); !slices.Equal(got, []string{"db.a.dev.example.com", "db.b.dev.legacy.com", "db.waggle", "published.json"}) {
		t.Fatalf("a zone per stack name, none for .localhost: %v", got)
	}
	zone, _ := os.ReadFile(filepath.Join(c.Zones, "db.a.dev.example.com"))
	for _, part := range []string{"$ORIGIN a.dev.example.com.", "IN SOA", "@ 10 IN A 10.8.0.1", "* 10 IN A 10.8.0.1"} {
		if !strings.Contains(string(zone), part) {
			t.Errorf("the zone lacks %q:\n%s", part, zone)
		}
	}
	if info, _ := os.Stat(filepath.Join(c.Zones, "db.waggle")); info.Mode().Perm() != 0o644 {
		t.Errorf("CoreDNS, another user, reads the zones: %v", info.Mode())
	}
	relay, _ := os.ReadFile(filepath.Join(c.Zones, "db.waggle"))
	for _, part := range []string{`relay 10 IN TXT "https dev.example.com"`, `relay 10 IN TXT "http localhost"`,
		`relay 10 IN TXT "tls dev.legacy.com"`, `relay 10 IN TXT "ports 3000"`} {
		if !strings.Contains(string(relay), part) {
			t.Errorf("the published list lacks %q:\n%s", part, relay)
		}
	}

	// Stack b stops: its zone goes, its domain stays published; a new mode for a domain replaces the former one
	delete(f.containers, "2")
	f.add("3", "c-front-1", "p", "c", "tls://{stack}.dev.example.com 443", 3)
	delete(f.containers, "1")
	if err := c.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := names(); !slices.Equal(got, []string{"db.c.dev.example.com", "db.waggle", "published.json"}) {
		t.Fatalf("a stopped stack loses its zone: %v", got)
	}
	relay2, _ := os.ReadFile(filepath.Join(c.Zones, "db.waggle"))
	if !strings.Contains(string(relay2), `"tls dev.legacy.com"`) || !strings.Contains(string(relay2), `"http localhost"`) ||
		!strings.Contains(string(relay2), `"tls dev.example.com"`) || strings.Contains(string(relay2), `"https dev.example.com"`) {
		t.Errorf("the list keeps the domains served before, in their current mode:\n%s", relay2)
	}
	if string(relay) == string(relay2) || strings.Split(string(relay), "\n")[2] == strings.Split(string(relay2), "\n")[2] {
		t.Error("a changed zone has a new serial, for CoreDNS to reload it")
	}
}
