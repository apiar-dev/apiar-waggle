# Les essais qui demandent un serveur ou un appareil

Ces essais ne se font pas sur un Mac seul. Ils sont préparés ici, à lancer à la main, une fois
waggle installé (`install.sh`, puis les réponses de `waggle setup`). Chacun dit ce qu'il établit, comment le lancer, et
ce qu'on attend.

Ce qu'un Mac seul pouvait éprouver l'est déjà, par `make integration` et `make firewall`. En
particulier, la règle de pare-feu, posée par la vraie commande `waggle firewall`, a été
éprouvée dans un Docker-in-Docker.

Les adresses sont celles d'un exemple, à adapter :
- un serveur dont l'interface WireGuard `wg0` porte `10.8.0.1`, avec une instance de waggle
  sur cette adresse, nommée ici `team` ;
- un Mac, membre du tunnel en `10.8.0.2`.

## 1. La règle `DOCKER-USER`, sur le vrai serveur, vérifiée hors du tunnel

**Ce qu'il établit** (ADR-0001, décision 4) :
- sans la règle, le port publié sur l'adresse du VPN est joignable par une autre interface ;
- la règle de waggle le ferme, sans couper les membres du tunnel ;
- elle revient après un redémarrage de Docker, et du serveur.

**Le voisin.** On le simule par un espace de noms réseau, relié au serveur par une paire
`veth`. Son paquet vers `10.8.0.1` arrive par une autre interface que `wg0`, comme celui
d'un voisin du segment de l'hébergeur. On peut aussi prendre un vrai voisin, une autre
machine du réseau privé de l'hébergeur : `ip route add 10.8.0.1/32 via <adresse du serveur>`.

Sur le serveur, en root :

```bash
VPN=10.8.0.1 WG=wg0 INSTANCE=team
ip netns add waggle-dehors
ip link add waggle-h type veth peer name waggle-d
ip link set waggle-d netns waggle-dehors
ip addr add 192.0.2.1/30 dev waggle-h && ip link set waggle-h up
ip netns exec waggle-dehors sh -c "ip addr add 192.0.2.2/30 dev waggle-d && ip link set waggle-d up && ip route add $VPN/32 via 192.0.2.1"
dehors() { ip netns exec waggle-dehors curl -s -m 3 -o /dev/null -w '%{http_code}\n' http://$VPN/; }
```

Puis, dans l'ordre :

| Étape | Commande | Attendu |
| --- | --- | --- |
| La règle de waggle | `iptables -S DOCKER-USER \| grep ctorigdst` | la règle, avec `--ctdir ORIGINAL` |
| Le voisin | `dehors` | `000` |
| Le Mac, tunnel actif | `curl -s -m 3 -o /dev/null -w '%{http_code}\n' http://10.8.0.1/` | `404` : Traefik répond, aucune route sans nom |
| Sans la règle | `docker stop waggle-$INSTANCE-firewall-1`, puis `iptables -D DOCKER-USER -m conntrack --ctorigdst $VPN --ctdir ORIGINAL ! -i $WG -j DROP`, puis `dehors` | `404` : c'est la fuite que la règle ferme. Avec `000`, Docker ferme déjà ce chemin sur ce serveur, et la règle reste en défense |
| La règle reposée | `docker start waggle-$INSTANCE-firewall-1`, puis `dehors` | `000` |
| Docker redémarré | `systemctl restart docker`, une minute, puis `dehors` | `000` : le service `firewall` redémarre avec Docker et repose la règle |
| Le serveur redémarré | `reboot`, puis la même vérification | `000` |

À noter : le délai entre le démarrage de Docker et la règle reposée, d'après le journal
(`docker logs waggle-$INSTANCE-firewall-1`).

Le retrait du voisin :

```bash
ip netns del waggle-dehors; ip link del waggle-h 2>/dev/null
```

## 2. Le port 53 libre sur l'adresse du VPN

**Ce qu'il établit.** Le DNS du VPN peut s'y publier (ADR-0001, décision 6).

Sur le serveur : `ss -lunpt | grep ':53 '`. Il est attendu que seul `systemd-resolved` y
écoute, sur `127.0.0.53` et `127.0.0.54`. Un service à l'écoute sur `0.0.0.0:53` empêcherait
la publication, et `waggle up` le dirait.

Puis, depuis le Mac, tunnel actif, une stack levée sur le serveur :

```bash
dig +short @10.8.0.1 <dossier>.<domaine>
```

Attendu : `10.8.0.1`. Puis `dig +short @10.8.0.1 www.<domaine>` : la vraie réponse du domaine, pas
`10.8.0.1` (ADR-0003, décision 8). Et la liste que le serveur publie à ses postes :

```bash
dig +short @10.8.0.1 relay.waggle TXT
```

Attendu : une ligne par domaine servi, avec son mode, et ses ports historiques (décision 9).

## 3. Un conteneur de Docker Desktop joint l'adresse du serveur par le tunnel du Mac

**Ce qu'il établit.** Le relais part du Traefik du Mac, un conteneur. Docker Desktop dit
faire ressortir le trafic des conteneurs sur le Mac lui-même, VPN compris (ADR-0001,
décision 5).

Sur le Mac, tunnel actif :

```bash
docker run --rm alpine:3.20 wget -q -T 3 -O /dev/null -S http://10.8.0.1/ 2>&1 | head -1
```

Attendu : `HTTP/1.1 404 Not Found`. Le Traefik du serveur répond : le paquet est entré par
`wg0`.

## 4. Plusieurs tunnels WireGuard actifs à la fois sur un Mac

**Ce qu'il établit.** Un poste peut relayer vers deux serveurs de deux VPN (ADR-0001,
décision 5). L'app WireGuard de macOS n'en active peut-être qu'un à la fois.

1. **Dans l'app**, deux tunnels vers deux serveurs : activer l'un, puis l'autre. Les deux
   restent-ils actifs ? Pour le voir : `ifconfig | grep -A3 '^utun'` et
   `netstat -rn -f inet | grep utun`, puis un `curl` vers chaque serveur.
2. **Si l'app n'en garde qu'un** : un seul tunnel, à deux pairs et deux adresses. Chaque
   serveur connaît la clé publique du Mac, avec l'adresse du Mac sur son VPN.

   ```ini
   [Interface]
   PrivateKey = <clé du Mac>
   Address = 10.8.0.2/32, 10.9.0.2/32

   [Peer]
   # serveur A
   PublicKey = <clé de A>
   Endpoint = <hôte de A>:51820
   AllowedIPs = 10.8.0.0/24

   [Peer]
   # serveur B
   PublicKey = <clé de B>
   Endpoint = <hôte de B>:51820
   AllowedIPs = 10.9.0.0/24
   ```

   Aucune ligne `DNS` : le poste n'a pas besoin du DNS du VPN, son relais suffit.
3. **Autre voie** : `wg-quick`, des `wireguard-tools` de Homebrew, qui lève une interface par
   tunnel.

À noter : la voie qui marche. Les réglages de waggle et sa documentation la suivront.

## 5. Un téléphone, Android et iOS, avec l'autorité de waggle

**Ce qu'il établit.** Un téléphone peut ouvrir en HTTPS une stack du serveur par le DNS du
VPN (ADR-0001, décisions 6 et 8 ; ADR-0002, décision 2). L'autorité de waggle est en X.509 v3,
`CA:TRUE` ; ses certificats durent 397 jours, en ECDSA P-256.

L'autorité de l'instance s'obtient par `waggle ca team > ca.crt`, sur le
serveur.

**Android** (le Pixel) :
1. Le tunnel de l'app WireGuard porte `DNS = 10.8.0.1`. Ouvrir
   `https://<dossier>.<domaine du DNS>` dans Chrome, avant d'installer l'autorité. Attendu :
   un avertissement de certificat. Le nom résout donc, par le DNS du tunnel.
2. Installer l'autorité : Paramètres → Sécurité et confidentialité → Plus de paramètres de
   sécurité → Chiffrement et identifiants → Installer un certificat → Certificat CA. À noter :
   le chemin exact sur le Pixel.
3. Rouvrir la page dans Chrome. Attendu : la page, sans avertissement.
4. Le DNS privé : le régler sur un fournisseur (`dns.google`), puis rouvrir la page. Attendu :
   elle ne s'ouvre plus. Ce réglage contournerait le DNS du tunnel, et le README le dit. Le
   remettre en « Automatique », et constater que la page revient.
5. Facultatif : Firefox, avec « Utiliser les certificats d'autorité tiers » activé.

**iOS** :
1. Envoyer `ca.crt` à l'iPhone (AirDrop ou courriel), puis l'installer : Réglages →
   Profil téléchargé.
2. Réglages → Général → Informations → Réglages de confiance des certificats : activer
   l'autorité.
3. Le tunnel actif, avec `DNS = 10.8.0.1`, ouvrir `https://<dossier>.<domaine du DNS>` dans
   Safari. Attendu : la page, sans avertissement.

**Un projet resté en passage** présente son propre certificat, signé par l'autorité du
projet. Si celle-ci est en X.509 v1, sans `basicConstraints`, refaire les étapes avec elle :
- Android pourrait refuser de l'installer comme autorité ;
- iOS pourrait refuser de la proposer à la confiance totale.

Il faudrait alors au projet une autorité en v3. C'est une décision du projet, pas de waggle.

## 6. Sur un serveur, une stack qui joint sa propre URL

**Ce qu'il établit.** C'est le coût que l'ADR-0002 laisse ouvert. Les e2e d'une stack visent
sa vraie URL. Sur un serveur, cette URL est l'adresse du VPN, et la règle de pare-feu refuse
ce qui vient des ponts de Docker.

Sur le serveur, waggle servant `<nom>` en HTTPS :

```bash
docker run --rm --add-host <nom>:10.8.0.1 alpine:3.20 wget -q -T 3 -O /dev/null -S --no-check-certificate https://<nom>/ 2>&1 | head -1
```

Attendu avec la règle actuelle : rien, au bout de trois secondes. C'est le constat qui
décidera du prochain incrément : une exception pour les ponts de Docker, ou le client de la
stack branché sur le réseau de waggle.

## 7. Un agent du serveur joint ses stacks par la boucle locale

**Ce qu'il établit.** Le serveur teste ses propres projets sous leurs vraies URL (ADR-0003,
décision 2) : les agents qui y travaillent ouvrent une stack comme on le ferait du Mac.

Sur le serveur, l'instance `team` avec `loopback: true`, et une stack levée :

```bash
docker version --format '{{.Server.Version}}'
```

Attendu : 28 ou plus. C'est à partir de là que Docker refuse, dans sa table `raw`, ce qui vise
`127.0.0.1` sans entrer par `lo`. `sudo iptables -t raw -S | grep 127.0.0.1` montre une règle
par port publié.

Puis, toujours sur le serveur :

```bash
curl -sS -o /dev/null -w '%{http_code}\n' https://<dossier>.<domaine>/
```

```bash
curl -sS -o /dev/null -w '%{http_code}\n' http://admin.<dossier>.localhost/
```

Attendu : la réponse de la stack, sans `--resolve` ni `/etc/hosts`. Le DNS public de
`<domaine>` répond `127.0.0.1`, et `curl` envoie `*.localhost` à la boucle locale. En passage
TLS, `curl` doit faire confiance à l'autorité du projet ; en HTTPS terminé par waggle, à celle
de l'instance (`waggle ca team`).

Puis Playwright, ou le navigateur qu'un agent pilote, sur la même URL.

## 8. Le VPS sous un autre VPN que WireGuard, Tailscale par exemple

**Ce qu'il établit.** waggle prend le VPN qui est là (ADR-0003, décision 7).

Sur le serveur, Tailscale levé :
1. `waggle setup` propose `tailscale0 (Tailscale), 100.x.y.z`. Attendu : l'instance sur cette
   adresse, avec `loopback: true`, et un relais qui fait confiance à `100.64.0.0/10`.
2. La règle de waggle à côté des chaînes de Tailscale :
   `sudo iptables -S FORWARD | head -5`, puis l'essai 1, avec `tailscale0` à la place de
   `wg0`. Attendu : le membre du tailnet servi, le voisin refusé.

Sur le Mac, Tailscale levé :
1. L'essai 3, vers l'adresse Tailscale du serveur : un conteneur de Docker Desktop la joint.
2. `waggle setup`, avec l'adresse du serveur : il dit ce que le serveur sert. Puis une stack du
   serveur, dans le navigateur du Mac, sous son URL.

Sur le téléphone : dans la console de Tailscale, DNS → un serveur de noms, l'adresse du serveur,
restreint au domaine de dev. Puis l'essai 5, sans l'app WireGuard.
