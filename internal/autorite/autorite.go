// Package autorite tient l'autorité de certification d'une instance, et les certificats qu'elle signe pour les domaines que
// waggle termine (ADR-0002, décision 2). Les certificats tiennent les exigences d'Apple, qui valent même pour une autorité
// privée — 825 jours au plus, un SAN, l'usage serverAuth —, et celles d'Android, qui n'installe comme autorité qu'un
// certificat qui se dit tel (CA:TRUE).
package autorite

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"time"
)

const (
	// DureeAutorite : la durée de l'autorité. Un appareil l'approuve une fois.
	DureeAutorite = 10 * 365 * 24 * time.Hour
	// DureeCertificat : la durée d'un certificat, sous les 398 jours des navigateurs et les 825 jours d'Apple.
	DureeCertificat = 397 * 24 * time.Hour
	// Marge : un certificat se renouvelle à 30 jours de son terme.
	Marge = 30 * 24 * time.Hour
)

// Autorite : le certificat et la clé d'une autorité.
type Autorite struct {
	Certificat *x509.Certificate
	cle        *ecdsa.PrivateKey
}

// Creer crée une autorité : ECDSA P-256, X.509 v3, CA:TRUE, sans autorité intermédiaire sous elle.
func Creer(nom string, maintenant time.Time) (certPEM, clePEM []byte, err error) {
	cle, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	gabarit := &x509.Certificate{
		SerialNumber:          numero(),
		Subject:               pkix.Name{CommonName: nom, Organization: []string{"waggle"}},
		NotBefore:             maintenant.Add(-time.Hour),
		NotAfter:              maintenant.Add(DureeAutorite),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, gabarit, gabarit, &cle.PublicKey, cle)
	if err != nil {
		return nil, nil, err
	}
	return encoder(der, cle)
}

// Charger relit une autorité.
func Charger(certPEM, clePEM []byte) (*Autorite, error) {
	certificat, err := lireCertificat(certPEM)
	if err != nil {
		return nil, fmt.Errorf("le certificat de l'autorité : %w", err)
	}
	if !certificat.IsCA {
		return nil, errors.New("le certificat de l'autorité ne se dit pas autorité (CA:TRUE)")
	}
	bloc, _ := pem.Decode(clePEM)
	if bloc == nil {
		return nil, errors.New("la clé de l'autorité n'est pas en PEM")
	}
	brute, err := x509.ParsePKCS8PrivateKey(bloc.Bytes)
	if err != nil {
		return nil, fmt.Errorf("la clé de l'autorité : %w", err)
	}
	cle, ok := brute.(*ecdsa.PrivateKey)
	if !ok || !cle.PublicKey.Equal(certificat.PublicKey) {
		return nil, errors.New("la clé ne va pas avec le certificat de l'autorité")
	}
	return &Autorite{Certificat: certificat, cle: cle}, nil
}

// Emettre signe un certificat pour ces noms, le premier faisant aussi le sujet.
func (a *Autorite) Emettre(noms []string, maintenant time.Time) (certPEM, clePEM []byte, err error) {
	if len(noms) == 0 {
		return nil, nil, errors.New("un certificat sans nom")
	}
	cle, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	fin := maintenant.Add(DureeCertificat)
	if fin.After(a.Certificat.NotAfter) {
		fin = a.Certificat.NotAfter
	}
	gabarit := &x509.Certificate{
		SerialNumber:          numero(),
		Subject:               pkix.Name{CommonName: noms[0]},
		DNSNames:              noms,
		NotBefore:             maintenant.Add(-time.Hour),
		NotAfter:              fin,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, gabarit, a.Certificat, &cle.PublicKey, a.cle)
	if err != nil {
		return nil, nil, err
	}
	return encoder(der, cle)
}

// Vaut dit si un certificat sert encore : signé par cette autorité, pour exactement ces noms, et loin de son terme.
func (a *Autorite) Vaut(certPEM []byte, noms []string, maintenant time.Time) bool {
	c, err := lireCertificat(certPEM)
	if err != nil {
		return false
	}
	if !bytes.Equal(c.RawIssuer, a.Certificat.RawSubject) || c.CheckSignatureFrom(a.Certificat) != nil {
		return false
	}
	if maintenant.Add(Marge).After(c.NotAfter) {
		return false
	}
	a1, a2 := slices.Clone(c.DNSNames), slices.Clone(noms)
	slices.Sort(a1)
	slices.Sort(a2)
	return slices.Equal(a1, a2)
}

// PEM : le certificat de l'autorité, à installer sur les appareils.
func (a *Autorite) PEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: a.Certificat.Raw})
}

func lireCertificat(certPEM []byte) (*x509.Certificate, error) {
	bloc, _ := pem.Decode(certPEM)
	if bloc == nil || bloc.Type != "CERTIFICATE" {
		return nil, errors.New("pas de certificat en PEM")
	}
	return x509.ParseCertificate(bloc.Bytes)
}

func encoder(der []byte, cle *ecdsa.PrivateKey) ([]byte, []byte, error) {
	brute, err := x509.MarshalPKCS8PrivateKey(cle)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: brute}), nil
}

func numero() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		panic(err)
	}
	return n.Add(n, big.NewInt(1))
}
