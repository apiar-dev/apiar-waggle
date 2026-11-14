// waggle : le routage de dev d'une machine, partagé par les projets, jamais sur internet (docs/adr/0001, 0002).
//
// Ces commandes tournent dans l'image de waggle ; `bin/waggle` les lance depuis l'hôte, avec le socket Docker, les réglages
// et le dossier d'état.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/apiar-dev/apiar-waggle/internal/autorite"
	"github.com/apiar-dev/apiar-waggle/internal/controleur"
	"github.com/apiar-dev/apiar-waggle/internal/docker"
	"github.com/apiar-dev/apiar-waggle/internal/generation"
	"github.com/apiar-dev/apiar-waggle/internal/parefeu"
	"github.com/apiar-dev/apiar-waggle/internal/reglages"
)

// Version : posée à la construction.
var Version = "dev"

const aide = `waggle — le routage de dev d'une machine, partagé par les projets, jamais sur internet

  installer          lit les réglages, écrit et lève chaque instance ; relancé, il ne change que ce qui a changé
  arreter            arrête les instances, et garde leur autorité et leur configuration
  desinstaller       retire les instances et leurs volumes ; --tout retire aussi leur autorité
  etat               les conteneurs de chaque instance, et son autorité
  autorite [nom]     le certificat de l'autorité d'une instance, à approuver sur un appareil
  version

  controleur         (dans l'instance) traduit les routes des stacks pour Traefik
  pare-feu           (dans l'instance d'un serveur) pose la règle DOCKER-USER de son adresse
`

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		fmt.Print(aide)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "installer":
		err = installer()
	case "arreter":
		err = composer("down", "--remove-orphans")
	case "desinstaller":
		err = desinstaller(os.Args[2:])
	case "etat":
		err = afficherEtat()
	case "autorite":
		err = afficherAutorite(os.Args[2:])
	case "controleur":
		err = lancerControleur(os.Args[2:])
	case "pare-feu":
		err = lancerPareFeu(os.Args[2:])
	case "version":
		fmt.Println(Version)
	case "aide", "-h", "--help":
		fmt.Print(aide)
	default:
		err = fmt.Errorf("commande inconnue : %s\n\n%s", os.Args[1], aide)
	}
	if err != nil {
		log.Fatalf("waggle : %v", err)
	}
}

func env(cle, defaut string) string {
	if v := os.Getenv(cle); v != "" {
		return v
	}
	return defaut
}

func dossierEtat() (string, error) {
	etat := os.Getenv("WAGGLE_ETAT")
	if etat == "" {
		return "", errors.New("WAGGLE_ETAT manque : lancer waggle par bin/waggle")
	}
	return etat, nil
}

func instances() ([]*reglages.Instance, error) {
	f, err := reglages.Lire(env("WAGGLE_REGLAGES_INTERNE", "/reglages.yaml"))
	if err != nil {
		return nil, err
	}
	return reglages.Valider(f, reglages.Machine{Systeme: os.Getenv("WAGGLE_SYSTEME"), Cartes: reglages.CartesDeLaMachine})
}

func installer() error {
	etat, err := dossierEtat()
	if err != nil {
		return err
	}
	insts, err := instances()
	if err != nil {
		return err
	}
	image := env("WAGGLE_IMAGE", "waggle:local")
	machine := env("WAGGLE_MACHINE", "cette machine")
	voulues := map[string]bool{}
	for _, inst := range insts {
		voulues[inst.Nom] = true
		if err := preparer(inst, filepath.Join(etat, inst.Nom), image, machine); err != nil {
			return fmt.Errorf("instance %s : %w", inst.Nom, err)
		}
	}
	// Une instance retirée des réglages s'arrête ; son dossier, et son autorité, restent
	anciennes, _ := filepath.Glob(filepath.Join(etat, "*", "compose.json"))
	for _, compose := range anciennes {
		nom := filepath.Base(filepath.Dir(compose))
		if !voulues[nom] {
			fmt.Printf("waggle : l'instance %s n'est plus dans les réglages, elle s'arrête\n", nom)
			if err := dockerCompose(compose, "down", "--remove-orphans"); err != nil {
				return err
			}
		}
	}
	for _, inst := range insts {
		compose := filepath.Join(etat, inst.Nom, "compose.json")
		if err := dockerCompose(compose, "up", "--detach", "--remove-orphans", "--quiet-pull"); err != nil {
			return fmt.Errorf("instance %s : %w", inst.Nom, err)
		}
		resumer(inst, filepath.Join(etat, inst.Nom))
	}
	return nil
}

// preparer écrit l'autorité, si elle manque, puis instance.json, le Corefile et compose.json.
func preparer(inst *reglages.Instance, dossier, image, machine string) error {
	if err := os.MkdirAll(filepath.Join(dossier, "autorite"), 0o700); err != nil {
		return err
	}
	cert := filepath.Join(dossier, "autorite", "autorite.crt")
	if _, err := os.Stat(cert); errors.Is(err, os.ErrNotExist) {
		certPEM, clePEM, err := autorite.Creer(fmt.Sprintf("waggle %s (%s)", inst.Nom, machine), time.Now())
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dossier, "autorite", "autorite.key"), clePEM, 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(cert, certPEM, 0o644); err != nil {
			return err
		}
		fmt.Printf("waggle : l'instance %s a son autorité, %s\n", inst.Nom, cert)
	}
	empreintes := map[string]string{}
	instJSON, err := json.MarshalIndent(inst, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dossier, "instance.json"), append(instJSON, '\n'), 0o644); err != nil {
		return err
	}
	empreintes["controleur.instance"] = generation.Empreinte(instJSON)
	if len(inst.DNS) > 0 {
		corefile := []byte(generation.Corefile(inst))
		if err := os.WriteFile(filepath.Join(dossier, "Corefile"), corefile, 0o644); err != nil {
			return err
		}
		empreintes["dns.corefile"] = generation.Empreinte(corefile)
	}
	compose, err := json.MarshalIndent(generation.Compose(inst, dossier, image, empreintes), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dossier, "compose.json"), append(compose, '\n'), 0o644)
}

func resumer(inst *reglages.Instance, dossier string) {
	var ports []string
	for _, p := range inst.PortsPublies() {
		if inst.DecalagePorts > 0 {
			ports = append(ports, fmt.Sprintf("%d→%d", p+inst.DecalagePorts, p))
		} else {
			ports = append(ports, fmt.Sprint(p))
		}
	}
	fmt.Printf("waggle : l'instance %s écoute sur %s, ports %s\n", inst.Nom, inst.Adresse, strings.Join(ports, ", "))
	if len(inst.DNS) > 0 {
		fmt.Printf("  DNS du VPN, sur %s:%d, pour %s\n", inst.Adresse, 53+inst.DecalagePorts, strings.Join(inst.DNS, ", "))
	}
	if inst.Interface != "" {
		fmt.Printf("  pare-feu : %s\n", parefeu.Commande(inst.Adresse, inst.Interface))
	}
	for _, r := range inst.Relais {
		fmt.Printf("  relais vers %s : %s\n", r.Vers, strings.Join(r.Domaines, ", "))
	}
	fmt.Printf("  autorité, à approuver sur chaque appareil : %s\n", filepath.Join(dossier, "autorite", "autorite.crt"))
}

func composer(args ...string) error {
	etat, err := dossierEtat()
	if err != nil {
		return err
	}
	insts, err := instances()
	if err != nil {
		return err
	}
	for _, inst := range insts {
		compose := filepath.Join(etat, inst.Nom, "compose.json")
		if _, err := os.Stat(compose); err != nil {
			continue
		}
		if err := dockerCompose(compose, args...); err != nil {
			return err
		}
	}
	return nil
}

func desinstaller(args []string) error {
	fs := flag.NewFlagSet("desinstaller", flag.ExitOnError)
	tout := fs.Bool("tout", false, "retirer aussi le dossier d'état, autorité comprise")
	fs.Parse(args)
	if err := composer("down", "--remove-orphans", "--volumes"); err != nil {
		return err
	}
	if !*tout {
		return nil
	}
	etat, _ := dossierEtat()
	insts, err := instances()
	if err != nil {
		return err
	}
	for _, inst := range insts {
		dossier := filepath.Join(etat, inst.Nom)
		if err := os.RemoveAll(dossier); err != nil {
			return err
		}
		fmt.Printf("waggle : %s retiré, autorité comprise : les appareils qui l'approuvaient ne la reconnaîtront plus\n", dossier)
	}
	return nil
}

func afficherEtat() error {
	etat, err := dossierEtat()
	if err != nil {
		return err
	}
	insts, err := instances()
	if err != nil {
		return err
	}
	for _, inst := range insts {
		dossier := filepath.Join(etat, inst.Nom)
		fmt.Printf("== instance %s, sur %s\n", inst.Nom, inst.Adresse)
		if _, err := os.Stat(filepath.Join(dossier, "compose.json")); err != nil {
			fmt.Println("  pas installée (bin/waggle installer)")
			continue
		}
		if err := dockerCompose(filepath.Join(dossier, "compose.json"), "ps"); err != nil {
			return err
		}
		fmt.Printf("  autorité : %s\n", filepath.Join(dossier, "autorite", "autorite.crt"))
	}
	return nil
}

func afficherAutorite(args []string) error {
	etat, err := dossierEtat()
	if err != nil {
		return err
	}
	insts, err := instances()
	if err != nil {
		return err
	}
	var noms []string
	for _, inst := range insts {
		noms = append(noms, inst.Nom)
	}
	sort.Strings(noms)
	nom := ""
	switch {
	case len(args) > 0:
		nom = args[0]
	case len(noms) == 1:
		nom = noms[0]
	default:
		return fmt.Errorf("plusieurs instances : nommer celle dont on veut l'autorité (%s)", strings.Join(noms, ", "))
	}
	contenu, err := os.ReadFile(filepath.Join(etat, nom, "autorite", "autorite.crt"))
	if err != nil {
		return fmt.Errorf("l'instance %s n'a pas d'autorité : bin/waggle installer d'abord", nom)
	}
	_, err = os.Stdout.Write(contenu)
	return err
}

func dockerCompose(fichier string, args ...string) error {
	cmd := exec.Command("docker", append([]string{"compose", "--file", fichier}, args...)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose %s : %w", strings.Join(args, " "), err)
	}
	return nil
}

func contexteArretable() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
}

func lancerControleur(args []string) error {
	fs := flag.NewFlagSet("controleur", flag.ExitOnError)
	cheminInstance := fs.String("instance", "/etat/instance.json", "les réglages validés de l'instance")
	dossierAutorite := fs.String("autorite", "/etat/autorite", "le dossier de l'autorité")
	dynamique := fs.String("dynamique", generation.Dynamique, "le dossier que lit Traefik")
	socket := fs.String("socket", "/var/run/docker.sock", "le socket de Docker")
	fs.Parse(args)
	inst, a, err := controleur.Charger(*cheminInstance, *dossierAutorite)
	if err != nil {
		return err
	}
	ctx, arreter := contexteArretable()
	defer arreter()
	journal := log.New(os.Stdout, "", log.Ltime)
	journal.Printf("contrôleur de l'instance %s, sur %s", inst.Nom, inst.Adresse)
	c := &controleur.Controleur{Instance: inst, Autorite: a, Docker: docker.Nouveau(*socket), Dossier: *dynamique, Journal: journal}
	return c.Tourner(ctx)
}

func lancerPareFeu(args []string) error {
	fs := flag.NewFlagSet("pare-feu", flag.ExitOnError)
	adresse := fs.String("adresse", "", "l'adresse du VPN")
	iface := fs.String("interface", "", "son interface WireGuard")
	uneFois := fs.Bool("une-fois", false, "poser la règle, puis rendre la main")
	intervalle := fs.Duration("intervalle", 15*time.Second, "l'intervalle entre deux vérifications")
	fs.Parse(args)
	if *adresse == "" || *iface == "" {
		return errors.New("pare-feu : --adresse et --interface sont requis")
	}
	ctx, arreter := contexteArretable()
	defer arreter()
	journal := log.New(os.Stdout, "", log.Ltime)
	journal.Printf("pare-feu : %s", parefeu.Commande(*adresse, *iface))
	for {
		poses, err := parefeu.Assurer(parefeu.Systeme, *adresse, *iface)
		switch {
		case err != nil:
			journal.Printf("pare-feu : %v", err)
			if *uneFois {
				return err
			}
		case len(poses) > 0:
			journal.Printf("pare-feu : règle posée (%s)", strings.Join(poses, ", "))
		}
		if *uneFois {
			return nil
		}
		select {
		case <-ctx.Done():
			// La règle reste : waggle arrêté, rien n'est publié, et elle ne gêne personne
			return nil
		case <-time.After(*intervalle):
		}
	}
}
