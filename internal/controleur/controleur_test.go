package controleur

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apiar-dev/apiar-waggle/internal/autorite"
	"github.com/apiar-dev/apiar-waggle/internal/docker"
	"github.com/apiar-dev/apiar-waggle/internal/reglages"
)

// fauxDocker : des conteneurs en mémoire, que Brancher branche sur le réseau.
type fauxDocker struct {
	conteneurs map[string]*docker.Detail
	crees      map[string]int64
	branches   []string
}

func (f *fauxDocker) ajouter(id, nom, projet, dossier, routesValeur string, cree int64) {
	d := &docker.Detail{ID: id, Name: "/" + nom}
	d.State.Running = true
	d.Config.Labels = map[string]string{EtiquetteProjet: projet, EtiquetteDossier: dossier, "waggle.routes": routesValeur}
	d.NetworkSettings.Networks = map[string]struct {
		IPAddress string `json:"IPAddress"`
	}{dossier + "_default": {IPAddress: "172.18.0.2"}}
	f.conteneurs[id] = d
	f.crees[id] = cree
}

func (f *fauxDocker) Conteneurs(_ context.Context, _ string) ([]docker.Resume, error) {
	var l []docker.Resume
	for id, d := range f.conteneurs {
		l = append(l, docker.Resume{ID: id, Labels: d.Config.Labels, Created: f.crees[id]})
	}
	return l, nil
}

func (f *fauxDocker) Inspecter(_ context.Context, id string) (*docker.Detail, error) {
	return f.conteneurs[id], nil
}

func (f *fauxDocker) Brancher(_ context.Context, reseau, id string) error {
	f.branches = append(f.branches, id)
	f.conteneurs[id].NetworkSettings.Networks[reseau] = struct {
		IPAddress string `json:"IPAddress"`
	}{IPAddress: "172.30.0." + id}
	return nil
}

func (f *fauxDocker) Evenements(ctx context.Context, _ time.Time, _ string, _ []string, _ func(docker.Evenement)) error {
	<-ctx.Done()
	return ctx.Err()
}

func monter(t *testing.T, inst *reglages.Instance) (*Controleur, *fauxDocker, *bytes.Buffer) {
	t.Helper()
	maintenant := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	cert, cle, err := autorite.Creer("essai", maintenant)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := autorite.Charger(cert, cle)
	f := &fauxDocker{conteneurs: map[string]*docker.Detail{}, crees: map[string]int64{}}
	journal := &bytes.Buffer{}
	c := &Controleur{Instance: inst, Autorite: a, Docker: f, Dossier: t.TempDir(), Journal: log.New(journal, "", 0),
		Maintenant: func() time.Time { return maintenant }}
	return c, f, journal
}

func lireJSON(t *testing.T, chemin string) map[string]any {
	t.Helper()
	contenu, err := os.ReadFile(chemin)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(contenu, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func routeurs(t *testing.T, c *Controleur) map[string]any {
	t.Helper()
	v := lireJSON(t, filepath.Join(c.Dossier, "routes.yaml"))
	h, _ := v["http"].(map[string]any)
	r, _ := h["routers"].(map[string]any)
	return r
}

func TestReconstruire(t *testing.T) {
	inst := &reglages.Instance{Nom: "poste", Adresse: "127.0.0.1", Projets: []string{"p"}}
	c, f, journal := monter(t, inst)
	f.ajouter("1", "a-front-1", "p", "a", "https://{dossier}.essai.test/ 8080\nhttps://pirate.essai.test/ 80", 10)
	f.ajouter("2", "b-front-1", "p", "b", "https://{dossier}.essai.test/ 8080\nhttps://api.{dossier}.essai.test/ 8080", 20)
	f.ajouter("3", "x-front-1", "autre", "x", "https://{dossier}.essai.test/ 8080", 5)
	if err := c.Reconstruire(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.branches, ",") != "1,2" {
		t.Errorf("seuls les services de ses projets sont branchés : %v", f.branches)
	}
	r := routeurs(t, c)
	if len(r) != 3 {
		t.Fatalf("trois routes : %v", r)
	}
	if r["a-front-1-1"].(map[string]any)["rule"] != "Host(`a.essai.test`)" {
		t.Errorf("la route de a : %v", r["a-front-1-1"])
	}
	if !strings.Contains(journal.String(), "ne porte pas {dossier}") {
		t.Errorf("la route pirate est refusée, et dite :\n%s", journal)
	}

	tls := lireJSON(t, filepath.Join(c.Dossier, "tls.yaml"))
	certs := tls["tls"].(map[string]any)["certificates"].([]any)
	if len(certs) != 2 {
		t.Fatalf("le joker du domaine et le certificat de la stack b : %v", certs)
	}
	for _, e := range certs {
		fichier := e.(map[string]any)["certFile"].(string)
		if _, err := os.Stat(fichier); err != nil {
			t.Errorf("le certificat est écrit avant que Traefik le lise : %v", err)
		}
	}

	// Rien n'a changé : rien n'est réécrit, et aucun certificat n'est refait
	avant, _ := os.Stat(filepath.Join(c.Dossier, "routes.yaml"))
	entrees, _ := os.ReadDir(filepath.Join(c.Dossier, "certs"))
	time.Sleep(10 * time.Millisecond)
	if err := c.Reconstruire(context.Background()); err != nil {
		t.Fatal(err)
	}
	apres, _ := os.Stat(filepath.Join(c.Dossier, "routes.yaml"))
	entrees2, _ := os.ReadDir(filepath.Join(c.Dossier, "certs"))
	if !avant.ModTime().Equal(apres.ModTime()) || len(entrees) != len(entrees2) || entrees[0].Name() != entrees2[0].Name() {
		t.Error("une reconstruction sans changement ne réécrit rien")
	}

	// La stack b s'arrête : sa route part, et son certificat avec
	delete(f.conteneurs, "2")
	if err := c.Reconstruire(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(routeurs(t, c)) != 1 || !strings.Contains(journal.String(), "route retirée") {
		t.Errorf("une stack arrêtée perd ses routes :\n%s", journal)
	}
	if entrees3, _ := os.ReadDir(filepath.Join(c.Dossier, "certs")); len(entrees3) != 2 {
		t.Errorf("le certificat de b est retiré : %d fichiers", len(entrees3))
	}
}

func TestDepartager(t *testing.T) {
	inst := &reglages.Instance{Nom: "poste", Adresse: "127.0.0.1",
		Relais: []reglages.Relais{{Vers: "10.8.0.1", Domaines: []string{"tls://ancien.test"}}}}
	c, f, journal := monter(t, inst)
	f.ajouter("1", "jeune-1", "p", "a", "https://{dossier}.essai.test/ 80", 20)
	f.ajouter("2", "ancien-1", "p", "a", "https://{dossier}.essai.test/ 80", 10)
	f.ajouter("3", "passage-1", "p", "c", "tls://{dossier}.essai.test 443", 30)
	f.ajouter("4", "melange-1", "p", "d", "https://{dossier}.ancien.test/ 80", 40)
	if err := c.Reconstruire(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := routeurs(t, c)
	if _, ok := r["ancien-1-1"]; !ok || len(r) != 1 {
		t.Errorf("la plus ancienne stack garde la route disputée : %v", r)
	}
	for _, attendu := range []string{"déjà servie par ancien-1", "le domaine essai.test est déjà en https", "le domaine ancien.test est déjà en tls"} {
		if !strings.Contains(journal.String(), attendu) {
			t.Errorf("le journal dit %q :\n%s", attendu, journal)
		}
	}
}

func TestCatalogue(t *testing.T) {
	c, f, journal := monter(t, &reglages.Instance{Nom: "poste", Adresse: "127.0.0.1", Ports: []int{3000}})
	f.ajouter("1", "a-1", "p", "a", "https://{dossier}.essai.test:3000/ 80\nhttps://{dossier}.essai.test:4000/ 80", 1)
	if err := c.Reconstruire(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(routeurs(t, c)) != 1 || !strings.Contains(journal.String(), "le port 4000 n'est pas au catalogue") {
		t.Errorf("un port hors du catalogue est refusé :\n%s", journal)
	}
}
