// Package render writes, for each instance, the Compose project that runs it (`compose.json`) and its DNS Corefile.
//
// An instance is:
//   - traefik, which publishes its entry points on the instance address only, and reads files only: it does not mount
//     the Docker socket;
//   - controller, the only one to mount the socket: it connects routed services, translates their routes, issues their
//     certificates, and writes Traefik's configuration into a volume;
//   - dns, when there are domains to resolve for the VPN (ADR-0001, decision 6);
//   - firewall, on a VPN address: the DOCKER-USER rule, put back each time Docker starts (ADR-0001, decision 4).
package render

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/apiar-dev/apiar-waggle/internal/config"
	"github.com/apiar-dev/apiar-waggle/internal/traefik"
)

const (
	// TraefikImage is the Traefik version the ADRs tested (negative priorities, routing on Header).
	TraefikImage = "traefik:v3.6"
	// CoreDNSImage is the version tested for the VPN DNS.
	CoreDNSImage = "coredns/coredns:1.11.3"
	// DynamicDir is where Traefik reads, and the controller writes, the dynamic configuration.
	DynamicDir = "/dynamic"
)

type object = map[string]any

// ProjectName is the name of an instance's Compose project, and of its network.
func ProjectName(inst *config.Instance) string { return "waggle-" + inst.Name }

// Compose is an instance's Compose project. `dir` is the instance's state directory, on the host; `image`, waggle's
// image; `hashes`, those of the files the services read, so that Compose recreates them when they change.
func Compose(inst *config.Instance, dir, image string, hashes map[string]string) object {
	project := ProjectName(inst)
	labels := func(service string) object {
		l := object{"waggle.instance": inst.Name, "waggle.service": service}
		for key, value := range hashes {
			if strings.HasPrefix(key, service+".") {
				l["waggle.hash."+strings.TrimPrefix(key, service+".")] = value
			}
		}
		return l
	}

	var ports []string
	for _, p := range inst.PublishedPorts() {
		ports = append(ports, fmt.Sprintf("%s:%d:%d", inst.Address, p+inst.PortOffset, p))
	}
	// A server also serves its own machine, whose agents test the URLs of its stacks (ADR-0003)
	for _, p := range inst.LoopbackPorts() {
		ports = append(ports, fmt.Sprintf("%s:%d:%d", config.LoopbackAddress, p+inst.PortOffset, p))
	}
	services := object{
		"traefik": object{
			"image":    TraefikImage,
			"restart":  "unless-stopped",
			"command":  TraefikArgs(inst),
			"ports":    ports,
			"volumes":  []string{"dynamic:" + DynamicDir + ":ro"},
			"networks": []string{"waggle"},
			"labels":   labels("traefik"),
		},
		"controller": object{
			"image":       image,
			"pull_policy": "never",
			"restart":     "unless-stopped",
			// A process as PID 1 ignores the stop signal, which Docker only enforces after ten seconds
			"init":         true,
			"network_mode": "none",
			"command":      []string{"controller", "--instance", "/state/instance.json", "--ca", "/state/ca"},
			"volumes": []string{
				"/var/run/docker.sock:/var/run/docker.sock",
				dir + ":/state:ro",
				"dynamic:" + DynamicDir,
			},
			"labels": labels("controller"),
		},
	}
	networks := object{"waggle": object{"name": project}}

	if len(inst.DNS) > 0 {
		services["dns"] = object{
			"image":   CoreDNSImage,
			"restart": "unless-stopped",
			"command": []string{"-conf", "/Corefile"},
			"ports": []string{
				fmt.Sprintf("%s:%d:53/udp", inst.Address, 53+inst.PortOffset),
				fmt.Sprintf("%s:%d:53/tcp", inst.Address, 53+inst.PortOffset),
			},
			"volumes":  []string{dir + "/Corefile:/Corefile:ro"},
			"networks": []string{"dns"},
			"labels":   labels("dns"),
		}
		networks["dns"] = object{}
	}

	if inst.Interface != "" {
		services["firewall"] = object{
			"image":        image,
			"pull_policy":  "never",
			"restart":      "unless-stopped",
			"init":         true,
			"network_mode": "host",
			"cap_add":      []string{"NET_ADMIN", "NET_RAW"},
			"command":      []string{"firewall", "--address", inst.Address, "--interface", inst.Interface},
			"labels":       labels("firewall"),
		}
	}

	return object{
		"name":     project,
		"services": services,
		"networks": networks,
		"volumes":  object{"dynamic": object{"name": project + "-dynamic"}},
	}
}

// TraefikArgs is Traefik's static configuration: its entry points and its file provider.
func TraefikArgs(inst *config.Instance) []string {
	args := []string{"--entrypoints.p80.address=:80"}
	for _, p := range inst.TLSPorts() {
		args = append(args, fmt.Sprintf("--entrypoints.%s.address=:%d", traefik.EntryPoint(p), p))
	}
	if rl := inst.RelayListener; rl != nil {
		args = append(args,
			fmt.Sprintf("--entrypoints.relay.address=:%d", rl.Port),
			"--entrypoints.relay.forwardedHeaders.trustedIPs="+strings.Join(rl.TrustedIPs, ","),
		)
	}
	return append(args,
		"--providers.file.directory="+DynamicDir,
		"--providers.file.watch=true",
		"--global.checkNewVersion=false",
		"--global.sendAnonymousUsage=false",
		"--log.level=INFO",
	)
}

// Corefile is the VPN DNS. It answers the instance address for its domains and everything below them, answers nothing
// for AAAA, and forwards the rest (ADR-0001, decision 6).
func Corefile(inst *config.Instance) string {
	var b strings.Builder
	b.WriteString("# Written by waggle: the VPN DNS of instance " + inst.Name + ".\n")
	for _, d := range inst.DNS {
		fmt.Fprintf(&b, "%s:53 {\n", d)
		fmt.Fprintf(&b, "    template IN A {\n        answer \"{{ .Name }} 60 IN A %s\"\n    }\n", inst.Address)
		b.WriteString("    template IN AAAA {\n        rcode NOERROR\n    }\n")
		b.WriteString("    errors\n}\n")
	}
	b.WriteString(".:53 {\n    forward . /etc/resolv.conf\n    cache 30\n    errors\n}\n")
	return b.String()
}

// Hash is a short hash of some content.
func Hash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:6])
}
