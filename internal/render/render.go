// Package render writes, for each instance, the Compose project that runs it (`compose.json`) and its DNS Corefile.
//
// An instance is:
//   - traefik, which publishes its entry points on the instance address only, and reads files only: it does not mount
//     the Docker socket;
//   - controller, the only one to mount the socket: it connects routed services, translates their routes, issues their
//     certificates, and writes Traefik's configuration into a volume;
//   - dns, on an instance that shares its stacks: CoreDNS, which loads the zones the controller writes for the stacks it
//     serves, and the list of domains it publishes to workstations (ADR-0003, decisions 8 and 9);
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
	// ZonesDir is where CoreDNS reads, and the controller writes, the VPN DNS zones.
	ZonesDir = "/dns"
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
	controllerCommand := []string{"controller", "--instance", "/state/instance.json", "--ca", "/state/ca"}
	controllerVolumes := []string{
		"/var/run/docker.sock:/var/run/docker.sock",
		dir + ":/state:ro",
		"dynamic:" + DynamicDir,
	}
	volumes := object{"dynamic": object{"name": project + "-dynamic"}}
	if inst.ServesDNS() {
		controllerCommand = append(controllerCommand, "--zones", ZonesDir)
		controllerVolumes = append(controllerVolumes, "zones:"+ZonesDir)
		volumes["zones"] = object{"name": project + "-zones"}
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
			"command":      controllerCommand,
			"volumes":      controllerVolumes,
			"labels":       labels("controller"),
		},
	}
	networks := object{"waggle": object{"name": project}}

	if inst.ServesDNS() {
		services["dns"] = object{
			"image":   CoreDNSImage,
			"restart": "unless-stopped",
			"command": []string{"-conf", "/Corefile"},
			"ports": []string{
				fmt.Sprintf("%s:%d:53/udp", inst.Address, 53+inst.PortOffset),
				fmt.Sprintf("%s:%d:53/tcp", inst.Address, 53+inst.PortOffset),
			},
			"volumes":  []string{dir + "/Corefile:/Corefile:ro", "zones:" + ZonesDir + ":ro"},
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
		"volumes":  volumes,
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

// Corefile is the VPN DNS. CoreDNS loads by itself the zones the controller writes, one per stack name it serves, and
// forwards the rest (ADR-0003, decision 8). No cache: a stack's name answers as soon as it is served, and stops when it
// is no longer.
func Corefile(inst *config.Instance) string {
	return "# Written by waggle: the VPN DNS of instance " + inst.Name + ". Its controller writes the zones into " + ZonesDir + ".\n" +
		".:53 {\n" +
		"    auto {\n" +
		"        directory " + ZonesDir + " db\\.(.*) {1}\n" +
		"        reload 2s\n" +
		"    }\n" +
		"    forward . /etc/resolv.conf\n" +
		"    errors\n" +
		"}\n"
}

// Hash is a short hash of some content.
func Hash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:6])
}
