# VDP — Virtual Display

Add and remove Windows **virtual displays** from the notification area.
Useful when your physical screens are off at night but your GPU benchmarks need a "lit" display to measure the real display load.

- Resolutions: **1080p** (1920×1080), **4K** (3840×2160), **3440 wide** (3440×1440), at 60 Hz — per display.
- Add or remove one display at a time, without reloading the driver (no display freeze).
- **Global shortcut** (none by default) that disables all virtual displays.
- Notification area icon (taskbar arrow): Open, Disable all virtual displays, Quit.
- Starts automatically with Windows (no UAC prompt), built-in updater (SHA-256 verified).

## Installation

1. Download `vdp.exe` from the [releases](https://github.com/RandazzoMaxime/vdp/releases/latest).
2. Run it (UAC prompt: installing a driver requires administrator rights), then click **Install**.

The installer is included in the executable: it copies the app to `C:\Program Files\VDP`, installs the
**Parsec Virtual Display Driver** if it is missing, creates the automatic startup and the Start menu entry,
and registers itself in "Installed apps".

The Parsec driver (proprietary, signed by Parsec Cloud, Inc.) is not bundled: it is downloaded from Parsec
(`builds.parsec.app`) during installation, then verified by SHA-256. If it is already present, it is reused and never uninstalled.
The old MttVDD driver from versions 0.1.x is cleaned up.

Windows SmartScreen may warn: the executable is not signed with a code-signing certificate.

## How it works

The driver is an *Indirect Display Driver* (IddCx). VDP sends it `DeviceIoControl` calls (add, remove, a "ping" every 100 ms),
activates the monitor in the display topology (`SetDisplayConfig`), then sets its resolution with `ChangeDisplaySettingsEx`.

The driver removes the displays as soon as the application stops "pinging" it: **virtual displays only exist while VDP is running**.
That is why VDP stays in the notification area and restores your displays at startup.

## Building

```
go build -trimpath -ldflags "-s -w -H=windowsgui -X main.version=dev" -o vdp.exe .
__COMPAT_LAYER=RunAsInvoker go test ./...
```

The Windows resources (`rsrc_windows_amd64.syso`) are generated with `windres -O coff -i app.rc -o rsrc_windows_amd64.syso`.

## Uninstalling

"Installed apps" → VDP, or `vdp.exe --uninstall`. The Parsec driver is only removed if VDP installed it.

## License

MIT (VDP's code). The Parsec driver remains subject to its own license.
