# ADR-0003 — Travailler en local, sur un serveur, ou les deux : waggle sur chaque machine qui ouvre ou teste une URL

**Statut** : **Proposé** (2026-11-14).
- Les décisions 1 à 5, le modèle, ont été accordées par Benjamin en conversation, le 2026-11-14.
- Les décisions 6 et 7, l'installation et WireGuard, restent à trancher.

**Amende** l'ADR-0001, décision 4 : un serveur écoute aussi sur sa boucle locale.
**Zone** : où waggle tourne, selon la machine où l'on travaille, et comment on l'y installe.

## Contexte

Benjamin décrit trois façons de travailler, qui doivent toutes marcher, pour chacun des
projets :

1. **Tout en local** : sur le Mac, et l'agent travaille dans des worktrees du Mac.
2. **Parfois en local, parfois sur un VPS** : par un outil qui y crée les worktrees, ou en se
   connectant au VPS pour y créer un worktree et y lancer une session.
3. **Tout sur le VPS** : le Mac n'a pas le projet du tout, mais il ouvre ses URL par le VPN.

Il y ajoute une exigence : **le VPS doit pouvoir tester ses propres projets**. Les agents y
lancent la QA et les tests pendant leurs sessions.

Les projets ont deux profils :
- **en passage TLS**, sous un vrai domaine (`https://<dossier>.dev.example.com`), sur le 443 et
  des ports historiques ;
- **en HTTP**, sous des noms en `.localhost` (`http://admin.<dossier>.localhost`).

### Ce que fait le navigateur

Un navigateur envoie toujours un nom de dev au `127.0.0.1` de sa propre machine :
- pour `*.dev.example.com`, c'est le DNS public qui répond `127.0.0.1` ;
- pour `*.localhost`, le navigateur ne demande même pas : c'est la boucle locale, par
  définition (RFC 6761).

Sur la machine du navigateur, quelque chose doit donc écouter sur `127.0.0.1`.

### Ce qui manque après l'ADR-0001

- **Le serveur n'écoute que sur son adresse WireGuard.** Sur le VPS, les mêmes noms
  résolvent vers `127.0.0.1`, où rien n'écoute. Un agent qui ouvre l'URL de sa stack depuis
  le VPS, par `curl`, Playwright ou un navigateur, n'a donc pas de réponse.
- **Les e2e qui tournent dans la stack marchent déjà** : la stack fait résoudre son propre
  hôte vers son point d'entrée, par un alias réseau, ou par le résolveur du navigateur de test.
- **Le cas 3 n'est pas décrit** : un poste qui ne sert rien, et ne fait que passer.

## Décision

1. **waggle tourne sur chaque machine qui ouvre ou teste une URL de dev.**
   - Le Mac l'a dès qu'on y ouvre une URL, même s'il n'a aucun projet.
   - Le serveur l'a dès qu'il lève des stacks.
   - La raison est celle du navigateur : il envoie le nom à sa propre boucle locale.

2. **Chaque machine sert ses stacks sur sa boucle locale**, pour ses navigateurs et ses
   agents.
   - Un poste le fait déjà : son instance écoute sur `127.0.0.1`.
   - Un serveur le fait par `loopback: true` : son instance publie aussi ses points d'entrée
     (80, 443, les ports historiques) sur `127.0.0.1`. Le port du relais reste sur l'adresse
     du VPN : seuls les postes l'atteignent, par le tunnel.
   - **Une seule instance par machine sert sa boucle locale.** Sur un serveur à deux VPN, les
     agents du serveur n'atteignent donc, par la boucle locale, que les projets de l'une des
     deux.
   - **Une adresse de boucle ne s'ouvre pas au voisin.** Docker refuse, dans sa table `raw`,
     tout paquet vers `127.0.0.1` qui n'entre pas par `lo` (§ « Les essais »).

3. **Un serveur partage ses stacks avec un VPN, sur son adresse WireGuard**, comme le dit
   l'ADR-0001 : le pare-feu, le DNS du VPN, le point d'entrée `relay`. **Un poste ne partage
   jamais** les siennes.

4. **Un poste relaie au serveur ce qu'il ne sert pas.** Les trois façons de travailler ne
   diffèrent que par l'endroit où tournent les stacks :

   | | Le Mac | Le VPS |
   | --- | --- | --- |
   | 1. Tout en local | waggle, sans relais | rien |
   | 2. Les deux | waggle, avec un relais vers le VPS : il sert ses stacks, et passe le reste | waggle, `loopback: true` |
   | 3. Tout sur le VPS | waggle, avec le même relais, sans aucune stack : il ne fait que passer | waggle, `loopback: true` |

   - **Les URL sont les mêmes partout**, quelle que soit la machine de la stack. Si un nom
     existe des deux côtés, le poste garde le sien.
   - **La façon de créer le worktree n'y change rien.** waggle lit les étiquettes des
     conteneurs : peu importe qu'un outil, un agent ou un humain ait créé le worktree et lancé
     la stack.
   - **C'est le même chemin pour tous les profils de projet** : le passage TLS sous un vrai
     domaine et le HTTP en `.localhost` passent par le même relais.

5. **Un appareil sans waggle passe par le DNS du VPN**, comme un téléphone. C'est aussi le
   cas d'un Mac sans Docker. Il n'ouvre alors que de vrais domaines : `dev.example.com`, oui ;
   un nom en `.localhost`, jamais.

6. **L'installation : une seule commande partout, `install.sh`** *(à trancher)*.
   - **Sur un Mac**, rien ne change : l'installeur écrit la configuration du poste, lève
     l'instance et approuve son autorité.
   - **Sur un serveur Linux sans configuration**, l'installeur écrit celle qu'il peut déduire,
     puis lève l'instance :
     - **une interface WireGuard** : l'instance de ce VPN, sur son adresse, avec
       `loopback: true` ;
     - **aucune** : la boucle locale seule. Le serveur sert alors ses propres agents, et
       personne d'autre ;
     - **plusieurs** : l'installeur s'arrête. La configuration s'écrit à la main, une instance
       par VPN.
   - **Ce qui reste à écrire à la main** : les ports historiques d'un projet, les domaines du
     DNS du VPN, et, sur le poste, les relais vers le serveur.

7. **WireGuard : waggle peut le monter sur un serveur, jamais sur un poste ni sur un
   téléphone** *(à trancher)*.
   - **Sur un serveur**, il le ferait par une commande à part, explicite, que l'on lance par
     `sudo` : `waggle vpn init` monte le VPN, et `waggle vpn add <appareil>` ajoute un
     appareil. Cette seconde commande fait ce qui est fastidieux : la paire de clés, l'adresse
     dans le réseau du VPN, le fichier de configuration de l'appareil et son QR code.
   - **Le VPN reste un WireGuard standard** : `/etc/wireguard/wg0.conf`, sous
     `wg-quick@wg0`. On le lit par `wg show`, et on le retire sans waggle.
   - **Un WireGuard existant n'est jamais touché.** waggle en lit l'adresse, comme
     aujourd'hui.
   - **Sur un poste et sur un téléphone**, c'est l'application officielle de WireGuard. Elle
     importe le fichier ou le QR code que le serveur a produit. Le système du poste n'a rien à
     installer de plus.
   - **Pourquoi pas dans `install.sh`** : monter un VPN demande `root`, ouvre un port UDP vers
     internet et touche au réseau de la machine. C'est un geste que l'on fait sciemment, une
     fois, pas une étape cachée d'une installation que l'on relance pour mettre à jour.

## Les essais

### Faits, le 2026-11-14

| Fait | Comment |
| --- | --- |
| Une machine atteint un port publié sur sa boucle locale | `make firewall`, Docker-in-Docker (Docker 29) : `200` |
| Elle ne répond pas à un voisin qui vise `127.0.0.1` | même essai : aucun `SYN-ACK` |
| Elle ne lui répond pas non plus quand l'interface accepte ces paquets (`route_localnet`) | même essai : aucun `SYN-ACK` |
| C'est Docker qui les refuse, dans sa table `raw` | `-A PREROUTING -d 127.0.0.1/32 ! -i lo -p tcp --dport <port> -j DROP` |
| Sans cette règle, la machine répondrait : le chemin est réel | même essai, la règle retirée un instant : un `SYN-ACK` |
| macOS refuse à un utilisateur l'écoute sur un port privilégié de `127.0.0.1` | macOS 26.4.1, sans root : `127.0.0.1:444` et `127.0.0.1:81` refusés |

La mesure porte sur la réponse de la machine, pas sur le `curl` du voisin. Le noyau du voisin
jette lui-même un `SYN-ACK` venu de `127.0.0.1`, mais un client forgé ne le ferait pas.

### À éprouver sur le VPS

- Un agent du VPS joint l'URL de sa stack par la boucle locale : `curl`, puis Playwright.
- La version de Docker du VPS pose la règle `raw` (Docker 28 et suivants).
- Les essais de `docs/essais/0001-sur-un-serveur-et-un-appareil.md`, qui restent à faire :
  l'installeur sur Linux, le pare-feu, le DNS du VPN, le relais depuis Docker Desktop.

## Alternatives écartées

- **Le Mac passe par le DNS du VPN, sans waggle.**
  - Les noms en `.localhost` n'y vont jamais : le navigateur les garde pour sa boucle
    locale.
  - Tant que le tunnel tourne, les stacks locales d'un même domaine ne répondent plus : le DNS
    du VPN envoie tout le domaine au serveur.
  - Ce chemin reste celui d'un appareil sans waggle (décision 5).
- **Un tunnel SSH du Mac vers la boucle locale du VPS, à la place de WireGuard.**
  - Il demande `root` sur le Mac pour écouter sur 80 et 443 (§ « Les essais »).
  - Il tient le temps d'une connexion, et n'apporte rien au téléphone.
  - Il reste possible à la main, avec `sudo ssh -L`.
- **Une seconde instance, sur la boucle locale du serveur, qui lirait les mêmes projets.**
  Chaque stack aurait deux contrôleurs, deux autorités, et un réseau partagé entre les
  projets de VPN différents.
- **Installer WireGuard dans `install.sh`.** Monter un VPN serait une étape cachée d'une
  commande que l'on relance pour mettre à jour (décision 7).
- **Tailscale, ou headscale.** Ajouter un appareil y est plus simple, mais au prix d'un
  service tiers, ou d'un serveur de coordination de plus à tenir. L'ADR-0001 a choisi
  WireGuard.

## Conséquences

- **Pour les projets, rien ne change** : les mêmes étiquettes servent les trois façons de
  travailler.
- **Sur le Mac**, les cas 2 et 3 ajoutent un relais à la configuration :
  `relays: [{to: <adresse WireGuard du VPS>, domains: [...]}]`.
- **Sur le VPS**, `loopback: true`, que l'installeur pose seul quand le VPS a une interface
  WireGuard.
- **Les e2e d'une stack restent dans la stack.** Une stack qui joindrait sa propre URL par
  l'adresse du VPN reste refusée par le pare-feu (`docs/essais`, § 6).
- **Ce qui reste ouvert** :
  - les décisions 6 et 7 ;
  - un poste qui apprendrait du serveur la liste de ses domaines, pour ne plus l'écrire dans
    ses relais ;
  - un DNS du VPN qui déduirait ses domaines des routes servies, pour ne plus les écrire dans
    `dns`.
