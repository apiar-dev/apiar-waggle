// Package parefeu pose, sur un serveur, la règle qui ferme l'adresse d'un VPN à tout ce qui n'entre pas par son interface
// (ADR-0001, décision 4) :
//
//	iptables -I DOCKER-USER -m conntrack --ctorigdst <adresse> --ctdir ORIGINAL ! -i <interface> -j DROP
//
// `--ctdir ORIGINAL` est indispensable : les réponses d'une stack entrent dans FORWARD par le pont de Docker et portent la
// même destination d'origine ; sans lui, la règle les refuse aussi, et coupe les membres du tunnel.
package parefeu

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Executer : lance une commande et rend sa sortie.
type Executer func(nom string, args ...string) (string, error)

// Systeme lance la commande sur la machine.
func Systeme(nom string, args ...string) (string, error) {
	sortie, err := exec.Command(nom, args...).CombinedOutput()
	return strings.TrimSpace(string(sortie)), err
}

// Regle : la règle, sans son verbe (-I, -C, -D).
func Regle(adresse, iface string) []string {
	return []string{"DOCKER-USER", "-m", "conntrack", "--ctorigdst", adresse, "--ctdir", "ORIGINAL", "!", "-i", iface, "-j", "DROP"}
}

// Binaires : les deux pare-feux qu'un hôte peut faire tourner. Docker écrit ses règles dans l'un ou l'autre ; la règle va
// là où vit sa chaîne DOCKER-USER.
var Binaires = []string{"iptables-nft", "iptables-legacy"}

// Assurer pose la règle dans le premier pare-feu qui porte la chaîne DOCKER-USER, si elle n'y est pas déjà, et rend le
// binaire où elle vient de la poser. Il s'arrête au premier : sonder iptables-legacy sur un hôte en nftables y créerait
// des tables vides.
func Assurer(executer Executer, adresse, iface string) ([]string, error) {
	for _, b := range Binaires {
		if _, err := executer(b, "-n", "-L", "DOCKER-USER"); err != nil {
			continue
		}
		if _, err := executer(b, append([]string{"-C"}, Regle(adresse, iface)...)...); err == nil {
			return nil, nil
		}
		if sortie, err := executer(b, append([]string{"-I"}, Regle(adresse, iface)...)...); err != nil {
			return nil, fmt.Errorf("%s n'a pas posé la règle : %v %s", b, err, sortie)
		}
		return []string{b}, nil
	}
	return nil, errors.New("aucune chaîne DOCKER-USER sur cette machine : Docker tourne-t-il avec son pare-feu iptables ?")
}

// Commande : la règle telle qu'on la lirait dans un terminal.
func Commande(adresse, iface string) string {
	return "iptables -I " + strings.Join(Regle(adresse, iface), " ")
}
