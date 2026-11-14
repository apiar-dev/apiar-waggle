#!/bin/sh
# La règle de pare-feu de waggle (ADR-0001, décision 4), éprouvée par la vraie commande `waggle pare-feu`, dans un
# Docker-in-Docker isolé : il a son propre réseau et son propre pare-feu, et ne touche ni à la VM ni aux proxies du poste.
#
# Dedans :
#   - un port publié sur 10.8.0.1 seulement, comme waggle sur l'adresse d'un VPN ;
#   - « membre », derrière wg0, un membre du tunnel ;
#   - « dehors », derrière une autre interface, un voisin qui route vers 10.8.0.1 ;
#   - « autre », derrière wg1, le membre d'un second VPN, qui route de même vers 10.8.0.1.
# Attendu : sans la règle, tous trois joignent le port ; avec elle, le membre seul.
set -eu

nom=waggle-essai-pare-feu-$$
image=${WAGGLE_IMAGE:-waggle:local}
nettoyer() { docker rm --force --volumes "$nom" >/dev/null 2>&1 || true; }
trap nettoyer EXIT INT TERM

docker run --detach --privileged --name "$nom" docker:29-dind >/dev/null
i=0
until docker exec "$nom" docker info >/dev/null 2>&1; do
	i=$((i + 1))
	[ "$i" -lt 60 ] || { echo "le Docker-in-Docker ne démarre pas" >&2; exit 1; }
	sleep 1
done
docker exec "$nom" apk add --quiet --no-progress iproute2 curl
docker save nginx:1.27-alpine "$image" | docker exec -i "$nom" docker load --quiet >/dev/null

docker exec -i -e IMAGE="$image" "$nom" sh -s <<'DEDANS'
set -eu
relier() { # interface de l'hôte, adresse, espace, adresse de l'espace
	ip netns add "$3"
	ip link add "$1" type veth peer name "$3-0"
	ip link set "$3-0" netns "$3"
	ip addr add "$2" dev "$1" && ip link set "$1" up
	ip netns exec "$3" sh -c "ip addr add $4 dev $3-0 && ip link set $3-0 up && ip link set lo up"
}
relier wg0 10.8.0.1/24 membre 10.8.0.2/24
relier h0 192.0.2.1/30 dehors 192.0.2.2/30
relier wg1 10.9.0.1/24 autre 10.9.0.2/24
ip netns exec dehors ip route add 10.8.0.1/32 via 192.0.2.1
ip netns exec autre ip route add 10.8.0.1/32 via 10.9.0.1
docker run --detach --rm --name cible --publish 10.8.0.1:18081:80 nginx:1.27-alpine >/dev/null
sleep 2

demander() { ip netns exec "$1" curl --silent --max-time 3 --output /dev/null --write-out '%{http_code}' http://10.8.0.1:18081/ || true; }
etat() { echo "$(demander membre) $(demander dehors) $(demander autre)"; }
pare_feu() { docker run --rm --network host --cap-add NET_ADMIN --cap-add NET_RAW "$IMAGE" pare-feu --adresse 10.8.0.1 --interface wg0 --une-fois; }

echets=0
verifier() { # libellé, attendu, obtenu
	if [ "$2" = "$3" ]; then printf '  ok     %-44s %s\n' "$1" "$3"
	else printf '  ÉCHEC  %-44s %s (attendu : %s)\n' "$1" "$3" "$2"; echets=$((echets + 1)); fi
}

echo "membre · voisin · autre VPN"
verifier "sans la règle, tous joignent le port" "200 200 200" "$(etat)"
pare_feu
verifier "la règle posée par waggle pare-feu" "200 000 000" "$(etat)"
pare_feu
verifier "relancé, waggle ne la pose pas deux fois" "1" "$(iptables -S DOCKER-USER | grep -c -- '--ctdir ORIGINAL')"
iptables -S DOCKER-USER | grep -- '--ctorigdst' | sed 's/^/    /'
[ "$echets" -eq 0 ]
DEDANS
echo "pare-feu : ok"
