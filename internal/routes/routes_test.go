package routes

import (
	"strings"
	"testing"
)

func TestLire(t *testing.T) {
	valeur := `
https://{dossier}.dev.exemple.fr/          8080
https://{dossier}.dev.exemple.fr:3000/     5173   # Vite, sur son port historique
https://*.{dossier}.dev.exemple.fr/api/    8000
http://admin.{dossier}.localhost/          5173
tls://{dossier}.dev.ancien.fr:3000         3000
`
	rs, errs := Lire(valeur, "go-1234", nil)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	attendus := []struct {
		mode                           Mode
		hote, prefixe, domaine, chemin string
		port, cible                    int
		url                            string
	}{
		{HTTPS, "go-1234.dev.exemple.fr", "", "dev.exemple.fr", "/", 443, 8080, "https://go-1234.dev.exemple.fr/"},
		{HTTPS, "go-1234.dev.exemple.fr", "", "dev.exemple.fr", "/", 3000, 5173, "https://go-1234.dev.exemple.fr:3000/"},
		{HTTPS, "*.go-1234.dev.exemple.fr", "*", "dev.exemple.fr", "/api", 443, 8000, "https://*.go-1234.dev.exemple.fr/api"},
		{HTTP, "admin.go-1234.localhost", "admin", "localhost", "/", 80, 5173, "http://admin.go-1234.localhost/"},
		{TLS, "go-1234.dev.ancien.fr", "", "dev.ancien.fr", "/", 3000, 3000, "tls://go-1234.dev.ancien.fr:3000"},
	}
	if len(rs) != len(attendus) {
		t.Fatalf("%d routes lues : %+v", len(rs), rs)
	}
	for i, a := range attendus {
		r := rs[i]
		if r.Mode != a.mode || r.Hote != a.hote || r.Prefixe != a.prefixe || r.Domaine != a.domaine || r.Chemin != a.chemin ||
			r.Port != a.port || r.Cible != a.cible || r.URL() != a.url {
			t.Errorf("route %d : %+v, attendu %+v", i, r, a)
		}
	}
	if !rs[2].Joker() || rs[0].Joker() {
		t.Error("seul un hôte en *. est une famille")
	}
}

func TestPortExpose(t *testing.T) {
	rs, errs := Lire("https://{dossier}.dev.exemple.fr/", "a", []int{8080})
	if len(errs) > 0 || rs[0].Cible != 8080 {
		t.Fatalf("le seul port exposé sert de cible : %+v %v", rs, errs)
	}
	_, errs = Lire("https://{dossier}.dev.exemple.fr/", "a", []int{80, 443})
	if len(errs) != 1 {
		t.Fatal("deux ports exposés : il faut nommer le sien")
	}
}

func TestRefus(t *testing.T) {
	cas := map[string]string{
		"https://pirate.dev.exemple.fr/ 80":      "ne porte pas {dossier}",
		"https://{dossier}.{dossier}.fr/ 80":     "qu'une fois",
		"http://{dossier}.localhost:3000/ 80":    "port 80 seulement",
		"tls://{dossier}.dev.ancien.fr/api 3000": "pas de chemin",
		"ftp://{dossier}.dev.exemple.fr/ 21":     "n'est pas https",
		"{dossier}.dev.exemple.fr 80":            "schéma",
		"https://{dossier}/ 80":                  "domaine après",
		"https://{dossier}.dev.exemple.fr/ 0":    "n'existe pas",
		"https://a.*.{dossier}.fr/ 80":           "seul le premier",
	}
	for ligne, attendu := range cas {
		_, errs := Lire(ligne, "a", nil)
		if len(errs) != 1 || !strings.Contains(errs[0].Error(), attendu) {
			t.Errorf("%q : attendu %q, obtenu %v", ligne, attendu, errs)
		}
	}
	if _, errs := Lire("https://{dossier}.dev.exemple.fr/ 80", "Mon_Dossier", nil); len(errs) != 1 {
		t.Error("un nom de stack hors d'un label DNS est refusé")
	}
}

func TestUneLigneFausseNEmporteQuElle(t *testing.T) {
	rs, errs := Lire("https://pirate.fr/ 80\nhttps://{dossier}.dev.exemple.fr/ 80", "a", nil)
	if len(rs) != 1 || len(errs) != 1 {
		t.Fatalf("%d routes, %d erreurs", len(rs), len(errs))
	}
}
