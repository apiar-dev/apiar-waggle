//go:build integration

// The integration suite: real waggle instances, started by bin/waggle, and throwaway stacks behind them. Routing is not
// read, it is run.
//
// Two instances on this machine, on offset ports so they run next to whatever already listens:
//   - "workstation" (+20000) terminates its stacks' TLS, serves plain HTTP on p80 and passthrough, and relays to the
//     "server" what it does not serve, through host.docker.internal, as it would to a WireGuard address;
//   - "server" (+30000) has a relay entry point, and a VPN DNS.
//
// Everything is removed at the end: stacks, instances, their volumes, their authority, the .tests/<id> directory.
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

	"github.com/apiar-dev/apiar-waggle/internal/ca"
)

const (
	workstationOffset = 20000
	serverOffset      = 30000
)

var (
	host = envOr("WAGGLE_TEST_HOST", "host.docker.internal")
	root string
	id   string
	dir  string
	// The authorities: each instance's, and that of a passthrough project
	workstationCA, serverCA, projectCA *x509.CertPool
	projectCAName                      string
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func stack(letter string) string { return id + "-" + letter }

func TestMain(m *testing.M) {
	wd, _ := os.Getwd()
	root = filepath.Dir(filepath.Dir(wd))
	b := make([]byte, 3)
	rand.Read(b)
	id = "wt" + hex.EncodeToString(b)
	dir = filepath.Join(root, ".tests", id)
	code := 1
	if err := setup(); err != nil {
		log.Printf("setup failed: %v", err)
	} else {
		code = m.Run()
	}
	if code != 0 {
		for _, inst := range []string{"workstation", "server"} {
			out, _ := exec.Command("docker", "logs", "waggle-"+id+"-"+inst+"-controller-1").CombinedOutput()
			log.Printf("== %s controller log\n%s", inst, out)
		}
	}
	if os.Getenv("WAGGLE_TEST_KEEP") != "" {
		log.Printf("WAGGLE_TEST_KEEP: everything stays up, in %s", dir)
		os.Exit(code)
	}
	teardown()
	os.Exit(code)
}

func run(env []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out), nil
}

func waggle(args ...string) (string, error) {
	return run([]string{
		"WAGGLE_CONFIG=" + filepath.Join(dir, "config.yaml"),
		"WAGGLE_STATE=" + filepath.Join(dir, "state"),
	}, filepath.Join(root, "bin", "waggle"), args...)
}

func setup() error {
	if err := os.MkdirAll(filepath.Join(dir, "project"), 0o755); err != nil {
		return err
	}
	// A passthrough project's authority, and the certificates of its two stacks
	now := time.Now()
	projectCAName = "a project's authority, " + id
	certPEM, keyPEM, err := ca.Create(projectCAName, now)
	if err != nil {
		return err
	}
	pa, _ := ca.Load(certPEM, keyPEM)
	projectCA = x509.NewCertPool()
	projectCA.AppendCertsFromPEM(certPEM)
	for _, letter := range []string{"w-t", "s-q"} {
		name := stack(letter) + ".legacy.test"
		c, k, err := pa.Issue([]string{name}, now)
		if err != nil {
			return err
		}
		os.WriteFile(filepath.Join(dir, "project", name+".crt"), c, 0o644)
		os.WriteFile(filepath.Join(dir, "project", name+".key"), k, 0o644)
	}

	cfg := fmt.Sprintf(`instances:
  %[1]s-workstation:
    address: 127.0.0.1
    port_offset: %[2]d
    ports: [3000]
    projects: [%[1]s-w]
    relays:
      - to: host.docker.internal
        port_offset: %[3]d
        domains: [https://example.test, tls://legacy.test, http://localhost]
  %[1]s-server:
    address: 127.0.0.1
    port_offset: %[3]d
    ports: [3000]
    projects: [%[1]s-s]
    dns: [example.test]
    relay_listener: {port: 8080, trusted_ips: [0.0.0.0/0]}
`, id, workstationOffset, serverOffset)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		return err
	}
	out, err := waggle("up")
	if err != nil {
		return err
	}
	log.Print(out)
	for inst, pool := range map[string]**x509.CertPool{"workstation": &workstationCA, "server": &serverCA} {
		pem, err := os.ReadFile(filepath.Join(dir, "state", id+"-"+inst, "ca", "ca.crt"))
		if err != nil {
			return err
		}
		*pool = x509.NewCertPool()
		(*pool).AppendCertsFromPEM(pem)
	}

	stacks := map[string]map[string]string{
		"w-a": {
			"front": "https://{stack}.example.test/ 8080\nhttps://{stack}.example.test:3000/ 8081\nhttps://api.{stack}.example.test/ 8080\nhttp://admin.{stack}.localhost/ 8080",
			"api":   "https://{stack}.example.test/api 8080",
		},
		"w-b": {"front": "https://{stack}.example.test/ 8080\nhttps://intruder.example.test/ 8080"},
		"w-t": {"front": "tls://{stack}.legacy.test 8443"},
		"s-r": {"front": "https://{stack}.example.test/ 8080\nhttps://{stack}.example.test:3000/ 8081\nhttp://{stack}.localhost/ 8080"},
		"s-q": {"front": "tls://{stack}.legacy.test 8443"},
	}
	for letter, services := range stacks {
		if err := writeStack(letter, services); err != nil {
			return err
		}
		if _, err := run(nil, "docker", "compose", "-f", filepath.Join(dir, stack(letter), "compose.yaml"), "up", "-d", "--quiet-pull"); err != nil {
			return err
		}
	}
	// The controller sees the stacks, connects, writes; Traefik reloads, at most every two seconds. Every stack answers
	// before the tests start
	ready := []struct {
		name   string
		port   int
		pool   *x509.CertPool
		answer string
	}{
		{stack("w-a") + ".example.test", 443 + workstationOffset, workstationCA, stack("w-a") + " front"},
		{stack("w-b") + ".example.test", 443 + workstationOffset, workstationCA, stack("w-b") + " front"},
		{stack("w-t") + ".legacy.test", 443 + workstationOffset, projectCA, stack("w-t") + " front"},
		{stack("s-r") + ".example.test", 443 + serverOffset, serverCA, stack("s-r") + " front"},
		{stack("s-q") + ".legacy.test", 443 + serverOffset, projectCA, stack("s-q") + " front"},
	}
	return waitFor(func() error {
		for _, r := range ready {
			_, body, _, err := get("https", r.name, r.port, "/", r.pool)
			if err != nil || !strings.Contains(body, r.answer) {
				return fmt.Errorf("%s: %q, %v", r.name, body, err)
			}
		}
		return nil
	}, 90*time.Second)
}

func writeStack(letter string, services map[string]string) error {
	d := filepath.Join(dir, stack(letter))
	os.MkdirAll(d, 0o755)
	project := id + "-" + letter[:1]
	var compose strings.Builder
	fmt.Fprintf(&compose, "name: %s\nservices:\n", stack(letter))
	for service, rs := range services {
		var conf string
		if strings.HasPrefix(rs, "tls://") {
			name := stack(letter) + ".legacy.test"
			conf = fmt.Sprintf("server { listen 8443 ssl; ssl_certificate /certs/%[1]s.crt; ssl_certificate_key /certs/%[1]s.key; default_type text/plain; return 200 '%[2]s %[3]s passthrough $ssl_server_name'; }\n", name, stack(letter), service)
		} else {
			for _, port := range []int{8080, 8081} {
				conf += fmt.Sprintf("server { listen %d; default_type text/plain; return 200 '%s %s %d host=$host proto=$http_x_forwarded_proto'; }\n", port, stack(letter), service, port)
			}
		}
		os.WriteFile(filepath.Join(d, service+".conf"), []byte(conf), 0o644)
		fmt.Fprintf(&compose, "  %s:\n    image: nginx:1.27-alpine\n    volumes: [\"./%s.conf:/etc/nginx/conf.d/default.conf:ro\", \"../project:/certs:ro\"]\n", service, service)
		fmt.Fprintf(&compose, "    labels:\n      waggle.project: %s\n      waggle.routes: |\n", project)
		for _, line := range strings.Split(rs, "\n") {
			fmt.Fprintf(&compose, "        %s\n", line)
		}
	}
	return os.WriteFile(filepath.Join(d, "compose.yaml"), []byte(compose.String()), 0o644)
}

func teardown() {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if compose := filepath.Join(dir, e.Name(), "compose.yaml"); e.IsDir() && exists(compose) {
			run(nil, "docker", "compose", "-f", compose, "down", "--volumes", "--remove-orphans")
		}
	}
	if exists(filepath.Join(dir, "config.yaml")) {
		if out, err := waggle("uninstall", "--all"); err != nil {
			log.Printf("uninstall: %v %s", err, out)
		}
	}
	os.RemoveAll(dir)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func waitFor(try func() error, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var err error
	for time.Now().Before(deadline) {
		if err = try(); err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out: %v", err)
}

// get opens a fresh connection, like a browser's first one to this name, and sends one request.
func get(scheme, name string, port int, path string, roots *x509.CertPool) (int, string, *x509.Certificate, error) {
	t := &http.Transport{
		DisableKeepAlives: true,
		ForceAttemptHTTP2: true,
		TLSClientConfig:   &tls.Config{RootCAs: roots, ServerName: name},
		// The name does not resolve: everything goes to where the instance listens, as a workstation's DNS would send it
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		},
	}
	client := &http.Client{Transport: t, Timeout: 5 * time.Second}
	resp, err := client.Get(fmt.Sprintf("%s://%s:%d%s", scheme, name, port, path))
	if err != nil {
		return 0, "", nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var cert *x509.Certificate
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		cert = resp.TLS.PeerCertificates[0]
	}
	return resp.StatusCode, strings.TrimSpace(string(body)), cert, nil
}

// h2Conn is a single HTTP/2 connection, opened for one name, that will carry requests for other names — what a browser
// does when the certificate covers the new name and the address is the same.
func h2Conn(t *testing.T, sni string, port int, roots *x509.CertPool) (*http2.ClientConn, *x509.Certificate) {
	t.Helper()
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", net.JoinHostPort(host, strconv.Itoa(port)),
		&tls.Config{ServerName: sni, RootCAs: roots, NextProtos: []string{"h2"}})
	if err != nil {
		t.Fatal(err)
	}
	if conn.ConnectionState().NegotiatedProtocol != "h2" {
		t.Fatalf("no HTTP/2: %q", conn.ConnectionState().NegotiatedProtocol)
	}
	cc, err := (&http2.Transport{}).NewClientConn(conn)
	if err != nil {
		t.Fatal(err)
	}
	return cc, conn.ConnectionState().PeerCertificates[0]
}

func onConn(t *testing.T, cc *http2.ClientConn, name string, port int, path string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("https://%s:%d%s", name, port, path), nil)
	resp, err := cc.RoundTrip(req)
	if err != nil {
		t.Fatalf("%s on the connection: %v", name, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(body))
}

func expect(t *testing.T, what string, status int, body string, err error, part string) {
	t.Helper()
	if err != nil || status != 200 || !strings.Contains(body, part) {
		t.Errorf("%s: %d %q %v; want %q", what, status, body, err, part)
	}
}

func TestHTTPSTerminatedByWaggle(t *testing.T) {
	a := stack("w-a") + ".example.test"
	status, body, cert, err := get("https", a, 443+workstationOffset, "/", workstationCA)
	expect(t, "https, a fresh connection", status, body, err, stack("w-a")+" front 8080")
	if !strings.Contains(body, "proto=https") {
		t.Errorf("the application receives X-Forwarded-Proto: %q", body)
	}
	if cert == nil || !strings.HasPrefix(cert.Issuer.CommonName, "waggle "+id+"-workstation") {
		t.Fatalf("the certificate is signed by the instance's authority: %v", cert)
	}
	if d := cert.NotAfter.Sub(cert.NotBefore); d > 398*24*time.Hour {
		t.Errorf("a certificate of %v", d)
	}
	status, body, _, err = get("https", a, 443+workstationOffset, "/api/x", workstationCA)
	expect(t, "a path, to another service", status, body, err, stack("w-a")+" api")
	status, body, _, err = get("https", a, 443+workstationOffset, "/apiv2", workstationCA)
	expect(t, "/apiv2 is not below /api", status, body, err, stack("w-a")+" front")
	status, body, _, err = get("https", a, 3000+workstationOffset, "/", workstationCA)
	expect(t, "a legacy port, terminated by waggle", status, body, err, stack("w-a")+" front 8081")
	status, body, cert, err = get("https", "api."+a, 443+workstationOffset, "/", workstationCA)
	expect(t, "a two-label name", status, body, err, stack("w-a")+" front 8080")
	if cert != nil && cert.VerifyHostname("api."+a) != nil {
		t.Error("the stack's certificate covers its two-label names")
	}
}

func TestOneHTTP2ConnectionForSeveralStacks(t *testing.T) {
	// b only has one-label names: it is served under the domain's wildcard, which also covers a and r. Stack a has its
	// own certificate (it has a two-label name), which covers only itself: a browser would not put b on it
	a, b, r := stack("w-a")+".example.test", stack("w-b")+".example.test", stack("s-r")+".example.test"
	cc, cert := h2Conn(t, b, 443+workstationOffset, workstationCA)
	if cert.VerifyHostname(a) != nil || cert.VerifyHostname(r) != nil {
		t.Fatalf("the domain's wildcard covers the other stacks, and a browser would reuse the connection: %v", cert.DNSNames)
	}
	for _, c := range []struct{ name, path, part string }{
		{b, "/", stack("w-b") + " front"},
		{a, "/", stack("w-a") + " front"},
		{a, "/api", stack("w-a") + " api"},
		{r, "/", stack("s-r") + " front 8080"}, // a server name, relayed on the same connection
		{b, "/", stack("w-b") + " front"},
	} {
		status, body := onConn(t, cc, c.name, 443+workstationOffset, c.path)
		expect(t, "on b's connection, "+c.name+c.path, status, body, nil, c.part)
	}
	if _, certA := h2Conn(t, a, 443+workstationOffset, workstationCA); certA.VerifyHostname(b) == nil {
		t.Error("stack a's own certificate covers only itself")
	}
}

func TestHTTPOnP80(t *testing.T) {
	status, body, _, err := get("http", "admin."+stack("w-a")+".localhost", 80+workstationOffset, "/", nil)
	expect(t, "http on p80", status, body, err, stack("w-a")+" front 8080")
}

func TestPassthrough(t *testing.T) {
	status, body, cert, err := get("https", stack("w-t")+".legacy.test", 443+workstationOffset, "/", projectCA)
	expect(t, "local passthrough", status, body, err, stack("w-t")+" front passthrough")
	if cert == nil || cert.Issuer.CommonName != projectCAName {
		t.Errorf("in passthrough, the stack presents its project's certificate: %v", cert)
	}
}

func TestRelays(t *testing.T) {
	r := stack("s-r") + ".example.test"
	status, body, cert, err := get("https", r, 443+workstationOffset, "/", workstationCA)
	expect(t, "https relay, a fresh connection", status, body, err, stack("s-r")+" front 8080")
	if cert == nil || !strings.HasPrefix(cert.Issuer.CommonName, "waggle "+id+"-workstation") {
		t.Errorf("the workstation also terminates the server's names, with its own authority: %v", cert)
	}
	if !strings.Contains(body, "proto=https") {
		t.Errorf("the server's stack receives the workstation's X-Forwarded-Proto: %q", body)
	}
	status, body, _, err = get("https", r, 3000+workstationOffset, "/", workstationCA)
	expect(t, "https relay, from a legacy port", status, body, err, stack("s-r")+" front 8081")
	status, body, cert, err = get("https", stack("s-q")+".legacy.test", 443+workstationOffset, "/", projectCA)
	expect(t, "passthrough relay", status, body, err, stack("s-q")+" front passthrough")
	if cert == nil || cert.Issuer.CommonName != projectCAName {
		t.Errorf("in passthrough, the certificate comes from the server's stack: %v", cert)
	}
	status, body, _, err = get("http", stack("s-r")+".localhost", 80+workstationOffset, "/", nil)
	expect(t, "http relay", status, body, err, stack("s-r")+" front 8080")
	status, _, _, err = get("https", "nobody.example.test", 443+workstationOffset, "/", workstationCA)
	if err != nil || status != 404 {
		t.Errorf("a name unknown everywhere: 404 from the server; got %d %v", status, err)
	}
}

func TestAPhoneOnTheServer(t *testing.T) {
	status, body, cert, err := get("https", stack("s-r")+".example.test", 443+serverOffset, "/", serverCA)
	expect(t, "straight to the server", status, body, err, stack("s-r")+" front 8080")
	if cert == nil || !strings.HasPrefix(cert.Issuer.CommonName, "waggle "+id+"-server") {
		t.Errorf("the server presents a certificate from its own authority: %v", cert)
	}
	// The relay entry point only serves a route for the entry port the workstation names
	status, _, _, _ = get("http", stack("s-r")+".example.test", 8080+serverOffset, "/", nil)
	if status != 404 {
		t.Errorf("without X-Waggle-Port, the relay entry point serves nothing: %d", status)
	}
}

func TestVPNDNS(t *testing.T) {
	for _, proto := range []string{"udp", "tcp"} {
		rcode, ips, err := query(proto, stack("s-r")+".example.test", 1)
		if err != nil || rcode != 0 || len(ips) != 1 || ips[0] != "127.0.0.1" {
			t.Errorf("%s, A: rcode %d, %v, %v; want the instance address", proto, rcode, ips, err)
		}
		rcode, ips, err = query(proto, stack("s-r")+".example.test", 28)
		if err != nil || rcode != 0 || len(ips) != 0 {
			t.Errorf("%s, AAAA: rcode %d, %v, %v; want NOERROR and nothing", proto, rcode, ips, err)
		}
	}
	rcode, ips, err := query("udp", "example.com", 1)
	if err != nil || rcode != 0 || len(ips) == 0 {
		t.Errorf("the rest is forwarded: rcode %d, %v, %v", rcode, ips, err)
	}
}

func TestRefusalAndRemoval(t *testing.T) {
	out, _ := exec.Command("docker", "logs", "waggle-"+id+"-workstation-controller-1").CombinedOutput()
	if !bytes.Contains(out, []byte("does not carry {stack}")) {
		t.Errorf("the intruder route is refused, and reported:\n%s", out)
	}
	status, _, _, _ := get("https", "intruder.example.test", 443+workstationOffset, "/", workstationCA)
	if status == 200 {
		t.Error("the intruder route serves nothing")
	}
	b := stack("w-b")
	if _, err := run(nil, "docker", "compose", "-f", filepath.Join(dir, b, "compose.yaml"), "down"); err != nil {
		t.Fatal(err)
	}
	err := waitFor(func() error {
		status, _, _, _ := get("https", b+".example.test", 443+workstationOffset, "/", workstationCA)
		if status != 404 {
			return fmt.Errorf("%d", status)
		}
		return nil
	}, 20*time.Second)
	if err != nil {
		t.Errorf("a removed stack loses its route, and its name goes to the relay: %v", err)
	}
}

func TestRunAgain(t *testing.T) {
	before, _ := run(nil, "docker", "ps", "-q", "--filter", "label=waggle.instance="+id+"-workstation")
	if _, err := waggle("up"); err != nil {
		t.Fatal(err)
	}
	after, _ := run(nil, "docker", "ps", "-q", "--filter", "label=waggle.instance="+id+"-workstation")
	if before != after {
		t.Errorf("running up again recreates nothing:\n%s\n%s", before, after)
	}
	if _, err := run(nil, "docker", "restart", "waggle-"+id+"-workstation-controller-1"); err != nil {
		t.Fatal(err)
	}
	status, body, _, err := get("https", stack("w-a")+".example.test", 443+workstationOffset, "/", workstationCA)
	expect(t, "controller restarted, routes hold", status, body, err, stack("w-a")+" front")
}

// query asks the server's DNS a question, and returns the response code and the addresses.
func query(proto, name string, qtype uint16) (int, []string, error) {
	var q bytes.Buffer
	binary.Write(&q, binary.BigEndian, []uint16{0x1234, 0x0100, 1, 0, 0, 0})
	for _, l := range strings.Split(name, ".") {
		q.WriteByte(byte(len(l)))
		q.WriteString(l)
	}
	q.WriteByte(0)
	binary.Write(&q, binary.BigEndian, []uint16{qtype, 1})
	conn, err := net.DialTimeout(proto, net.JoinHostPort(host, strconv.Itoa(53+serverOffset)), 3*time.Second)
	if err != nil {
		return 0, nil, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	msg := q.Bytes()
	if proto == "tcp" {
		msg = append(binary.BigEndian.AppendUint16(nil, uint16(len(msg))), msg...)
	}
	if _, err := conn.Write(msg); err != nil {
		return 0, nil, err
	}
	resp := make([]byte, 4096)
	n, err := io.ReadAtLeast(conn, resp, 12)
	if err != nil {
		return 0, nil, err
	}
	resp = resp[:n]
	if proto == "tcp" {
		resp = resp[2:]
	}
	rcode := int(resp[3] & 0x0f)
	qd, an := binary.BigEndian.Uint16(resp[4:]), binary.BigEndian.Uint16(resp[6:])
	i := 12
	for ; qd > 0; qd-- {
		i = skipName(resp, i) + 4
	}
	var ips []string
	for ; an > 0; an-- {
		i = skipName(resp, i)
		typ, length := binary.BigEndian.Uint16(resp[i:]), int(binary.BigEndian.Uint16(resp[i+8:]))
		i += 10
		if typ == 1 && length == 4 {
			ips = append(ips, net.IP(resp[i:i+4]).String())
		}
		if typ == 28 && length == 16 {
			ips = append(ips, net.IP(resp[i:i+16]).String())
		}
		i += length
	}
	return rcode, ips, nil
}

func skipName(m []byte, i int) int {
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
