// Package reglages lit et valide le fichier de réglages d'une machine : ses instances, leurs adresses, leurs ports, leurs
// projets, leurs relais et leur DNS (ADR-0001, décisions 1, 4 et 7). Une adresse qui n'est ni la boucle locale ni celle d'une
// interface WireGuard est refusée : une URL de dev n'est jamais sur internet.
package reglages

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// PortRelais est le port par défaut du point d'entrée `relais` d'un serveur, où les postes relaient en HTTP les domaines
// terminés par waggle (ADR-0002, décision 3).
const PortRelais = 8080

// Fichier : les réglages d'une machine.
type Fichier struct {
	Instances map[string]*Instance `yaml:"instances"`
}

// Instance : un Traefik, son contrôleur, son réseau, et selon les réglages un DNS et une règle de pare-feu.
type Instance struct {
	Nom           string         `yaml:"-" json:"nom"`
	Adresse       string         `yaml:"adresse" json:"adresse"`
	Interface     string         `yaml:"interface,omitempty" json:"interface,omitempty"`
	Projets       []string       `yaml:"projets,omitempty" json:"projets,omitempty"`
	Ports         []int          `yaml:"ports,omitempty" json:"ports,omitempty"`
	Relais        []Relais       `yaml:"relais,omitempty" json:"relais,omitempty"`
	DNS           []string       `yaml:"dns,omitempty" json:"dns,omitempty"`
	RelaisEntrant *RelaisEntrant `yaml:"relais_entrant,omitempty" json:"relais_entrant,omitempty"`
	DecalagePorts int            `yaml:"decalage_ports,omitempty" json:"decalage_ports,omitempty"`
}

// Relais : un serveur vers lequel ce poste relaie les domaines qu'il ne sert pas.
type Relais struct {
	Vers          string   `yaml:"vers" json:"vers"`
	Domaines      []string `yaml:"domaines" json:"domaines"`
	PortRelais    int      `yaml:"port_relais,omitempty" json:"port_relais,omitempty"`
	DecalagePorts int      `yaml:"decalage_ports,omitempty" json:"decalage_ports,omitempty"`
}

// RelaisEntrant : le point d'entrée HTTP d'un serveur pour le relais des postes, et les adresses dont il croit les en-têtes
// X-Forwarded-*.
type RelaisEntrant struct {
	Port      int      `yaml:"port" json:"port"`
	Confiance []string `yaml:"confiance" json:"confiance"`
}

// DomaineRelaye : un domaine d'un relais, lu de « https://dev.exemple.fr ». Le schéma dit le mode, comme pour les routes.
type DomaineRelaye struct {
	Mode    string // https, tls ou http
	Domaine string // « * » : tout le reste, en http seulement
}

// Carte : une interface réseau de la machine.
type Carte struct {
	Nom       string
	WireGuard bool
	Reseaux   []*net.IPNet
}

// Machine : ce que la validation doit savoir de la machine.
type Machine struct {
	Systeme string // « Linux », « Darwin »…
	Cartes  func() ([]Carte, error)
}

var (
	reNom     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	reDomaine = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// Lire lit un fichier de réglages, en refusant les clés inconnues : une faute de frappe ne doit pas passer pour un réglage.
func Lire(chemin string) (*Fichier, error) {
	contenu, err := os.ReadFile(chemin)
	if err != nil {
		return nil, fmt.Errorf("les réglages : %w", err)
	}
	return Decoder(contenu)
}

// Decoder lit des réglages en YAML.
func Decoder(contenu []byte) (*Fichier, error) {
	dec := yaml.NewDecoder(bytes.NewReader(contenu))
	dec.KnownFields(true)
	var f Fichier
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("les réglages ne se lisent pas : %w", err)
	}
	return &f, nil
}

// Valider vérifie les réglages, complète ce qui se déduit — l'interface d'une adresse de VPN, son point d'entrée de relais
// et le réseau dont il croit les en-têtes — et rend les instances triées par nom.
func Valider(f *Fichier, m Machine) ([]*Instance, error) {
	if f == nil || len(f.Instances) == 0 {
		return nil, errors.New("les réglages ne décrivent aucune instance (clé « instances »)")
	}
	var cartes []Carte
	if m.Systeme == "Linux" && m.Cartes != nil {
		var err error
		if cartes, err = m.Cartes(); err != nil {
			return nil, fmt.Errorf("les interfaces de la machine : %w", err)
		}
	}
	var instances []*Instance
	var erreurs []error
	for nom, inst := range f.Instances {
		if inst == nil {
			inst = &Instance{}
		}
		inst.Nom = nom
		for _, err := range validerInstance(inst, m.Systeme, cartes) {
			erreurs = append(erreurs, fmt.Errorf("instance %s : %w", nom, err))
		}
		instances = append(instances, inst)
	}
	sort.Slice(instances, func(i, j int) bool { return instances[i].Nom < instances[j].Nom })
	erreurs = append(erreurs, validerEnsemble(instances)...)
	if len(erreurs) > 0 {
		return nil, errors.Join(erreurs...)
	}
	return instances, nil
}

func validerInstance(inst *Instance, systeme string, cartes []Carte) []error {
	var erreurs []error
	ajouter := func(format string, args ...any) { erreurs = append(erreurs, fmt.Errorf(format, args...)) }

	if !reNom.MatchString(inst.Nom) {
		ajouter("le nom doit tenir en minuscules, chiffres et tirets")
	}
	ip := net.ParseIP(inst.Adresse).To4()
	switch {
	case inst.Adresse == "":
		ajouter("l'adresse manque (127.0.0.1 sur un poste, l'adresse WireGuard sur un serveur)")
	case ip == nil:
		ajouter("l'adresse %q n'est pas une adresse IPv4", inst.Adresse)
	case ip.IsUnspecified():
		ajouter("waggle refuse %s : une URL de dev n'est jamais sur internet", inst.Adresse)
	case ip.IsLoopback():
		if inst.Interface != "" {
			ajouter("une adresse de la boucle locale n'a pas d'interface à nommer")
		}
	case systeme != "Linux":
		ajouter("sur %s, waggle n'écoute que sur la boucle locale : %s refusée", nomSysteme(systeme), inst.Adresse)
	default:
		carte, reseau := carteDe(ip, cartes)
		switch {
		case carte == nil:
			ajouter("aucune interface de la machine ne porte %s", inst.Adresse)
		case !carte.WireGuard:
			ajouter("%s est sur %s, qui n'est pas une interface WireGuard : waggle n'écoute que sur la boucle locale ou un VPN", inst.Adresse, carte.Nom)
		case inst.Interface != "" && inst.Interface != carte.Nom:
			ajouter("%s est sur %s, et non sur %s", inst.Adresse, carte.Nom, inst.Interface)
		default:
			inst.Interface = carte.Nom
			if inst.RelaisEntrant == nil {
				inst.RelaisEntrant = &RelaisEntrant{Port: PortRelais, Confiance: []string{reseau.String()}}
			}
		}
	}

	vus := map[int]bool{}
	for _, p := range inst.Ports {
		switch {
		case p < 1 || p > 65535:
			ajouter("le port %d n'existe pas", p)
		case p == 80 || p == 443:
			ajouter("le port %d est déjà au catalogue : n'y mettre que les ports historiques", p)
		case p == 53:
			ajouter("le port 53 est celui du DNS du VPN")
		case vus[p]:
			ajouter("le port %d est nommé deux fois", p)
		}
		vus[p] = true
	}
	sort.Ints(inst.Ports)

	if inst.DecalagePorts < 0 || inst.DecalagePorts > 60000 {
		ajouter("le décalage des ports doit être entre 0 et 60000")
	}
	for _, p := range inst.PortsPublies() {
		if p+inst.DecalagePorts > 65535 {
			ajouter("le port %d, décalé de %d, dépasse 65535", p, inst.DecalagePorts)
		}
	}

	for _, projet := range inst.Projets {
		if !reNom.MatchString(projet) {
			ajouter("le projet %q doit tenir en minuscules, chiffres et tirets", projet)
		}
	}

	if re := inst.RelaisEntrant; re != nil {
		if re.Port < 1 || re.Port > 65535 || re.Port == 80 || re.Port == 443 || vus[re.Port] {
			ajouter("le port du relais entrant, %d, est invalide ou déjà pris", re.Port)
		}
		if len(re.Confiance) == 0 {
			ajouter("le relais entrant doit dire de quelles adresses il croit les en-têtes (confiance)")
		}
		for _, c := range re.Confiance {
			if _, _, err := net.ParseCIDR(c); err != nil {
				ajouter("la confiance du relais entrant, %q, n'est pas un réseau (10.8.0.0/24)", c)
			}
		}
	}

	for i := range inst.Relais {
		r := &inst.Relais[i]
		if r.Vers == "" {
			ajouter("le relais %d ne dit pas vers où (vers)", i+1)
		}
		if r.PortRelais == 0 {
			r.PortRelais = PortRelais
		}
		if r.DecalagePorts < 0 || r.DecalagePorts > 60000 {
			ajouter("le décalage des ports du relais %d doit être entre 0 et 60000", i+1)
		}
		if len(r.Domaines) == 0 {
			ajouter("le relais vers %s ne nomme aucun domaine", r.Vers)
		}
		for _, d := range r.Domaines {
			if _, err := LireDomaineRelaye(d); err != nil {
				ajouter("le relais vers %s : %v", r.Vers, err)
			}
		}
	}

	for _, d := range inst.DNS {
		if !reDomaine.MatchString(d) {
			ajouter("le domaine %q du DNS n'est pas un nom de domaine", d)
		}
	}
	return erreurs
}

func validerEnsemble(instances []*Instance) []error {
	var erreurs []error
	proprietaire := map[string]string{}
	sansProjets := 0
	adresses := map[string]string{}
	for _, inst := range instances {
		if len(inst.Projets) == 0 {
			sansProjets++
		}
		for _, p := range inst.Projets {
			if autre, ok := proprietaire[p]; ok && autre != inst.Nom {
				erreurs = append(erreurs, fmt.Errorf("le projet %s est lu par deux instances, %s et %s : l'isolement l'interdit", p, autre, inst.Nom))
			}
			proprietaire[p] = inst.Nom
		}
		cle := fmt.Sprintf("%s+%d", inst.Adresse, inst.DecalagePorts)
		if autre, ok := adresses[cle]; ok {
			erreurs = append(erreurs, fmt.Errorf("les instances %s et %s écoutent sur la même adresse, %s", autre, inst.Nom, inst.Adresse))
		}
		adresses[cle] = inst.Nom
	}
	if sansProjets > 0 && len(instances) > 1 {
		erreurs = append(erreurs, errors.New("avec plusieurs instances, chacune nomme ses projets (projets) : une instance sans liste les lirait tous"))
	}
	return erreurs
}

// LireDomaineRelaye lit « https://dev.exemple.fr », « tls://dev.ancien.fr », « http://localhost » ou « http://* ».
func LireDomaineRelaye(texte string) (DomaineRelaye, error) {
	mode, domaine, ok := strings.Cut(texte, "://")
	if !ok {
		return DomaineRelaye{}, fmt.Errorf("%q : il faut un schéma, https://, tls:// ou http://", texte)
	}
	domaine = strings.TrimSuffix(strings.ToLower(domaine), "/")
	switch mode {
	case "https", "tls":
		if !reDomaine.MatchString(domaine) || !strings.Contains(domaine, ".") {
			return DomaineRelaye{}, fmt.Errorf("%q : un domaine en %s compte au moins deux labels", texte, mode)
		}
	case "http":
		if domaine != "*" && !reDomaine.MatchString(domaine) {
			return DomaineRelaye{}, fmt.Errorf("%q n'est pas un nom de domaine", texte)
		}
	default:
		return DomaineRelaye{}, fmt.Errorf("%q : le schéma doit être https://, tls:// ou http://", texte)
	}
	return DomaineRelaye{Mode: mode, Domaine: domaine}, nil
}

// LitProjet dit si cette instance sert le projet.
func (inst *Instance) LitProjet(projet string) bool {
	if len(inst.Projets) == 0 {
		return true
	}
	for _, p := range inst.Projets {
		if p == projet {
			return true
		}
	}
	return false
}

// PortsTLS : les points d'entrée TLS, 443 et les ports historiques.
func (inst *Instance) PortsTLS() []int {
	return append([]int{443}, inst.Ports...)
}

// PortsPublies : les ports TCP des points d'entrée de Traefik, que l'instance publie sur son adresse. Le DNS publie en plus
// le 53, en UDP et en TCP.
func (inst *Instance) PortsPublies() []int {
	ports := append([]int{80}, inst.PortsTLS()...)
	if inst.RelaisEntrant != nil {
		ports = append(ports, inst.RelaisEntrant.Port)
	}
	return ports
}

// AuCatalogue dit si un port TLS est un point d'entrée de l'instance.
func (inst *Instance) AuCatalogue(port int) bool {
	for _, p := range inst.PortsTLS() {
		if p == port {
			return true
		}
	}
	return false
}

func carteDe(ip net.IP, cartes []Carte) (*Carte, *net.IPNet) {
	for i := range cartes {
		for _, r := range cartes[i].Reseaux {
			if r.IP.Equal(ip) {
				return &cartes[i], &net.IPNet{IP: r.IP.Mask(r.Mask), Mask: r.Mask}
			}
		}
	}
	return nil, nil
}

func nomSysteme(s string) string {
	switch s {
	case "Darwin":
		return "macOS"
	case "":
		return "ce système"
	}
	return s
}

// CartesDeLaMachine lit les interfaces de la machine, et reconnaît celles de WireGuard à leur uevent (DEVTYPE=wireguard).
// Lancé dans un conteneur, il faut `--network host` pour voir celles de l'hôte.
func CartesDeLaMachine() ([]Carte, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var cartes []Carte
	for _, iface := range ifaces {
		c := Carte{Nom: iface.Name}
		if uevent, err := os.ReadFile("/sys/class/net/" + iface.Name + "/uevent"); err == nil {
			c.WireGuard = bytes.Contains(uevent, []byte("DEVTYPE=wireguard"))
		}
		adresses, _ := iface.Addrs()
		for _, a := range adresses {
			if r, ok := a.(*net.IPNet); ok {
				c.Reseaux = append(c.Reseaux, r)
			}
		}
		cartes = append(cartes, c)
	}
	return cartes, nil
}
