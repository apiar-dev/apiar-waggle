package generation

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/apiar-dev/apiar-waggle/internal/reglages"
)

func services(t *testing.T, c map[string]any) map[string]map[string]any {
	t.Helper()
	octets, _ := json.Marshal(c)
	var v struct {
		Services map[string]map[string]any `json:"services"`
	}
	json.Unmarshal(octets, &v)
	return v.Services
}

func liste(v any) []string {
	var out []string
	for _, e := range v.([]any) {
		out = append(out, e.(string))
	}
	return out
}

func TestUnPoste(t *testing.T) {
	inst := &reglages.Instance{Nom: "poste", Adresse: "127.0.0.1", Ports: []int{3000}}
	s := services(t, Compose(inst, "/etat/poste", "waggle:local", map[string]string{"controleur.instance": "abc"}))
	if _, ok := s["pare-feu"]; ok {
		t.Error("un poste n'a pas de pare-feu")
	}
	if _, ok := s["dns"]; ok {
		t.Error("un poste sans domaines n'a pas de DNS")
	}
	ports := liste(s["traefik"]["ports"])
	if !slices.Equal(ports, []string{"127.0.0.1:80:80", "127.0.0.1:443:443", "127.0.0.1:3000:3000"}) {
		t.Errorf("Traefik ne publie que sur l'adresse de l'instance : %v", ports)
	}
	for _, v := range liste(s["traefik"]["volumes"]) {
		if strings.Contains(v, "docker.sock") {
			t.Error("Traefik ne monte pas le socket Docker")
		}
	}
	if s["controleur"]["network_mode"] != "none" {
		t.Error("le contrôleur ne parle qu'au socket")
	}
	if s["controleur"]["labels"].(map[string]any)["waggle.empreinte.instance"] != "abc" {
		t.Error("l'empreinte des réglages recrée le contrôleur quand ils changent")
	}
}

func TestUnServeur(t *testing.T) {
	inst := &reglages.Instance{
		Nom: "equipe", Adresse: "10.8.0.1", Interface: "wg0", DNS: []string{"dev.exemple.fr"},
		RelaisEntrant: &reglages.RelaisEntrant{Port: 8080, Confiance: []string{"10.8.0.0/24"}},
	}
	s := services(t, Compose(inst, "/etat/equipe", "waggle:local", nil))
	pf := s["pare-feu"]
	if pf == nil || pf["network_mode"] != "host" || !slices.Contains(liste(pf["cap_add"]), "NET_ADMIN") {
		t.Fatalf("le pare-feu d'un serveur : %v", pf)
	}
	if !slices.Equal(liste(pf["command"]), []string{"pare-feu", "--adresse", "10.8.0.1", "--interface", "wg0"}) {
		t.Errorf("commande du pare-feu : %v", pf["command"])
	}
	if !slices.Equal(liste(s["dns"]["ports"]), []string{"10.8.0.1:53:53/udp", "10.8.0.1:53:53/tcp"}) {
		t.Errorf("le DNS sur l'adresse du VPN : %v", s["dns"]["ports"])
	}
	if !slices.Contains(liste(s["traefik"]["ports"]), "10.8.0.1:8080:8080") {
		t.Error("le relais entrant est publié sur l'adresse du VPN")
	}
	cmd := CommandeTraefik(inst)
	if !slices.Contains(cmd, "--entrypoints.relais.forwardedHeaders.trustedIPs=10.8.0.0/24") {
		t.Errorf("le relais entrant ne croit que le VPN : %v", cmd)
	}
}

func TestDecalage(t *testing.T) {
	inst := &reglages.Instance{Nom: "essai", Adresse: "127.0.0.1", DecalagePorts: 20000, DNS: []string{"essai.test"}}
	s := services(t, Compose(inst, "/e", "w", nil))
	if !slices.Equal(liste(s["traefik"]["ports"]), []string{"127.0.0.1:20080:80", "127.0.0.1:20443:443"}) {
		t.Errorf("décalage : %v", s["traefik"]["ports"])
	}
	if liste(s["dns"]["ports"])[0] != "127.0.0.1:20053:53/udp" {
		t.Errorf("décalage du DNS : %v", s["dns"]["ports"])
	}
}

func TestCorefile(t *testing.T) {
	c := Corefile(&reglages.Instance{Nom: "equipe", Adresse: "10.8.0.1", DNS: []string{"dev.exemple.fr"}})
	for _, morceau := range []string{"dev.exemple.fr:53 {", `answer "{{ .Name }} 60 IN A 10.8.0.1"`, "template IN AAAA", "rcode NOERROR", "forward . /etc/resolv.conf"} {
		if !strings.Contains(c, morceau) {
			t.Errorf("le Corefile ne contient pas %q :\n%s", morceau, c)
		}
	}
}
