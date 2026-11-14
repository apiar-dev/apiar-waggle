package reglages

import (
	"net"
	"strings"
	"testing"
)

func valider(t *testing.T, yaml string, m Machine) ([]*Instance, error) {
	t.Helper()
	f, err := Decoder([]byte(yaml))
	if err != nil {
		return nil, err
	}
	return Valider(f, m)
}

var mac = Machine{Systeme: "Darwin"}

func serveur(cartes ...Carte) Machine {
	return Machine{Systeme: "Linux", Cartes: func() ([]Carte, error) { return cartes, nil }}
}

func carte(nom string, wg bool, cidr string) Carte {
	ip, reseau, _ := net.ParseCIDR(cidr)
	reseau.IP = ip
	return Carte{Nom: nom, WireGuard: wg, Reseaux: []*net.IPNet{reseau}}
}

func attendreErreur(t *testing.T, err error, morceau string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), morceau) {
		t.Fatalf("erreur attendue contenant %q, obtenu : %v", morceau, err)
	}
}

func TestUnPoste(t *testing.T) {
	insts, err := valider(t, `
instances:
  poste:
    adresse: 127.0.0.1
    ports: [3001, 3000]
    relais:
      - vers: 10.8.0.1
        domaines: [https://dev.exemple.fr, tls://dev.ancien.fr, http://localhost]
`, mac)
	if err != nil {
		t.Fatal(err)
	}
	p := insts[0]
	if p.Nom != "poste" || p.Interface != "" || p.RelaisEntrant != nil {
		t.Fatalf("un poste n'a ni interface ni relais entrant : %+v", p)
	}
	if got := p.PortsTLS(); len(got) != 3 || got[0] != 443 || got[1] != 3000 {
		t.Fatalf("les ports TLS, triés après 443 : %v", got)
	}
	if p.Relais[0].PortRelais != PortRelais {
		t.Fatalf("le port du relais par défaut : %d", p.Relais[0].PortRelais)
	}
	if !p.LitProjet("nimporte") {
		t.Fatal("une instance sans projets les lit tous")
	}
}

func TestJamaisSurInternet(t *testing.T) {
	_, err := valider(t, "instances:\n  poste:\n    adresse: 0.0.0.0\n", mac)
	attendreErreur(t, err, "jamais sur internet")
	_, err = valider(t, "instances:\n  poste:\n    adresse: 10.8.0.2\n", mac)
	attendreErreur(t, err, "boucle locale")
	_, err = valider(t, "instances:\n  vps:\n    adresse: 51.15.0.1\n", serveur(carte("eth0", false, "51.15.0.1/24")))
	attendreErreur(t, err, "pas une interface WireGuard")
	_, err = valider(t, "instances:\n  vps:\n    adresse: 10.9.0.1\n", serveur(carte("wg0", true, "10.8.0.1/24")))
	attendreErreur(t, err, "aucune interface")
}

func TestUnServeur(t *testing.T) {
	insts, err := valider(t, `
instances:
  equipe:
    adresse: 10.8.0.1
    projets: [mon-projet]
    dns: [dev.exemple.fr]
  clients:
    adresse: 10.9.0.1
    projets: [autre]
`, serveur(carte("eth0", false, "51.15.0.1/24"), carte("wg0", true, "10.8.0.1/24"), carte("wg1", true, "10.9.0.1/24")))
	if err != nil {
		t.Fatal(err)
	}
	c, e := insts[0], insts[1]
	if c.Nom != "clients" || e.Nom != "equipe" {
		t.Fatalf("les instances, triées : %s, %s", c.Nom, e.Nom)
	}
	if e.Interface != "wg0" || c.Interface != "wg1" {
		t.Fatalf("l'interface se déduit de l'adresse : %s, %s", e.Interface, c.Interface)
	}
	if e.RelaisEntrant == nil || e.RelaisEntrant.Port != PortRelais || e.RelaisEntrant.Confiance[0] != "10.8.0.0/24" {
		t.Fatalf("le relais entrant croit le réseau du VPN : %+v", e.RelaisEntrant)
	}
	if e.LitProjet("autre") || !e.LitProjet("mon-projet") {
		t.Fatal("une instance ne lit que ses projets")
	}
}

func TestIsolement(t *testing.T) {
	m := serveur(carte("wg0", true, "10.8.0.1/24"), carte("wg1", true, "10.9.0.1/24"))
	_, err := valider(t, `
instances:
  a: {adresse: 10.8.0.1, projets: [p]}
  b: {adresse: 10.9.0.1, projets: [p]}
`, m)
	attendreErreur(t, err, "lu par deux instances")
	_, err = valider(t, `
instances:
  a: {adresse: 10.8.0.1}
  b: {adresse: 10.9.0.1, projets: [q]}
`, m)
	attendreErreur(t, err, "chacune nomme ses projets")
}

func TestRefus(t *testing.T) {
	cas := map[string]string{
		"instances:\n  poste: {adresse: 127.0.0.1, ports: [443]}\n":                                       "déjà au catalogue",
		"instances:\n  poste: {adresse: 127.0.0.1, ports: [3000, 3000]}\n":                                "deux fois",
		"instances:\n  poste: {adresse: 127.0.0.1, porte: [3000]}\n":                                      "field porte not found",
		"instances:\n  Poste: {adresse: 127.0.0.1}\n":                                                     "minuscules",
		"instances:\n  poste: {adresse: 127.0.0.1, relais: [{vers: x, domaines: [ftp://a.b]}]}\n":         "schéma",
		"instances:\n  poste: {adresse: 127.0.0.1, relais: [{vers: x, domaines: [https://localhost]}]}\n": "deux labels",
		"instances:\n  a: {adresse: 127.0.0.1, projets: [x]}\n  b: {adresse: 127.0.0.1, projets: [y]}\n":  "même adresse",
	}
	for yaml, attendu := range cas {
		_, err := valider(t, yaml, mac)
		attendreErreur(t, err, attendu)
	}
}

func TestDecalage(t *testing.T) {
	insts, err := valider(t, `
instances:
  a: {adresse: 127.0.0.1, projets: [x], decalage_ports: 20000}
  b: {adresse: 127.0.0.1, projets: [y], decalage_ports: 30000, relais_entrant: {port: 8080, confiance: [0.0.0.0/0]}}
`, mac)
	if err != nil {
		t.Fatal(err)
	}
	if got := insts[1].PortsPublies(); got[len(got)-1] != 8080 {
		t.Fatalf("le relais entrant est publié : %v", got)
	}
}

func TestLExempleSeLit(t *testing.T) {
	f, err := Lire("../../reglages.exemple.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Valider(f, mac); err != nil {
		t.Fatalf("l'exemple du dépôt se valide sur un poste : %v", err)
	}
}

func TestDomaineRelaye(t *testing.T) {
	for texte, attendu := range map[string]DomaineRelaye{
		"https://Dev.Exemple.fr/": {Mode: "https", Domaine: "dev.exemple.fr"},
		"tls://dev.ancien.fr":     {Mode: "tls", Domaine: "dev.ancien.fr"},
		"http://localhost":        {Mode: "http", Domaine: "localhost"},
		"http://*":                {Mode: "http", Domaine: "*"},
	} {
		got, err := LireDomaineRelaye(texte)
		if err != nil || got != attendu {
			t.Errorf("%s : %+v, %v", texte, got, err)
		}
	}
	if _, err := LireDomaineRelaye("https://*"); err == nil {
		t.Error("un relais https ne prend pas tout : il faut un certificat par domaine")
	}
}
