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
docker exec "$name" apk add --quiet --no-progress iproute2 curl
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
[ "$failures" -eq 0 ]
INSIDE
echo "firewall: ok"
