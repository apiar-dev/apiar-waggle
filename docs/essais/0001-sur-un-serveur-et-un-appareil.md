# Les essais de l'ADR-0001 qui demandent un serveur ou un appareil

Ces essais ne se font pas sur un Mac seul. Ils sont préparés ici, à lancer à la main. Chacun
dit ce qu'il établit, comment le lancer, ce qu'on attend, et comment le retirer.

Les adresses sont celles d'un exemple, à adapter :
- un serveur dont l'interface WireGuard `wg0` porte `10.8.0.1` ;
- un Mac, membre du tunnel en `10.8.0.2`.

## 1. La règle `DOCKER-USER`, sur le vrai serveur, vérifiée hors du tunnel

**Ce qu'il établit.** Il établit trois choses (ADR-0001, décision 4) :
- un port publié sur l'adresse du VPN reste joignable par une autre interface ;
- la règle le ferme, sans couper les membres du tunnel ;
- la règle survit, ou non, à un redémarrage de Docker et du serveur.

La logique a déjà été éprouvée dans un Docker-in-Docker. Reste le vrai noyau, le vrai
WireGuard et la vraie persistance.

**Le voisin.** On le simule par un espace de noms réseau, relié au serveur par une paire
`veth`. Son paquet vers `10.8.0.1` arrive par une autre interface que `wg0`, comme celui
d'un voisin du segment de l'hébergeur. On peut aussi prendre un vrai voisin, une autre
machine du réseau privé de l'hébergeur : `ip route add 10.8.0.1/32 via <adresse du serveur>`.

Sur le serveur, en root :

```bash
VPN=10.8.0.1 WG=wg0
docker run -d --rm --name waggle-essai -p $VPN:18081:80 nginx:1.27-alpine
ip netns add waggle-dehors
ip link add waggle-h type veth peer name waggle-d
ip link set waggle-d netns waggle-dehors
ip addr add 192.0.2.1/30 dev waggle-h && ip link set waggle-h up
ip netns exec waggle-dehors sh -c "ip addr add 192.0.2.2/30 dev waggle-d && ip link set waggle-d up && ip route add $VPN/32 via 192.0.2.1"
dehors() { ip netns exec waggle-dehors curl -s -m 3 -o /dev/null -w '%{http_code}\n' http://$VPN:18081/; }
```

Puis, dans l'ordre :

| Étape | Commande | Attendu |
| --- | --- | --- |
| Sans règle, le voisin | `dehors` | `200` : c'est la fuite. Avec `000`, Docker ferme déjà ce chemin sur ce serveur, et la règle reste en défense |
| Sans règle, le Mac, tunnel actif | `curl -s -m 3 -o /dev/null -w '%{http_code}\n' http://10.8.0.1:18081/` | `200` |
| Poser la règle | `iptables -I DOCKER-USER -m conntrack --ctorigdst $VPN --ctdir ORIGINAL ! -i $WG -j DROP` | |
| Avec la règle, le voisin | `dehors` | `000` |
| Avec la règle, le Mac | la même commande depuis le Mac | `200` |
| Après un redémarrage de Docker | `systemctl restart docker && iptables -S DOCKER-USER`, puis relancer le conteneur | à noter : la règle est-elle encore là ? |

Le retrait :

```bash
iptables -D DOCKER-USER -m conntrack --ctorigdst $VPN --ctdir ORIGINAL ! -i $WG -j DROP
docker rm -f waggle-essai; ip netns del waggle-dehors; ip link del waggle-h 2>/dev/null
```

Le redémarrage du serveur se constate à part, règle posée. Sans rien pour la reposer, elle
disparaît. C'est ce que l'installation de waggle devra faire.

## 2. Le port 53 libre sur l'adresse du VPN

**Ce qu'il établit.** Le DNS du VPN peut s'y publier (décision 6).

Sur le serveur : `ss -lunpt | grep ':53 '`. Il est attendu que seul `systemd-resolved` y
écoute, sur `127.0.0.53` et `127.0.0.54`. Un service à l'écoute sur `0.0.0.0:53` empêcherait
la publication.

## 3. Un conteneur de Docker Desktop joint l'adresse du serveur par le tunnel du Mac

**Ce qu'il établit.** Le relais part du Traefik du Mac, un conteneur. Docker Desktop dit
faire ressortir le trafic des conteneurs sur le Mac lui-même, VPN compris (décision 5).

Pendant l'essai 1, conteneur `waggle-essai` levé et règle posée, sur le Mac, tunnel actif :

```bash
docker run --rm alpine:3.20 wget -q -T 3 -O /dev/null -S http://10.8.0.1:18081/ 2>&1 | head -1
```

Attendu : `HTTP/1.1 200 OK`. Le paquet entre au serveur par `wg0`, comme celui du Mac.

## 4. Plusieurs tunnels WireGuard actifs à la fois sur un Mac

**Ce qu'il établit.** Un poste peut relayer vers deux serveurs de deux VPN (décision 5).
L'app WireGuard de macOS n'en active peut-être qu'un à la fois.

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

## 5. La confiance totale d'iOS envers une autorité en X.509 v1

**Ce qu'il établit.** Un téléphone peut ouvrir en HTTPS une stack d'un projet en TLS, par le
DNS du VPN (décisions 6 et 8). L'autorité d'un projet existant est un certificat X.509 v1,
sans `basicConstraints`.

1. Lire la version de l'autorité sur le Mac :
   `openssl x509 -in <autorité>.pem -noout -text | grep -E 'Version|CA:'`.
2. L'envoyer à l'iPhone (AirDrop ou courriel), puis l'installer : Réglages → Profil
   téléchargé.
3. Réglages → Général → Informations → Réglages de confiance des certificats : l'autorité
   y figure-t-elle, avec son interrupteur ? L'activer.
4. Le tunnel actif, avec `DNS = 10.8.0.1` dans sa configuration, ouvrir
   `https://<dossier>.<domaine de dev>` dans Safari. Attendu : la page, sans avertissement.

Si l'autorité n'apparaît pas à l'étape 3, iOS refuse la confiance totale à une autorité v1.
Il faudra au projet une autorité en X.509 v3 (`basicConstraints = CA:TRUE`) : c'est une
décision du projet, pas de waggle.
