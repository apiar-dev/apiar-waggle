// waggle: a machine's dev routing, shared by the projects, never on the internet (docs/adr/0001, 0002).
//
// These commands run inside waggle's image; `bin/waggle` starts them from the host, with the Docker socket, the
// configuration file and the state directory.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/apiar-dev/apiar-waggle/internal/ca"
	"github.com/apiar-dev/apiar-waggle/internal/config"
	"github.com/apiar-dev/apiar-waggle/internal/controller"
	"github.com/apiar-dev/apiar-waggle/internal/docker"
	"github.com/apiar-dev/apiar-waggle/internal/firewall"
	"github.com/apiar-dev/apiar-waggle/internal/publish"
	"github.com/apiar-dev/apiar-waggle/internal/render"
	"github.com/apiar-dev/apiar-waggle/internal/routes"
)

// Version is set at build time.
var Version = "dev"

const usage = `waggle — a machine's dev routing, shared by the projects, never on the internet

  setup              ask this machine's configuration, with what it detects as the answers, and write it
                     (--share none|<interface> on Linux, --relay <server>|none: the answers, without a terminal)
  up                 read the configuration, write and start each instance; run again, it only changes what changed
  down               stop and remove the instances' containers, keeping their authority and configuration
  uninstall          remove the instances and their volumes; --all also removes their authority
  status             each instance's containers, and its authority
  ca [instance]      an instance's authority certificate, to trust on a device
  version

  controller         (inside an instance) translate the stacks' routes for Traefik
  firewall           (inside a server instance) put the DOCKER-USER rule for its address
`

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "setup":
		err = setup(os.Args[2:])
	case "up":
		err = up()
	case "down":
		err = composeAll("down", "--remove-orphans")
	case "uninstall":
		err = uninstall(os.Args[2:])
	case "status":
		err = status()
	case "ca":
		err = printCA(os.Args[2:])
	case "controller":
		err = runController(os.Args[2:])
	case "firewall":
		err = runFirewall(os.Args[2:])
	case "version":
		fmt.Println(Version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		err = fmt.Errorf("unknown command: %s\n\n%s", os.Args[1], usage)
	}
	if err != nil {
		log.Fatalf("waggle: %v", err)
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func stateDir() (string, error) {
	state := os.Getenv("WAGGLE_STATE")
	if state == "" {
		return "", errors.New("WAGGLE_STATE is missing: run waggle through bin/waggle")
	}
	return state, nil
}

func instances() ([]*config.Instance, error) {
	f, err := config.Read(env("WAGGLE_CONFIG_PATH", "/config.yaml"))
	if err != nil {
		return nil, err
	}
	return config.Validate(f, config.Host{OS: os.Getenv("WAGGLE_OS"), Links: config.HostLinks})
}

func up() error {
	state, err := stateDir()
	if err != nil {
		return err
	}
	insts, err := instances()
	if err != nil {
		return err
	}
	image := env("WAGGLE_IMAGE", "waggle:local")
	hostname := env("WAGGLE_HOSTNAME", "this machine")
	ctx := context.Background()
	labeled, err := docker.New("/var/run/docker.sock").AllContainers(ctx, controller.ProjectLabel)
	if err != nil {
		fmt.Printf("waggle: the projects' routes cannot be read (%v): only the configuration's ports are published\n", err)
	}
	wanted := map[string]bool{}
	for _, inst := range insts {
		wanted[inst.Name] = true
		declaredPorts(inst, labeled)
		learnRelays(ctx, inst, filepath.Join(state, inst.Name))
		if err := prepare(inst, filepath.Join(state, inst.Name), image, hostname); err != nil {
			return fmt.Errorf("instance %s: %w", inst.Name, err)
		}
	}
	// An instance removed from the configuration stops; its directory, and its authority, stay
	previous, _ := filepath.Glob(filepath.Join(state, "*", "compose.json"))
	for _, compose := range previous {
		name := filepath.Base(filepath.Dir(compose))
		if !wanted[name] {
			fmt.Printf("waggle: instance %s is no longer in the configuration, stopping it\n", name)
			if err := dockerCompose(compose, "down", "--remove-orphans"); err != nil {
				return err
			}
		}
	}
	for _, inst := range insts {
		compose := filepath.Join(state, inst.Name, "compose.json")
		if err := dockerCompose(compose, "up", "--detach", "--remove-orphans", "--quiet-pull"); err != nil {
			return fmt.Errorf("instance %s: %w", inst.Name, err)
		}
		summarize(inst, filepath.Join(state, inst.Name))
	}
	return nil
}

// prepare writes the authority, when missing, then instance.json, the Corefile and compose.json.
func prepare(inst *config.Instance, dir, image, hostname string) error {
	if err := os.MkdirAll(filepath.Join(dir, "ca"), 0o700); err != nil {
		return err
	}
	cert := filepath.Join(dir, "ca", "ca.crt")
	if _, err := os.Stat(cert); errors.Is(err, os.ErrNotExist) {
		certPEM, keyPEM, err := ca.Create(fmt.Sprintf("waggle %s (%s)", inst.Name, hostname), time.Now())
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "ca", "ca.key"), keyPEM, 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(cert, certPEM, 0o644); err != nil {
			return err
		}
		fmt.Printf("waggle: instance %s has its authority, %s\n", inst.Name, cert)
	}
	hashes := map[string]string{}
	instJSON, err := json.MarshalIndent(inst, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "instance.json"), append(instJSON, '\n'), 0o644); err != nil {
		return err
	}
	hashes["controller.instance"] = render.Hash(instJSON)
	if inst.ServesDNS() {
		corefile := []byte(render.Corefile(inst))
		if err := os.WriteFile(filepath.Join(dir, "Corefile"), corefile, 0o644); err != nil {
			return err
		}
		hashes["dns.corefile"] = render.Hash(corefile)
	}
	compose, err := json.MarshalIndent(render.Compose(inst, dir, image, hashes), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "compose.json"), append(compose, '\n'), 0o644)
}

func summarize(inst *config.Instance, dir string) {
	var ports []string
	for _, p := range inst.PublishedPorts() {
		if inst.PortOffset > 0 {
			ports = append(ports, fmt.Sprintf("%d→%d", p+inst.PortOffset, p))
		} else {
			ports = append(ports, fmt.Sprint(p))
		}
	}
	fmt.Printf("waggle: instance %s listens on %s, ports %s\n", inst.Name, inst.Address, strings.Join(ports, ", "))
	if inst.Loopback {
		fmt.Printf("  and on %s, for this machine's own browsers and agents\n", config.LoopbackAddress)
	}
	if inst.ServesDNS() {
		fmt.Printf("  VPN DNS, on %s:%d: the names of its stacks, and the list of its domains, for workstations\n", inst.Address, 53+inst.PortOffset)
	}
	if inst.Interface != "" {
		fmt.Printf("  firewall: %s\n", firewall.Command(inst.Address, inst.Interface))
	}
	for _, r := range inst.Relays {
		how := ""
		if r.Learned {
			how = ", as it publishes them"
		}
		domains := strings.Join(r.Domains, ", ")
		if domains == "" {
			domains = "nothing yet"
		}
		fmt.Printf("  relay to %s%s: %s\n", r.To, how, domains)
	}
	fmt.Printf("  authority, to trust on each device: %s\n", filepath.Join(dir, "ca", "ca.crt"))
}

// declaredPorts adds to the instance the legacy ports its projects' routes need, on their containers, running or not
// (ADR-0003, decision 6).
func declaredPorts(inst *config.Instance, labeled []docker.Summary) {
	var ports []int
	for _, c := range labeled {
		if inst.ServesProject(c.Labels[controller.ProjectLabel]) {
			ports = append(ports, routes.DeclaredPorts(c.Labels[routes.Label])...)
		}
	}
	for _, why := range inst.AddPorts(ports) {
		fmt.Printf("waggle: instance %s: a route asks for a port it cannot publish: %s\n", inst.Name, why)
	}
}

// learnedFile keeps, per server, the last list it published: with the VPN down, a workstation still relays.
const learnedFile = "learned.json"

// learnRelays reads, for each relay without domains, the list its server publishes, and adds its ports (ADR-0003,
// decision 9). A server that does not answer leaves the last list read.
func learnRelays(ctx context.Context, inst *config.Instance, dir string) {
	learned := map[string]publish.List{}
	path := filepath.Join(dir, learnedFile)
	if content, err := os.ReadFile(path); err == nil {
		json.Unmarshal(content, &learned)
	}
	changed := false
	for i := range inst.Relays {
		r := &inst.Relays[i]
		if len(r.Domains) > 0 {
			continue
		}
		r.Learned = true
		list, err := publish.Lookup(ctx, r.To, 53+r.PortOffset)
		if err != nil {
			former, ok := learned[r.To]
			if !ok {
				fmt.Printf("waggle: relay to %s: %v; nothing is relayed until waggle up reaches it\n", r.To, err)
				continue
			}
			fmt.Printf("waggle: relay to %s: %v; the list it published before stays\n", r.To, err)
			list = former
		} else {
			learned[r.To], changed = list, true
		}
		r.Domains = list.Domains
		for _, why := range inst.AddPorts(list.Ports) {
			fmt.Printf("waggle: relay to %s: %s\n", r.To, why)
		}
	}
	if changed && os.MkdirAll(dir, 0o700) == nil {
		content, _ := json.MarshalIndent(learned, "", "  ")
		os.WriteFile(path, append(content, '\n'), 0o644)
	}
}

func composeAll(args ...string) error {
	state, err := stateDir()
	if err != nil {
		return err
	}
	insts, err := instances()
	if err != nil {
		return err
	}
	for _, inst := range insts {
		compose := filepath.Join(state, inst.Name, "compose.json")
		if _, err := os.Stat(compose); err != nil {
			continue
		}
		if err := dockerCompose(compose, args...); err != nil {
			return err
		}
	}
	return nil
}

func uninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	all := fs.Bool("all", false, "also remove the state directory, authority included")
	fs.Parse(args)
	if err := composeAll("down", "--remove-orphans", "--volumes"); err != nil {
		return err
	}
	if !*all {
		return nil
	}
	state, _ := stateDir()
	insts, err := instances()
	if err != nil {
		return err
	}
	for _, inst := range insts {
		dir := filepath.Join(state, inst.Name)
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		fmt.Printf("waggle: %s removed, authority included: devices that trusted it will no longer recognize it\n", dir)
	}
	return nil
}

func status() error {
	state, err := stateDir()
	if err != nil {
		return err
	}
	insts, err := instances()
	if err != nil {
		return err
	}
	for _, inst := range insts {
		dir := filepath.Join(state, inst.Name)
		on := inst.Address
		if inst.Loopback {
			on += " and " + config.LoopbackAddress
		}
		fmt.Printf("== instance %s, on %s\n", inst.Name, on)
		if _, err := os.Stat(filepath.Join(dir, "compose.json")); err != nil {
			fmt.Println("  not started (waggle up)")
			continue
		}
		if err := dockerCompose(filepath.Join(dir, "compose.json"), "ps"); err != nil {
			return err
		}
		fmt.Printf("  authority: %s\n", filepath.Join(dir, "ca", "ca.crt"))
		staleRelays(inst, dir)
	}
	return nil
}

// staleRelays says when a server now publishes domains or ports this workstation does not relay yet.
func staleRelays(inst *config.Instance, dir string) {
	learned := map[string]publish.List{}
	if content, err := os.ReadFile(filepath.Join(dir, learnedFile)); err == nil {
		json.Unmarshal(content, &learned)
	}
	for _, r := range inst.Relays {
		if len(r.Domains) > 0 {
			continue
		}
		now, err := publish.Lookup(context.Background(), r.To, 53+r.PortOffset)
		if err != nil {
			fmt.Printf("  relay to %s: %v\n", r.To, err)
			continue
		}
		before := learned[r.To]
		if fmt.Sprint(now) != fmt.Sprint(before) {
			fmt.Printf("  relay to %s: the server now publishes %s, ports %v: run waggle up\n", r.To, strings.Join(now.Domains, ", "), now.Ports)
		} else {
			fmt.Printf("  relay to %s: %s\n", r.To, strings.Join(now.Domains, ", "))
		}
	}
}

func printCA(args []string) error {
	state, err := stateDir()
	if err != nil {
		return err
	}
	insts, err := instances()
	if err != nil {
		return err
	}
	var names []string
	for _, inst := range insts {
		names = append(names, inst.Name)
	}
	sort.Strings(names)
	name := ""
	switch {
	case len(args) > 0:
		name = args[0]
	case len(names) == 1:
		name = names[0]
	default:
		return fmt.Errorf("several instances: name the one whose authority you want (%s)", strings.Join(names, ", "))
	}
	content, err := os.ReadFile(filepath.Join(state, name, "ca", "ca.crt"))
	if err != nil {
		return fmt.Errorf("instance %s has no authority: run waggle up first", name)
	}
	_, err = os.Stdout.Write(content)
	return err
}

func dockerCompose(file string, args ...string) error {
	cmd := exec.Command("docker", append([]string{"compose", "--file", file}, args...)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func stoppableContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
}

func runController(args []string) error {
	fs := flag.NewFlagSet("controller", flag.ExitOnError)
	instancePath := fs.String("instance", "/state/instance.json", "the instance's validated configuration")
	caDir := fs.String("ca", "/state/ca", "the authority directory")
	dynamic := fs.String("dynamic", render.DynamicDir, "the directory Traefik reads")
	zones := fs.String("zones", "", "the directory of the VPN DNS zones, on an instance that runs it")
	socket := fs.String("socket", "/var/run/docker.sock", "the Docker socket")
	fs.Parse(args)
	inst, a, err := controller.Load(*instancePath, *caDir)
	if err != nil {
		return err
	}
	ctx, stop := stoppableContext()
	defer stop()
	logger := log.New(os.Stdout, "", log.Ltime)
	logger.Printf("controller of instance %s, on %s", inst.Name, inst.Address)
	c := &controller.Controller{Instance: inst, CA: a, Docker: docker.New(*socket), Dir: *dynamic, Zones: *zones, Log: logger}
	return c.Run(ctx)
}

func runFirewall(args []string) error {
	fs := flag.NewFlagSet("firewall", flag.ExitOnError)
	address := fs.String("address", "", "the VPN address")
	iface := fs.String("interface", "", "its VPN interface")
	once := fs.Bool("once", false, "put the rule, then return")
	interval := fs.Duration("interval", 15*time.Second, "the time between two checks")
	fs.Parse(args)
	if *address == "" || *iface == "" {
		return errors.New("firewall: --address and --interface are required")
	}
	ctx, stop := stoppableContext()
	defer stop()
	logger := log.New(os.Stdout, "", log.Ltime)
	logger.Printf("firewall: %s", firewall.Command(*address, *iface))
	for {
		put, err := firewall.Ensure(firewall.System, *address, *iface)
		switch {
		case err != nil:
			logger.Printf("firewall: %v", err)
			if *once {
				return err
			}
		case len(put) > 0:
			logger.Printf("firewall: rule put (%s)", strings.Join(put, ", "))
		}
		if *once {
			return nil
		}
		select {
		case <-ctx.Done():
			// The rule stays: with waggle stopped, nothing is published, and it bothers no one
			return nil
		case <-time.After(*interval):
		}
	}
}
