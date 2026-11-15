#!/bin/sh
# waggle's firewall rule (ADR-0001, decision 4), tested through the real `waggle firewall` command, inside an isolated
# Docker-in-Docker: it has its own network and its own firewall, and touches neither the VM nor the workstation's
# proxies.
#
# Inside:
#   - a port published on 10.8.0.1 only, like waggle on a VPN address;
#   - "member", behind wg0, a tunnel member;
#   - "outside", behind another interface, a neighbor that routes to 10.8.0.1;
#   - "other", behind wg1, a member of a second VPN, that routes to 10.8.0.1 as well.
# Expected: without the rule, all three reach the port; with it, only the member.
#
# Then a port published on 127.0.0.1, like a server instance with loopback: true (ADR-0003): the machine itself reaches it,
# and never answers the neighbor, even when the neighbor sends packets addressed to 127.0.0.1 and the interface accepts
# them (route_localnet). What is measured is the machine's answer, a SYN-ACK from the port: the neighbor's own kernel
# would drop it anyway, since it comes from 127.0.0.1, and a crafted client would not. Docker drops the neighbor's packets
# in its raw table; the trial removes that rule once, to show that the path it closes is real.
set -eu

name=waggle-test-firewall-$$
image=${WAGGLE_IMAGE:-waggle:local}
cleanup() { docker rm --force --volumes "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM

docker run --detach --privileged --name "$name" docker:29-dind >/dev/null
i=0
until docker exec "$name" docker info >/dev/null 2>&1; do
	i=$((i + 1))
	[ "$i" -lt 60 ] || { echo "the Docker-in-Docker does not start" >&2; exit 1; }
	sleep 1
done
docker exec "$name" apk add --quiet --no-progress iproute2 curl tcpdump
docker save nginx:1.27-alpine "$image" | docker exec -i "$name" docker load --quiet >/dev/null

docker exec -i -e IMAGE="$image" "$name" sh -s <<'INSIDE'
set -eu
link() { # host interface, address, namespace, namespace address
	ip netns add "$3"
	ip link add "$1" type veth peer name "$3-0"
	ip link set "$3-0" netns "$3"
	ip addr add "$2" dev "$1" && ip link set "$1" up
	ip netns exec "$3" sh -c "ip addr add $4 dev $3-0 && ip link set $3-0 up && ip link set lo up"
}
link wg0 10.8.0.1/24 member 10.8.0.2/24
link h0 192.0.2.1/30 outside 192.0.2.2/30
link wg1 10.9.0.1/24 other 10.9.0.2/24
ip netns exec outside ip route add 10.8.0.1/32 via 192.0.2.1
ip netns exec other ip route add 10.8.0.1/32 via 10.9.0.1
docker run --detach --rm --name target --publish 10.8.0.1:18081:80 nginx:1.27-alpine >/dev/null
sleep 2

request() { ip netns exec "$1" curl --silent --max-time 3 --output /dev/null --write-out '%{http_code}' http://10.8.0.1:18081/ || true; }
state() { echo "$(request member) $(request outside) $(request other)"; }
firewall() { docker run --rm --network host --cap-add NET_ADMIN --cap-add NET_RAW "$IMAGE" firewall --address 10.8.0.1 --interface wg0 --once; }

failures=0
check() { # label, expected, got
	if [ "$2" = "$3" ]; then printf '  ok    %-42s %s\n' "$1" "$3"
	else printf '  FAIL  %-42s %s (expected: %s)\n' "$1" "$3" "$2"; failures=$((failures + 1)); fi
}

echo "member · outside · other VPN"
check "without the rule, all reach the port" "200 200 200" "$(state)"
firewall
check "the rule put by waggle firewall" "200 000 000" "$(state)"
firewall
check "run again, waggle does not put it twice" "1" "$(iptables -S DOCKER-USER | grep -c -- '--ctdir ORIGINAL')"
iptables -S DOCKER-USER | grep -- '--ctorigdst' | sed 's/^/    /'

echo "loopback: this machine · the neighbor, addressing 127.0.0.1"
docker run --detach --rm --name looped --publish 127.0.0.1:18082:80 nginx:1.27-alpine >/dev/null
sleep 2
# The neighbor sends to 127.0.0.1 through the host, instead of its own loopback
ip netns exec outside sh -c 'ip route del table local 127.0.0.0/8 dev lo; ip route del table local 127.0.0.1 dev lo; ip route add 127.0.0.1/32 via 192.0.2.1; sysctl -q -w net.ipv4.conf.outside-0.route_localnet=1'
local_request() { curl --silent --max-time 3 --output /dev/null --write-out '%{http_code}' http://127.0.0.1:18082/ || true; }
# answers: whether the machine answers the neighbor's connection from its loopback port (1) or not (0)
answers() {
	tcpdump -nli h0 -c 1 'src port 18082 and tcp[tcpflags] & (tcp-syn|tcp-ack) == (tcp-syn|tcp-ack)' >/tmp/answers 2>/dev/null &
	pid=$!
	sleep 1
	ip netns exec outside curl --silent --max-time 2 --output /dev/null http://127.0.0.1:18082/ || true
	sleep 1
	kill "$pid" 2>/dev/null || true
	wait "$pid" 2>/dev/null || true
	grep -c 'Flags \[S\.\]' /tmp/answers || true
}
check "the machine reaches its loopback" "200" "$(local_request)"
check "it does not answer the neighbor" "0" "$(answers)"
sysctl -q -w net.ipv4.conf.all.route_localnet=1 net.ipv4.conf.h0.route_localnet=1
check "nor once the interface accepts 127.0.0.1" "0" "$(answers)"
guard=$(iptables -t raw -S | grep -- '-d 127.0.0.1/32' | grep -- '--dport 18082' | grep -- '-j DROP' | head -1)
check "Docker drops it in its raw table" "1" "$(printf '%s' "$guard" | grep -c DROP)"
printf '    %s\n' "$guard"
# The control: without Docker's rule, the machine answers, so the trial did test something
if [ -n "$guard" ]; then
	# shellcheck disable=SC2046 # the rule splits into iptables arguments
	iptables -t raw $(printf '%s' "$guard" | sed 's/^-A/-D/')
	check "control: without Docker's rule, it answers" "1" "$(answers)"
	# shellcheck disable=SC2086 # the rule splits into iptables arguments
	iptables -t raw $guard
fi
[ "$failures" -eq 0 ]
INSIDE
echo "firewall: ok"
