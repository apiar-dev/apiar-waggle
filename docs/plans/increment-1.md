# Plan — increment 1 · waggle, des ADR 0001 et 0002 au code

## Ce que l'increment obtient

waggle s'installe sur une machine, Mac ou serveur, d'une seule commande, `bin/waggle installer`.
Il sert les stacks des projets d'après les routes qu'elles déclarent :
- en HTTPS terminé par waggle ;
- en HTTP sur `p80` ;
- en passage TLS.

Il couvre aussi le relais d'un poste vers un serveur, une instance par VPN, le DNS du VPN et
la règle de pare-feu. Tout est éprouvé sur un Mac par une suite d'intégration, le serveur y
étant simulé par une seconde instance. Le pare-feu s'éprouve dans un Docker-in-Docker. Restent
à constater sur le vrai serveur la règle de pare-feu et le DNS ; sur un téléphone, Android et
iOS, l'autorité de waggle.

## Les décisions, et leur pourquoi

- **Go, sa bibliothèque standard, et une seule dépendance pour le YAML** (`go.yaml.in/yaml/v3`).
  *(Benjamin, 2026-11-14, après une comparaison avec Rust.)*
  - waggle est de la colle autour de Docker, Traefik et CoreDNS, écrits en Go.
  - Il parle à Docker par son socket, et signe ses certificats avec `crypto/x509`.
  - Le test d'intégration ajoute `golang.org/x/net`, pour ouvrir une connexion HTTP/2 à la
    main.
- **Rien à installer hors Docker.** Tout le reste vit dans une image `waggle:local`, construite
  depuis ce dépôt.
  - `bin/waggle` est un script POSIX : il construit l'image, puis lance la commande dedans,
    avec le socket Docker, le fichier de réglages et le dossier d'état.
  - Sur Linux, la commande lit les interfaces de l'hôte (`--network host`) pour vérifier les
    adresses.
- **Les réglages, en YAML**, dans `~/.config/waggle/reglages.yaml`, non committé
  *(Benjamin, 2026-11-14)*. L'état de chaque instance va dans `~/.local/state/waggle/<instance>/` :
  - son autorité ;
  - `instance.json`, ses réglages validés ;
  - `compose.json`, le projet Compose qu'en tire waggle ;
  - son `Corefile`.
- **Une instance, un projet Compose `waggle-<instance>`**, de trois ou quatre services :
  - `traefik` (v3.6), sans le socket Docker : il ne lit que des fichiers ;
  - `controleur`, le compagnon devenu traducteur (ADR-0002, décision 1). Seul à monter le
    socket, il branche chaque service routé sur le réseau `waggle-<instance>`, traduit ses
    `waggle.routes`, émet les certificats, et écrit la configuration de Traefik dans un
    volume. Le piège de la relecture de Traefik disparaît avec le fournisseur Docker ;
  - `dns`, un CoreDNS, s'il y a des domaines à résoudre pour le VPN ;
  - `pare-feu`, sur une adresse de VPN : il pose la règle `DOCKER-USER`, et la repose à chaque
    démarrage de Docker, puisqu'il redémarre avec lui. C'est la persistance que demandait
    l'ADR-0001.
- **Le relais d'un domaine terminé** vise le point d'entrée `relais` du serveur, en HTTP.
  - Le poste y nomme, dans `X-Waggle-Port`, le point d'entrée où la requête lui est arrivée.
    Une route du port 3000 n'y répond qu'à `X-Waggle-Port: 3000`.
  - `X-Forwarded-Port` ne convient pas : il reprend le port de l'en-tête `Host`, qui ne dit
    pas toujours le point d'entrée.
  - Ce point d'entrée ne croit les en-têtes `X-Forwarded-*` que des adresses du VPN.
  - Un seul point d'entrée suffit ainsi, quel que soit le catalogue.
- **Les certificats** : ECDSA P-256 et SHA-256, un SAN, `serverAuth`, 397 jours, et un
  renouvellement à 30 jours du terme. Ils satisfont Apple (825 jours au plus, même pour une
  autorité privée) comme Android. L'autorité est en X.509 v3, `CA:TRUE`, et dure 10 ans ;
  Android n'installe comme autorité qu'un certificat qui se dit tel.
- **Un décalage de ports** (`decalage_ports`) pour les essais : une instance publie le
  port `p` sur `p + décalage`. La suite d'intégration tourne ainsi à côté des proxies du
  poste.

## Les faits supposés

| Fait | Vérifié ? |
| --- | --- |
| Traefik relit un dossier de configuration (`providers.file.directory`, `watch`) écrit par un autre conteneur dans un volume | **oui**, `make integration` : routes servies, retirées, et un contrôleur redémarré sans perte. La relecture prend jusqu'à deux secondes (`providersThrottleDuration`) |
| Traefik route une requête d'après `Header(`X-Waggle-Port`, …)` sur le point d'entrée `relais` | **oui**, `make integration` : le relais d'un port historique arrive à sa route ; sans l'en-tête, 404 |
| Un conteneur `network_mode: host` muni de `NET_ADMIN` pose une règle dans le `DOCKER-USER` de l'hôte | **oui**, `make pare-feu`, dans un Docker-in-Docker : posée sur `iptables-nft`, une seule fois ; le membre passe, le voisin et l'autre VPN non |
| Sonder `iptables-legacy` sur un hôte en nftables est sans effet | **non** : il y crée des tables vides, d'où un avertissement. waggle s'arrête au premier pare-feu qui porte la chaîne |
| Le DNS du VPN répond en UDP et en TCP | **oui**, `make integration` : A pour le domaine, AAAA vide en `NOERROR`, le reste relayé |
| Chrome pour Android fait confiance à une autorité installée par l'utilisateur | à constater sur le Pixel (`docs/essais`, § 5) |
| Le DNS privé d'Android, réglé sur un fournisseur, contourne le DNS du tunnel | à constater sur le Pixel ; « Automatique » ne le contourne pas |

## Ce qui n'est pas dans l'increment

- Les migrations des projets existants : chacun par sa décision.
- Une image publiée sur un registre, et la CI de ce dépôt : il faut d'abord savoir sous quel
  compte publier sur `apiar-dev`.
