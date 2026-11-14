# waggle

Le routage de dev d'une machine, Mac ou serveur, partagé par tous les projets qui lèvent une stack Docker par dossier. Une
URL de dev n'y est jamais sur internet.

Le nom vient de la danse frétillante (*waggle dance*), par laquelle une abeille indique aux autres le chemin vers chaque
fleur : waggle indique au navigateur le chemin vers chaque stack.

Les décisions sont dans [`docs/adr/`](docs/adr/) :
- [ADR-0001](docs/adr/0001-waggle-le-routage-de-dev-d-une-machine.md), une instance par machine, jamais sur internet ;
- [ADR-0002](docs/adr/0002-le-modele-upsun-des-routes-declarees-un-tls-termine-par-waggle.md), des routes déclarées par le
  projet, un TLS terminé par waggle.

Le plan de cet incrément : [`docs/plans/increment-1.md`](docs/plans/increment-1.md).

## Installer waggle sur une machine

Il ne faut que Docker : Docker Desktop sur un Mac, Docker Engine sur un serveur Linux. Pour l'instant, waggle s'installe
depuis ce dépôt, cloné une fois par machine, avec la commande `waggle` liée dans un dossier du `PATH` :

```bash
git clone https://github.com/apiar-dev/apiar-waggle.git ~/.local/share/waggle
```

```bash
ln -s ~/.local/share/waggle/bin/waggle ~/.local/bin/waggle
```

- `waggle` construit son image depuis le clone au premier lancement, puis à chaque mise à jour ; le cache de Docker rend
  les lancements suivants instantanés.
- Mettre à jour : `git -C ~/.local/share/waggle pull`, puis `waggle install`.
- Le dépôt est privé : il faut y avoir accès pour le cloner.
- Les commandes `make` n'ont rien à voir avec l'installation. Elles ne servent qu'à développer waggle, et se lancent dans
  ce dépôt (§ « Développer waggle »).

## Sur un poste

1. La configuration : copier [`config.example.yaml`](config.example.yaml) dans `~/.config/waggle/config.yaml`. Pour un
   poste seul, l'instance `workstation` sur `127.0.0.1` suffit.
2. Les ports 80 et 443 de `127.0.0.1` doivent être libres : arrêter d'abord le proxy « du poste » d'un projet qui n'a pas
   encore migré.
3. `waggle install`. Relancé, il ne change que ce qui a changé.
4. Approuver l'autorité du poste, une fois : `waggle trust`.
   - macOS l'ajoute au trousseau de session, approuvée pour le TLS, et demande le mot de passe de la session.
   - Safari et Chrome la lisent là. Si Firefox ne la voit pas : dans `about:config`, régler
     `security.enterprise_roots.enabled` sur `true`.

`waggle status` montre les conteneurs de chaque instance. Le journal du contrôleur dit chaque route servie, retirée ou
refusée : `docker logs -f waggle-workstation-controller-1`.

## Sur un serveur

- WireGuard d'abord. L'instance d'un VPN écoute sur l'adresse WireGuard du serveur : waggle en déduit l'interface, et
  refuse toute adresse qui n'est ni la boucle locale ni celle d'une interface WireGuard.
- Une instance par VPN, chacune avec la liste de ses projets (`projects`). Deux VPN ne voient jamais les stacks l'un de
  l'autre.
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

- **Un hôte joker**, `https://*.{stack}.dev.example.com/`, sert une famille d'hôtes.
- **Un chemin**, `/api`, prend `/api` et ce qui est dessous, mais pas `/apiv2`.
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
| `make firewall` | la commande `waggle firewall`, dans un Docker-in-Docker isolé : un membre du tunnel, un voisin, un membre d'un autre VPN |
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
