# ADR-0003 — Travailler en local, sur un serveur, ou les deux : waggle sur chaque machine qui ouvre ou teste une URL

**Statut** : **Proposé** (2026-11-14, complété le 2026-11-15).
- Les décisions 1 à 5, le modèle, ont été accordées par Benjamin en conversation, le 2026-11-14.
- Les décisions 6 à 9, l'installation, le VPN, le DNS et les relais, l'ont été le 2026-11-15.
- Reste à les éprouver sur le VPS (§ « Les essais »).

**Amende** l'ADR-0001 :
- décision 4 : un serveur écoute aussi sur sa boucle locale, et sur l'interface de n'importe
  quel VPN, pas seulement WireGuard ;
- décision 6 : le DNS du VPN déduit ses noms des routes servies ;
- l'alternative du VPN managé, qui n'est plus écartée : waggle prend le VPN qui est là.

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

3. **Un serveur partage ses stacks avec un VPN, sur son adresse dans ce VPN**, comme le dit
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

6. **L'installation : `install.sh` installe, `waggle setup` configure.**
   - **`install.sh` installe ou met à jour**, et rien d'autre : le clone, le lien, l'image.
     Avec une configuration, il relance les instances, et approuve l'autorité sur un Mac.
     Sans configuration, il lance `waggle setup`, qui pose ses questions sur le terminal.
   - **`waggle setup` pose une question par machine**, et propose en réponse ce qu'il
     détecte :
     - **sur Linux** : d'autres appareils joignent-ils cette machine ? Non : elle ne sert que
       ses propres agents, sur `127.0.0.1`. Oui, par l'une des interfaces de VPN détectées.
       Oui, par une autre interface, que l'on nomme ;
     - **sur une machine qui ne partage pas**, un Mac toujours : ouvre-t-on les stacks d'un
       serveur ? Si oui, son adresse sur le VPN.
   - **Il montre le fichier, l'écrit, puis lève les instances**, et approuve l'autorité sur un
     Mac. L'ancien fichier reste à côté (`config.yaml.bak`). On le relance pour changer
     d'avis : le jour où l'on prend un VPS, par exemple.
   - **Sans terminal, il ne devine rien.** Un agent ou un script donne les réponses en
     options : `--share none` ou `--share <interface>`, `--relay <serveur>`. Sur un Mac, la
     réponse par défaut est sans danger : un poste, sans relais.
   - **Pourquoi pas la détection seule**, qu'avait proposée la première version de cette
     décision :
     - un VPN qui n'est pas WireGuard n'est pas vu, et le serveur ne servirait que ses
       propres agents, sans que personne ne sache pourquoi ;
     - un WireGuard qui sert à autre chose, vers le réseau d'une entreprise par exemple,
       verrait waggle y ouvrir les stacks de dev sans qu'on le lui ait demandé.
   - **Les ports historiques ne s'écrivent plus.** `waggle up` les lit dans les routes des
     projets, sur leurs conteneurs, levés ou non, et les ajoute à `ports`. Une route sur un
     port nouveau reste refusée jusqu'au `waggle up` suivant, et le journal du contrôleur le
     dit.

7. **Le VPN : waggle prend celui qui est là.**
   - **Toute interface de tunnel est acceptée** : WireGuard (`DEVTYPE=wireguard`), ou TUN et
     TAP (`tun_flags`). Cela couvre WireGuard, Tailscale et headscale, NetBird, ZeroTier,
     Nebula et OpenVPN. Les ponts de Docker et les cartes physiques en sont exclus.
   - **Une autre interface**, pour IPsec, un réseau local ou le réseau privé d'un hébergeur,
     ne sert que si on la nomme (`interface:`). Sur un serveur dans le nuage, une adresse
     privée peut être joignable depuis internet : `waggle setup` le rappelle.
   - **L'adresse est toujours privée** : RFC 1918, ou `100.64.0.0/10`. Une adresse publique
     reste refusée.
   - **Le pare-feu ne change pas.** Sa règle ferme l'adresse à tout ce qui n'entre pas par
     son interface, quel que soit le VPN.
   - **Le réseau de confiance du relais** est celui de l'interface. Tailscale et NetBird y
     posent une adresse seule (`/32`) : waggle prend alors `100.64.0.0/10`, la plage qu'ils
     utilisent, et demande `relay_listener.trusted_ips` pour les autres.
   - **Sur un téléphone, un seul VPN est actif à la fois**, sous iOS comme sous Android. Un
     développeur déjà sous Tailscale ne peut pas y ajouter un autre tunnel sans couper le
     premier. waggle doit donc marcher avec le VPN que le téléphone a déjà.
   - **Le DNS d'un téléphone se règle dans le VPN** : la ligne `DNS =` d'un tunnel
     WireGuard ; le DNS partagé de Tailscale (*split DNS*), qui envoie le domaine de dev à
     l'adresse du serveur ; les réglages DNS de ZeroTier ou de NetBird.
   - **waggle ne monte pas de VPN.** Une commande `waggle vpn`, qui monterait un WireGuard
     standard sur un serveur qui n'en a aucun, attend un besoin réel : sans VPN, Tailscale est
     aujourd'hui le plus simple, sans port à ouvrir, avec une application pour téléphone, et
     waggle n'a qu'à s'en servir. Si elle vient, elle refusera de tourner à côté d'un autre
     VPN, et restera hors de `install.sh` : monter un VPN demande `root`, ouvre un port UDP
     vers internet et touche au réseau de la machine.

8. **Le DNS du VPN déduit ses noms des routes servies.**
   - **Une zone par nom de stack servi sous un vrai domaine**, que le contrôleur écrit et que
     CoreDNS charge seul (plugin `auto`) : `<stack>.<domaine>` et tout ce qui est dessous
     répondent l'adresse de l'instance, rien en AAAA. Le reste est relayé.
   - **Jamais tout un domaine.** Répondre pour tout `example.com` prendrait au téléphone les
     vrais sites de l'entreprise.
   - **Les noms en `.localhost` n'y sont jamais** : un téléphone ne les ouvre pas.
   - **Il tourne sur toute instance qui partage ses stacks**, sans réglage. `dns: false`
     l'éteint, quand le port 53 de l'adresse est déjà pris, par le DNS du VPN lui-même par
     exemple.
   - **La clé `dns: [domaines]` disparaît.**

9. **Un poste apprend du serveur ce qu'il relaie.**
   - **Le poste n'écrit plus que l'adresse du serveur** : `relays: [{to: <serveur>}]`.
   - **Le DNS du serveur publie**, en TXT sous `relay.waggle`, les domaines qu'il a servis,
     avec leur mode, et ses ports historiques. La liste ne fait que grandir : le domaine d'une
     stack arrêtée y reste.
   - **À chaque `waggle up`, le poste la lit**, relaie ces domaines et publie ces ports. Si le
     serveur ne répond pas, il garde la dernière liste lue.
   - **Le poste la lit en TCP.** Docker Desktop, où tourne le waggle d'un Mac, ne fait pas
     passer l'UDP vers l'adresse d'un VPN ; le TCP passe. Le DNS du serveur répond aux deux.
   - **Une nouvelle stack d'un projet connu ne demande rien.** Un nouveau projet sur le
     serveur demande un `waggle up` sur le poste, et `waggle status` le signale.
   - **`domains:` reste possible, à la main.** Elle remplace la liste apprise : pour
     `http://*`, ou pour un serveur sans DNS (`dns: false`).

## Les essais

### Faits

| Fait | Comment |
| --- | --- |
| Une machine atteint un port publié sur sa boucle locale | `make firewall`, Docker-in-Docker (Docker 29) : `200` |
| Elle ne répond pas à un voisin qui vise `127.0.0.1` | même essai : aucun `SYN-ACK` |
| Elle ne lui répond pas non plus quand l'interface accepte ces paquets (`route_localnet`) | même essai : aucun `SYN-ACK` |
| C'est Docker qui les refuse, dans sa table `raw` | `-A PREROUTING -d 127.0.0.1/32 ! -i lo -p tcp --dport <port> -j DROP` |
| Sans cette règle, la machine répondrait : le chemin est réel | même essai, la règle retirée un instant : un `SYN-ACK` |
| macOS refuse à un utilisateur l'écoute sur un port privilégié de `127.0.0.1` | macOS 26.4.1, sans root : `127.0.0.1:444` et `127.0.0.1:81` refusés |
| CoreDNS charge seul les zones d'un dossier, et relaie le reste (2026-11-15) | `coredns/coredns:1.11.3`, plugin `auto`, `reload 2s` : le nom de la stack et un nom à deux labels dessous répondent ; AAAA vide ; un nom voisin et `example.com` relayés ; une zone ajoutée, changée (numéro de série) ou retirée, prise en compte en quatre secondes |

La mesure porte sur la réponse de la machine, pas sur le `curl` du voisin. Le noyau du voisin
jette lui-même un `SYN-ACK` venu de `127.0.0.1`, mais un client forgé ne le ferait pas.

### À éprouver sur le VPS

- Un agent du VPS joint l'URL de sa stack par la boucle locale : `curl`, puis Playwright.
- La version de Docker du VPS pose la règle `raw` (Docker 28 et suivants).
- `waggle setup` y détecte le VPN, quel qu'il soit.
- Sous Tailscale : la règle de waggle à côté des chaînes de Tailscale (`ts-input`,
  `ts-forward`) ; un conteneur de Docker Desktop, sur le Mac, qui joint l'adresse Tailscale du
  serveur ; le DNS partagé de Tailscale vers le DNS de waggle, sur un téléphone.
- Les essais de `docs/essais/0001-sur-un-serveur-et-un-appareil.md`, qui restent à faire :
  le pare-feu, le DNS du VPN, le relais depuis Docker Desktop.

## Alternatives écartées

- **Le Mac passe par le DNS du VPN, sans waggle.**
  - Les noms en `.localhost` n'y vont jamais : le navigateur les garde pour sa boucle
    locale.
  - Tant que le tunnel tourne, les stacks locales d'un même domaine ne répondent plus : le DNS
    du VPN envoie leurs noms au serveur.
  - Ce chemin reste celui d'un appareil sans waggle (décision 5).
- **Un tunnel SSH du Mac vers la boucle locale du VPS, à la place d'un VPN.**
  - Il demande `root` sur le Mac pour écouter sur 80 et 443 (§ « Les essais »).
  - Il tient le temps d'une connexion, et n'apporte rien au téléphone.
  - Il reste possible à la main, avec `sudo ssh -L`.
- **Une seconde instance, sur la boucle locale du serveur, qui lirait les mêmes projets.**
  Chaque stack aurait deux contrôleurs, deux autorités, et un réseau partagé entre les
  projets de VPN différents.
- **La configuration déduite sans question** (décision 6) : elle se tromperait en silence.
- **WireGuard seul** : un téléphone n'a qu'un VPN actif, et beaucoup d'équipes en ont déjà
  un (décision 7).
- **Monter WireGuard dans `install.sh`** : ce serait une étape cachée d'une commande que l'on
  relance pour mettre à jour (décision 7).
- **Un DNS du VPN qui répond pour tout un domaine** : il prendrait les vrais noms de ce
  domaine (décision 8).
- **Publier la liste des domaines par HTTP, sur le point d'entrée du relais** : Traefik ne
  sert pas de contenu, et il faudrait un conteneur de plus. Le DNS du serveur est déjà là,
  joignable par le VPN.

## Conséquences

- **Pour les projets, rien ne change** : les mêmes étiquettes servent les trois façons de
  travailler.
- **Sur chaque machine, deux commandes** : `install.sh`, puis les réponses de `waggle setup`.
  - Sur le Mac, les cas 2 et 3 répondent l'adresse du serveur sur le VPN.
  - Sur le VPS, on choisit l'interface du VPN, et `loopback: true` suit.
- **Plus rien à écrire par projet** : ni ports historiques, ni domaines du DNS, ni domaines
  des relais.
- **Les e2e d'une stack restent dans la stack.** Une stack qui joindrait sa propre URL par
  l'adresse du VPN reste refusée par le pare-feu (`docs/essais`, § 6).
- **Ce qui reste ouvert** :
  - les essais sur le VPS ;
  - `waggle vpn`, si un serveur sans VPN en a besoin.
