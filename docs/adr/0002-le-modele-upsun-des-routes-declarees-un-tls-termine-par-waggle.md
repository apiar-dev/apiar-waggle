# ADR-0002 — Le modèle d'Upsun : des routes que le projet déclare, un TLS que waggle termine

**Statut** : **Accepté** (2026-11-14), par Benjamin.
**Amende** l'ADR-0001, décisions 2, 3 et 5.
**Identifiants** : en anglais, comme tout ce qui se configure (`waggle.project`, `{stack}`, le point
d'entrée `relay`) *(Benjamin, 2026-11-14)*.
**Zone** : ce qu'un projet déclare à waggle, et où se termine le TLS.

## Contexte

La question de Benjamin :

> Est-ce que waggle ne devrait pas se comporter comme Upsun, et que ce soit dans la config de
> waggle qu'on définit vers quelle app/container mène une route ? Car finalement même cette
> logique de routage va être répétée dans chaque projet.

### Ce que fait Upsun

- **Les routes vivent dans le projet**, sous la clé `routes` de son `.upsun/config.yaml`.
- **Chaque route mène une URL à une application.** L'URL donne un hôte, et au besoin un
  chemin (`https://{default}/blog`). L'application est désignée par `upstream: "app:http"`,
  son nom suivi de `:http`.
- **La plateforme exécute ces routes par son routeur**, qui termine le TLS.
- **Dans l'application, son serveur garde son rôle** (`web.locations` : le statique, ou le
  PHP).
- **Le « suffixe » de l'URL est le nommage de la plateforme.** `{default}` y devient un hôte
  qui encode l'environnement et le projet, par exemple
  `feature-t6dnbai-abcdef1234567.us-2.platformsh.site`. C'est l'équivalent de notre
  `<dossier>.<domaine de dev>`.
- **Les certificats.** Un joker ne couvre qu'un label, et une route comme `api.{default}`
  en ajoute un. Upsun a essayé de tout tenir dans un seul label, avec des triples tirets, puis
  y a renoncé : un label ne dépasse pas 63 caractères. Elle signe désormais un certificat par
  environnement, qui couvre tous ses noms.

Sources : Upsun, « Define routes », et « How Upsun provisions TLS certificates at scale ».

La définition des routes reste donc dans le projet. Sinon, la configuration de chaque machine
devrait connaître les routes de chaque projet. Ce qu'Upsun ôte au projet, c'est leur
exécution : le TLS, l'aiguillage, la syntaxe d'un routeur.

### Ce que l'ADR-0001 laisse au projet

- **Des étiquettes Traefik, verbeuses et recopiées d'un projet à l'autre.** Un projet en TLS
  en porte cinq lignes par port, et il a dix ports.
- **Le TLS**, que la stack termine elle-même. waggle ne voit que le SNI. Il en découle :
  - un port par service, faute de pouvoir router par chemin ;
  - un service qui aiguille par port dans la stack ;
  - un certificat par stack. Le navigateur réutilise une connexion HTTP/2 pour tout nom que
    son certificat couvre, et waggle n'aiguille une connexion qu'une fois.

### L'existant a déjà pesé cette question

L'ADR d'un projet existant a écarté « terminer le TLS au proxy », pour trois raisons :
- la stack ne serait complète qu'avec le proxy, que ni les e2e, ni un orchestrateur, ni la CI
  n'ont sous la main ;
- l'application verrait du HTTP, et dépendrait de `X-Forwarded-Proto` pour ses URL et ses
  callbacks OAuth ;
- le websocket de rechargement de Vite demanderait son propre réglage.

Les essais (§ Les essais) répondent à la troisième. Les deux premières sont des coûts réels,
repris aux conséquences.

Un précédent : le routeur de DDEV. C'est un Traefik commun aux projets d'une machine, qui
termine le TLS avec une autorité locale (mkcert), dont chaque projet tire sa configuration,
et qui passe les requêtes en HTTP au serveur du projet (DDEV, « Router Customization and
Debugging », FAQ).

## Décision

Le modèle d'Upsun vaut pour les nouveaux projets et pour ceux qui migrent. Le passage TLS de
l'ADR-0001 reste un second mode, domaine par domaine. Les projets existants migrent à leur
rythme, par leur propre décision. *(Tranché avec Benjamin, 2026-11-14.)*

1. **Un format de routes propre à waggle, déclaré par le projet, traduit par waggle.**
   - Chaque service routé porte ses routes, en étiquettes de waggle, et non plus de Traefik.
     Elles voyagent avec le conteneur, et waggle les lit par l'API de Docker, sans accès au
     dossier du projet *(tranché avec Benjamin, 2026-11-14)*. Une route est une URL et le
     port du service. `{stack}` y désigne le nom de la stack. Une esquisse, arrêtée au plan
     d'implémentation :

     ```yaml
     app:
       labels:
         waggle.project: my-project
         waggle.routes: |
           https://{stack}.dev.exemple.fr/      8080
           https://{stack}.dev.exemple.fr:3000/ 5173
     api:
       labels:
         waggle.project: my-project
         waggle.routes: https://{stack}.dev.exemple.fr/api 8000
     ```

   - **Le schéma de l'URL dit le mode** :
     - `https://` : waggle termine le TLS, et route par hôte et par chemin ;
     - `http://` : sur `p80`, comme aujourd'hui (`http://admin.{stack}.localhost/`) ;
     - `tls://` : le passage, routé par le SNI, d'un projet qui termine son TLS (décision 4).
   - **Un hôte joker** (`https://*.{stack}.dev.exemple.fr/`) sert une famille d'hôtes.
   - **waggle refuse une route dont l'hôte ne porte pas `{stack}`** : une stack ne peut pas
     prendre le nom d'une autre.
   - **Le compagnon devient le traducteur.** À chaque service qui démarre, il le branche, lit
     ses routes et les écrit dans la configuration de Traefik, qui la relit d'elle-même. Le
     fournisseur Docker de Traefik n'est plus nécessaire, et son piège devrait disparaître
     avec lui. C'est attendu, à éprouver à l'implémentation.

2. **waggle termine le TLS, avec une autorité par instance** *(tranché avec Benjamin,
   2026-11-14)*.
   - **Sur un poste**, l'autorité de waggle naît à son installation, et sa clé ne quitte pas
     la machine. Le navigateur du poste l'approuve une fois.
   - **Sur un serveur**, il y a une autorité par instance, donc par VPN. Un téléphone
     approuve celle de son VPN, et rien d'un autre.
   - **Un joker par domaine** couvre les noms à un label (`<dossier>.<domaine>`). Une stack
     qui a des noms à deux labels (`api.<dossier>.<domaine>`) reçoit de waggle son propre
     certificat. C'est le choix d'Upsun, un certificat par environnement.
   - **La réutilisation d'une connexion HTTP/2 devient sans danger** : Traefik route chaque
     requête d'après son `Host`.
   - **L'application reçoit du HTTP**, avec `X-Forwarded-Proto`, `X-Forwarded-Host` et
     `X-Forwarded-Port`. Elle fait confiance à waggle comme à un répartiteur en production.
   - **Les ports historiques restent possibles**, terminés par waggle sur `p<port>` : un
     projet qui migre garde ses URL. Un nouveau projet passe par 443, et route par nom ou par
     chemin.

3. **Le relais suit le mode de chaque domaine.**
   - **Pour un domaine terminé par waggle**, le poste termine le TLS avec sa propre autorité,
     y compris pour les noms du serveur, et relaie en HTTP, requête par requête.
     - Le relais vise un point d'entrée `relay` du serveur, en HTTP, sur l'adresse du VPN :
       le tunnel chiffre.
     - Ce point d'entrée ne croit les en-têtes `X-Forwarded-*` que des adresses du VPN.
     - Le poste n'a qu'une autorité à approuver. Une même connexion porte des noms locaux et
       des noms du serveur.
   - **Pour un domaine en passage**, le relais reste TCP, en passage, routé par le SNI.
   - **Un relais par domaine, jamais de relais TCP fourre-tout sur 443.** Sur un point
     d'entrée qui porte des routes HTTPS, un relais TCP `HostSNI(`*`)` ne reçoit plus rien
     (constaté). L'ADR-0001 relayait « TCP sur tout SNI » : la décision 5 s'amende ainsi.
   - Le relais HTTP sur `p80`, sur tout `Host`, ne change pas.

4. **Le passage TLS de l'ADR-0001 reste un second mode, par domaine.**
   - Un projet qui doit rester complet sans waggle garde son TLS et ses routes `tls://`, sur
     un domaine que waggle ne termine pas. C'est le cas des e2e qui visent la vraie URL
     depuis la stack, ou d'une CI qui lève la stack seule.
   - **Un domaine est terminé ou en passage, jamais les deux.** Le joker de waggle couvre tout
     le domaine. Un navigateur qui réutilise une connexion terminée par waggle pour un nom en
     passage de ce domaine obtient un 404 (constaté).

5. **L'isolement par VPN de l'ADR-0001 ne change pas.**
   - Toujours une instance par VPN, la règle de pare-feu par adresse, et un DNS du VPN.
   - S'y ajoute une autorité par instance (décision 2).
   - Le point d'entrée `relay` du serveur est publié sur l'adresse du VPN : la même règle
     le ferme au reste.

## Les essais

### Faits sur un Mac, le 2026-11-14

Ils ont tourné sur des projets Compose jetables et des ports libres, retirés ensuite. Les
proxies qui tournent sur le poste n'ont rien lu.

- **Le montage** :
  - Traefik v3.6, en deux instances : un poste, qui termine le TLS et relaie, et un serveur.
  - Trois autorités jetables : celle du poste, celle du serveur, et celle d'un projet en
    passage.
  - Derrière chaque instance, nginx répond l'hôte et les en-têtes `X-Forwarded-*` reçus.
- **Les connexions HTTP/2 réutilisées** : un client Node ouvre une seule connexion pour un
  nom, puis y envoie des requêtes pour d'autres noms. C'est ce que fait un navigateur quand le
  certificat couvre le nouveau nom.

| Essai | Résultat |
| --- | --- |
| Une connexion HTTP/2 ouverte pour la stack a, sous le joker du poste, puis des requêtes pour b, a, et un nom inconnu | chacune va à sa stack ; 404 pour l'inconnu |
| La même, ouverte pour b | idem |
| Les en-têtes reçus par l'application | `X-Forwarded-Proto: https`, `X-Forwarded-Host` (avec le port), `X-Forwarded-Port` |
| Des noms à deux labels (`api.a2…`), sous le certificat de leur stack (`a2…` et `*.a2…`) | servis ; Traefik choisit le certificat le plus précis |
| Un port historique terminé par waggle (`p3000`) | servi par le port du service ; un nom sans route sur ce port reçoit un 404 |
| Le websocket de Vite 8.3.8, Vite en HTTP derrière waggle, qui termine le TLS | client et jeton servis, `101`, `connected`, puis `full-reload` quand `main.js` change. Aucun réglage du HMR : seul `allowedHosts` |
| Le websocket sur HTTP/2 (RFC 8441) | non annoncé par Traefik : un navigateur ouvre une connexion HTTP/1.1 pour le websocket, celle de l'essai ci-dessus |
| Relais : un nom du serveur, une connexion neuve vers le poste | le poste présente son certificat, relaie en HTTP au point d'entrée `relay` du serveur, et l'application reçoit `X-Forwarded-Proto: https` |
| Relais : une connexion ouverte pour un nom local, réutilisée pour un nom du serveur | servi par le serveur, sur la même connexion |
| Un téléphone, directement sur le serveur | le serveur présente son propre certificat et sert la stack |
| Deux modes sur le même 443 du poste : un nom en passage local, un nom en passage relayé par domaine | chacun servi, avec le certificat de l'autorité du projet |
| Un nom en passage *dans* le domaine terminé par waggle : en connexion neuve, puis sur une connexion réutilisée | connexion neuve : servi en passage ; connexion réutilisée : 404 |
| Un relais TCP fourre-tout `HostSNI(`*`)`, à priorité `-1` puis par défaut, à côté de routes HTTPS | il ne reçoit rien : le poste présente son propre certificat |
| Une autre stack joint `https://a…` par waggle, via `host-gateway` | servie, avec l'autorité du poste |

### À éprouver

- **Sur un serveur, une stack qui joint sa propre URL.** C'est le cas des e2e. Elle passe
  par l'adresse du VPN, et la règle de pare-feu de l'ADR-0001 refuse ce qui vient des ponts
  de Docker. Il faut une exception pour eux, ou brancher le client de la stack sur le réseau
  de waggle.
- **La confiance d'iOS envers une autorité de waggle**, en X.509 v3 cette fois.
- **Le compagnon traducteur.** Traefik relit son fichier sans le piège du fournisseur
  Docker, et résout le nom du service à chaque connexion.

## Alternatives écartées

- **Le statu quo, l'ADR-0001** : le passage TLS, et des étiquettes Traefik dans les projets.
  La répétition demeure, et le routage reste limité au SNI : pas de chemin, des ports.
- **Le format seul, avec le passage TLS** : il ôte la verbosité, pas les ports, ni le service
  qui aiguille par port, ni le certificat par stack. Le SNI ne porte pas de chemin.
- **Le modèle d'Upsun seul, sans passage** : plus simple, mais tout projet existant doit
  migrer d'un coup, et une stack ne serait plus jamais complète sans waggle.
- **Les routes dans la configuration de la machine** : chaque machine devrait connaître les
  routes de chaque projet. C'est le contraire de ce que fait Upsun.
- **Une autorité commune à l'équipe** : une seule confiance par appareil, mais une clé qui
  circule. Une clé par instance ne sort pas de la machine.
- **Les routes dans un fichier du projet**, à la `.upsun/config.yaml` : waggle devrait le
  lire dans le dossier de chaque stack, alors que les étiquettes lui arrivent par l'API de
  Docker.
- **Relayer le TLS jusqu'au serveur, pour un domaine terminé par waggle** : le serveur
  terminerait avec son autorité. Mais le poste et le serveur présentent tous deux un joker
  sur ce domaine, à la même adresse. Le navigateur réutiliserait la connexion de l'un pour un
  nom de l'autre, et obtiendrait un 404.

## Conséquences

- **Un projet en HTTP routé par `Host`** traduit ses étiquettes Traefik en routes `http://`.
  Rien d'autre ne change. Il gagne le routage par chemin, s'il le veut.
- **Un projet en TLS qui passe au modèle d'Upsun** :
  - il retire le service qui aiguille par port, le service qui signe ses certificats, et le
    TLS de chacun de ses serveurs ;
  - il déclare ses routes `https://`. Il peut garder ses ports, et donc ses URL ;
  - son application fait confiance à waggle comme proxy ;
  - chaque appareil approuve l'autorité de waggle, et non plus celle du projet ;
  - sa stack n'est plus complète sans waggle. Ses e2e passent par waggle, et sa CI lève
    waggle comme une machine de plus. Cela revient sur ses propres décisions, par sa
    propre décision.
- **Un projet en TLS qui reste en passage** déclare ses routes `tls://` dans le format de
  waggle, sur un domaine à lui. Rien d'autre ne change.
- **waggle détient des clés** : une autorité par instance, ses jokers, et les certificats de
  stacks. Il les signe, les renouvelle, et ne les publie jamais.
- **La CI d'un projet terminé par waggle lève waggle.** waggle doit pouvoir s'installer de
  façon non interactive, sur la boucle locale d'un runner.
