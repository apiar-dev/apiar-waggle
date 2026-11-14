# Les essais qui demandent un serveur ou un appareil

Ces essais ne se font pas sur un Mac seul. Ils sont préparés ici, à lancer à la main, une fois
waggle installé (`bin/waggle installer`). Chacun dit ce qu'il établit, comment le lancer, et
ce qu'on attend.

Ce qu'un Mac seul pouvait éprouver l'est déjà, par `make integration` et `make pare-feu`. En
particulier, la règle de pare-feu, posée par la vraie commande `waggle pare-feu`, a été
éprouvée dans un Docker-in-Docker.

Les adresses sont celles d'un exemple, à adapter :
- un serveur dont l'interface WireGuard `wg0` porte `10.8.0.1`, avec une instance de waggle
  sur cette adresse, nommée ici `equipe` ;
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
VPN=10.8.0.1 WG=wg0 INSTANCE=equipe
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
| Sans la règle | `docker stop waggle-$INSTANCE-pare-feu-1`, puis `iptables -D DOCKER-USER -m conntrack --ctorigdst $VPN --ctdir ORIGINAL ! -i $WG -j DROP`, puis `dehors` | `404` : c'est la fuite que la règle ferme. Avec `000`, Docker ferme déjà ce chemin sur ce serveur, et la règle reste en défense |
| La règle reposée | `docker start waggle-$INSTANCE-pare-feu-1`, puis `dehors` | `000` |
| Docker redémarré | `systemctl restart docker`, une minute, puis `dehors` | `000` : le service `pare-feu` redémarre avec Docker et repose la règle |
| Le serveur redémarré | `reboot`, puis la même vérification | `000` |

À noter : le délai entre le démarrage de Docker et la règle reposée, d'après le journal
(`docker logs waggle-$INSTANCE-pare-feu-1`).

Le retrait du voisin :

```bash
ip netns del waggle-dehors; ip link del waggle-h 2>/dev/null
```

## 2. Le port 53 libre sur l'adresse du VPN

**Ce qu'il établit.** Le DNS du VPN peut s'y publier (ADR-0001, décision 6).

Sur le serveur : `ss -lunpt | grep ':53 '`. Il est attendu que seul `systemd-resolved` y
écoute, sur `127.0.0.53` et `127.0.0.54`. Un service à l'écoute sur `0.0.0.0:53` empêcherait
la publication, et `bin/waggle installer` le dirait.

Puis, depuis le Mac, tunnel actif : `dig +short @10.8.0.1 <dossier>.<domaine du DNS>`. Attendu :
`10.8.0.1`.

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

L'autorité de l'instance s'obtient par `bin/waggle autorite equipe > autorite.crt`, sur le
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
1. Envoyer `autorite.crt` à l'iPhone (AirDrop ou courriel), puis l'installer : Réglages →
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
