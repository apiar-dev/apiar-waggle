// Package controller is the companion turned translator (ADR-0002, decision 1). It follows the containers that carry
// `waggle.project`, keeps only its instance's projects, connects each one to the instance network, parses its routes,
// issues the certificates they need, and writes Traefik's configuration into its dynamic directory. Traefik reloads it
// by itself: it has neither socket nor Docker provider, and the reload trap goes away with them.
package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/apiar-dev/apiar-waggle/internal/ca"
	"github.com/apiar-dev/apiar-waggle/internal/config"
	"github.com/apiar-dev/apiar-waggle/internal/docker"
	"github.com/apiar-dev/apiar-waggle/internal/render"
	"github.com/apiar-dev/apiar-waggle/internal/routes"
	"github.com/apiar-dev/apiar-waggle/internal/traefik"
)

// ProjectLabel is how an instance recognizes its projects (ADR-0001, decision 2).
const ProjectLabel = "waggle.project"

// StackLabel is the stack name, which Compose puts on each container.
const StackLabel = "com.docker.compose.project"

// Docker is what the controller asks of Docker.
type Docker interface {
	Containers(ctx context.Context, label string) ([]docker.Summary, error)
	Inspect(ctx context.Context, id string) (*docker.Container, error)
	Connect(ctx context.Context, network, id string) error
	Events(ctx context.Context, since time.Time, label string, actions []string, receive func(docker.Event)) error
}

// Controller is an instance's state.
type Controller struct {
	Instance *config.Instance
	CA       *ca.Authority
	Docker   Docker
	Dir      string // Traefik's dynamic directory
	Log      *log.Logger
	Now      func() time.Time

	seenRoutes   map[string]string // key → log line, to only report what changes
	seenRefusals map[string]bool
}

// Load reads the instance and its authority.
func Load(instancePath, caDir string) (*config.Instance, *ca.Authority, error) {
	content, err := os.ReadFile(instancePath)
	if err != nil {
		return nil, nil, err
	}
	var inst config.Instance
	if err := json.Unmarshal(content, &inst); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", instancePath, err)
	}
	cert, err := os.ReadFile(filepath.Join(caDir, "ca.crt"))
	if err != nil {
		return nil, nil, err
	}
	key, err := os.ReadFile(filepath.Join(caDir, "ca.key"))
	if err != nil {
		return nil, nil, err
	}
	a, err := ca.Load(cert, key)
	if err != nil {
		return nil, nil, err
	}
	return &inst, a, nil
}

// Run rebuilds the configuration at start, whenever a project container starts or stops, and every minute — for
// certificates close to expiry, and to catch up with a lost event.
func (c *Controller) Run(ctx context.Context) error {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Log == nil {
		c.Log = log.New(io.Discard, "", 0)
	}
	since := c.Now()
	if err := c.Rebuild(ctx); err != nil {
		c.Log.Printf("rebuild: %v", err)
	}
	signal := make(chan struct{}, 1)
	go func() {
		for ctx.Err() == nil {
			err := c.Docker.Events(ctx, since, ProjectLabel, []string{"start", "die", "destroy"}, func(e docker.Event) {
				if e.TimeNano > 0 {
					since = time.Unix(0, e.TimeNano)
				}
				select {
				case signal <- struct{}{}:
				default:
				}
			})
			if ctx.Err() != nil {
				return
			}
			c.Log.Printf("the event stream stopped (%v): resuming in two seconds", err)
			time.Sleep(2 * time.Second)
		}
	}()
	minute := time.NewTicker(time.Minute)
	defer minute.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-signal:
			// A stack's events come in bursts: one rebuild per burst
			time.Sleep(300 * time.Millisecond)
			select {
			case <-signal:
			default:
			}
		case <-minute.C:
		}
		if err := c.Rebuild(ctx); err != nil {
			c.Log.Printf("rebuild: %v", err)
		}
	}
}

type candidate struct {
	route   traefik.ServedRoute
	created int64
	order   int
}

// Rebuild reads the containers again, and rewrites what changed.
func (c *Controller) Rebuild(ctx context.Context) error {
	if c.seenRoutes == nil {
		c.seenRoutes, c.seenRefusals = map[string]string{}, map[string]bool{}
	}
	network := render.ProjectName(c.Instance)
	list, err := c.Docker.Containers(ctx, ProjectLabel)
	if err != nil {
		return err
	}
	var candidates []candidate
	refusals := map[string]bool{}
	refuse := func(format string, args ...any) { refusals[fmt.Sprintf(format, args...)] = true }

	for _, summary := range list {
		if !c.Instance.ServesProject(summary.Labels[ProjectLabel]) {
			continue
		}
		ct, err := c.Docker.Inspect(ctx, summary.ID)
		if err != nil || !ct.State.Running {
			continue
		}
		name := ct.ShortName()
		stack := ct.Config.Labels[StackLabel]
		if stack == "" {
			refuse("%s: no stack name (started outside Compose?); no route", name)
			continue
		}
		if _, connected := ct.NetworkSettings.Networks[network]; !connected {
			if err := c.Docker.Connect(ctx, network, ct.ID); err != nil {
				refuse("%s: connecting to %s failed: %v", name, network, err)
				continue
			}
			c.Log.Printf("connected to %s: %s", network, name)
			if ct, err = c.Docker.Inspect(ctx, summary.ID); err != nil {
				continue
			}
		}
		ip := ct.NetworkSettings.Networks[network].IPAddress
		if ip == "" {
			continue
		}
		parsed, errs := routes.Parse(ct.Config.Labels[routes.Label], stack, ct.ExposedTCPPorts())
		for _, e := range errs {
			refuse("%s, route refused: %v", name, e)
		}
		for i, r := range parsed {
			if r.Mode != routes.HTTP && !c.Instance.InCatalog(r.Port) {
				refuse("%s, route refused: %q: port %d is not in the instance catalog (configuration, ports)", name, r.Text, r.Port)
				continue
			}
			candidates = append(candidates, candidate{route: traefik.ServedRoute{Route: r, Container: name, IP: ip}, created: summary.Created, order: i})
		}
	}

	kept := c.settle(candidates, refuse)
	if err := c.write(kept); err != nil {
		return err
	}
	c.report(kept, refusals)
	return nil
}

// settle: the oldest stack keeps a contested route, and a domain keeps its first mode. A domain is terminated or
// passthrough, never both (ADR-0002, decision 4).
func (c *Controller) settle(candidates []candidate, refuse func(string, ...any)) []traefik.ServedRoute {
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.created != b.created {
			return a.created < b.created
		}
		if a.route.Container != b.route.Container {
			return a.route.Container < b.route.Container
		}
		return a.order < b.order
	})
	modes := map[string]routes.Mode{}
	for _, rel := range c.Instance.Relays {
		for _, text := range rel.Domains {
			if d, err := config.ParseRelayedDomain(text); err == nil && d.Mode != "http" {
				modes[d.Domain] = routes.Mode(d.Mode)
			}
		}
	}
	taken := map[string]string{}
	var kept []traefik.ServedRoute
	for _, cand := range candidates {
		r := cand.route
		if r.Mode != routes.HTTP {
			if mode, ok := modes[r.Domain]; ok && mode != r.Mode {
				refuse("%s, route refused: %q: domain %s is already %s; a domain is terminated or passthrough, never both",
					r.Container, r.Text, r.Domain, mode)
				continue
			}
			modes[r.Domain] = r.Mode
		}
		if other, ok := taken[r.Key()]; ok {
			refuse("%s, route refused: %s is already served by %s", r.Container, r.URL(), other)
			continue
		}
		taken[r.Key()] = r.Container
		kept = append(kept, r)
	}
	return kept
}

// write writes the certificates first, then the files Traefik reads, each with a single rename.
func (c *Controller) write(kept []traefik.ServedRoute) error {
	certs, err := c.certificates(traefik.WantedCertificates(c.Instance, kept))
	if err != nil {
		return err
	}
	files := map[string]any{
		"tls.yaml":    traefik.TLS(certs),
		"routes.yaml": traefik.Routes(c.Instance, kept),
		"relays.yaml": traefik.Relays(c.Instance),
	}
	for name, content := range files {
		b, err := json.MarshalIndent(content, "", "  ")
		if err != nil {
			return err
		}
		if err := writeIfChanged(filepath.Join(c.Dir, name), append(b, '\n')); err != nil {
			return err
		}
	}
	return nil
}

// certificates keeps one certificate per wanted entry, reissued when it is no longer valid. Its file name carries its
// hash: a reissued certificate is a new file, which Traefik sees, and the old one is removed.
func (c *Controller) certificates(wanted map[string][]string) ([]traefik.Certificate, error) {
	dir := filepath.Join(c.Dir, "certs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	keep := map[string]bool{}
	var list []traefik.Certificate
	var keys []string
	for k := range wanted {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		names := wanted[k]
		base := traefik.Sanitize(k)
		cert, valid := c.existingCertificate(dir, base, names)
		if !valid {
			certPEM, keyPEM, err := c.CA.Issue(names, c.Now())
			if err != nil {
				return nil, fmt.Errorf("certificate for %s: %w", strings.Join(names, ", "), err)
			}
			cert = filepath.Join(dir, base+"-"+render.Hash(certPEM)+".crt")
			if err := writeIfChanged(strings.TrimSuffix(cert, ".crt")+".key", keyPEM); err != nil {
				return nil, err
			}
			if err := writeIfChanged(cert, certPEM); err != nil {
				return nil, err
			}
			c.Log.Printf("certificate issued: %s", strings.Join(names, ", "))
		}
		keep[filepath.Base(cert)] = true
		keep[strings.TrimSuffix(filepath.Base(cert), ".crt")+".key"] = true
		list = append(list, traefik.Certificate{CertFile: cert, KeyFile: strings.TrimSuffix(cert, ".crt") + ".key"})
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !keep[e.Name()] {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	return list, nil
}

func (c *Controller) existingCertificate(dir, base string, names []string) (string, bool) {
	paths, _ := filepath.Glob(filepath.Join(dir, base+"-*.crt"))
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if _, err := os.Stat(strings.TrimSuffix(path, ".crt") + ".key"); err != nil {
			continue
		}
		if c.CA.Valid(content, names, c.Now()) {
			return path, true
		}
	}
	return "", false
}

// report only tells what changes: a route that appears, one that goes away, a new refusal.
func (c *Controller) report(kept []traefik.ServedRoute, refusals map[string]bool) {
	current := map[string]string{}
	for _, r := range kept {
		current[r.Key()+" "+r.Container] = fmt.Sprintf("%s → %s:%d", r.URL(), r.Container, r.Target)
	}
	for key, line := range current {
		if _, ok := c.seenRoutes[key]; !ok {
			c.Log.Printf("route: %s", line)
		}
	}
	for key, line := range c.seenRoutes {
		if _, ok := current[key]; !ok {
			c.Log.Printf("route removed: %s", line)
		}
	}
	c.seenRoutes = current
	for r := range refusals {
		if !c.seenRefusals[r] {
			c.Log.Print(r)
		}
	}
	c.seenRefusals = refusals
}

// writeIfChanged writes a file through a rename, and only when its content changes: Traefik only reloads what moves.
func writeIfChanged(path string, content []byte) error {
	if current, err := os.ReadFile(path); err == nil && string(current) == string(content) {
		return nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
