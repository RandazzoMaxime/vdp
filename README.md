# VDP — Virtual Display

Ajoute et supprime des **écrans virtuels** Windows depuis la zone de notification.
Utile quand tes écrans physiques sont éteints la nuit mais que tes benchs GPU ont besoin d'un écran « allumé » pour mesurer la vraie charge d'affichage.

- Résolutions : **1080p** (1920×1080), **4K** (3840×2160), **3440 wide** (3440×1440), à 60 Hz — par écran.
- Ajout et retrait d'un écran à la fois, sans recharger le pilote (pas de gel de l'affichage).
- **Raccourci global** (aucun par défaut) qui désactive tous les écrans virtuels.
- Icône dans la zone de notification (flèche de la barre des tâches) : Ouvrir, Désactiver tous les écrans virtuels, Quitter.
- Démarrage automatique avec Windows (sans invite UAC), mise à jour intégrée (SHA-256 vérifié).

## Installation

1. Télécharge `vdp.exe` depuis les [releases](https://github.com/RandazzoMaxime/vdp/releases/latest).
2. Lance-le (invite UAC : l'installation d'un pilote exige les droits administrateur) puis clique **Installer**.

L'installateur est inclus dans l'exécutable : il copie l'app dans `C:\Program Files\VDP`, installe le
pilote **Parsec Virtual Display Driver** s'il est absent, crée le démarrage automatique et l'entrée du menu Démarrer,
et s'enregistre dans « Applications installées ».

Le pilote Parsec (propriétaire, signé Parsec Cloud, Inc.) n'est pas embarqué : il est téléchargé chez Parsec
(`builds.parsec.app`) à l'installation, puis vérifié par SHA-256. S'il est déjà présent, il est réutilisé et jamais désinstallé.
L'ancien pilote MttVDD des versions 0.1.x est nettoyé.

Windows SmartScreen peut avertir : l'exécutable n'est pas signé par un certificat de code.

## Fonctionnement

Le pilote est un *Indirect Display Driver* (IddCx). VDP lui envoie des `DeviceIoControl` (ajout, retrait, « ping » toutes les 100 ms),
active le moniteur dans la topologie d'affichage (`SetDisplayConfig`) puis fixe sa résolution avec `ChangeDisplaySettingsEx`.

Le pilote retire les écrans dès que l'application cesse de le « pinger » : **les écrans virtuels n'existent que tant que VDP tourne**.
Il reste donc dans la zone de notification et restaure tes écrans au démarrage.

## Compiler

```
go build -trimpath -ldflags "-s -w -H=windowsgui -X main.version=dev" -o vdp.exe .
__COMPAT_LAYER=RunAsInvoker go test ./...
```

Les ressources Windows (`rsrc_windows_amd64.syso`) sont générées avec `windres -O coff -i app.rc -o rsrc_windows_amd64.syso`.

## Désinstaller

« Applications installées » → VDP, ou `vdp.exe --uninstall`. Le pilote Parsec n'est retiré que s'il a été installé par VDP.

## Licence

MIT (le code de VDP). Le pilote Parsec reste soumis à sa propre licence.
