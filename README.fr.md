# Unagikuma

<p align="center"><img src="docs/images/IMG_8128.jpeg" alt="Unagikuma" width="240"></p>

**Encore un outil de surveillance de disponibilité : u(ptime) + nagi(os) + kuma(Uptime Kuma).**

**Le meilleur des deux mondes :** un fichier sur une machine fonctionne tout de suite ; pour aller plus loin, joignez d'autres machines et cela devient un cluster. Même programme, rien à migrer.

Une supervision auto-hébergée et simple : sachez dès qu’un site ou un serveur tombe, sans être noyé sous les alertes.

[English](README.md) · [中文](README.zh.md) · [हिन्दी](README.hi.md) · [Español](README.es.md) · [العربية](README.ar.md) · **Français** · [বাংলা](README.bn.md) · [Português](README.pt.md) · [Русский](README.ru.md) · [Bahasa Indonesia](README.id.md) · [اردو](README.ur.md) · [Deutsch](README.de.md) · [日本語](README.ja.md) · [Tiếng Việt](README.vi.md) · [한국어](README.ko.md)

![La liste des moniteurs dans le ton Papier](docs/images/monitors-paper.png)

## Pourquoi Unagikuma

- **Prêt en 30 secondes environ.** Téléchargez un fichier, lancez-le, ouvrez votre navigateur.
- **Un seul fichier, rien d’autre à installer.** Fonctionne sous Linux, macOS, Windows et FreeBSD.
- **Surveille l’essentiel.** Sites web, serveurs, ports, ping, DNS, certificats SSL et expiration des noms de domaine.
- **Des alertes là où vous êtes.** E-mail, Telegram, Slack, Discord, Feishu, DingTalk, WeCom, webhooks et plus encore.
- **Des pages de statut** pour tenir vos utilisateurs informés.
- **Facile de changer.** Récupérez vos moniteurs depuis Uptime Kuma, Nagios, Checkmate ou Gatus, dans l'interface web ou en ligne de commande.
- **Parle votre langue.** L’interface existe en 15 langues.

## Démarrage rapide

**1.** Téléchargez le fichier correspondant à votre système depuis [Releases](https://github.com/githubflyideas/Unagikuma/releases/latest) et décompressez-le.

**2.** Lancez-le :

```sh
./unagikuma
```

Sous Windows, lancez `unagikuma.exe`.

**3.** Ouvrez http://localhost:8077 dans votre navigateur et créez votre compte administrateur.

**4.** Cliquez sur **Ajouter une sonde**, saisissez l’adresse d’un site, et c’est tout.

> Vous l’ouvrez depuis un autre ordinateur ? Allez sur `http://<adresse-du-serveur>:8077` et saisissez le **Jeton d’installation** affiché par le programme lors de son premier démarrage.

## Une machine, deux machines, un cluster

- **Une machine :** lancez le fichier, ajoutez un moniteur, c'est fini. Ni base de données, ni agents.
- **Deux machines :** collez un code d'adhésion dans **Mode de fonctionnement**. Chaque machine vérifie et alerte de son côté ; si l'une tombe, les alertes continuent.
- **Un cluster :** ajoutez autant de sites que vous voulez. Chaque alerte montre ce que voit chaque site, pour distinguer « en panne partout » de « en panne d'ici ».

![Un moniteur en panne avec le résultat de chaque site](docs/images/detail-sites.png)

## Pas d'avalanche d'alertes

- **Votre propre réseau est coupé ?** Un seul message sur le réseau, pas un par moniteur.
- **Un hôte est en panne ?** Ses services ne sont pas signalés un par un.
- **Au plus trois alertes par minute et par canal ;** le reste arrive en un seul résumé. Les rétablissements partent toujours immédiatement.

## Venir d'un autre outil

Dans l'interface web, ouvrez **Configuration › Importer**, choisissez Uptime Kuma, Nagios, Checkmate ou Gatus, envoyez le fichier, vérifiez l'aperçu et ajoutez.

![Import depuis Uptime Kuma, Nagios, Checkmate ou Gatus dans l'interface web](docs/images/import.png)

Ou en ligne de commande : regardez d'abord avec `--dry-run` (rien n'est modifié), puis relancez la même commande sans.

```sh
./unagikuma import kuma      --dry-run /path/to/kuma.db
./unagikuma import nagios    --dry-run /etc/nagios4/nagios.cfg
./unagikuma import checkmate --dry-run monitors.json
./unagikuma import gatus     --dry-run config.yaml
```

Checkmate et Gatus : les vérifications HTTP sont reprises. Ce qui ne peut pas être repris à l'identique est listé et arrête l'import, pour que rien ne soit fait à moitié.

## Mot de passe oublié ?

Sur la machine où tourne Unagikuma :

```sh
./unagikuma reset-password admin
```

Tapez le nouveau mot de passe deux fois (il ne s'affiche pas). Ajoutez `--generate` pour en obtenir un au hasard.


## Alertes, contacts et adresse d'écoute

- E-mail/SMS réservés aux contacts actifs. Le test de canal utilise la même liste.
- Une notification en échec est retentée quelques fois (`notify.channel-retries=0` pour désactiver). Les essais de sonde sont séparés.
- L'UI est en HTTP clair par défaut. Préférez un reverse proxy TLS ou `--tls-cert`/`--tls-key`. Les agents utilisent déjà du TLS épinglé sur le même port.

## En savoir plus

- [Manuel d’utilisation (en anglais)](docs/manual.md) — tout le reste, étape par étape
- [Journal des modifications](CHANGELOG.md)

## Licence

Apache License 2.0, voir [LICENSE](LICENSE).
