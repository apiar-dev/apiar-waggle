package traefik

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/apiar-dev/apiar-waggle/internal/reglages"
	"github.com/apiar-dev/apiar-waggle/internal/routes"
)

func servies(t *testing.T, valeur, dossier, conteneur, ip string) []RouteServie {
	t.Helper()
	rs, errs := routes.Lire(valeur, dossier, nil)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	var out []RouteServie
	for _, r := range rs {
		out = append(out, RouteServie{Route: r, Conteneur: conteneur, IP: ip})
	}
	return out
}

// chemin lit une valeur dans la configuration, par ses clés.
func chemin(t *testing.T, config map[string]any, cles ...string) any {
	t.Helper()
	octets, _ := json.Marshal(config)
	var v any
	json.Unmarshal(octets, &v)
	for _, c := range cles {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("%v : pas d'objet à %q", cles, c)
		}
		v = m[c]
	}
	return v
}

func TestRoutes(t *testing.T) {
	inst := &reglages.Instance{Nom: "poste", Ports: []int{3000}}
	rs := servies(t, `
https://{dossier}.essai.test/ 8080
https://{dossier}.essai.test/api 9000
https://*.{dossier}.essai.test:3000/ 5173
http://admin.{dossier}.localhost/ 8080
tls://{dossier}.ancien.test 8443
`, "a", "/a-front-1", "172.30.0.5")
	c := Routes(inst, rs)
	if got := chemin(t, c, "http", "routers", "a-front-1-1", "rule"); got != "Host(`a.essai.test`)" {
		t.Errorf("route https : %v", got)
	}
	if got := chemin(t, c, "http", "routers", "a-front-1-1", "tls"); !reflect.DeepEqual(got, map[string]any{}) {
		t.Errorf("waggle termine le TLS : %v", got)
	}
	if got := chemin(t, c, "http", "routers", "a-front-1-2", "rule"); got != "Host(`a.essai.test`) && (Path(`/api`) || PathPrefix(`/api/`))" {
		t.Errorf("route avec chemin : %v", got)
	}
	if got := chemin(t, c, "http", "routers", "a-front-1-3", "rule"); got != "HostRegexp(`^.+\\.a\\.essai\\.test$`)" {
		t.Errorf("famille d'hôtes : %v", got)
	}
	if got := chemin(t, c, "http", "routers", "a-front-1-3", "entryPoints"); !reflect.DeepEqual(got, []any{"p3000"}) {
		t.Errorf("port historique : %v", got)
	}
	if got := chemin(t, c, "http", "routers", "a-front-1-4", "entryPoints"); !reflect.DeepEqual(got, []any{"p80"}) {
		t.Errorf("route http : %v", got)
	}
	if got := chemin(t, c, "http", "services", "a-front-1-2", "loadBalancer", "servers"); !reflect.DeepEqual(got, []any{map[string]any{"url": "http://172.30.0.5:9000"}}) {
		t.Errorf("service : %v", got)
	}
	if got := chemin(t, c, "tcp", "routers", "a-front-1-5", "tls", "passthrough"); got != true {
		t.Errorf("route en passage : %v", got)
	}
	if got := chemin(t, c, "tcp", "services", "a-front-1-5", "loadBalancer", "servers"); !reflect.DeepEqual(got, []any{map[string]any{"address": "172.30.0.5:8443"}}) {
		t.Errorf("service en passage : %v", got)
	}
	if chemin(t, c, "http", "routers", "a-front-1-1-relais") != nil {
		t.Error("un poste n'a pas de point d'entrée relais")
	}
}

func TestRoutesDUnServeur(t *testing.T) {
	inst := &reglages.Instance{Nom: "equipe", RelaisEntrant: &reglages.RelaisEntrant{Port: 8080, Confiance: []string{"10.8.0.0/24"}}}
	c := Routes(inst, servies(t, "https://{dossier}.essai.test:443/ 8080", "r", "r-app-1", "172.30.0.9"))
	got := chemin(t, c, "http", "routers", "r-app-1-1-relais")
	attendu := map[string]any{
		"entryPoints": []any{"relais"},
		"rule":        "(Host(`r.essai.test`)) && Header(`X-Waggle-Port`, `443`)",
		"service":     "r-app-1-1",
	}
	if !reflect.DeepEqual(got, attendu) {
		t.Errorf("le relais entrant d'un serveur : %v", got)
	}
}

func TestRelais(t *testing.T) {
	inst := &reglages.Instance{Nom: "poste", Ports: []int{3000}, Relais: []reglages.Relais{{
		Vers: "10.8.0.1", PortRelais: 8080,
		Domaines: []string{"https://essai.test", "tls://ancien.test", "http://localhost", "http://*"},
	}}}
	c := Relais(inst)
	for _, p := range []string{"p443", "p3000"} {
		r := chemin(t, c, "http", "routers", "relais-1-essai-test-https-"+p).(map[string]any)
		if r["rule"] != "HostRegexp(`^(.+\\.)?essai\\.test$`)" || r["priority"] != float64(-1) || r["service"] != "relais-1-https" {
			t.Errorf("relais https sur %s : %v", p, r)
		}
		if got := chemin(t, c, "http", "middlewares", "relais-port-"+p, "headers", "customRequestHeaders", "X-Waggle-Port"); got != strings.TrimPrefix(p, "p") {
			t.Errorf("le relais nomme son port d'entrée, %s : %v", p, got)
		}
		tr := chemin(t, c, "tcp", "routers", "relais-1-ancien-test-tls-"+p).(map[string]any)
		if tr["rule"] != "HostSNIRegexp(`^(.+\\.)?ancien\\.test$`)" || tr["priority"] != float64(-1) {
			t.Errorf("relais tls sur %s : %v", p, tr)
		}
	}
	if got := chemin(t, c, "http", "services", "relais-1-https", "loadBalancer", "servers"); !reflect.DeepEqual(got, []any{map[string]any{"url": "http://10.8.0.1:8080"}}) {
		t.Errorf("le relais https vise le point d'entrée relais du serveur : %v", got)
	}
	if got := chemin(t, c, "tcp", "services", "relais-1-tls-p3000", "loadBalancer", "servers"); !reflect.DeepEqual(got, []any{map[string]any{"address": "10.8.0.1:3000"}}) {
		t.Errorf("le relais tls vise le même port : %v", got)
	}
	tout := chemin(t, c, "http", "routers", "relais-1-joker-http").(map[string]any)
	if tout["rule"] != "PathPrefix(`/`)" || tout["priority"] != float64(-2) {
		t.Errorf("le relais http de tout le reste passe après les autres : %v", tout)
	}
	if got := chemin(t, c, "http", "serversTransports", "relais", "forwardingTimeouts", "dialTimeout"); got != DelaiRelais {
		t.Errorf("délai du relais : %v", got)
	}
	if got := chemin(t, c, "tcp", "serversTransports", "relais", "dialTimeout"); got != DelaiRelais {
		t.Errorf("délai du relais tcp : %v", got)
	}
}

func TestCertificatsVoulus(t *testing.T) {
	inst := &reglages.Instance{Relais: []reglages.Relais{{Vers: "x", Domaines: []string{"https://serveur.test", "tls://ancien.test"}}}}
	rs := servies(t, `
https://{dossier}.essai.test/ 80
https://api.{dossier}.essai.test/ 80
https://*.{dossier}.essai.test/ 80
https://{dossier}.localhost/ 80
tls://{dossier}.ancien.test 443
http://{dossier}.localhost/ 80
`, "a", "a-1", "1.1.1.1")
	got := CertificatsVoulus(inst, rs)
	attendu := map[string][]string{
		"joker.essai.test":   {"*.essai.test", "essai.test"},
		"pile.a.essai.test":  {"*.a.essai.test", "a.essai.test"},
		"pile.a.localhost":   {"a.localhost"},
		"joker.serveur.test": {"*.serveur.test", "serveur.test"},
	}
	if !reflect.DeepEqual(got, attendu) {
		t.Errorf("certificats voulus : %v", got)
	}
}

func TestNettoyer(t *testing.T) {
	if got := Nettoyer("/Mon_Projet-a.front.1"); got != "mon-projet-a-front-1" || strings.ContainsAny(got, "_./") {
		t.Errorf("%q", got)
	}
}
