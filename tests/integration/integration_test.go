//go:build integration

// La suite d'intégration : de vraies instances de waggle, levées par bin/waggle, et des stacks jetables derrière elles. Un
// routage ne se lit pas, il s'exécute.
//
// Deux instances sur cette machine, sur des ports décalés pour tourner à côté de ce qui écoute déjà :
//   - « poste » (+20000) termine le TLS de ses stacks, sert le HTTP sur p80 et le passage, et relaie au « serveur » ce qu'il
//     ne sert pas, par host.docker.internal, comme vers une adresse WireGuard ;
//   - « serveur » (+30000) a un point d'entrée relais, et un DNS du VPN.
//
// Tout est retiré à la fin : les stacks, les instances, leurs volumes, leur autorité, le dossier .essais/<id>.
package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/http2"

	"github.com/apiar-dev/apiar-waggle/internal/autorite"
)

const (
	decPoste   = 20000
	decServeur = 30000
)

var (
	hote    = valeur("WAGGLE_ESSAI_HOTE", "host.docker.internal")
	racine  string
	id      string
	dossier string
	// Les autorités : celle de chaque instance, et celle d'un projet en passage
	caPoste, caServeur, caProjet *x509.CertPool
	caProjetNom                  string
)

func valeur(cle, defaut string) string {
	if v := os.Getenv(cle); v != "" {
		return v
	}
	return defaut
}

func pile(lettre string) string { return id + "-" + lettre }

func TestMain(m *testing.M) {
	wd, _ := os.Getwd()
	racine = filepath.Dir(filepath.Dir(wd))
	octets := make([]byte, 3)
	rand.Read(octets)
	id = "we" + hex.EncodeToString(octets)
	dossier = filepath.Join(racine, ".essais", id)
	code := 1
	if err := monter(); err != nil {
		log.Printf("la mise en place a échoué : %v", err)
	} else {
		code = m.Run()
	}
	if code != 0 {
		for _, inst := range []string{"poste", "serveur"} {
			sortie, _ := exec.Command("docker", "logs", "waggle-"+id+"-"+inst+"-controleur-1").CombinedOutput()
			log.Printf("== journal du contrôleur %s\n%s", inst, sortie)
		}
	}
	if os.Getenv("WAGGLE_ESSAI_GARDER") != "" {
		log.Printf("WAGGLE_ESSAI_GARDER : tout reste levé, dans %s", dossier)
		os.Exit(code)
	}
	demonter()
	os.Exit(code)
}

func lancer(env []string, nom string, args ...string) (string, error) {
	cmd := exec.Command(nom, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Dir = racine
	sortie, err := cmd.CombinedOutput()
	if err != nil {
		return string(sortie), fmt.Errorf("%s %s : %v\n%s", nom, strings.Join(args, " "), err, sortie)
	}
	return string(sortie), nil
}

func waggle(args ...string) (string, error) {
	return lancer([]string{
		"WAGGLE_REGLAGES=" + filepath.Join(dossier, "reglages.yaml"),
		"WAGGLE_ETAT=" + filepath.Join(dossier, "etat"),
	}, filepath.Join(racine, "bin", "waggle"), args...)
}

func monter() error {
	if err := os.MkdirAll(filepath.Join(dossier, "projet"), 0o755); err != nil {
		return err
	}
	// L'autorité d'un projet en passage, et les certificats de ses deux stacks
	maintenant := time.Now()
	caNom := "autorite d'un projet, " + id
	certPEM, clePEM, err := autorite.Creer(caNom, maintenant)
	if err != nil {
		return err
	}
	caProjetNom = caNom
	ap, _ := autorite.Charger(certPEM, clePEM)
	caProjet = x509.NewCertPool()
	caProjet.AppendCertsFromPEM(certPEM)
	for _, lettre := range []string{"p-t", "s-q"} {
		nom := pile(lettre) + ".ancien.test"
		c, k, err := ap.Emettre([]string{nom}, maintenant)
		if err != nil {
			return err
		}
		os.WriteFile(filepath.Join(dossier, "projet", nom+".crt"), c, 0o644)
		os.WriteFile(filepath.Join(dossier, "projet", nom+".key"), k, 0o644)
	}

	reglages := fmt.Sprintf(`instances:
  %[1]s-poste:
    adresse: 127.0.0.1
    decalage_ports: %[2]d
    ports: [3000]
    projets: [%[1]s-p]
    relais:
      - vers: host.docker.internal
        decalage_ports: %[3]d
        domaines: [https://essai.test, tls://ancien.test, http://localhost]
  %[1]s-serveur:
    adresse: 127.0.0.1
    decalage_ports: %[3]d
    ports: [3000]
    projets: [%[1]s-s]
    dns: [essai.test]
    relais_entrant: {port: 8080, confiance: [0.0.0.0/0]}
`, id, decPoste, decServeur)
	if err := os.WriteFile(filepath.Join(dossier, "reglages.yaml"), []byte(reglages), 0o644); err != nil {
		return err
	}
	if sortie, err := waggle("installer"); err != nil {
		return err
	} else {
		log.Print(sortie)
	}
	for inst, pool := range map[string]**x509.CertPool{"poste": &caPoste, "serveur": &caServeur} {
		pem, err := os.ReadFile(filepath.Join(dossier, "etat", id+"-"+inst, "autorite", "autorite.crt"))
		if err != nil {
			return err
		}
		*pool = x509.NewCertPool()
		(*pool).AppendCertsFromPEM(pem)
	}

	piles := map[string]map[string]string{
		"p-a": {
			"front": "https://{dossier}.essai.test/ 8080\nhttps://{dossier}.essai.test:3000/ 8081\nhttps://api.{dossier}.essai.test/ 8080\nhttp://admin.{dossier}.localhost/ 8080",
			"api":   "https://{dossier}.essai.test/api 8080",
		},
		"p-b": {"front": "https://{dossier}.essai.test/ 8080\nhttps://pirate.essai.test/ 8080"},
		"p-t": {"front": "tls://{dossier}.ancien.test 8443"},
		"s-r": {"front": "https://{dossier}.essai.test/ 8080\nhttps://{dossier}.essai.test:3000/ 8081\nhttp://{dossier}.localhost/ 8080"},
		"s-q": {"front": "tls://{dossier}.ancien.test 8443"},
	}
	for lettre, services := range piles {
		if err := ecrirePile(lettre, services); err != nil {
			return err
		}
		if _, err := lancer(nil, "docker", "compose", "-f", filepath.Join(dossier, pile(lettre), "compose.yaml"), "up", "-d", "--quiet-pull"); err != nil {
			return err
		}
	}
	// Le contrôleur voit les stacks, branche, écrit ; Traefik relit, au plus toutes les deux secondes. Chaque stack répond
	// avant que les essais commencent
	pretes := []struct {
		nom     string
		port    int
		ca      *x509.CertPool
		reponse string
	}{
		{pile("p-a") + ".essai.test", 443 + decPoste, caPoste, pile("p-a") + " front"},
		{pile("p-b") + ".essai.test", 443 + decPoste, caPoste, pile("p-b") + " front"},
		{pile("p-t") + ".ancien.test", 443 + decPoste, caProjet, pile("p-t") + " front"},
		{pile("s-r") + ".essai.test", 443 + decServeur, caServeur, pile("s-r") + " front"},
		{pile("s-q") + ".ancien.test", 443 + decServeur, caProjet, pile("s-q") + " front"},
	}
	return attendre(func() error {
		for _, p := range pretes {
			_, corps, _, err := demander("https", p.nom, p.port, "/", p.ca)
			if err != nil || !strings.Contains(corps, p.reponse) {
				return fmt.Errorf("%s : %q, %v", p.nom, corps, err)
			}
		}
		return nil
	}, 90*time.Second)
}

func ecrirePile(lettre string, services map[string]string) error {
	d := filepath.Join(dossier, pile(lettre))
	os.MkdirAll(d, 0o755)
	projet := id + "-" + lettre[:1]
	var compose strings.Builder
	fmt.Fprintf(&compose, "name: %s\nservices:\n", pile(lettre))
	for service, routes := range services {
		var conf string
		if strings.HasPrefix(routes, "tls://") {
			nom := pile(lettre) + ".ancien.test"
			conf = fmt.Sprintf("server { listen 8443 ssl; ssl_certificate /certs/%[1]s.crt; ssl_certificate_key /certs/%[1]s.key; default_type text/plain; return 200 '%[2]s %[3]s passage $ssl_server_name'; }\n", nom, pile(lettre), service)
		} else {
			for _, port := range []int{8080, 8081} {
				conf += fmt.Sprintf("server { listen %d; default_type text/plain; return 200 '%s %s %d host=$host proto=$http_x_forwarded_proto'; }\n", port, pile(lettre), service, port)
			}
		}
		os.WriteFile(filepath.Join(d, service+".conf"), []byte(conf), 0o644)
		fmt.Fprintf(&compose, "  %s:\n    image: nginx:1.27-alpine\n    volumes: [\"./%s.conf:/etc/nginx/conf.d/default.conf:ro\", \"../projet:/certs:ro\"]\n", service, service)
		fmt.Fprintf(&compose, "    labels:\n      waggle.projet: %s\n      waggle.routes: |\n", projet)
		for _, ligne := range strings.Split(routes, "\n") {
			fmt.Fprintf(&compose, "        %s\n", ligne)
		}
	}
	return os.WriteFile(filepath.Join(d, "compose.yaml"), []byte(compose.String()), 0o644)
}

func demonter() {
	entrees, _ := os.ReadDir(dossier)
	for _, e := range entrees {
		if compose := filepath.Join(dossier, e.Name(), "compose.yaml"); e.IsDir() && fichierExiste(compose) {
			lancer(nil, "docker", "compose", "-f", compose, "down", "--volumes", "--remove-orphans")
		}
	}
	if fichierExiste(filepath.Join(dossier, "reglages.yaml")) {
		if sortie, err := waggle("desinstaller", "--tout"); err != nil {
			log.Printf("désinstallation : %v %s", err, sortie)
		}
	}
	os.RemoveAll(dossier)
}

func fichierExiste(chemin string) bool {
	_, err := os.Stat(chemin)
	return err == nil
}

func attendre(essai func() error, delai time.Duration) error {
	fin := time.Now().Add(delai)
	var err error
	for time.Now().Before(fin) {
		if err = essai(); err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("délai dépassé : %v", err)
}

// demander : une connexion neuve, comme la première d'un navigateur vers ce nom, et une requête.
func demander(schema, nom string, port int, chemin string, racines *x509.CertPool) (int, string, *x509.Certificate, error) {
	t := &http.Transport{
		DisableKeepAlives: true,
		ForceAttemptHTTP2: true,
		TLSClientConfig:   &tls.Config{RootCAs: racines, ServerName: nom},
		// Le nom ne se résout pas : tout va à l'adresse où écoute l'instance, comme le DNS d'un poste l'y enverrait
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(hote, strconv.Itoa(port)))
		},
	}
	client := &http.Client{Transport: t, Timeout: 5 * time.Second}
	rep, err := client.Get(fmt.Sprintf("%s://%s:%d%s", schema, nom, port, chemin))
	if err != nil {
		return 0, "", nil, err
	}
	defer rep.Body.Close()
	corps, _ := io.ReadAll(rep.Body)
	var cert *x509.Certificate
	if rep.TLS != nil && len(rep.TLS.PeerCertificates) > 0 {
		cert = rep.TLS.PeerCertificates[0]
	}
	return rep.StatusCode, strings.TrimSpace(string(corps)), cert, nil
}

// connexionH2 : une seule connexion HTTP/2, ouverte pour un nom, qui portera des requêtes pour d'autres noms — ce que fait un
// navigateur quand le certificat couvre le nouveau nom et que l'adresse est la même.
func connexionH2(t *testing.T, sni string, port int, racines *x509.CertPool) (*http2.ClientConn, *x509.Certificate) {
	t.Helper()
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", net.JoinHostPort(hote, strconv.Itoa(port)),
		&tls.Config{ServerName: sni, RootCAs: racines, NextProtos: []string{"h2"}})
	if err != nil {
		t.Fatal(err)
	}
	if conn.ConnectionState().NegotiatedProtocol != "h2" {
		t.Fatalf("pas de HTTP/2 : %q", conn.ConnectionState().NegotiatedProtocol)
	}
	cc, err := (&http2.Transport{}).NewClientConn(conn)
	if err != nil {
		t.Fatal(err)
	}
	return cc, conn.ConnectionState().PeerCertificates[0]
}

func surLaConnexion(t *testing.T, cc *http2.ClientConn, nom string, port int, chemin string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("https://%s:%d%s", nom, port, chemin), nil)
	rep, err := cc.RoundTrip(req)
	if err != nil {
		t.Fatalf("%s sur la connexion : %v", nom, err)
	}
	defer rep.Body.Close()
	corps, _ := io.ReadAll(rep.Body)
	return rep.StatusCode, strings.TrimSpace(string(corps))
}

func attendu(t *testing.T, quoi string, statut int, corps string, err error, morceau string) {
	t.Helper()
	if err != nil || statut != 200 || !strings.Contains(corps, morceau) {
		t.Errorf("%s : %d %q %v ; attendu %q", quoi, statut, corps, err, morceau)
	}
}

func TestHTTPSTermineParWaggle(t *testing.T) {
	a := pile("p-a") + ".essai.test"
	statut, corps, cert, err := demander("https", a, 443+decPoste, "/", caPoste)
	attendu(t, "https, une connexion neuve", statut, corps, err, pile("p-a")+" front 8080")
	if !strings.Contains(corps, "proto=https") {
		t.Errorf("l'application reçoit X-Forwarded-Proto : %q", corps)
	}
	if cert == nil || !strings.HasPrefix(cert.Issuer.CommonName, "waggle "+id+"-poste") {
		t.Fatalf("le certificat est signé par l'autorité de l'instance : %v", cert)
	}
	if d := cert.NotAfter.Sub(cert.NotBefore); d > 398*24*time.Hour {
		t.Errorf("un certificat de %v", d)
	}
	statut, corps, _, err = demander("https", a, 443+decPoste, "/api/x", caPoste)
	attendu(t, "un chemin, vers un autre service", statut, corps, err, pile("p-a")+" api")
	statut, corps, _, err = demander("https", a, 443+decPoste, "/apiv2", caPoste)
	attendu(t, "/apiv2 n'est pas sous /api", statut, corps, err, pile("p-a")+" front")
	statut, corps, _, err = demander("https", a, 3000+decPoste, "/", caPoste)
	attendu(t, "un port historique, terminé par waggle", statut, corps, err, pile("p-a")+" front 8081")
	statut, corps, cert, err = demander("https", "api."+a, 443+decPoste, "/", caPoste)
	attendu(t, "un nom à deux labels", statut, corps, err, pile("p-a")+" front 8080")
	if cert != nil && cert.VerifyHostname("api."+a) != nil {
		t.Error("le certificat de la stack couvre ses noms à deux labels")
	}
}

func TestUneConnexionHTTP2PourPlusieursStacks(t *testing.T) {
	// b n'a que des noms à un label : il est servi sous le joker du domaine, qui couvre aussi a et r. La stack a, elle, a
	// son propre certificat (elle a un nom à deux labels), qui ne couvre qu'elle : un navigateur n'y mettrait pas b
	a, b, r := pile("p-a")+".essai.test", pile("p-b")+".essai.test", pile("s-r")+".essai.test"
	cc, cert := connexionH2(t, b, 443+decPoste, caPoste)
	if cert.VerifyHostname(a) != nil || cert.VerifyHostname(r) != nil {
		t.Fatalf("le joker du domaine couvre les autres stacks, et un navigateur réutiliserait la connexion : %v", cert.DNSNames)
	}
	for _, cas := range []struct{ nom, chemin, morceau string }{
		{b, "/", pile("p-b") + " front"},
		{a, "/", pile("p-a") + " front"},
		{a, "/api", pile("p-a") + " api"},
		{r, "/", pile("s-r") + " front 8080"}, // un nom du serveur, relayé sur la même connexion
		{b, "/", pile("p-b") + " front"},
	} {
		statut, corps := surLaConnexion(t, cc, cas.nom, 443+decPoste, cas.chemin)
		attendu(t, "sur la connexion de b, "+cas.nom+cas.chemin, statut, corps, nil, cas.morceau)
	}
	if _, certA := connexionH2(t, a, 443+decPoste, caPoste); certA.VerifyHostname(b) == nil {
		t.Error("le certificat propre à la stack a ne couvre qu'elle")
	}
}

func TestHTTPSurP80(t *testing.T) {
	statut, corps, _, err := demander("http", "admin."+pile("p-a")+".localhost", 80+decPoste, "/", nil)
	attendu(t, "http sur p80", statut, corps, err, pile("p-a")+" front 8080")
}

func TestPassage(t *testing.T) {
	statut, corps, cert, err := demander("https", pile("p-t")+".ancien.test", 443+decPoste, "/", caProjet)
	attendu(t, "en passage, local", statut, corps, err, pile("p-t")+" front passage")
	if cert == nil || cert.Issuer.CommonName != caProjetNom {
		t.Errorf("en passage, la stack présente le certificat de son projet : %v", cert)
	}
}

func TestRelais(t *testing.T) {
	r := pile("s-r") + ".essai.test"
	statut, corps, cert, err := demander("https", r, 443+decPoste, "/", caPoste)
	attendu(t, "relais https, une connexion neuve", statut, corps, err, pile("s-r")+" front 8080")
	if cert == nil || !strings.HasPrefix(cert.Issuer.CommonName, "waggle "+id+"-poste") {
		t.Errorf("le poste termine aussi les noms du serveur, avec sa propre autorité : %v", cert)
	}
	if !strings.Contains(corps, "proto=https") {
		t.Errorf("la stack du serveur reçoit X-Forwarded-Proto du poste : %q", corps)
	}
	statut, corps, _, err = demander("https", r, 3000+decPoste, "/", caPoste)
	attendu(t, "relais https, d'un port historique", statut, corps, err, pile("s-r")+" front 8081")
	statut, corps, cert, err = demander("https", pile("s-q")+".ancien.test", 443+decPoste, "/", caProjet)
	attendu(t, "relais en passage", statut, corps, err, pile("s-q")+" front passage")
	if cert == nil || cert.Issuer.CommonName != caProjetNom {
		t.Errorf("en passage, le certificat vient de la stack du serveur : %v", cert)
	}
	statut, corps, _, err = demander("http", pile("s-r")+".localhost", 80+decPoste, "/", nil)
	attendu(t, "relais http", statut, corps, err, pile("s-r")+" front 8080")
	statut, _, _, err = demander("https", "personne.essai.test", 443+decPoste, "/", caPoste)
	if err != nil || statut != 404 {
		t.Errorf("un nom inconnu partout : 404 du serveur ; obtenu %d %v", statut, err)
	}
}

func TestUnTelephoneSurLeServeur(t *testing.T) {
	statut, corps, cert, err := demander("https", pile("s-r")+".essai.test", 443+decServeur, "/", caServeur)
	attendu(t, "directement sur le serveur", statut, corps, err, pile("s-r")+" front 8080")
	if cert == nil || !strings.HasPrefix(cert.Issuer.CommonName, "waggle "+id+"-serveur") {
		t.Errorf("le serveur présente un certificat de sa propre autorité : %v", cert)
	}
	// Le point d'entrée relais ne sert une route qu'au port d'entrée que le poste nomme
	statut, _, _, _ = demander("http", pile("s-r")+".essai.test", 8080+decServeur, "/", nil)
	if statut != 404 {
		t.Errorf("sans X-Waggle-Port, le point d'entrée relais ne sert rien : %d", statut)
	}
}

func TestLeDNSDuVPN(t *testing.T) {
	for _, proto := range []string{"udp", "tcp"} {
		rcode, ips, err := interroger(proto, pile("s-r")+".essai.test", 1)
		if err != nil || rcode != 0 || len(ips) != 1 || ips[0] != "127.0.0.1" {
			t.Errorf("%s, A : rcode %d, %v, %v ; attendu l'adresse de l'instance", proto, rcode, ips, err)
		}
		rcode, ips, err = interroger(proto, pile("s-r")+".essai.test", 28)
		if err != nil || rcode != 0 || len(ips) != 0 {
			t.Errorf("%s, AAAA : rcode %d, %v, %v ; attendu NOERROR et rien", proto, rcode, ips, err)
		}
	}
	rcode, ips, err := interroger("udp", "example.com", 1)
	if err != nil || rcode != 0 || len(ips) == 0 {
		t.Errorf("le reste est relayé : rcode %d, %v, %v", rcode, ips, err)
	}
}

func TestRefusEtRetrait(t *testing.T) {
	sortie, _ := exec.Command("docker", "logs", "waggle-"+id+"-poste-controleur-1").CombinedOutput()
	if !bytes.Contains(sortie, []byte("ne porte pas {dossier}")) {
		t.Errorf("la route pirate est refusée, et dite :\n%s", sortie)
	}
	statut, _, _, _ := demander("https", "pirate.essai.test", 443+decPoste, "/", caPoste)
	if statut == 200 {
		t.Error("la route pirate ne sert rien")
	}
	b := pile("p-b")
	if _, err := lancer(nil, "docker", "compose", "-f", filepath.Join(dossier, b, "compose.yaml"), "down"); err != nil {
		t.Fatal(err)
	}
	err := attendre(func() error {
		statut, _, _, _ := demander("https", b+".essai.test", 443+decPoste, "/", caPoste)
		if statut != 404 {
			return fmt.Errorf("%d", statut)
		}
		return nil
	}, 20*time.Second)
	if err != nil {
		t.Errorf("une stack retirée perd sa route, et son nom part au relais : %v", err)
	}
}

func TestRelancer(t *testing.T) {
	avant, _ := lancer(nil, "docker", "ps", "-q", "--filter", "label=waggle.instance="+id+"-poste")
	if _, err := waggle("installer"); err != nil {
		t.Fatal(err)
	}
	apres, _ := lancer(nil, "docker", "ps", "-q", "--filter", "label=waggle.instance="+id+"-poste")
	if avant != apres {
		t.Errorf("relancer l'installation ne recrée rien :\n%s\n%s", avant, apres)
	}
	if _, err := lancer(nil, "docker", "restart", "waggle-"+id+"-poste-controleur-1"); err != nil {
		t.Fatal(err)
	}
	statut, corps, _, err := demander("https", pile("p-a")+".essai.test", 443+decPoste, "/", caPoste)
	attendu(t, "le contrôleur redémarré, les routes tiennent", statut, corps, err, pile("p-a")+" front")
}

// interroger pose une question DNS au DNS du serveur, et rend le code de réponse et les adresses.
func interroger(proto, nom string, qtype uint16) (int, []string, error) {
	var q bytes.Buffer
	binary.Write(&q, binary.BigEndian, []uint16{0x1234, 0x0100, 1, 0, 0, 0})
	for _, l := range strings.Split(nom, ".") {
		q.WriteByte(byte(len(l)))
		q.WriteString(l)
	}
	q.WriteByte(0)
	binary.Write(&q, binary.BigEndian, []uint16{qtype, 1})
	conn, err := net.DialTimeout(proto, net.JoinHostPort(hote, strconv.Itoa(53+decServeur)), 3*time.Second)
	if err != nil {
		return 0, nil, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	message := q.Bytes()
	if proto == "tcp" {
		message = append(binary.BigEndian.AppendUint16(nil, uint16(len(message))), message...)
	}
	if _, err := conn.Write(message); err != nil {
		return 0, nil, err
	}
	rep := make([]byte, 4096)
	n, err := io.ReadAtLeast(conn, rep, 12)
	if err != nil {
		return 0, nil, err
	}
	rep = rep[:n]
	if proto == "tcp" {
		rep = rep[2:]
	}
	rcode := int(rep[3] & 0x0f)
	qd, an := binary.BigEndian.Uint16(rep[4:]), binary.BigEndian.Uint16(rep[6:])
	i := 12
	for ; qd > 0; qd-- {
		i = sauterNom(rep, i) + 4
	}
	var ips []string
	for ; an > 0; an-- {
		i = sauterNom(rep, i)
		typ, longueur := binary.BigEndian.Uint16(rep[i:]), int(binary.BigEndian.Uint16(rep[i+8:]))
		i += 10
		if typ == 1 && longueur == 4 {
			ips = append(ips, net.IP(rep[i:i+4]).String())
		}
		if typ == 28 && longueur == 16 {
			ips = append(ips, net.IP(rep[i:i+16]).String())
		}
		i += longueur
	}
	return rcode, ips, nil
}

func sauterNom(m []byte, i int) int {
	for i < len(m) {
		switch l := int(m[i]); {
		case l == 0:
			return i + 1
		case l&0xc0 == 0xc0:
			return i + 2
		default:
			i += l + 1
		}
	}
	return i
}
