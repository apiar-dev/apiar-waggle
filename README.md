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

## Ce qu'il faut

Docker, et rien d'autre : Docker Desktop sur un Mac, Docker Engine sur un serveur Linux. `bin/waggle` construit l'image de
waggle depuis ce dépôt, puis y lance chaque commande.

## Sur un poste

1. Les réglages : copier [`reglages.exemple.yaml`](reglages.exemple.yaml) dans `~/.config/waggle/reglages.yaml`. Pour un
   poste seul, l'instance `poste` sur `127.0.0.1` suffit.
2. Les ports 80 et 443 de `127.0.0.1` doivent être libres : arrêter d'abord le proxy « du poste » d'un projet qui n'a pas
   encore migré.
3. `bin/waggle installer`. Relancé, il ne change que ce qui a changé.
4. Approuver l'autorité du poste, une fois : `bin/waggle approuver`.
   - macOS l'ajoute au trousseau de session, approuvée pour le TLS, et demande le mot de passe de la session.
   - Safari et Chrome la lisent là. Si Firefox ne la voit pas : dans `about:config`, régler
     `security.enterprise_roots.enabled` sur `true`.

`bin/waggle etat` montre les conteneurs de chaque instance. Le journal du contrôleur dit chaque route servie, retirée ou
refusée : `docker logs -f waggle-poste-controleur-1`.

## Sur un serveur

- WireGuard d'abord. L'instance d'un VPN écoute sur l'adresse WireGuard du serveur : waggle en déduit l'interface, et
  refuse toute adresse qui n'est ni la boucle locale ni celle d'une interface WireGuard.
- Une instance par VPN, chacune avec la liste de ses projets (`projets`). Deux VPN ne voient jamais les stacks l'un de
  l'autre.
- Le pare-feu se pose seul.
  - Le service `pare-feu` de chaque instance pose la règle qui ferme l'adresse du VPN à tout ce qui n'entre pas par son
    interface :
    `iptables -I DOCKER-USER -m conntrack --ctorigdst <adresse> --ctdir ORIGINAL ! -i <interface> -j DROP`.
  - Il la repose à chaque démarrage de Docker, puisqu'il redémarre avec lui, et la vérifie toutes les quinze secondes.
  - Docker doit garder son pare-feu `iptables`, celui par défaut.
- `dns: [dev.exemple.fr]` lève le DNS du VPN. Il répond l'adresse de l'instance pour ce domaine et tout ce qui est dessous,
  rien en AAAA, et relaie le reste.
- Un serveur ne relaie jamais : seul un poste a des `relais`.

## Les réglages

| Clé | Ce qu'elle règle |
| --- | --- |
| `instances.<nom>.adresse` | `127.0.0.1` sur un poste ; l'adresse WireGuard d'un VPN sur un serveur |
| `ports` | les ports historiques d'un projet existant, en TLS, en plus de 80 et 443 |
| `projets` | les projets que l'instance lit (`waggle.projet`) ; sans liste, tous, s'il n'y a qu'une instance |
| `relais` | d'un poste vers un serveur : `vers`, l'adresse du serveur ; `domaines`, une URL par domaine, dont le schéma dit le mode |
| `dns` | les domaines que le DNS du VPN résout vers l'adresse de l'instance |
| `relais_entrant` | sur un serveur, le point d'entrée où les postes relaient : port 8080, et le réseau du VPN, par défaut |
| `decalage_ports` | pour les essais : publier chaque port `p` sur `p + décalage` |

Une clé inconnue est refusée : une faute de frappe ne passe pas pour un réglage.

## Le contrat d'un projet

Un projet ne définit plus de proxy. Chaque service routé porte deux étiquettes :

```yaml
services:
  app:
    labels:
      waggle.projet: mon-projet
      waggle.routes: |
        https://{dossier}.dev.exemple.fr/        8080
        https://{dossier}.dev.exemple.fr:3000/   5173
  api:
    labels:
      waggle.projet: mon-projet
      waggle.routes: https://{dossier}.dev.exemple.fr/api 8000
```

- **Une route est une URL, puis le port du service.** `{dossier}` y devient le nom de la stack, celui que Compose donne
  au projet. Si l'image n'expose qu'un port, il peut se taire.
- **Le schéma dit le mode** :

  | Schéma | Mode |
  | --- | --- |
  | `https://` | waggle termine le TLS, et route par hôte et par chemin |
  | `http://` | en clair sur le port 80, par exemple `http://admin.{dossier}.localhost/` |
  | `tls://` | le passage : la stack termine son TLS, et waggle route par le SNI |

- **Un hôte joker**, `https://*.{dossier}.dev.exemple.fr/`, sert une famille d'hôtes.
- **Un chemin**, `/api`, prend `/api` et ce qui est dessous, mais pas `/apiv2`.
- **Les refus.** Une route dont l'hôte ne porte pas `{dossier}` est refusée : une stack ne prend pas le nom d'une autre.
  Une route disputée reste à la stack la plus ancienne.
- **Un domaine est terminé ou en passage, jamais les deux** (ADR-0002, décision 4).
- **Ce qu'une stack ne fait plus** : publier un port, partager un réseau ou un volume. Le contrôleur de waggle branche
  lui-même chaque service routé sur le réseau de son instance.
- **Les noms de dossier portent le préfixe du projet** : Compose n'a qu'un espace de noms par machine.
- **En HTTPS, l'application reçoit du HTTP**, avec `X-Forwarded-Proto`, `X-Forwarded-Host` et `X-Forwarded-Port`. Elle
  fait confiance à waggle comme à un répartiteur en production.

## Les certificats

- Chaque instance a son autorité : `~/.local/state/waggle/<instance>/autorite/`. Elle naît à l'installation, sa clé ne
  quitte pas la machine, et elle dure dix ans.
- Le contrôleur signe ce que les routes demandent :
  - un joker par domaine ;
  - un certificat propre à une stack qui a des noms à deux labels (`api.<dossier>.<domaine>`).
- Chaque certificat dure 397 jours, et se renouvelle seul à 30 jours de son terme.
- `bin/waggle autorite [instance]` écrit le certificat de l'autorité, à passer à un appareil.
- `bin/waggle desinstaller --tout` retire l'autorité avec le reste. Les appareils qui l'approuvaient ne la reconnaîtront
  plus.

## Un téléphone

Un téléphone n'a pas de waggle : il rejoint le VPN du serveur, et en prend le DNS. Les noms en `.localhost` ne s'ouvrent
jamais sur un téléphone ; un projet en HTTP qui veut son téléphone prend un vrai domaine.

**Android** (un Pixel, par exemple) :
1. Dans l'app WireGuard, le tunnel du serveur porte `DNS = <adresse du VPN>`.
2. Le DNS privé (Paramètres → Réseau et Internet → DNS privé) reste en « Automatique » ou « Désactivé ». Réglé sur un
   fournisseur, il enverrait les requêtes hors du tunnel.
3. Installer l'autorité de l'instance : Paramètres → Sécurité et confidentialité → Plus de paramètres de sécurité →
   Chiffrement et identifiants → Installer un certificat → Certificat CA, puis choisir `autorite.crt`.
4. Chrome lui fait confiance. Firefox la lit si l'on active « Utiliser les certificats d'autorité tiers », dans ses
   paramètres secrets.

**iOS** :
1. Dans l'app WireGuard, le tunnel du serveur porte `DNS = <adresse du VPN>` : iOS lui envoie alors tout son DNS.
2. Envoyer `autorite.crt` à l'iPhone (AirDrop ou courriel), puis l'installer : Réglages → Profil téléchargé.
3. Activer la confiance totale : Réglages → Général → Informations → Réglages de confiance des certificats.

Les chemins des menus et le comportement du DNS privé d'Android restent à constater sur l'appareil :
[`docs/essais/0001-sur-un-serveur-et-un-appareil.md`](docs/essais/0001-sur-un-serveur-et-un-appareil.md).

## Développer waggle

Tout tourne dans Docker :

| Commande | Ce qu'elle fait |
| --- | --- |
| `make verifier` | le format, `go vet` et les tests unitaires |
| `make integration` | deux vraies instances, l'une poste et l'autre serveur, sur des ports décalés, avec des stacks jetables. Tout est retiré à la fin ; `WAGGLE_ESSAI_GARDER=1` garde tout levé, pour lire |
| `make pare-feu` | la commande `waggle pare-feu`, dans un Docker-in-Docker isolé : un membre du tunnel, un voisin, un membre d'un autre VPN |
| `make shellcheck` | les scripts |

Le code :
- `cmd/waggle`, les commandes ;
- `internal/reglages`, les réglages et leur validation ;
- `internal/routes`, l'étiquette `waggle.routes` ;
- `internal/traefik`, la traduction pour Traefik ;
- `internal/autorite`, les certificats ;
- `internal/controleur`, le contrôleur ;
- `internal/generation`, le projet Compose et le Corefile ;
- `internal/parefeu`, la règle de pare-feu ;
- `internal/docker`, l'API de Docker par son socket.
