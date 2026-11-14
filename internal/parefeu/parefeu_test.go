package parefeu

import (
	"errors"
	"strings"
	"testing"
)

// faux : un hôte dont seul `iptables-nft` a la chaîne DOCKER-USER, et qui garde les règles posées.
type faux struct {
	chaines map[string]bool
	regles  map[string]bool
	appels  []string
}

func (f *faux) executer(nom string, args ...string) (string, error) {
	f.appels = append(f.appels, nom+" "+strings.Join(args, " "))
	if !f.chaines[nom] {
		return "", errors.New("pas de chaîne")
	}
	regle := nom + " " + strings.Join(args[1:], " ")
	switch args[0] {
	case "-n":
		return "", nil
	case "-C":
		if f.regles[regle] {
			return "", nil
		}
		return "", errors.New("absente")
	case "-I":
		f.regles[regle] = true
		return "", nil
	}
	return "", errors.New("inattendu")
}

func TestAssurer(t *testing.T) {
	f := &faux{chaines: map[string]bool{"iptables-nft": true}, regles: map[string]bool{}}
	poses, err := Assurer(f.executer, "10.8.0.1", "wg0")
	if err != nil || len(poses) != 1 || poses[0] != "iptables-nft" {
		t.Fatalf("la règle se pose là où vit la chaîne : %v, %v", poses, err)
	}
	attendu := "iptables-nft -I DOCKER-USER -m conntrack --ctorigdst 10.8.0.1 --ctdir ORIGINAL ! -i wg0 -j DROP"
	if !strings.Contains(strings.Join(f.appels, "\n"), attendu) {
		t.Fatalf("la règle, --ctdir ORIGINAL compris :\n%s", strings.Join(f.appels, "\n"))
	}
	poses, err = Assurer(f.executer, "10.8.0.1", "wg0")
	if err != nil || len(poses) != 0 {
		t.Fatalf("déjà posée, rien ne change : %v, %v", poses, err)
	}
}

func TestUnSeulPareFeu(t *testing.T) {
	f := &faux{chaines: map[string]bool{"iptables-nft": true, "iptables-legacy": true}, regles: map[string]bool{}}
	if _, err := Assurer(f.executer, "10.8.0.1", "wg0"); err != nil {
		t.Fatal(err)
	}
	for _, a := range f.appels {
		if strings.HasPrefix(a, "iptables-legacy") {
			t.Fatalf("le premier pare-feu qui porte la chaîne suffit : %v", f.appels)
		}
	}
}

func TestSansChaine(t *testing.T) {
	f := &faux{chaines: map[string]bool{}, regles: map[string]bool{}}
	if _, err := Assurer(f.executer, "10.8.0.1", "wg0"); err == nil || !strings.Contains(err.Error(), "DOCKER-USER") {
		t.Fatalf("sans chaîne DOCKER-USER, waggle le dit : %v", err)
	}
}
