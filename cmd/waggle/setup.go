package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/apiar-dev/apiar-waggle/internal/config"
	"github.com/apiar-dev/apiar-waggle/internal/publish"
)

// none is the answer that shares nothing, or relays to no server.
const none = "none"

const header = `# This machine's waggle configuration, written by waggle setup: run it again to change it, or edit it, then waggle up.
# Its keys are in the README.
`

// setup asks a machine's configuration, one question per machine, with what it detects as the answer, then writes it
// (ADR-0003, decision 6). bin/waggle starts the instances afterwards. Without a terminal, it guesses nothing: the
// answers are options.
func setup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	share := fs.String("share", "", "on Linux: none, or the interface through which other devices reach this machine")
	relay := fs.String("relay", "", "the address of a server whose stacks this machine opens, or none")
	fs.Parse(args)

	path := env("WAGGLE_CONFIG_PATH", "/config.yaml")
	shown := env("WAGGLE_CONFIG_HOST", path)
	osName := os.Getenv("WAGGLE_OS")
	var current *config.File
	if content, err := os.ReadFile(path); err == nil {
		if current, err = config.Decode(content); err != nil {
			return fmt.Errorf("%s: %w; fix it by hand, or move it away", shown, err)
		}
		if len(current.Instances) > 1 {
			return fmt.Errorf("%s describes several instances: setup only writes one; edit it by hand, then waggle up", shown)
		}
	}
	var links []config.Link
	if osName == "Linux" {
		var err error
		if links, err = config.HostLinks(); err != nil {
			return err
		}
	}
	var p *prompt
	if info, err := os.Stdin.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
		p = &prompt{in: bufio.NewReader(os.Stdin), out: os.Stdout}
	}

	a := answers{share: *share, relay: *relay}
	if err := a.ask(p, osName, current, links); err != nil {
		return err
	}
	f, err := plan(current, a, links)
	if err != nil {
		return err
	}
	content, err := marshal(f)
	if err != nil {
		return err
	}
	check, _ := config.Decode(content)
	if _, err := config.Validate(check, config.Host{OS: osName, Links: func() ([]config.Link, error) { return links, nil }}); err != nil {
		return fmt.Errorf("this configuration does not hold: %w\n\n%s\nwrite it by hand in %s (README), then waggle up", err, content, shown)
	}

	if p != nil {
		fmt.Printf("\nThe configuration, for %s:\n\n%s\n", shown, indent(content))
		if !p.confirm("Write it, then start waggle?", true) {
			return errors.New("nothing written")
		}
	}
	if err := write(path, content); err != nil {
		return err
	}
	fmt.Printf("waggle: configuration written to %s\n", shown)
	phones(f, links)
	return nil
}

// answers are the two questions of setup: through which interface other devices reach this machine, and which server
// it relays to.
type answers struct {
	share string // none, or an interface's name
	relay string // none, or a server's address
}

// ask fills in the answers the options did not give: from the terminal, with what it detects or what the current
// configuration says as the default; without a terminal, from the current configuration only, on Linux, since a VPN
// interface is no proof that its devices should reach the stacks.
func (a *answers) ask(p *prompt, osName string, current *config.File, links []config.Link) error {
	inst := single(current)
	if osName != "Linux" {
		a.share = none // a workstation never shares its stacks (ADR-0003, decision 3)
	}
	if a.share == "" {
		tunnels := vpnLinks(links)
		def := none
		switch {
		case inst != nil && inst.Address != config.LoopbackAddress:
			def = linkName(links, inst.Address, inst.Interface)
		case inst == nil && len(tunnels) == 1:
			def = tunnels[0].Name
		}
		if p == nil {
			if inst == nil {
				return fmt.Errorf("no terminal to ask: say through which interface other devices reach this machine, with --share none or --share <interface>%s", detected(tunnels))
			}
			a.share = def
		} else {
			a.share = p.shareQuestion(tunnels, links, def)
		}
	}
	if a.share != none {
		a.relay = none // a server does not relay
		return nil
	}
	if a.relay == "" {
		def := none
		if inst != nil && len(inst.Relays) > 0 {
			def = inst.Relays[0].To
		}
		a.relay = def
		if p != nil {
			a.relay = p.relayQuestion(def)
		}
	}
	return nil
}

// plan is the configuration the answers give, from the current one: what setup does not ask stays as it was.
func plan(current *config.File, a answers, links []config.Link) (*config.File, error) {
	name, inst := "", config.Instance{}
	if c := single(current); c != nil {
		for n := range current.Instances {
			name = n
		}
		inst = *c
	}
	// A machine that changes role changes instance, and authority: its name follows
	if wasServer := inst.Address != "" && inst.Address != config.LoopbackAddress; wasServer != (a.share != none) {
		name = ""
	}
	if a.share == none {
		if name == "" {
			name = "workstation"
		}
		inst.Address, inst.Loopback, inst.Interface, inst.RelayListener, inst.DNS = config.LoopbackAddress, false, "", nil, nil
		var kept []config.Relay
		if a.relay != none && a.relay != "" {
			r := config.Relay{To: a.relay}
			for _, former := range inst.Relays {
				if former.To == a.relay {
					r = former
				}
			}
			kept = []config.Relay{r}
		}
		inst.Relays = kept
	} else {
		link := find(links, a.share)
		if link == nil {
			return nil, fmt.Errorf("this machine has no interface %s%s", a.share, detected(vpnLinks(links)))
		}
		ip := link.IPv4()
		if ip == nil {
			return nil, fmt.Errorf("%s has no IPv4 address", link.Name)
		}
		if name == "" {
			name = "vpn"
		}
		if inst.Address != ip.String() {
			inst.RelayListener = nil
		}
		inst.Address, inst.Loopback, inst.Relays = ip.String(), true, nil
		inst.Interface = ""
		if !link.Tunnel() {
			inst.Interface = link.Name // another interface serves only when named (ADR-0003, decision 7)
		}
	}
	return &config.File{Instances: map[string]*config.Instance{name: &inst}}, nil
}

// marshal writes the configuration as one would by hand: two spaces of indentation, under the header.
func marshal(f *config.File) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(header)
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return nil, err
	}
	return b.Bytes(), enc.Close()
}

func single(f *config.File) *config.Instance {
	if f == nil {
		return nil
	}
	for _, inst := range f.Instances {
		if inst == nil {
			return &config.Instance{}
		}
		return inst
	}
	return nil
}

// vpnLinks are the VPN interfaces that carry an IPv4 address.
func vpnLinks(links []config.Link) []config.Link {
	var l []config.Link
	for _, link := range links {
		if link.Tunnel() && link.IPv4() != nil {
			l = append(l, link)
		}
	}
	return l
}

// otherLinks are the other interfaces with a private address, Docker's own and loopback aside.
func otherLinks(links []config.Link) []config.Link {
	var l []config.Link
	for _, link := range links {
		ip := link.IPv4()
		if link.Tunnel() || ip == nil || ip.IsLoopback() || !config.Private(ip) || link.Name == "docker0" ||
			strings.HasPrefix(link.Name, "br-") || strings.HasPrefix(link.Name, "veth") {
			continue
		}
		l = append(l, link)
	}
	return l
}

func find(links []config.Link, name string) *config.Link {
	for i := range links {
		if links[i].Name == name {
			return &links[i]
		}
	}
	return nil
}

func linkName(links []config.Link, address, named string) string {
	if named != "" {
		return named
	}
	for _, l := range links {
		if ip := l.IPv4(); ip != nil && ip.String() == address {
			return l.Name
		}
	}
	return none
}

func describe(l config.Link) string {
	if vpn := l.VPN(); vpn != "" {
		return fmt.Sprintf("%s (%s), %s", l.Name, vpn, l.IPv4())
	}
	return fmt.Sprintf("%s, %s", l.Name, l.IPv4())
}

func detected(tunnels []config.Link) string {
	if len(tunnels) == 0 {
		return " (no VPN interface detected)"
	}
	var l []string
	for _, t := range tunnels {
		l = append(l, describe(t))
	}
	return " (detected: " + strings.Join(l, "; ") + ")"
}

// write writes the configuration, keeping the former one next to it.
func write(path string, content []byte) error {
	if former, err := os.ReadFile(path); err == nil {
		if bytes.Equal(former, content) {
			return nil
		}
		if err := os.WriteFile(path+".bak", former, 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(path, content, 0o644)
}

// phones says what a server's VPN must tell its phones: their DNS, for the dev domains (README, « Un téléphone »).
func phones(f *config.File, links []config.Link) {
	inst := single(f)
	if inst == nil || inst.Address == config.LoopbackAddress {
		return
	}
	vpn := ""
	if l := find(links, linkName(links, inst.Address, inst.Interface)); l != nil {
		vpn = l.VPN()
	}
	switch vpn {
	case "WireGuard":
		fmt.Printf("  phones: in their WireGuard tunnel, DNS = %s\n", inst.Address)
	case "Tailscale":
		fmt.Printf("  phones: in Tailscale's admin console, DNS → add nameserver %s, restricted to the dev domain (split DNS)\n", inst.Address)
	default:
		fmt.Printf("  phones: in the VPN's settings, %s as the DNS of the dev domain\n", inst.Address)
	}
}

// prompt asks on a terminal.
type prompt struct {
	in  *bufio.Reader
	out io.Writer
}

func (p *prompt) read() string {
	line, _ := p.in.ReadString('\n')
	return strings.TrimSpace(line)
}

func (p *prompt) confirm(question string, def bool) bool {
	hint := "[Y/n]"
	if !def {
		hint = "[y/N]"
	}
	for {
		fmt.Fprintf(p.out, "%s %s ", question, hint)
		switch strings.ToLower(p.read()) {
		case "":
			return def
		case "y", "yes", "o", "oui":
			return true
		case "n", "no", "non":
			return false
		}
	}
}

func (p *prompt) shareQuestion(tunnels, links []config.Link, def string) string {
	options := []string{"No: it serves its own browsers and agents only, on 127.0.0.1"}
	names := []string{none}
	for _, t := range tunnels {
		options = append(options, "Yes, through "+describe(t))
		names = append(names, t.Name)
	}
	others := otherLinks(links)
	if len(others) > 0 {
		options = append(options, "Yes, through another interface")
		names = append(names, "")
	}
	choice := 1
	for i, n := range names {
		if n == def {
			choice = i + 1
		}
	}
	if def != none && find(tunnels, def) == nil && len(others) > 0 {
		choice = len(names)
	}
	fmt.Fprintln(p.out, "\nDo other devices, a workstation or a phone, reach this machine's stacks?")
	for i, o := range options {
		fmt.Fprintf(p.out, "  %d. %s\n", i+1, o)
	}
	for {
		fmt.Fprintf(p.out, "Your choice [%d]: ", choice)
		text := p.read()
		n := choice
		if text != "" {
			var err error
			if n, err = strconv.Atoi(text); err != nil || n < 1 || n > len(names) {
				continue
			}
		}
		if names[n-1] != "" {
			return names[n-1]
		}
		return p.otherQuestion(others, def)
	}
}

func (p *prompt) otherQuestion(others []config.Link, def string) string {
	fmt.Fprintln(p.out, "Whatever reaches this interface's address will open the stacks. On a cloud server, a private address")
	fmt.Fprintln(p.out, "may be reachable from the internet: check the provider's firewall first.")
	var l []string
	for _, o := range others {
		l = append(l, describe(o))
	}
	if find(others, def) == nil {
		def = others[0].Name
	}
	for {
		fmt.Fprintf(p.out, "Which interface? %s [%s]: ", strings.Join(l, "; "), def)
		text := p.read()
		if text == "" {
			return def
		}
		if find(others, text) != nil {
			return text
		}
	}
}

func (p *prompt) relayQuestion(def string) string {
	fmt.Fprintln(p.out, "\nDoes this machine also open the stacks of a server, through a VPN?")
	for {
		fmt.Fprintf(p.out, "Its address, or name, on the VPN; none if it does not [%s]: ", def)
		text := p.read()
		if text == "" {
			text = def
		}
		if text == none {
			return none
		}
		if strings.ContainsAny(text, " /:") {
			continue
		}
		list, err := publish.Lookup(context.Background(), text, 53)
		switch {
		case err != nil:
			fmt.Fprintf(p.out, "  %s: %v; waggle up will try again\n", text, err)
		case len(list.Domains) == 0:
			fmt.Fprintf(p.out, "  %s publishes nothing yet: its domains will be relayed once it serves a stack, at the next waggle up\n", text)
		default:
			fmt.Fprintf(p.out, "  it serves %s", strings.Join(list.Domains, ", "))
			if len(list.Ports) > 0 {
				fmt.Fprintf(p.out, ", on ports %v", list.Ports)
			}
			fmt.Fprintln(p.out)
		}
		return text
	}
}

func indent(content []byte) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(string(content), "\n"), "\n") {
		if !strings.HasPrefix(line, "#") {
			b.WriteString("  " + line + "\n")
		}
	}
	return b.String()
}
