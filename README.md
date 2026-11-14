# waggle

Le routage de dev d'une machine, Mac ou serveur, partagé par tous les projets qui lèvent une stack Docker par dossier. Une
URL de dev n'y est jamais sur internet.

Le nom vient de la danse frétillante (*waggle dance*), par laquelle une abeille indique aux autres le chemin vers chaque
fleur : waggle indique au navigateur le chemin vers chaque stack.

Les décisions sont dans [`docs/adr/`](docs/adr/) :
- [ADR-0001](docs/adr/0001-waggle-le-routage-de-dev-d-une-machine.md), une instance par machine, jamais sur internet ;
- [ADR-0002](docs/adr/0002-le-modele-upsun-des-routes-declarees-un-tls-termine-par-waggle.md), des routes déclarées par le
  projet, un TLS terminé par waggle ;
- [ADR-0003](docs/adr/0003-waggle-sur-chaque-machine-qui-ouvre-ou-teste-une-url.md), waggle sur chaque machine qui ouvre ou
  teste une URL : en local, sur un serveur, ou les deux.

Le plan de cet incrément : [`docs/plans/increment-1.md`](docs/plans/increment-1.md).

## Installer waggle sur une machine

Il faut Docker (Docker Desktop sur un Mac, Docker Engine sur un serveur Linux), git, et `gh`, connecté à un compte qui
peut lire ce dépôt : il est privé. Puis, sur un Mac comme sur un serveur :

```bash
gh api repos/apiar-dev/apiar-waggle/contents/install.sh -H 'Accept: application/vnd.github.raw+json' | sh
```

Le script, [`install.sh`](install.sh) :
1. vérifie git, `gh` et Docker ;
2. clone ce dépôt dans `~/.local/share/waggle`. Chaque machine y construit l'image de waggle ;
3. lie la commande dans `~/.local/bin/waggle`. Si ce dossier n'est pas dans le `PATH`, il affiche la ligne à ajouter au
   fichier du shell, sans y toucher lui-même ;
4. sur un Mac, écrit la configuration du poste s'il n'y en a pas, lève l'instance (`waggle up`) et approuve son autorité
   dans le trousseau. macOS demande alors le mot de passe de la session, une seule fois ;
5. sur Linux, écrit la configuration que la machine dit d'elle-même, s'il n'y en a pas, puis lève l'instance :
   - une interface WireGuard : l'instance de ce VPN, sur son adresse, qui sert aussi la boucle locale de la machine à ses
     propres agents (`loopback: true`) ;
   - aucune : la boucle locale seule ;
   - plusieurs : le script s'arrête, et la configuration s'écrit à la main, une instance par VPN (§ « Sur un serveur »).

**Mettre à jour, c'est relancer la même commande.**
- Le script amène le clone à la dernière version de `main`, puis reconstruit l'image.
- Compose ne recrée que les conteneurs dont l'image a changé : le contrôleur, pas Traefik.
- Un clone modifié à la main est refusé, plutôt qu'écrasé.

Les réglages du script :
- `WAGGLE_REF`, une branche ou une étiquette, `main` par défaut ;
- `WAGGLE_HOME`, `WAGGLE_BIN` et `WAGGLE_CONFIG`, ses dossiers et son fichier de configuration ;
- `WAGGLE_NO_TRUST=1`, pour laisser le trousseau tranquille.

Pour essayer une branche :

```bash
gh api 'repos/apiar-dev/apiar-waggle/contents/install.sh?ref=increment-1' -H 'Accept: application/vnd.github.raw+json' | WAGGLE_REF=increment-1 sh
```

Pour retirer waggle : `waggle uninstall --all`, puis supprimer `~/.local/bin/waggle` et `~/.local/share/waggle`.

Les commandes `make` n'ont rien à voir avec l'installation. Elles ne servent qu'à développer waggle, et se lancent dans ce
dépôt (§ « Développer waggle »).

## Les commandes

| Commande | Ce qu'elle fait |
| --- | --- |
| `waggle up` | lit la configuration, écrit et lève chaque instance. Relancée, elle ne change que ce qui a changé |
| `waggle down` | arrête et retire les conteneurs des instances ; leur autorité et leur configuration restent |
| `waggle uninstall [--all]` | retire les instances et leurs volumes ; `--all` retire aussi leur autorité |
| `waggle status` | les conteneurs de chaque instance, et son autorité |
| `waggle ca [instance]` | le certificat de l'autorité d'une instance, à passer à un appareil |
| `waggle trust [instance]` | sur un Mac, approuve l'autorité dans le trousseau, sauf si elle l'est déjà |

Le journal du contrôleur dit chaque route servie, retirée ou refusée : `docker logs -f waggle-workstation-controller-1`.

## Trois façons de travailler

Le navigateur envoie toujours un nom de dev au `127.0.0.1` de sa machine : le DNS public le dit pour `*.dev.example.com`, et
`*.localhost` n'est jamais demandé. waggle tourne donc sur chaque machine qui ouvre ou teste une URL. Il sert les stacks de
cette machine, et relaie au serveur ce qu'il ne sert pas ([ADR-0003](docs/adr/0003-waggle-sur-chaque-machine-qui-ouvre-ou-teste-une-url.md)).

| | Le Mac | Le serveur |
| --- | --- | --- |
| 1. Tout en local | waggle, sans relais | rien |
| 2. Parfois en local, parfois sur le serveur | waggle, avec un relais vers le serveur : il sert ses stacks, et passe le reste | waggle, `loopback: true` |
| 3. Tout sur le serveur | waggle, avec le même relais, sans aucune stack : il ne fait que passer | waggle, `loopback: true` |

- **Les URL sont les mêmes partout**, quelle que soit la machine de la stack. Si un nom existe des deux côtés, le Mac garde
  le sien.
- **La façon de créer le worktree n'y change rien** : un outil, un agent, ou un humain connecté au serveur. waggle lit
  les étiquettes des conteneurs.
- **Le serveur teste ses propres stacks** : `loopback: true` lui fait servir les mêmes URL sur sa boucle locale, à ses
  agents (`curl`, Playwright, un navigateur).
- **Le relais du Mac**, pour les cas 2 et 3, s'ajoute à son instance. Un domaine par ligne, dont le schéma dit le mode, le
  même que celui des routes du projet :

  ```yaml
  instances:
    workstation:
      address: 127.0.0.1
      ports: [3000]
      relays:
        - to: 10.8.0.1              # the server's WireGuard address
          domains:
            - tls://dev.example.com # a passthrough domain
            - http://localhost      # .localhost names
  ```

- **Un appareil sans waggle**, un téléphone ou un Mac sans Docker, passe par le DNS du VPN (§ « Un téléphone ») : il
  n'ouvre que de vrais domaines.

## Sur un poste

- Les ports 80 et 443 de `127.0.0.1` doivent être libres : arrêter d'abord le proxy « du poste » d'un projet qui n'a pas
  encore migré. Sinon `waggle up` échoue : il suffit de relancer le script une fois les ports libérés.
- La configuration que le script écrit est [`config.example.yaml`](config.example.yaml), copié dans
  `~/.config/waggle/config.yaml` : l'instance `workstation`, sur `127.0.0.1`, suffit à un poste seul. Pour y ajouter des
  ports ou des relais : modifier le fichier, puis `waggle up`.
- L'autorité approuvée, Safari et Chrome la lisent dans le trousseau. Si Firefox ne la voit pas : dans `about:config`,
  régler `security.enterprise_roots.enabled` sur `true`.
- Sur un poste Linux, l'exemple convient tel quel : le copier, puis relancer le script.

## Sur un serveur

- WireGuard d'abord. L'instance d'un VPN écoute sur l'adresse WireGuard du serveur : waggle en déduit l'interface, et
  refuse toute adresse qui n'est ni la boucle locale ni celle d'une interface WireGuard.
- Une instance par VPN, chacune avec la liste de ses projets (`projects`). Deux VPN ne voient jamais les stacks l'un de
  l'autre.
- La configuration : avec une seule interface WireGuard, le script l'écrit. Avec plusieurs, elle s'écrit à la main :
  copier [`config.example.yaml`](config.example.yaml) dans `~/.config/waggle/config.yaml`, et y décrire une instance par
  VPN. Puis relancer le script, ou `waggle up`.
- `loopback: true` : l'instance publie aussi ses points d'entrée sur `127.0.0.1`, pour les agents du serveur. Une seule
  instance par machine sert sa boucle locale. Le port du relais reste sur l'adresse du VPN. Une adresse de boucle ne
  s'ouvre pas au voisin : Docker refuse, dans sa table `raw`, ce qui vise `127.0.0.1` sans entrer par `lo`
  (`make firewall` le vérifie).
- Le pare-feu se pose seul.
  - Le service `firewall` de chaque instance pose la règle qui ferme l'adresse du VPN à tout ce qui n'entre pas par son
    interface :
    `iptables -I DOCKER-USER -m conntrack --ctorigdst <adresse> --ctdir ORIGINAL ! -i <interface> -j DROP`.
  - Il la repose à chaque démarrage de Docker, puisqu'il redémarre avec lui, et la vérifie toutes les quinze secondes.
  - Docker doit garder son pare-feu `iptables`, celui par défaut.
- `dns: [dev.example.com]` lève le DNS du VPN. Il répond l'adresse de l'instance pour ce domaine et tout ce qui est
  dessous, rien en AAAA, et relaie le reste.
- Un serveur ne relaie jamais : seul un poste a des `relays`.

## Les réglages

| Clé | Ce qu'elle règle |
| --- | --- |
| `instances.<nom>.address` | `127.0.0.1` sur un poste ; l'adresse WireGuard d'un VPN sur un serveur |
| `loopback` | sur un serveur : servir aussi la boucle locale, aux agents de la machine. Une instance par machine au plus |
| `ports` | les ports historiques d'un projet existant, en TLS, en plus de 80 et 443 |
| `projects` | les projets que l'instance lit (`waggle.project`) ; sans liste, tous, s'il n'y a qu'une instance |
| `relays` | d'un poste vers un serveur : `to`, l'adresse du serveur ; `domains`, une URL par domaine, dont le schéma dit le mode |
| `dns` | les domaines que le DNS du VPN résout vers l'adresse de l'instance |
| `relay_listener` | sur un serveur, le point d'entrée où les postes relaient : `port`, 8080 par défaut, et `trusted_ips`, le réseau du VPN par défaut |
| `port_offset` | pour les essais : publier chaque port `p` sur `p + décalage` |

Une clé inconnue est refusée : une faute de frappe ne passe pas pour un réglage.

## Le contrat d'un projet

Un projet ne définit plus de proxy. Chaque service routé porte deux étiquettes :

```yaml
services:
  app:
    labels:
      waggle.project: my-project
      waggle.routes: |
        https://{stack}.dev.example.com/        8080
        https://{stack}.dev.example.com:3000/   5173
  api:
    labels:
      waggle.project: my-project
      waggle.routes: https://{stack}.dev.example.com/api 8000
```

- **Une route est une URL, puis le port du service.** `{stack}` y devient le nom de la stack, celui que Compose donne au
  projet. Si l'image n'expose qu'un port, il peut se taire.
- **Le schéma dit le mode** :

  | Schéma | Mode |
  | --- | --- |
  | `https://` | waggle termine le TLS, et route par hôte et par chemin |
  | `http://` | en clair sur le port 80, par exemple `http://admin.{stack}.localhost/` |
  | `tls://` | le passage : la stack termine son TLS, et waggle route par le SNI |

- **Un hôte joker**, `https://*.{stack}.dev.example.com/`, sert une famille d'hôtes, y compris les noms à plusieurs
  labels sous la stack. Un nom exact de la même stack passe avant lui : `admin.{stack}…` reste à la route qui le nomme.
- **Un chemin**, `/api`, prend `/api` et ce qui est dessous, mais pas `/apiv2`. Le plus long passe avant les autres.
- **Les refus.** Une route dont l'hôte ne porte pas `{stack}` est refusée : une stack ne prend pas le nom d'une autre. Une
  route disputée reste à la stack la plus ancienne.
- **Un domaine est terminé ou en passage, jamais les deux** (ADR-0002, décision 4).
- **Ce qu'une stack ne fait plus** : publier un port, partager un réseau ou un volume. Le contrôleur de waggle branche
  lui-même chaque service routé sur le réseau de son instance.
- **Les noms de dossier portent le préfixe du projet** : Compose n'a qu'un espace de noms par machine.
- **En HTTPS, l'application reçoit du HTTP**, avec `X-Forwarded-Proto`, `X-Forwarded-Host` et `X-Forwarded-Port`. Elle
  fait confiance à waggle comme à un répartiteur en production.

## Les certificats

- Chaque instance a son autorité : `~/.local/state/waggle/<instance>/ca/`. Elle naît à l'installation, sa clé ne quitte
  pas la machine, et elle dure dix ans.
- Le contrôleur signe ce que les routes demandent :
  - un joker par domaine ;
  - un certificat propre à une stack qui a des noms à deux labels (`api.<stack>.<domaine>`).
- Chaque certificat dure 397 jours, et se renouvelle seul à 30 jours de son terme.
- `waggle ca [instance]` écrit le certificat de l'autorité, à passer à un appareil.
- `waggle uninstall --all` retire l'autorité avec le reste. Les appareils qui l'approuvaient ne la reconnaîtront
  plus.

## Un téléphone

Un téléphone n'a pas de waggle : il rejoint le VPN du serveur, et en prend le DNS. Les noms en `.localhost` ne s'ouvrent
jamais sur un téléphone ; un projet en HTTP qui veut son téléphone prend un vrai domaine.

**Android** (un Pixel, par exemple) :
1. Dans l'app WireGuard, le tunnel du serveur porte `DNS = <adresse du VPN>`.
2. Le DNS privé (Paramètres → Réseau et Internet → DNS privé) reste en « Automatique » ou « Désactivé ». Réglé sur un
   fournisseur, il enverrait les requêtes hors du tunnel.
3. Installer l'autorité de l'instance : Paramètres → Sécurité et confidentialité → Plus de paramètres de sécurité →
   Chiffrement et identifiants → Installer un certificat → Certificat CA, puis choisir `ca.crt`.
4. Chrome lui fait confiance. Firefox la lit si l'on active « Utiliser les certificats d'autorité tiers », dans ses
   paramètres secrets.

**iOS** :
1. Dans l'app WireGuard, le tunnel du serveur porte `DNS = <adresse du VPN>` : iOS lui envoie alors tout son DNS.
2. Envoyer `ca.crt` à l'iPhone (AirDrop ou courriel), puis l'installer : Réglages → Profil téléchargé.
3. Activer la confiance totale : Réglages → Général → Informations → Réglages de confiance des certificats.

Les chemins des menus et le comportement du DNS privé d'Android restent à constater sur l'appareil :
[`docs/essais/0001-sur-un-serveur-et-un-appareil.md`](docs/essais/0001-sur-un-serveur-et-un-appareil.md).

## Développer waggle

Ces commandes ne servent qu'à qui modifie waggle ; pour l'utiliser, la commande `waggle` suffit. Elles tournent toutes dans
Docker : il n'y a pas Go à installer.

| Commande | Ce qu'elle fait |
| --- | --- |
| `make check` | le format, `go vet` et les tests unitaires |
| `make integration` | deux vraies instances, l'une poste et l'autre serveur, sur des ports décalés, avec des stacks jetables. Tout est retiré à la fin ; `WAGGLE_TEST_KEEP=1` garde tout levé, pour lire |
| `make firewall` | dans un Docker-in-Docker isolé : la commande `waggle firewall` face à un membre du tunnel, un voisin et un membre d'un autre VPN ; puis un port publié sur `127.0.0.1`, qu'un voisin ne joint pas |
| `make shellcheck` | les scripts |

Le code :
- `cmd/waggle`, les commandes ;
- `internal/config`, la configuration et sa validation ;
- `internal/routes`, l'étiquette `waggle.routes` ;
- `internal/traefik`, la traduction pour Traefik ;
- `internal/ca`, l'autorité et les certificats ;
- `internal/controller`, le contrôleur ;
- `internal/render`, le projet Compose et le Corefile ;
- `internal/firewall`, la règle de pare-feu ;
- `internal/docker`, l'API de Docker par son socket.
