# Phantom Display

Ajoute et supprime des **écrans virtuels** Windows depuis la zone de notification.
Utile quand tes écrans physiques sont éteints la nuit mais que tes benchs GPU ont besoin d'un écran « allumé » pour mesurer la vraie charge d'affichage.

- Résolutions : **1080p** (1920×1080), **4K** (3840×2160), **3440 wide** (3440×1440), à 60 Hz — par écran.
- Jusqu'à 4 écrans, GPU de rendu au choix.
- **Raccourci global** (aucun par défaut) qui désactive tous les écrans virtuels.
- Icône dans la zone de notification (flèche de la barre des tâches) : Ouvrir, Désactiver tous les écrans virtuels, Quitter.
- Démarrage automatique avec Windows (sans invite UAC), mise à jour intégrée (SHA-256 vérifié).

## Installation

1. Télécharge `phantom-display.exe` depuis les [releases](https://github.com/RandazzoMaxime/phantom-display/releases/latest).
2. Lance-le (invite UAC : l'installation d'un pilote exige les droits administrateur) puis clique **Installer**.

L'installateur est inclus dans l'exécutable : il copie l'app dans `C:\Program Files\Phantom Display`, installe le
pilote [Virtual Display Driver](https://github.com/VirtualDrivers/Virtual-Display-Driver) (MIT, signé SignPath, embarqué),
crée le démarrage automatique et l'entrée du menu Démarrer, et s'enregistre dans « Applications installées ».

Windows SmartScreen peut avertir : l'exécutable n'est pas signé par un certificat de code.

## Fonctionnement

Le pilote est un *Indirect Display Driver* (IddCx). Phantom Display écrit `C:\VirtualDisplayDriver\vdd_settings.xml`,
envoie `SETDISPLAYCOUNT n` sur le pipe `\.\pipe\MTTVirtualDisplayPipe`, puis fixe la résolution de chaque moniteur avec
`ChangeDisplaySettingsEx`. « Désactiver tout » désactive le périphérique `Root\MttVDD`.

Ajouter ou retirer un écran recharge le pilote : les écrans virtuels clignotent, les écrans physiques non.

## Compiler

```
go build -trimpath -ldflags "-s -w -H=windowsgui -X main.version=dev" -o phantom-display.exe .
__COMPAT_LAYER=RunAsInvoker go test ./...
```

Les ressources Windows (`rsrc_windows_amd64.syso`) sont générées avec `windres -O coff -i app.rc -o rsrc_windows_amd64.syso`.

## Désinstaller

« Applications installées » → Phantom Display, ou `phantom-display.exe --uninstall`.

## Licence

MIT. Le pilote embarqué est © MikeTheTech / VirtualDrivers, MIT également (voir `driver/README.md`).
