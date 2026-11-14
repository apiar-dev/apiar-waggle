package render

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/apiar-dev/apiar-waggle/internal/config"
)

func services(t *testing.T, c map[string]any) map[string]map[string]any {
	t.Helper()
	b, _ := json.Marshal(c)
	var v struct {
		Services map[string]map[string]any `json:"services"`
	}
	json.Unmarshal(b, &v)
	return v.Services
}

func strs(v any) []string {
	var out []string
	for _, e := range v.([]any) {
		out = append(out, e.(string))
	}
	return out
}

func TestWorkstation(t *testing.T) {
	inst := &config.Instance{Name: "workstation", Address: "127.0.0.1", Ports: []int{3000}}
	s := services(t, Compose(inst, "/state/workstation", "waggle:local", map[string]string{"controller.instance": "abc"}))
	if _, ok := s["firewall"]; ok {
		t.Error("a workstation has no firewall")
	}
	if _, ok := s["dns"]; ok {
		t.Error("a workstation without domains has no DNS")
	}
	ports := strs(s["traefik"]["ports"])
	if !slices.Equal(ports, []string{"127.0.0.1:80:80", "127.0.0.1:443:443", "127.0.0.1:3000:3000"}) {
		t.Errorf("Traefik publishes on the instance address only: %v", ports)
	}
	for _, v := range strs(s["traefik"]["volumes"]) {
		if strings.Contains(v, "docker.sock") {
			t.Error("Traefik does not mount the Docker socket")
		}
	}
	if s["controller"]["network_mode"] != "none" {
		t.Error("the controller only talks to the socket")
	}
	if s["controller"]["labels"].(map[string]any)["waggle.hash.instance"] != "abc" {
		t.Error("the configuration hash recreates the controller when it changes")
	}
}

func TestServer(t *testing.T) {
	inst := &config.Instance{
		Name: "team", Address: "10.8.0.1", Interface: "wg0", DNS: []string{"dev.example.com"},
		RelayListener: &config.RelayListener{Port: 8080, TrustedIPs: []string{"10.8.0.0/24"}},
	}
	s := services(t, Compose(inst, "/state/team", "waggle:local", nil))
	fw := s["firewall"]
	if fw == nil || fw["network_mode"] != "host" || !slices.Contains(strs(fw["cap_add"]), "NET_ADMIN") {
		t.Fatalf("a server's firewall: %v", fw)
	}
	if !slices.Equal(strs(fw["command"]), []string{"firewall", "--address", "10.8.0.1", "--interface", "wg0"}) {
		t.Errorf("firewall command: %v", fw["command"])
	}
	if !slices.Equal(strs(s["dns"]["ports"]), []string{"10.8.0.1:53:53/udp", "10.8.0.1:53:53/tcp"}) {
		t.Errorf("DNS on the VPN address: %v", s["dns"]["ports"])
	}
	if !slices.Contains(strs(s["traefik"]["ports"]), "10.8.0.1:8080:8080") {
		t.Error("the relay listener is published on the VPN address")
	}
	if slices.Contains(strs(s["traefik"]["ports"]), "127.0.0.1:443:443") {
		t.Error("without loopback: true, a server publishes nothing on loopback")
	}
	inst.Loopback = true
	looped := strs(services(t, Compose(inst, "/state/team", "waggle:local", nil))["traefik"]["ports"])
	for _, want := range []string{"10.8.0.1:443:443", "127.0.0.1:80:80", "127.0.0.1:443:443"} {
		if !slices.Contains(looped, want) {
			t.Errorf("with loopback: true, %s is published: %v", want, looped)
		}
	}
	if slices.Contains(looped, "127.0.0.1:8080:8080") {
		t.Errorf("the relay listener stays on the VPN address: %v", looped)
	}
	args := TraefikArgs(inst)
	if !slices.Contains(args, "--entrypoints.relay.forwardedHeaders.trustedIPs=10.8.0.0/24") {
		t.Errorf("the relay listener only trusts the VPN: %v", args)
	}
}

func TestPortOffset(t *testing.T) {
	inst := &config.Instance{Name: "test", Address: "127.0.0.1", PortOffset: 20000, DNS: []string{"test.example"}}
	s := services(t, Compose(inst, "/s", "w", nil))
	if !slices.Equal(strs(s["traefik"]["ports"]), []string{"127.0.0.1:20080:80", "127.0.0.1:20443:443"}) {
		t.Errorf("offset: %v", s["traefik"]["ports"])
	}
	if strs(s["dns"]["ports"])[0] != "127.0.0.1:20053:53/udp" {
		t.Errorf("DNS offset: %v", s["dns"]["ports"])
	}
}

func TestCorefile(t *testing.T) {
	c := Corefile(&config.Instance{Name: "team", Address: "10.8.0.1", DNS: []string{"dev.example.com"}})
	for _, part := range []string{"dev.example.com:53 {", `answer "{{ .Name }} 60 IN A 10.8.0.1"`, "template IN AAAA", "rcode NOERROR", "forward . /etc/resolv.conf"} {
		if !strings.Contains(c, part) {
			t.Errorf("the Corefile lacks %q:\n%s", part, c)
		}
	}
}
