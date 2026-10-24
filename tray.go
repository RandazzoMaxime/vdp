package main

import (
	_ "embed"
	"fmt"
	"log"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"unsafe"

	"fyne.io/systray"
	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

//go:embed assets/icon.ico
var iconICO []byte

func runTray() {
	systray.Run(func() {
		systray.SetIcon(iconICO)
		systray.SetTooltip(appName)
		open := systray.AddMenuItem("Ouvrir "+appName, "")
		off := systray.AddMenuItem("Désactiver tous les écrans virtuels", "")
		systray.AddSeparator()
		quit := systray.AddMenuItem("Quitter", "")
		systray.SetOnTapped(openUI)
		go func() {
			for {
				select {
				case <-open.ClickedCh:
					openUI()
				case <-off.ClickedCh:
					go func() { log.Println("désactivation:", deactivateAll()) }()
				case <-quit.ClickedCh:
					systray.Quit()
					return
				}
			}
		}()
	}, func() {})
}

// ---- fenêtre (WebView2) ----

var uiHwnd atomic.Uintptr

func openUI() {
	if h := uiHwnd.Load(); h != 0 {
		user32.NewProc("ShowWindow").Call(h, 9 /* SW_RESTORE */)
		user32.NewProc("SetForegroundWindow").Call(h)
		return
	}
	go func() {
		runtime.LockOSThread()
		url := fmt.Sprintf("http://127.0.0.1:%d/?t=%s", srvPort, token)
		w := webview2.NewWithOptions(webview2.WebViewOptions{
			AutoFocus:     true,
			WindowOptions: webview2.WindowOptions{Title: appName, Width: 560, Height: 880, IconId: 1, Center: true},
		})
		if w == nil { // runtime WebView2 absent: navigateur par défaut
			log.Println("WebView2 indisponible")
			exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
			return
		}
		uiHwnd.Store(uintptr(w.Window()))
		defer func() { uiHwnd.Store(0); w.Destroy() }()
		w.Navigate(url)
		w.Run()
	}()
}

// ---- raccourci global ----

const (
	wmHotkey  = 0x0312
	wmApp     = 0x8000
	hotkeyID  = 1
	modNoRept = 0x4000
)

var (
	procRegisterHotKey   = user32.NewProc("RegisterHotKey")
	procUnregisterHotKey = user32.NewProc("UnregisterHotKey")
	procGetMessage       = user32.NewProc("GetMessageW")
	procPeekMessage      = user32.NewProc("PeekMessageW")
	procPostThreadMsg    = user32.NewProc("PostThreadMessageW")
	hkThread             uint32
	hkCh                 = make(chan hkReq)
)

type hkReq struct {
	combo string
	res   chan error
}

var vkNames = map[string]uint32{
	"SPACE": 0x20, "ENTER": 0x0D, "TAB": 0x09, "INSERT": 0x2D, "DELETE": 0x2E, "HOME": 0x24,
	"END": 0x23, "PAGEUP": 0x21, "PAGEDOWN": 0x22, "LEFT": 0x25, "UP": 0x26, "RIGHT": 0x27, "DOWN": 0x28,
}

// parseHotkey lit « Ctrl+Alt+K » ; au moins un modificateur est exigé pour ne pas capter une touche seule.
func parseHotkey(s string) (mods, vk uint32, err error) {
	for _, p := range strings.Split(s, "+") {
		u := strings.ToUpper(strings.TrimSpace(p))
		switch {
		case u == "ALT":
			mods |= 1
		case u == "CTRL":
			mods |= 2
		case u == "SHIFT":
			mods |= 4
		case u == "WIN":
			mods |= 8
		case vk != 0:
			return 0, 0, fmt.Errorf("raccourci invalide: %q", s)
		case len(u) == 1 && (u[0] >= 'A' && u[0] <= 'Z' || u[0] >= '0' && u[0] <= '9'):
			vk = uint32(u[0])
		case len(u) >= 2 && u[0] == 'F':
			var n int
			if _, e := fmt.Sscanf(u[1:], "%d", &n); e != nil || n < 1 || n > 24 {
				return 0, 0, fmt.Errorf("touche inconnue: %q", p)
			}
			vk = uint32(0x6F + n)
		case vkNames[u] != 0:
			vk = vkNames[u]
		default:
			return 0, 0, fmt.Errorf("touche inconnue: %q", p)
		}
	}
	if vk == 0 || mods == 0 {
		return 0, 0, fmt.Errorf("le raccourci demande au moins un modificateur et une touche")
	}
	return
}

// startHotkeys lance le thread qui possède le raccourci (RegisterHotKey est lié au thread appelant).
func startHotkeys(onFire func()) {
	ready := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		hkThread = windows.GetCurrentThreadId()
		var msg struct {
			Hwnd    uintptr
			Message uint32
			WParam  uintptr
			LParam  uintptr
			Time    uint32
			Pt      struct{ X, Y int32 }
		}
		procPeekMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0, 0) // crée la file de messages
		close(ready)
		var current string
		register := func(combo string) error {
			if combo == "" {
				return nil
			}
			mods, vk, err := parseHotkey(combo)
			if err != nil {
				return err
			}
			if r, _, e := procRegisterHotKey.Call(0, hotkeyID, uintptr(mods|modNoRept), uintptr(vk)); r == 0 {
				return fmt.Errorf("raccourci déjà utilisé ou refusé: %w", e)
			}
			return nil
		}
		for {
			if r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0); int32(r) <= 0 {
				return
			}
			switch msg.Message {
			case wmHotkey:
				onFire()
			case wmApp:
				req := <-hkCh
				if _, _, err := parseHotkey(req.combo); req.combo != "" && err != nil {
					req.res <- err
					continue
				}
				procUnregisterHotKey.Call(0, hotkeyID)
				err := register(req.combo)
				if err != nil {
					register(current) // on garde l'ancien raccourci si le nouveau échoue
				} else {
					current = req.combo
				}
				req.res <- err
			}
		}
	}()
	<-ready
}

func setHotkey(combo string) error {
	req := hkReq{combo, make(chan error, 1)}
	procPostThreadMsg.Call(uintptr(hkThread), wmApp, 0, 0)
	hkCh <- req
	return <-req.res
}
