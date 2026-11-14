// Package routes lit les routes qu'un service déclare dans son étiquette `waggle.routes` (ADR-0002, décision 1) : une URL
// par ligne, où `{dossier}` désigne le nom de la stack, suivie du port du service. Le schéma dit le mode :
//
//	https://{dossier}.dev.exemple.fr/          8080   waggle termine le TLS, et route par hôte et par chemin
//	https://{dossier}.dev.exemple.fr:3000/     5173   un port historique, terminé par waggle
//	https://*.{dossier}.dev.exemple.fr/api     8000   une famille d'hôtes, sous un chemin
//	http://admin.{dossier}.localhost/          5173   en HTTP, sur p80
//	tls://{dossier}.dev.ancien.fr:3000         3000   le passage : la stack termine son TLS
//
// Une route dont l'hôte ne porte pas `{dossier}` est refusée : une stack ne peut pas prendre le nom d'une autre.
package routes

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Mode : comment waggle sert une route.
type Mode string

const (
	HTTPS Mode = "https" // waggle termine le TLS
	HTTP  Mode = "http"  // en clair, sur p80
	TLS   Mode = "tls"   // le passage, routé par le SNI
)

// Etiquette : l'étiquette où un service déclare ses routes.
const Etiquette = "waggle.routes"

// Route : une route lue, `{dossier}` remplacé.
type Route struct {
	Mode    Mode
	Hote    string // a.dev.exemple.fr, ou *.a.dev.exemple.fr
	Port    int    // le point d'entrée : 80, 443, ou un port historique
	Chemin  string // « / », ou un préfixe sans barre finale : « /api »
	Cible   int    // le port du service
	Dossier string
	Prefixe string // les labels avant le dossier : « », « api », « * »
	Domaine string // les labels après le dossier : « dev.exemple.fr »
	Texte   string // la ligne d'origine, pour les journaux
}

var (
	reLabel   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	reChemin  = regexp.MustCompile(`^(/[A-Za-z0-9._~!$&'()*+,;=:@%-]+)+$`)
	reDossier = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// Lire lit la valeur de l'étiquette d'un service. `exposes` : les ports que l'image expose, pour une route qui ne nomme
// pas le sien. Chaque ligne fausse donne une erreur, et les autres restent.
func Lire(valeur, dossier string, exposes []int) ([]Route, []error) {
	var routes []Route
	var erreurs []error
	if !reDossier.MatchString(dossier) {
		return nil, []error{fmt.Errorf("le nom de la stack, %q, ne tient pas dans un label DNS (minuscules, chiffres, tirets)", dossier)}
	}
	for _, ligne := range strings.Split(valeur, "\n") {
		if i := strings.Index(ligne, "#"); i >= 0 {
			ligne = ligne[:i]
		}
		ligne = strings.TrimSpace(ligne)
		if ligne == "" {
			continue
		}
		r, err := lireLigne(ligne, dossier, exposes)
		if err != nil {
			erreurs = append(erreurs, fmt.Errorf("« %s » : %w", ligne, err))
			continue
		}
		routes = append(routes, r)
	}
	return routes, erreurs
}

func lireLigne(ligne, dossier string, exposes []int) (Route, error) {
	champs := strings.Fields(ligne)
	if len(champs) > 2 {
		return Route{}, fmt.Errorf("une route est une URL, puis le port du service")
	}
	r := Route{Texte: ligne, Dossier: dossier}
	schema, reste, ok := strings.Cut(champs[0], "://")
	if !ok {
		return Route{}, fmt.Errorf("il faut un schéma : https://, http:// ou tls://")
	}
	r.Mode = Mode(schema)
	switch r.Mode {
	case HTTPS, TLS:
		r.Port = 443
	case HTTP:
		r.Port = 80
	default:
		return Route{}, fmt.Errorf("le schéma %q n'est pas https, http ou tls", schema)
	}

	hotePort, chemin := reste, "/"
	if i := strings.Index(reste, "/"); i >= 0 {
		hotePort, chemin = reste[:i], reste[i:]
	}
	hote := hotePort
	if h, p, ok := strings.Cut(hotePort, ":"); ok {
		port, err := strconv.Atoi(p)
		if err != nil || port < 1 || port > 65535 {
			return Route{}, fmt.Errorf("le port %q n'existe pas", p)
		}
		hote, r.Port = h, port
	}
	if r.Mode == HTTP && r.Port != 80 {
		return Route{}, fmt.Errorf("une route http:// passe par le port 80 seulement")
	}

	if chemin != "/" {
		chemin = strings.TrimSuffix(chemin, "/")
		if r.Mode == TLS {
			return Route{}, fmt.Errorf("une route tls:// ne porte pas de chemin : le SNI n'en dit rien")
		}
		if !reChemin.MatchString(chemin) {
			return Route{}, fmt.Errorf("le chemin %q n'est pas un chemin d'URL simple", chemin)
		}
	}
	r.Chemin = chemin

	labels := strings.Split(strings.ToLower(hote), ".")
	position := -1
	for i, l := range labels {
		if l == "{dossier}" {
			if position >= 0 {
				return Route{}, fmt.Errorf("{dossier} n'apparaît qu'une fois")
			}
			position = i
		}
	}
	if position < 0 {
		return Route{}, fmt.Errorf("l'hôte ne porte pas {dossier} : une stack ne prend pas le nom d'une autre")
	}
	prefixe, domaine := labels[:position], labels[position+1:]
	if len(domaine) == 0 {
		return Route{}, fmt.Errorf("il faut un domaine après {dossier}")
	}
	for i, l := range prefixe {
		if l == "*" && i == 0 {
			continue
		}
		if !reLabel.MatchString(l) {
			return Route{}, fmt.Errorf("le label %q n'est pas un label DNS (seul le premier peut être *)", l)
		}
	}
	for _, l := range domaine {
		if !reLabel.MatchString(l) {
			return Route{}, fmt.Errorf("le label %q n'est pas un label DNS", l)
		}
	}
	r.Prefixe = strings.Join(prefixe, ".")
	r.Domaine = strings.Join(domaine, ".")
	r.Hote = strings.Join(append(append(append([]string{}, prefixe...), dossier), domaine...), ".")

	switch {
	case len(champs) == 2:
		cible, err := strconv.Atoi(champs[1])
		if err != nil || cible < 1 || cible > 65535 {
			return Route{}, fmt.Errorf("le port du service, %q, n'existe pas", champs[1])
		}
		r.Cible = cible
	case len(exposes) == 1:
		r.Cible = exposes[0]
	default:
		return Route{}, fmt.Errorf("le port du service manque, et l'image n'en expose pas un seul")
	}
	return r, nil
}

// Joker dit si l'hôte est une famille (*.a.dev.exemple.fr).
func (r Route) Joker() bool { return strings.HasPrefix(r.Hote, "*.") }

// Cle identifie ce qu'une route occupe : deux routes de même clé se disputent les mêmes requêtes.
func (r Route) Cle() string {
	return fmt.Sprintf("%s %s:%d%s", r.Mode, r.Hote, r.Port, r.Chemin)
}

// URL : la route telle qu'un navigateur l'écrit.
func (r Route) URL() string {
	port := ""
	if !(r.Mode == HTTP && r.Port == 80) && !(r.Mode != HTTP && r.Port == 443) {
		port = fmt.Sprintf(":%d", r.Port)
	}
	chemin := r.Chemin
	if r.Mode == TLS {
		chemin = ""
	}
	return fmt.Sprintf("%s://%s%s%s", r.Mode, r.Hote, port, chemin)
}
