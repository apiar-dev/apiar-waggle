// Package ca holds an instance's certificate authority, and the certificates it signs for the domains waggle terminates
// (ADR-0002, decision 2). Certificates meet Apple's requirements, which apply even to a private authority — at most 825
// days, a SAN, the serverAuth usage — and Android's, which only installs as an authority a certificate that says it is
// one (CA:TRUE).
package ca

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
	// AuthorityLifetime is how long the authority lasts. A device trusts it once.
	AuthorityLifetime = 10 * 365 * 24 * time.Hour
	// CertificateLifetime stays under browsers' 398 days and Apple's 825 days.
	CertificateLifetime = 397 * 24 * time.Hour
	// RenewBefore: a certificate is renewed 30 days before it expires.
	RenewBefore = 30 * 24 * time.Hour
)

// Authority is an authority's certificate and key.
type Authority struct {
	Certificate *x509.Certificate
	key         *ecdsa.PrivateKey
}

// Create creates an authority: ECDSA P-256, X.509 v3, CA:TRUE, with no intermediate below it.
func Create(name string, now time.Time) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: name, Organization: []string{"waggle"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(AuthorityLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	return encode(der, key)
}

// Load reads back an authority.
func Load(certPEM, keyPEM []byte) (*Authority, error) {
	cert, err := parseCertificate(certPEM)
	if err != nil {
		return nil, fmt.Errorf("authority certificate: %w", err)
	}
	if !cert.IsCA {
		return nil, errors.New("the authority certificate does not say it is an authority (CA:TRUE)")
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("the authority key is not PEM")
	}
	raw, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("authority key: %w", err)
	}
	key, ok := raw.(*ecdsa.PrivateKey)
	if !ok || !key.PublicKey.Equal(cert.PublicKey) {
		return nil, errors.New("the key does not match the authority certificate")
	}
	return &Authority{Certificate: cert, key: key}, nil
}

// Issue signs a certificate for these names; the first one is also the subject.
func (a *Authority) Issue(names []string, now time.Time) (certPEM, keyPEM []byte, err error) {
	if len(names) == 0 {
		return nil, nil, errors.New("a certificate with no name")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	notAfter := now.Add(CertificateLifetime)
	if notAfter.After(a.Certificate.NotAfter) {
		notAfter = a.Certificate.NotAfter
	}
	template := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: names[0]},
		DNSNames:              names,
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.Certificate, &key.PublicKey, a.key)
	if err != nil {
		return nil, nil, err
	}
	return encode(der, key)
}

// Valid tells whether a certificate still serves: signed by this authority, for exactly these names, and far from its
// expiry.
func (a *Authority) Valid(certPEM []byte, names []string, now time.Time) bool {
	c, err := parseCertificate(certPEM)
	if err != nil {
		return false
	}
	if !bytes.Equal(c.RawIssuer, a.Certificate.RawSubject) || c.CheckSignatureFrom(a.Certificate) != nil {
		return false
	}
	if now.Add(RenewBefore).After(c.NotAfter) {
		return false
	}
	have, want := slices.Clone(c.DNSNames), slices.Clone(names)
	slices.Sort(have)
	slices.Sort(want)
	return slices.Equal(have, want)
}

// PEM is the authority certificate, to install on devices.
func (a *Authority) PEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: a.Certificate.Raw})
}

func parseCertificate(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("no PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

func encode(der []byte, key *ecdsa.PrivateKey) ([]byte, []byte, error) {
	raw, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw}), nil
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		panic(err)
	}
	return n.Add(n, big.NewInt(1))
}
