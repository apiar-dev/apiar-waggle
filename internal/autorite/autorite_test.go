package autorite

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"
)

func lire(t *testing.T, p []byte) *x509.Certificate {
	t.Helper()
	b, _ := pem.Decode(p)
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAutoriteEtCertificat(t *testing.T) {
	maintenant := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	certPEM, clePEM, err := Creer("waggle essai", maintenant)
	if err != nil {
		t.Fatal(err)
	}
	ca := lire(t, certPEM)
	// Android n'installe comme autorité qu'un certificat qui se dit tel
	if ca.Version != 3 || !ca.IsCA || !ca.BasicConstraintsValid || ca.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Fatalf("l'autorité doit être en X.509 v3, CA:TRUE, keyCertSign : %+v", ca)
	}
	a, err := Charger(certPEM, clePEM)
	if err != nil {
		t.Fatal(err)
	}

	feuillePEM, _, err := a.Emettre([]string{"*.dev.exemple.fr", "dev.exemple.fr"}, maintenant)
	if err != nil {
		t.Fatal(err)
	}
	f := lire(t, feuillePEM)
	racines := x509.NewCertPool()
	racines.AddCert(ca)
	for _, nom := range []string{"go-1234.dev.exemple.fr", "dev.exemple.fr"} {
		if _, err := f.Verify(x509.VerifyOptions{DNSName: nom, Roots: racines, CurrentTime: maintenant}); err != nil {
			t.Errorf("%s : %v", nom, err)
		}
	}
	if _, err := f.Verify(x509.VerifyOptions{DNSName: "api.go-1234.dev.exemple.fr", Roots: racines, CurrentTime: maintenant}); err == nil {
		t.Error("un joker ne couvre qu'un label")
	}
	// Apple : 825 jours au plus, même pour une autorité privée ; un SAN ; serverAuth
	if d := f.NotAfter.Sub(f.NotBefore); d > 398*24*time.Hour {
		t.Errorf("le certificat dure %v", d)
	}
	if len(f.ExtKeyUsage) != 1 || f.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth || f.IsCA {
		t.Errorf("un certificat de serveur, et rien d'autre : %+v", f)
	}

	if !a.Vaut(feuillePEM, []string{"dev.exemple.fr", "*.dev.exemple.fr"}, maintenant) {
		t.Error("le certificat vaut, les noms dans un autre ordre")
	}
	if a.Vaut(feuillePEM, []string{"dev.exemple.fr"}, maintenant) {
		t.Error("d'autres noms : à refaire")
	}
	if a.Vaut(feuillePEM, []string{"*.dev.exemple.fr", "dev.exemple.fr"}, maintenant.Add(DureeCertificat-Marge+time.Hour)) {
		t.Error("à moins de 30 jours de son terme : à refaire")
	}
	autreCert, autreCle, _ := Creer("autre", maintenant)
	autre, _ := Charger(autreCert, autreCle)
	if autre.Vaut(feuillePEM, []string{"*.dev.exemple.fr", "dev.exemple.fr"}, maintenant) {
		t.Error("signé par une autre autorité : à refaire")
	}
	if _, err := Charger(certPEM, autreCle); err == nil {
		t.Error("une clé qui ne va pas avec le certificat est refusée")
	}
}
