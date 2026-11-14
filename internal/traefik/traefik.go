// Package traefik traduit les routes des stacks et les relais d'une instance en configuration dynamique de Traefik, que le
// fournisseur de fichiers relit (ADR-0002, décisions 1 à 3). Tout est écrit en JSON, que le YAML de Traefik lit tel quel.
package traefik

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/apiar-dev/apiar-waggle/internal/reglages"
	"github.com/apiar-dev/apiar-waggle/internal/routes"
)

// DelaiRelais : le délai de connexion d'un relais. VPN coupé, un nom relayé échoue en deux secondes, et non en trente
// (ADR-0001, décision 5).
const DelaiRelais = "2s"

// EnTetePort : l'en-tête où le relais d'un poste nomme le port d'entrée d'une requête. Le point d'entrée `relais` d'un
// serveur n'est ouvert qu'au VPN.
const EnTetePort = "X-Waggle-Port"

// RouteServie : une route, et le service qui la sert sur le réseau de l'instance.
type RouteServie struct {
	routes.Route
	Conteneur string
	IP        string
}

// Certificat : les fichiers d'un certificat, tels que Traefik les lit.
type Certificat struct {
	Cert string
	Cle  string
}

type section = map[string]any

// PointEntree : le nom du point d'entrée d'un port, p<port>.
func PointEntree(port int) string { return fmt.Sprintf("p%d", port) }

// Routes : les routeurs et les services des stacks.
func Routes(inst *reglages.Instance, rs []RouteServie) section {
	httpR, httpS, tcpR, tcpS := section{}, section{}, section{}, section{}
	rang := map[string]int{}
	for _, r := range rs {
		rang[r.Conteneur]++
		nom := fmt.Sprintf("%s-%d", Nettoyer(r.Conteneur), rang[r.Conteneur])
		switch r.Mode {
		case routes.HTTPS:
			regle := regleHTTP(r.Route)
			httpR[nom] = section{"entryPoints": []string{PointEntree(r.Port)}, "rule": regle, "tls": section{}, "service": nom}
			if inst.RelaisEntrant != nil {
				// Le relais d'un poste arrive en HTTP, et nomme dans EnTetePort le point d'entrée où la requête est arrivée
				// chez lui : une route du port 3000 n'y répond qu'à une requête venue du port 3000
				httpR[nom+"-relais"] = section{
					"entryPoints": []string{"relais"},
					"rule":        fmt.Sprintf("(%s) && Header(`%s`, `%d`)", regle, EnTetePort, r.Port),
					"service":     nom,
				}
			}
			httpS[nom] = serviceHTTP(fmt.Sprintf("http://%s:%d", r.IP, r.Cible), false)
		case routes.HTTP:
			httpR[nom] = section{"entryPoints": []string{"p80"}, "rule": regleHTTP(r.Route), "service": nom}
			httpS[nom] = serviceHTTP(fmt.Sprintf("http://%s:%d", r.IP, r.Cible), false)
		case routes.TLS:
			tcpR[nom] = section{
				"entryPoints": []string{PointEntree(r.Port)},
				"rule":        regleSNI(r.Route),
				"tls":         section{"passthrough": true},
				"service":     nom,
			}
			tcpS[nom] = serviceTCP(fmt.Sprintf("%s:%d", r.IP, r.Cible), false)
		}
	}
	return assembler(httpR, httpS, nil, tcpR, tcpS, false)
}

// Relais : les routes de relais d'un poste, une par domaine, dans le mode du domaine, à priorité négative : toute route
// d'une stack passe devant (ADR-0001, décision 5 ; ADR-0002, décision 3).
func Relais(inst *reglages.Instance) section {
	httpR, httpS, httpM, tcpR, tcpS := section{}, section{}, section{}, section{}, section{}
	for i, rel := range inst.Relais {
		n := i + 1
		for _, texte := range rel.Domaines {
			d, err := reglages.LireDomaineRelaye(texte)
			if err != nil {
				continue // les réglages validés n'en ont pas
			}
			base := fmt.Sprintf("relais-%d-%s", n, Nettoyer(d.Domaine))
			switch d.Mode {
			case "https":
				// Terminé ici, relayé en HTTP au point d'entrée `relais` du serveur, requête par requête
				service := fmt.Sprintf("relais-%d-https", n)
				httpS[service] = serviceHTTP(fmt.Sprintf("http://%s:%d", rel.Vers, rel.PortRelais+rel.DecalagePorts), true)
				for _, p := range inst.PortsTLS() {
					// Le serveur retrouve le point d'entrée d'origine par cet en-tête, et non par X-Forwarded-Port, qui
					// reprend le port de l'en-tête Host
					marque := fmt.Sprintf("relais-port-p%d", p)
					httpM[marque] = section{"headers": section{"customRequestHeaders": section{EnTetePort: fmt.Sprint(p)}}}
					httpR[fmt.Sprintf("%s-https-p%d", base, p)] = section{
						"entryPoints": []string{PointEntree(p)}, "rule": regleDomaine("HostRegexp", d.Domaine),
						"priority": -1, "tls": section{}, "middlewares": []string{marque}, "service": service,
					}
				}
			case "tls":
				// En passage, relayé en TCP sur le même port du serveur ; jamais de fourre-tout HostSNI(`*`), qui ne reçoit
				// plus rien à côté de routes HTTPS
				for _, p := range inst.PortsTLS() {
					service := fmt.Sprintf("relais-%d-tls-p%d", n, p)
					tcpS[service] = serviceTCP(fmt.Sprintf("%s:%d", rel.Vers, p+rel.DecalagePorts), true)
					tcpR[fmt.Sprintf("%s-tls-p%d", base, p)] = section{
						"entryPoints": []string{PointEntree(p)}, "rule": regleDomaine("HostSNIRegexp", d.Domaine),
						"priority": -1, "tls": section{"passthrough": true}, "service": service,
					}
				}
			case "http":
				service := fmt.Sprintf("relais-%d-http", n)
				httpS[service] = serviceHTTP(fmt.Sprintf("http://%s:%d", rel.Vers, 80+rel.DecalagePorts), true)
				regle, priorite := regleDomaine("HostRegexp", d.Domaine), -1
				if d.Domaine == "*" {
					regle, priorite = "PathPrefix(`/`)", -2
				}
				httpR[base+"-http"] = section{"entryPoints": []string{"p80"}, "rule": regle, "priority": priorite, "service": service}
			}
		}
	}
	return assembler(httpR, httpS, httpM, tcpR, tcpS, len(inst.Relais) > 0)
}

// TLS : les certificats que Traefik présente, choisis par le SNI.
func TLS(certificats []Certificat) section {
	if len(certificats) == 0 {
		return section{}
	}
	var liste []any
	for _, c := range certificats {
		liste = append(liste, section{"certFile": c.Cert, "keyFile": c.Cle})
	}
	return section{"tls": section{"certificates": liste}}
}

// CertificatsVoulus : les certificats qu'il faut pour les routes HTTPS et les domaines relayés en HTTPS, par nom de fichier.
//   - Un hôte à un label sous le domaine (<dossier>.<domaine>) : le joker du domaine, *.<domaine>.
//   - Un hôte à plusieurs labels (api.<dossier>.<domaine>) : le certificat de la stack, qui couvre <dossier>.<domaine> et
//     le parent de chacun de ses hôtes, en joker. C'est le choix d'Upsun, un certificat par environnement.
//   - Un domaine d'un seul label (localhost) n'a pas de joker : le certificat de la stack nomme ses hôtes.
func CertificatsVoulus(inst *reglages.Instance, rs []RouteServie) map[string][]string {
	ensembles := map[string]map[string]bool{}
	ajouter := func(fichier string, noms ...string) {
		if ensembles[fichier] == nil {
			ensembles[fichier] = map[string]bool{}
		}
		for _, n := range noms {
			ensembles[fichier][n] = true
		}
	}
	for _, r := range rs {
		if r.Mode != routes.HTTPS {
			continue
		}
		pile := r.Dossier + "." + r.Domaine
		switch {
		case r.Prefixe == "" && strings.Contains(r.Domaine, "."):
			ajouter("joker."+r.Domaine, "*."+r.Domaine, r.Domaine)
		case r.Prefixe == "":
			ajouter("pile."+pile, pile)
		default:
			_, parent, _ := strings.Cut(r.Hote, ".")
			ajouter("pile."+pile, pile, "*."+parent)
		}
	}
	for _, rel := range inst.Relais {
		for _, texte := range rel.Domaines {
			if d, err := reglages.LireDomaineRelaye(texte); err == nil && d.Mode == "https" {
				ajouter("joker."+d.Domaine, "*."+d.Domaine, d.Domaine)
			}
		}
	}
	voulus := map[string][]string{}
	for fichier, noms := range ensembles {
		var liste []string
		for n := range noms {
			liste = append(liste, n)
		}
		sort.Strings(liste)
		voulus[fichier] = liste
	}
	return voulus
}

// Nettoyer rend un nom utilisable comme nom de routeur ou de fichier.
func Nettoyer(s string) string {
	s = strings.ToLower(strings.TrimPrefix(s, "/"))
	s = strings.ReplaceAll(s, "*", "joker")
	return strings.Trim(reNonNom.ReplaceAllString(s, "-"), "-")
}

var reNonNom = regexp.MustCompile(`[^a-z0-9-]+`)

func regleHote(r routes.Route) string {
	if r.Joker() {
		return fmt.Sprintf("HostRegexp(`^.+\\.%s$`)", regexp.QuoteMeta(strings.TrimPrefix(r.Hote, "*.")))
	}
	return fmt.Sprintf("Host(`%s`)", r.Hote)
}

func regleHTTP(r routes.Route) string {
	regle := regleHote(r)
	if r.Chemin != "/" {
		// Path(`/api`) || PathPrefix(`/api/`) : /api et ce qui est dessous, mais pas /apiv2
		regle += fmt.Sprintf(" && (Path(`%s`) || PathPrefix(`%s/`))", r.Chemin, r.Chemin)
	}
	return regle
}

func regleSNI(r routes.Route) string {
	if r.Joker() {
		return fmt.Sprintf("HostSNIRegexp(`^.+\\.%s$`)", regexp.QuoteMeta(strings.TrimPrefix(r.Hote, "*.")))
	}
	return fmt.Sprintf("HostSNI(`%s`)", r.Hote)
}

// regleDomaine : un domaine et tout ce qui est dessous.
func regleDomaine(matcher, domaine string) string {
	return fmt.Sprintf("%s(`^(.+\\.)?%s$`)", matcher, regexp.QuoteMeta(domaine))
}

func serviceHTTP(url string, relais bool) section {
	lb := section{"passHostHeader": true, "servers": []any{section{"url": url}}}
	if relais {
		lb["serversTransport"] = "relais"
	}
	return section{"loadBalancer": lb}
}

func serviceTCP(adresse string, relais bool) section {
	lb := section{"servers": []any{section{"address": adresse}}}
	if relais {
		lb["serversTransport"] = "relais"
	}
	return section{"loadBalancer": lb}
}

func assembler(httpR, httpS, httpM, tcpR, tcpS section, transports bool) section {
	config := section{}
	if len(httpR) > 0 || transports {
		h := section{}
		if len(httpR) > 0 {
			h["routers"], h["services"] = httpR, httpS
		}
		if len(httpM) > 0 {
			h["middlewares"] = httpM
		}
		if transports {
			h["serversTransports"] = section{"relais": section{"forwardingTimeouts": section{"dialTimeout": DelaiRelais}}}
		}
		config["http"] = h
	}
	if len(tcpR) > 0 || transports {
		t := section{}
		if len(tcpR) > 0 {
			t["routers"], t["services"] = tcpR, tcpS
		}
		if transports {
			t["serversTransports"] = section{"relais": section{"dialTimeout": DelaiRelais}}
		}
		config["tcp"] = t
	}
	return config
}
