// Package controleur est le compagnon devenu traducteur (ADR-0002, décision 1). Il suit les conteneurs qui portent
// `waggle.projet`, ne garde que les projets de son instance, branche chacun sur le réseau de l'instance, lit ses routes,
// émet les certificats qu'elles demandent, et écrit la configuration de Traefik dans son dossier dynamique. Traefik la relit
// de lui-même : il n'a ni socket ni fournisseur Docker, et le piège de la relecture disparaît avec lui.
package controleur

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/apiar-dev/apiar-waggle/internal/autorite"
	"github.com/apiar-dev/apiar-waggle/internal/docker"
	"github.com/apiar-dev/apiar-waggle/internal/generation"
	"github.com/apiar-dev/apiar-waggle/internal/reglages"
	"github.com/apiar-dev/apiar-waggle/internal/routes"
	"github.com/apiar-dev/apiar-waggle/internal/traefik"
)

// EtiquetteProjet : l'étiquette par laquelle une instance reconnaît ses projets (ADR-0001, décision 2).
const EtiquetteProjet = "waggle.projet"

// EtiquetteDossier : le nom de la stack, que Compose pose sur chaque conteneur.
const EtiquetteDossier = "com.docker.compose.project"

// Docker : ce que le contrôleur demande à Docker.
type Docker interface {
	Conteneurs(ctx context.Context, etiquette string) ([]docker.Resume, error)
	Inspecter(ctx context.Context, id string) (*docker.Detail, error)
	Brancher(ctx context.Context, reseau, id string) error
	Evenements(ctx context.Context, depuis time.Time, etiquette string, actions []string, recevoir func(docker.Evenement)) error
}

// Controleur : l'état d'une instance.
type Controleur struct {
	Instance   *reglages.Instance
	Autorite   *autorite.Autorite
	Docker     Docker
	Dossier    string // le dossier dynamique de Traefik
	Journal    *log.Logger
	Maintenant func() time.Time

	routesVues map[string]string // clé → ligne de journal, pour ne dire que ce qui change
	refusVus   map[string]bool
}

// Charger lit l'instance et son autorité.
func Charger(cheminInstance, dossierAutorite string) (*reglages.Instance, *autorite.Autorite, error) {
	contenu, err := os.ReadFile(cheminInstance)
	if err != nil {
		return nil, nil, err
	}
	var inst reglages.Instance
	if err := json.Unmarshal(contenu, &inst); err != nil {
		return nil, nil, fmt.Errorf("%s : %w", cheminInstance, err)
	}
	cert, err := os.ReadFile(filepath.Join(dossierAutorite, "autorite.crt"))
	if err != nil {
		return nil, nil, err
	}
	cle, err := os.ReadFile(filepath.Join(dossierAutorite, "autorite.key"))
	if err != nil {
		return nil, nil, err
	}
	a, err := autorite.Charger(cert, cle)
	if err != nil {
		return nil, nil, err
	}
	return &inst, a, nil
}

// Tourner reconstruit la configuration au départ, à chaque démarrage ou arrêt d'un conteneur de projet, et chaque minute
// — pour les certificats proches de leur terme, et pour rattraper un événement perdu.
func (c *Controleur) Tourner(ctx context.Context) error {
	if c.Maintenant == nil {
		c.Maintenant = time.Now
	}
	if c.Journal == nil {
		c.Journal = log.New(io.Discard, "", 0)
	}
	depuis := c.Maintenant()
	if err := c.Reconstruire(ctx); err != nil {
		c.Journal.Printf("reconstruction : %v", err)
	}
	signal := make(chan struct{}, 1)
	go func() {
		for ctx.Err() == nil {
			err := c.Docker.Evenements(ctx, depuis, EtiquetteProjet, []string{"start", "die", "destroy"}, func(e docker.Evenement) {
				if e.TimeNano > 0 {
					depuis = time.Unix(0, e.TimeNano)
				}
				select {
				case signal <- struct{}{}:
				default:
				}
			})
			if ctx.Err() != nil {
				return
			}
			c.Journal.Printf("le flux des événements s'est arrêté (%v) : il reprend dans deux secondes", err)
			time.Sleep(2 * time.Second)
		}
	}()
	minute := time.NewTicker(time.Minute)
	defer minute.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-signal:
			// Les événements d'une stack arrivent en rafale : une reconstruction pour la rafale
			time.Sleep(300 * time.Millisecond)
			select {
			case <-signal:
			default:
			}
		case <-minute.C:
		}
		if err := c.Reconstruire(ctx); err != nil {
			c.Journal.Printf("reconstruction : %v", err)
		}
	}
}

type candidat struct {
	route traefik.RouteServie
	cree  int64
	ordre int
}

// Reconstruire relit les conteneurs, et réécrit ce qui a changé.
func (c *Controleur) Reconstruire(ctx context.Context) error {
	if c.routesVues == nil {
		c.routesVues, c.refusVus = map[string]string{}, map[string]bool{}
	}
	reseau := generation.Projet(c.Instance)
	liste, err := c.Docker.Conteneurs(ctx, EtiquetteProjet)
	if err != nil {
		return err
	}
	var candidats []candidat
	refus := map[string]bool{}
	refuser := func(format string, args ...any) { refus[fmt.Sprintf(format, args...)] = true }

	for _, resume := range liste {
		projet := resume.Labels[EtiquetteProjet]
		if !c.Instance.LitProjet(projet) {
			continue
		}
		detail, err := c.Docker.Inspecter(ctx, resume.ID)
		if err != nil || !detail.State.Running {
			continue
		}
		nom := detail.Nom()
		dossier := detail.Config.Labels[EtiquetteDossier]
		if dossier == "" {
			refuser("%s : pas de nom de stack (lancé hors de Compose ?) ; aucune route", nom)
			continue
		}
		if _, branche := detail.NetworkSettings.Networks[reseau]; !branche {
			if err := c.Docker.Brancher(ctx, reseau, detail.ID); err != nil {
				refuser("%s : le branchement sur %s a échoué : %v", nom, reseau, err)
				continue
			}
			c.Journal.Printf("branché sur %s : %s", reseau, nom)
			if detail, err = c.Docker.Inspecter(ctx, resume.ID); err != nil {
				continue
			}
		}
		ip := detail.NetworkSettings.Networks[reseau].IPAddress
		if ip == "" {
			continue
		}
		lues, erreurs := routes.Lire(detail.Config.Labels[routes.Etiquette], dossier, detail.PortsExposes())
		for _, e := range erreurs {
			refuser("%s, route refusée : %v", nom, e)
		}
		for i, r := range lues {
			if r.Mode != routes.HTTP && !c.Instance.AuCatalogue(r.Port) {
				refuser("%s, route refusée : « %s » : le port %d n'est pas au catalogue de l'instance (réglages, ports)", nom, r.Texte, r.Port)
				continue
			}
			candidats = append(candidats, candidat{route: traefik.RouteServie{Route: r, Conteneur: nom, IP: ip}, cree: resume.Created, ordre: i})
		}
	}

	retenues := c.departager(candidats, refuser)
	if err := c.ecrire(retenues); err != nil {
		return err
	}
	c.raconter(retenues, refus)
	return nil
}

// departager : la plus ancienne stack garde une route disputée, et le premier mode d'un domaine le garde. Un domaine est
// terminé ou en passage, jamais les deux (ADR-0002, décision 4).
func (c *Controleur) departager(candidats []candidat, refuser func(string, ...any)) []traefik.RouteServie {
	sort.SliceStable(candidats, func(i, j int) bool {
		a, b := candidats[i], candidats[j]
		if a.cree != b.cree {
			return a.cree < b.cree
		}
		if a.route.Conteneur != b.route.Conteneur {
			return a.route.Conteneur < b.route.Conteneur
		}
		return a.ordre < b.ordre
	})
	modes := map[string]routes.Mode{}
	for _, rel := range c.Instance.Relais {
		for _, texte := range rel.Domaines {
			if d, err := reglages.LireDomaineRelaye(texte); err == nil && d.Mode != "http" {
				modes[d.Domaine] = routes.Mode(d.Mode)
			}
		}
	}
	prises := map[string]string{}
	var retenues []traefik.RouteServie
	for _, cand := range candidats {
		r := cand.route
		if r.Mode != routes.HTTP {
			if mode, ok := modes[r.Domaine]; ok && mode != r.Mode {
				refuser("%s, route refusée : « %s » : le domaine %s est déjà en %s ; un domaine est terminé ou en passage, jamais les deux",
					r.Conteneur, r.Texte, r.Domaine, mode)
				continue
			}
			modes[r.Domaine] = r.Mode
		}
		if autre, ok := prises[r.Cle()]; ok {
			refuser("%s, route refusée : %s est déjà servie par %s", r.Conteneur, r.URL(), autre)
			continue
		}
		prises[r.Cle()] = r.Conteneur
		retenues = append(retenues, r)
	}
	return retenues
}

// ecrire écrit les certificats d'abord, puis les fichiers que Traefik lit, chacun d'un seul renommage.
func (c *Controleur) ecrire(retenues []traefik.RouteServie) error {
	certificats, err := c.certificats(traefik.CertificatsVoulus(c.Instance, retenues))
	if err != nil {
		return err
	}
	fichiers := map[string]any{
		"tls.yaml":    traefik.TLS(certificats),
		"routes.yaml": traefik.Routes(c.Instance, retenues),
		"relais.yaml": traefik.Relais(c.Instance),
	}
	for nom, contenu := range fichiers {
		octets, err := json.MarshalIndent(contenu, "", "  ")
		if err != nil {
			return err
		}
		if err := ecrireSiChange(filepath.Join(c.Dossier, nom), append(octets, '\n')); err != nil {
			return err
		}
	}
	return nil
}

// certificats tient un certificat par entrée voulue, renouvelé s'il ne vaut plus. Son fichier porte son empreinte : un
// certificat renouvelé est un nouveau fichier, que Traefik voit, et l'ancien est retiré.
func (c *Controleur) certificats(voulus map[string][]string) ([]traefik.Certificat, error) {
	dossier := filepath.Join(c.Dossier, "certs")
	if err := os.MkdirAll(dossier, 0o700); err != nil {
		return nil, err
	}
	gardes := map[string]bool{}
	var liste []traefik.Certificat
	var cles []string
	for cle := range voulus {
		cles = append(cles, cle)
	}
	sort.Strings(cles)
	for _, cle := range cles {
		noms := voulus[cle]
		base := traefik.Nettoyer(cle)
		cert, valide := c.certificatExistant(dossier, base, noms)
		if !valide {
			certPEM, clePEM, err := c.Autorite.Emettre(noms, c.Maintenant())
			if err != nil {
				return nil, fmt.Errorf("le certificat de %s : %w", strings.Join(noms, ", "), err)
			}
			cert = filepath.Join(dossier, base+"-"+generation.Empreinte(certPEM)+".crt")
			if err := ecrireSiChange(strings.TrimSuffix(cert, ".crt")+".key", clePEM); err != nil {
				return nil, err
			}
			if err := ecrireSiChange(cert, certPEM); err != nil {
				return nil, err
			}
			c.Journal.Printf("certificat émis : %s", strings.Join(noms, ", "))
		}
		gardes[filepath.Base(cert)] = true
		gardes[strings.TrimSuffix(filepath.Base(cert), ".crt")+".key"] = true
		liste = append(liste, traefik.Certificat{Cert: cert, Cle: strings.TrimSuffix(cert, ".crt") + ".key"})
	}
	entrees, _ := os.ReadDir(dossier)
	for _, e := range entrees {
		if !gardes[e.Name()] {
			os.Remove(filepath.Join(dossier, e.Name()))
		}
	}
	return liste, nil
}

func (c *Controleur) certificatExistant(dossier, base string, noms []string) (string, bool) {
	candidats, _ := filepath.Glob(filepath.Join(dossier, base+"-*.crt"))
	for _, chemin := range candidats {
		contenu, err := os.ReadFile(chemin)
		if err != nil {
			continue
		}
		if _, err := os.Stat(strings.TrimSuffix(chemin, ".crt") + ".key"); err != nil {
			continue
		}
		if c.Autorite.Vaut(contenu, noms, c.Maintenant()) {
			return chemin, true
		}
	}
	return "", false
}

// raconter ne dit que ce qui change : une route qui apparaît, qui disparaît, un refus nouveau.
func (c *Controleur) raconter(retenues []traefik.RouteServie, refus map[string]bool) {
	actuelles := map[string]string{}
	for _, r := range retenues {
		actuelles[r.Cle()+" "+r.Conteneur] = fmt.Sprintf("%s → %s:%d", r.URL(), r.Conteneur, r.Cible)
	}
	for cle, ligne := range actuelles {
		if _, ok := c.routesVues[cle]; !ok {
			c.Journal.Printf("route : %s", ligne)
		}
	}
	for cle, ligne := range c.routesVues {
		if _, ok := actuelles[cle]; !ok {
			c.Journal.Printf("route retirée : %s", ligne)
		}
	}
	c.routesVues = actuelles
	for r := range refus {
		if !c.refusVus[r] {
			c.Journal.Print(r)
		}
	}
	c.refusVus = refus
}

// ecrireSiChange écrit un fichier par un renommage, et seulement si son contenu change : Traefik ne relit que ce qui bouge.
func ecrireSiChange(chemin string, contenu []byte) error {
	if actuel, err := os.ReadFile(chemin); err == nil && string(actuel) == string(contenu) {
		return nil
	}
	provisoire := chemin + ".provisoire"
	if err := os.WriteFile(provisoire, contenu, 0o600); err != nil {
		return err
	}
	return os.Rename(provisoire, chemin)
}
