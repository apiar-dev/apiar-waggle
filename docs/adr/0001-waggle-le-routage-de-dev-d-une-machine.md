# ADR-0001 — waggle : le routage de dev d'une machine, partagé par les projets, jamais sur internet

**Statut** : **Accepté** (2026-11-14), par Benjamin.
**Amendé** par l'[ADR-0002](0002-le-modele-upsun-des-routes-declarees-un-tls-termine-par-waggle.md),
décisions 2, 3 et 5 : des routes au format de waggle, un TLS terminé par waggle, un relais par
domaine, dans le mode du domaine.
**Zone** : le routage des environnements de développement — ce qui tient les ports d'une
machine, résout les noms de dev et filtre le réseau —, pour tous les projets qui lèvent une
stack Docker par dossier.

Le nom vient de la danse frétillante (*waggle dance*), par laquelle une abeille indique aux
autres le chemin vers chaque fleur : waggle indique au navigateur le chemin vers chaque stack.

## Contexte

Plusieurs projets lèvent une **stack par dossier** : chaque worktree a son environnement
complet, joignable sous un nom qui dérive du nom du dossier. Pour que le navigateur atteigne
ces stacks, chaque projet a écrit son propre proxy « du poste » : un Traefik lancé à part, qui
découvre les stacks par leurs étiquettes, et un compagnon qui branche chaque service routé
sur son réseau. La mécanique est la même d'un projet à l'autre ; elle est réécrite dans
chacun.

Trois choses rendent ce découpage intenable :

- **Une machine n'a qu'un port 443 (ou 80) par adresse.** Deux projets qui embarquent chacun
  leur proxy se disputent les mêmes ports, ou s'excluent l'un l'autre par des étiquettes
  propres à chacun.
- **Les usages se multiplient.** Un dev travaille en local sur son Mac ; un autre, ou le
  même, sur un serveur distant joint par WireGuard, où tournent ses worktrees et ses sessions
  d'agent ; un troisième veut ouvrir une stack sur son téléphone, ou partager l'URL de son
  ticket.
- **Le routage n'est pas l'affaire d'un projet.** Ce qui tient les ports, résout les noms et
  filtre le réseau appartient à la machine ; ce qu'une stack déclare — « je sers tel nom, sur
  tel port » — appartient au projet. Les deux vivent aujourd'hui au même endroit.

Deux profils de projet existent, et doivent cohabiter :

| | HTTP, routé par `Host` | TLS de bout en bout, routé par le SNI |
| --- | --- | --- |
| Noms | `<service>.<dossier>.localhost`, ou un suffixe réglable | `<dossier>.<domaine de dev>`, un wildcard DNS public vers `127.0.0.1` |
| Ports | 80 | 443, et des ports historiques |
| Certificats | aucun | un par stack, signé par l'autorité du projet, servi par la stack |
| Pourquoi | le dev local, sans TLS | OAuth, cookies `Secure`, partenaires : il faut un vrai nom en HTTPS |

Une règle s'applique à tous : **une URL de dev n'est jamais exposée sur internet.**

## Décision

1. **Deux pièces : waggle, par machine ; le contrat, par projet.**
   - **waggle** vit dans ce dépôt et s'installe une fois par machine, un Mac ou un serveur.
     Il comprend Traefik et son compagnon, et, selon la machine, un DNS et une règle de
     pare-feu. Un fichier de réglages local, non committé, le règle : ses adresses, ses
     ports, ses projets, son relais.
   - **Un petit outil l'installe** : il écrit la configuration — Compose, Traefik, CoreDNS,
     pare-feu — à partir du fichier de réglages. Un `compose.yaml` fixe tiendrait pour un
     poste, pas pour ce que les réglages font varier :
     - le nombre d'instances ;
     - une route de relais par port du catalogue ;
     - les ports historiques ;
     - la règle de pare-feu et sa vérification ;
     - le refus des adresses.
     Son langage se choisit au plan d'implémentation. *(Tranché avec Benjamin, 2026-11-14.)*
   - **Un projet ne définit plus de proxy.** Il ne porte que son contrat (décision 2), et
     son installation dit « waggle installé ».

2. **Le contrat d'un projet.**
   - Chaque service routé porte `traefik.enable=true` et `waggle.project=<nom du projet>`,
     et déclare ses routes par étiquettes. L'étiquette de projet dit qui la lit, comme les
     étiquettes `traefik.*` *(tranché avec Benjamin, 2026-11-14 ; en anglais, comme tout
     identifiant)*. Les routes :
     - des routeurs HTTP (`Host`, `HostRegexp`) sur `p80`. Un routeur HTTP qui ne nomme
       pas de point d'entrée y va de lui-même ;
     - ou des routeurs TCP (`HostSNI`, en passage TLS) sur `p443` et les ports du
       catalogue (décision 3).
   - Une route garde la priorité par défaut, ou une priorité positive : les priorités
     négatives sont réservées au relais (décision 5).
   - **Aucune stack ne publie de port ni ne partage de réseau ou de volume.** C'est le
     compagnon de waggle qui branche ses services routés sur le réseau de waggle, et non
     l'inverse : un conteneur étranger sur le réseau d'une stack empêcherait
     `docker compose down` de le retirer. Dans ce sens, le service retiré emporte son
     branchement.
   - **Les noms de dossier portent le préfixe du projet** : Compose n'a qu'un espace de
     noms par machine, tous projets confondus.
   - **Un projet en TLS termine son TLS lui-même**, avec son autorité, et un certificat par
     stack, pour son seul nom. waggle n'aiguille une connexion TLS qu'une fois, d'après son
     SNI. Un certificat qui couvrirait plusieurs stacks laisserait le navigateur réutiliser
     une connexion HTTP/2 ouverte pour l'une afin de parler à une autre. waggle ne porte
     aucun certificat.

3. **Un catalogue de points d'entrée, nommés d'après leur port** *(tranché avec Benjamin,
   2026-11-14)*.
   - Ce dépôt n'en définit que deux :
     - `p80`, en HTTP routé par `Host`, le point d'entrée par défaut (`asDefault`) ;
     - `p443`, en TLS routé par le SNI, en passage.
   - Les ports historiques d'un projet existant s'ajoutent dans le fichier de réglages de la
     machine, en TLS routé par le SNI, sous le nom `p<port>`. Le dépôt ne nomme ainsi les
     ports d'aucun projet, et un poste n'ouvre que ceux des projets qu'il sert : un port
     comme 3000 reste libre pour le reste.
   - Un nouveau projet passe par 80 ou par 443, avec un nom par service.

4. **Jamais sur internet : c'est waggle qui le garantit.**
   - Sur un poste, waggle n'écoute que sur `127.0.0.1`. Sur un serveur, chaque instance
     (décision 7) n'écoute que sur l'adresse de son VPN. L'installation refuse toute autre
     adresse : `0.0.0.0`, une adresse publique, ou une adresse qu'aucune interface WireGuard
     de la machine ne porte.
   - **Sur un serveur, publier sur l'adresse d'un VPN ne suffit pas.**
     - Docker laisse un port publié sur une adresse précise joignable par les autres
       interfaces de l'hôte, dès qu'un hôte y route — un voisin du même segment chez
       l'hébergeur, par exemple —, et il active lui-même le routage IP. Seul `127.0.0.1`
       y échappe (documentation Docker, « Port publishing », § Gateway modes, et « Packet
       filtering and firewalls », § Docker on a router).
     - Un port publié passe aussi devant `ufw` (même page, § Docker and ufw).
     - Constaté sous Docker 29.9.3 (§ Les essais) : un voisin qui route vers l'adresse du
       VPN reçoit la page.
   - **L'installation pose donc une règle par adresse de VPN**, dans `DOCKER-USER`, la chaîne
     que Docker réserve à cet usage :
     `iptables -I DOCKER-USER -m conntrack --ctorigdst <adresse du VPN> --ctdir ORIGINAL ! -i <interface wg> -j DROP`.
     - Elle refuse, dans le sens de l'aller, ce dont la destination d'origine est cette
       adresse et qui n'entre pas par son interface.
     - `--ctdir ORIGINAL` est indispensable. Les réponses d'une stack entrent dans `FORWARD`
       par le pont de Docker, et portent la même destination d'origine. Sans cette option,
       la règle les refuse aussi, et coupe les membres du tunnel (constaté, § Les essais).
     - Elle vaut pour Traefik comme pour le DNS, tous deux publiés par Docker, en TCP comme
       en UDP.
     - L'installation vérifie que la chaîne existe, rend la règle persistante (après
       chaque démarrage de Docker), et la vérifie depuis l'extérieur du tunnel.
   - Une stack ne publiant rien (décision 2), un projet ne peut pas s'exposer par mégarde.

5. **Le relais : waggle d'un poste passe à waggle d'un serveur ce qu'il ne sert pas.**
   - Sur un poste qui travaille aussi à distance, waggle sert ses stacks locales, et relaie
     tout le reste, par le VPN, vers waggle du serveur. Le relais vise, sur le serveur, le
     même port qu'à l'arrivée :
     - en HTTP, tout `Host` (`PathPrefix(`/`)`), sur `p80` ;
     - en TCP, tout SNI (`HostSNI(`*`)`, en passage), sur `p443` et sur chaque port du
       catalogue.
   - **Le relais prend une priorité négative**, que Traefik accepte en HTTP comme en TCP. Il
     passe donc après toute route de projet, quelle que soit sa priorité. Un même nom des
     deux côtés : le local gagne.
   - **Les stacks locales et distantes répondent en même temps**, sous leurs noms, sans
     toucher au DNS du poste. `.localhost` compris, puisque le navigateur l'envoie sur sa
     propre boucle, donc à waggle du poste.
   - **Le relais porte un délai de connexion court (2 s).** VPN coupé, un nom relayé échoue
     vite, en 504 (HTTP) ou par une connexion fermée (TLS), au lieu de pendre 30 s. Les
     stacks locales répondent comme avant.
   - **Plusieurs serveurs** : une route de relais par domaine ou par préfixe de dossier
     (priorité `-1`), et au plus un serveur par défaut (priorité `-2`).
   - Un serveur ne relaie jamais, ce qui exclut toute boucle.
   - Le relais part d'un conteneur. Sur un Mac, Docker Desktop fait ressortir le trafic des
     conteneurs sur le Mac lui-même, VPN compris (documentation Docker Desktop,
     « Networking »). Reste à le constater avec le tunnel (§ Les essais).

6. **Le DNS du VPN, pour les appareils sans waggle.**
   - Sur un serveur, un CoreDNS par instance écoute sur l'adresse de son VPN (port 53, UDP et
     TCP). Il répond cette adresse pour les domaines de dev déclarés dans les réglages, ne
     répond rien en AAAA (`NOERROR`), et relaie le reste.
   - Un téléphone le prend comme DNS de son tunnel : l'app WireGuard d'iOS lui envoie alors
     tout son DNS.
   - Un poste n'en a pas besoin : son relais suffit.
   - Les noms en `.localhost` ne sortent jamais de la machine qui les affiche. Un projet en
     HTTP qui veut son téléphone prend un vrai domaine comme suffixe, déclaré au DNS du VPN.

7. **Plusieurs VPN, isolés : une instance par VPN.**
   - Traefik route par nom, quelle que soit l'adresse où arrive la connexion. Une instance
     unique, à l'écoute de deux VPN, laisserait un membre de l'un joindre les stacks de
     l'autre en demandant leur nom.
   - Sur un serveur, waggle lève donc une instance par VPN — un Traefik, son compagnon, son
     réseau, et un DNS si besoin —, liée à l'adresse de ce VPN.
     - Elle ne lit que ses projets, par une contrainte de Traefik sur `waggle.project`.
     - Deux projets de deux VPN ne partagent aucun réseau.
   - La règle de la décision 4, posée par adresse, ferme aussi chaque instance aux membres
     des autres VPN : leurs paquets arrivent par une autre interface (constaté, § Les
     essais).
   - Un seul fichier de réglages décrit les instances. Un poste n'en a qu'une, sur
     `127.0.0.1`, qui lit tous les projets.

8. **Partager l'URL d'un ticket.** Un membre du VPN joint les stacks du serveur, par son
   relais ou par le DNS du VPN, s'il approuve l'autorité du projet. Une stack sur un poste
   reste à ce poste.

## Les essais

### Faits sur un Mac, le 2026-11-14

Ils ont tourné sur des projets Compose jetables et des ports libres, retirés ensuite.

**Le relais.**
- Le montage :
  - Docker Desktop 29.9.2 et Traefik v3.6.
  - Trois instances : un poste, qui relaie, et deux serveurs.
  - Le poste joint les serveurs par `host.docker.internal`. Son flux sort donc de la VM de
    Docker par la pile réseau du Mac, comme vers une adresse WireGuard.
  - Derrière chaque instance, nginx répond son côté et le nom reçu. En TLS, il présente un
    certificat par nom, signé par une autorité jetable.
- Aucun conteneur d'essai ne portait `traefik.enable` : les proxies qui tournent sur le
  poste ne les ont pas lus.

**Le pare-feu** a tourné dans un Docker-in-Docker (Docker 29.9.3). Ce Docker a son propre
réseau et son propre pare-feu : il est isolé de la VM et des proxies du poste. Le montage :
- un port publié sur `10.8.0.1` seulement ;
- un membre du tunnel derrière `wg0` ;
- un voisin, derrière une autre interface, qui route vers `10.8.0.1` ;
- un membre d'un second VPN, derrière `wg1`, qui fait de même.

| Essai | Résultat |
| --- | --- |
| HTTP : un nom du poste ; un nom du serveur ; une famille d'hôtes du serveur (`HostRegexp`, priorité 10) | chacun servi de son côté, `Host` conservé jusqu'à la stack |
| HTTP : un même nom des deux côtés | le poste gagne |
| HTTP : un nom inconnu partout | 404, rendu par le serveur |
| TLS : un nom du poste ; un nom du serveur | chacun servi de son côté ; le certificat est celui de la stack du serveur, vérifié de bout en bout : le poste n'ouvre pas le TLS |
| TLS : un même nom des deux côtés | le poste gagne |
| TLS : une connexion sans SNI | relayée, puis refusée par le serveur (certificat par défaut de Traefik) |
| Deux serveurs, en HTTP et en TLS : un relais pour un domaine, un relais par défaut | chaque domaine va à son serveur ; un nom du poste pris dans le domaine d'un serveur reste au poste |
| Relais à priorité `-1`, puis `-1000`, face à une route de projet à priorité 1, en HTTP et en TCP | priorités acceptées ; la route du projet gagne, le relais prend le reste |
| Relais à priorité 1, face à une route de projet à priorité 1 | la route du projet gagne ici, mais rien ne garantit l'ordre d'une égalité |
| Serveur arrêté | un nom relayé échoue en 23 ms : 502 en HTTP, connexion fermée en TLS ; les noms locaux répondent |
| Serveur injoignable (une adresse qui ne répond pas) | 30 s d'attente par défaut, 2 s avec `dialTimeout: 2s` sur le relais |
| Un routeur HTTP sans point d'entrée, avec `asDefault` sur le point d'entrée HTTP | rangé sur ce point d'entrée seul |
| Une contrainte `Label(…, a) \|\| Label(…, b)` | les projets a et b lus, un troisième ignoré |
| Websocket de Vite 8.3.8, en HTTP par `Host`, à travers le poste puis le serveur | client et jeton servis par la chaîne ; `101`, `connected`, puis `full-reload` quand `main.js` change dans le conteneur |
| Le même essai en HTTPS par SNI, Vite terminant son TLS | même résultat, avec le certificat de la stack |
| La même chaîne HTTP dans un vrai navigateur (le Chromium de l'app Claude) | `v.<…>.localhost` va sur la boucle sans aucun réglage DNS ; la page se recharge seule quand `main.js` change |
| Pare-feu, sans règle | le membre, le voisin et le membre de l'autre VPN joignent tous le port publié sur `10.8.0.1`, et son DNS |
| Pare-feu, règle sans `--ctdir ORIGINAL` | le voisin est bloqué, mais le membre du tunnel aussi |
| Pare-feu, règle avec `--ctdir ORIGINAL` | le membre est servi, en HTTP comme en DNS (UDP) ; le voisin et le membre de l'autre VPN sont bloqués ; AAAA vide |

### À faire sur un serveur ou un appareil

Ils sont préparés, pas faits. La marche à suivre est dans
[`docs/essais/0001-sur-un-serveur-et-un-appareil.md`](../essais/0001-sur-un-serveur-et-un-appareil.md).

- La règle `DOCKER-USER` sur le vrai serveur, vérifiée hors du tunnel, et sa persistance
  après un redémarrage.
- Le port 53 libre sur l'adresse du VPN.
- Un conteneur de Docker Desktop qui joint l'adresse WireGuard du serveur, par le tunnel du
  Mac.
- Plusieurs tunnels WireGuard actifs à la fois sur un Mac ; à défaut, un tunnel à plusieurs
  pairs.
- La confiance totale d'iOS envers une autorité en X.509 v1.

## Alternatives écartées

- **Un proxy par projet**, le statu quo : les ports se disputent, et chaque projet réécrit la
  même mécanique — compagnon, relecture de Traefik, contrat.
- **Tout sur 443, un nom par service, pour tous les projets** : écarté pour l'existant, dont
  les ports historiques portent des URL et une configuration ; retenu comme convention des
  nouveaux projets (décision 3).
- **Un `compose.yaml` et un `Makefile`** pour installer waggle : simple, mais statique. Un
  serveur à plusieurs VPN demanderait de le dupliquer, et Compose ne sait pas boucler sur
  les ports du relais (décision 1).
- **Les ports historiques dans ce dépôt** : il nommerait les ports d'un projet, et chaque
  poste les ouvrirait, même sans ce projet.
- **L'étiquette `dev.projet`**, celle du brouillon : elle ne dit pas qui la lit.
- **Un nom pour chaque sous-partie** (les gardiennes pour le pare-feu, une ruche par
  instance) : des mots simples suffisent — instance, relais, pare-feu, DNS du VPN —, et ne
  demandent rien aux collègues.
- **Un DNS séparé sur le poste** (`/etc/resolver/<domaine>` vers le serveur) : il marche,
  mais avec trois défauts :
  - le poste perd ses stacks locales tant qu'il pointe vers le serveur ;
  - il ne résout plus rien, VPN coupé ;
  - il ne peut rien pour `.localhost`.
  Le relais fait mieux, sans DNS.
- **Un DNS public vers l'adresse du VPN** : une adresse privée dans un DNS public, que des
  résolveurs filtrent ; et `.localhost` n'y passe pas.
- **Une seule instance à l'écoute de plusieurs VPN** : elle rompt l'isolement (décision 7).
- **Une seule instance, avec un point d'entrée par VPN** : l'isolement dépendrait de chaque
  projet, qui devrait nommer le point d'entrée de son VPN. Un oubli, ou un routeur sans
  point d'entrée, l'ouvrirait aux deux.
- **Le relais à priorité 1**, avec une règle « jamais 1 » dans le contrat : une égalité ne
  garantit rien, et ce serait une règle de plus à tenir dans chaque projet.
- **Un VPN managé** (Tailscale et son DNS) : il remplacerait un WireGuard déjà en place. À
  reconsidérer si la gestion des tunnels devient le problème.
- **Exposer une URL le temps d'un partage** (un tunnel public) : contraire à la règle.

## Conséquences

- **Par machine, une fois** : installer waggle.
  - Sur un serveur : les adresses et les interfaces des VPN, les projets de chacun ; la
    règle de pare-feu, posée, rendue persistante et vérifiée.
  - Sur un poste qui travaille aussi à distance : l'adresse du serveur, pour le relais.
- **Par projet, une fois par machine** :
  - ses ports historiques dans les réglages, s'il en a ;
  - s'il est en TLS, approuver son autorité, sur le téléphone aussi, en profil et en
    confiance totale ;
  - sur un serveur, déclarer son domaine au DNS du VPN de son instance.
  Un projet comme un autre déjà servi ne demande rien de plus.
- **Par worktree** : rien.
- **Les projets existants migrent**, chacun par sa propre décision :
  - `traefik.enable` et `waggle.project` sur leurs services routés ;
  - leur proxy retiré ;
  - leur installation, qui renvoie à waggle.
  Un projet qui nomme déjà ses points d'entrée d'après leur port garde ses routes. Un projet
  dont les routeurs HTTP n'en nomment aucun va sur `p80` sans rien changer.
- **Le compagnon passe des projets à waggle**, avec son piège et son test. Traefik ne relit
  pas les conteneurs à un branchement réseau : après chaque branchement, le compagnon démarre
  un conteneur éphémère pour qu'il les relise.
- **L'isolement vaut entre les membres des VPN, pas entre les projets d'un même serveur.**
  Qui lève une stack a la main sur Docker, et le socket que lisent Traefik et le compagnon
  donne tout.
- **Traefik est épinglé (v3.6).** Les priorités négatives et `asDefault`, dont waggle
  dépend, s'éprouvent dans sa suite de tests à chaque montée de version.
- **Les webhooks entrants** restent hors de portée : ils exigent un hôte public. Il faudra un
  tunnel ponctuel, hors de waggle, le jour où il le faut.
