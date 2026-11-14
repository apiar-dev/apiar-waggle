package ca

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"
)

func parse(t *testing.T, p []byte) *x509.Certificate {
	t.Helper()
	b, _ := pem.Decode(p)
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAuthorityAndCertificate(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	certPEM, keyPEM, err := Create("waggle test", now)
	if err != nil {
		t.Fatal(err)
	}
	root := parse(t, certPEM)
	// Android only installs as an authority a certificate that says it is one
	if root.Version != 3 || !root.IsCA || !root.BasicConstraintsValid || root.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Fatalf("the authority must be X.509 v3, CA:TRUE, keyCertSign: %+v", root)
	}
	a, err := Load(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}

	leafPEM, _, err := a.Issue([]string{"*.dev.example.com", "dev.example.com"}, now)
	if err != nil {
		t.Fatal(err)
	}
	leaf := parse(t, leafPEM)
	roots := x509.NewCertPool()
	roots.AddCert(root)
	for _, name := range []string{"go-1234.dev.example.com", "dev.example.com"} {
		if _, err := leaf.Verify(x509.VerifyOptions{DNSName: name, Roots: roots, CurrentTime: now}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "api.go-1234.dev.example.com", Roots: roots, CurrentTime: now}); err == nil {
		t.Error("a wildcard covers only one label")
	}
	// Apple: at most 825 days, even for a private authority; a SAN; serverAuth
	if d := leaf.NotAfter.Sub(leaf.NotBefore); d > 398*24*time.Hour {
		t.Errorf("the certificate lasts %v", d)
	}
	if len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth || leaf.IsCA {
		t.Errorf("a server certificate, nothing else: %+v", leaf)
	}

	if !a.Valid(leafPEM, []string{"dev.example.com", "*.dev.example.com"}, now) {
		t.Error("the certificate is valid, names in another order")
	}
	if a.Valid(leafPEM, []string{"dev.example.com"}, now) {
		t.Error("other names: reissue")
	}
	if a.Valid(leafPEM, []string{"*.dev.example.com", "dev.example.com"}, now.Add(CertificateLifetime-RenewBefore+time.Hour)) {
		t.Error("less than 30 days before expiry: reissue")
	}
	otherCert, otherKey, _ := Create("other", now)
	other, _ := Load(otherCert, otherKey)
	if other.Valid(leafPEM, []string{"*.dev.example.com", "dev.example.com"}, now) {
		t.Error("signed by another authority: reissue")
	}
	if _, err := Load(certPEM, otherKey); err == nil {
		t.Error("a key that does not match the certificate is refused")
	}
}
