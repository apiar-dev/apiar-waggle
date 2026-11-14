// Package generation écrit, pour chaque instance, le projet Compose qui la lève (`compose.json`) et le Corefile de son DNS.
//
// Une instance, c'est :
//   - traefik, qui publie ses points d'entrée sur l'adresse de l'instance seulement, et ne lit que des fichiers : il ne
//     monte pas le socket Docker ;
//   - controleur, le seul à monter le socket : il branche les services routés, traduit leurs routes, émet leurs
//     certificats, et écrit la configuration de Traefik dans un volume ;
//   - dns, s'il y a des domaines à résoudre pour le VPN (ADR-0001, décision 6) ;
//   - pare-feu, sur une adresse de VPN : la règle DOCKER-USER, reposée à chaque démarrage de Docker (ADR-0001, décision 4).
package generation

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/apiar-dev/apiar-waggle/internal/reglages"
	"github.com/apiar-dev/apiar-waggle/internal/traefik"
)

const (
	// ImageTraefik : la version de Traefik éprouvée par les ADR (priorités négatives, routage par Header).
	ImageTraefik = "traefik:v3.6"
	// ImageCoreDNS : la version éprouvée pour le DNS du VPN.
	ImageCoreDNS = "coredns/coredns:1.11.3"
	// Dynamique : où Traefik lit, et où le contrôleur écrit, la configuration dynamique.
	Dynamique = "/dynamique"
)

type objet = map[string]any

// Projet : le nom du projet Compose d'une instance, et celui de son réseau.
func Projet(inst *reglages.Instance) string { return "waggle-" + inst.Nom }

// Compose : le projet Compose d'une instance. `dossier` est le dossier d'état de l'instance, sur l'hôte ; `image`, celle
// de waggle ; `empreintes`, celles des fichiers que lisent les services, pour que Compose les recrée quand ils changent.
func Compose(inst *reglages.Instance, dossier, image string, empreintes map[string]string) objet {
	projet := Projet(inst)
	etiquettes := func(service string) objet {
		e := objet{"waggle.instance": inst.Nom, "waggle.service": service}
		for cle, valeur := range empreintes {
			if strings.HasPrefix(cle, service+".") {
				e["waggle.empreinte."+strings.TrimPrefix(cle, service+".")] = valeur
			}
		}
		return e
	}

	var ports []string
	for _, p := range inst.PortsPublies() {
		ports = append(ports, fmt.Sprintf("%s:%d:%d", inst.Adresse, p+inst.DecalagePorts, p))
	}
	services := objet{
		"traefik": objet{
			"image":    ImageTraefik,
			"restart":  "unless-stopped",
			"command":  CommandeTraefik(inst),
			"ports":    ports,
			"volumes":  []string{"dynamique:" + Dynamique + ":ro"},
			"networks": []string{"waggle"},
			"labels":   etiquettes("traefik"),
		},
		"controleur": objet{
			"image":       image,
			"pull_policy": "never",
			"restart":     "unless-stopped",
			// Un processus en PID 1 ignore l'arrêt, que Docker n'impose qu'au bout de dix secondes
			"init":         true,
			"network_mode": "none",
			"command":      []string{"controleur", "--instance", "/etat/instance.json", "--autorite", "/etat/autorite"},
			"volumes": []string{
				"/var/run/docker.sock:/var/run/docker.sock",
				dossier + ":/etat:ro",
				"dynamique:" + Dynamique,
			},
			"labels": etiquettes("controleur"),
		},
	}
	reseaux := objet{"waggle": objet{"name": projet}}

	if len(inst.DNS) > 0 {
		services["dns"] = objet{
			"image":   ImageCoreDNS,
			"restart": "unless-stopped",
			"command": []string{"-conf", "/Corefile"},
			"ports": []string{
				fmt.Sprintf("%s:%d:53/udp", inst.Adresse, 53+inst.DecalagePorts),
				fmt.Sprintf("%s:%d:53/tcp", inst.Adresse, 53+inst.DecalagePorts),
			},
			"volumes":  []string{dossier + "/Corefile:/Corefile:ro"},
			"networks": []string{"dns"},
			"labels":   etiquettes("dns"),
		}
		reseaux["dns"] = objet{}
	}

	if inst.Interface != "" {
		services["pare-feu"] = objet{
			"image":        image,
			"pull_policy":  "never",
			"restart":      "unless-stopped",
			"init":         true,
			"network_mode": "host",
			"cap_add":      []string{"NET_ADMIN", "NET_RAW"},
			"command":      []string{"pare-feu", "--adresse", inst.Adresse, "--interface", inst.Interface},
			"labels":       etiquettes("pare-feu"),
		}
	}

	return objet{
		"name":     projet,
		"services": services,
		"networks": reseaux,
		"volumes":  objet{"dynamique": objet{"name": projet + "-dynamique"}},
	}
}

// CommandeTraefik : la configuration statique de Traefik, ses points d'entrée et son fournisseur de fichiers.
func CommandeTraefik(inst *reglages.Instance) []string {
	args := []string{"--entrypoints.p80.address=:80"}
	for _, p := range inst.PortsTLS() {
		args = append(args, fmt.Sprintf("--entrypoints.%s.address=:%d", traefik.PointEntree(p), p))
	}
	if re := inst.RelaisEntrant; re != nil {
		args = append(args,
			fmt.Sprintf("--entrypoints.relais.address=:%d", re.Port),
			"--entrypoints.relais.forwardedHeaders.trustedIPs="+strings.Join(re.Confiance, ","),
		)
	}
	return append(args,
		"--providers.file.directory="+Dynamique,
		"--providers.file.watch=true",
		"--global.checkNewVersion=false",
		"--global.sendAnonymousUsage=false",
		"--log.level=INFO",
	)
}

// Corefile : le DNS du VPN. Il répond l'adresse de l'instance pour ses domaines et tout ce qui est dessous, ne répond rien
// en AAAA, et relaie le reste (ADR-0001, décision 6).
func Corefile(inst *reglages.Instance) string {
	var b strings.Builder
	b.WriteString("# Écrit par waggle : le DNS du VPN de l'instance " + inst.Nom + ".\n")
	for _, d := range inst.DNS {
		fmt.Fprintf(&b, "%s:53 {\n", d)
		fmt.Fprintf(&b, "    template IN A {\n        answer \"{{ .Name }} 60 IN A %s\"\n    }\n", inst.Adresse)
		b.WriteString("    template IN AAAA {\n        rcode NOERROR\n    }\n")
		b.WriteString("    errors\n}\n")
	}
	b.WriteString(".:53 {\n    forward . /etc/resolv.conf\n    cache 30\n    errors\n}\n")
	return b.String()
}

// Empreinte : une empreinte courte d'un contenu.
func Empreinte(contenu []byte) string {
	somme := sha256.Sum256(contenu)
	return hex.EncodeToString(somme[:6])
}
