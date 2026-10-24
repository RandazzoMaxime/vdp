// Phantom Display: écrans virtuels Windows pilotés depuis la zone de notification.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
)

var version = "dev" // injecté par -ldflags "-X main.version=…"

const (
	appName = "Phantom Display"
	repo    = "RandazzoMaxime/phantom-display"
	exeName = "phantom-display.exe"
)

type config struct {
	Screens []string `json:"screens"` // identifiants de preset, un par écran
	Hotkey  string   `json:"hotkey"`  // vide = aucun raccourci
	GPU     string   `json:"gpu"`     // vide = choix du pilote
}

var (
	cfgMu sync.Mutex
	cfg   config
)

func dataDir() string {
	d := filepath.Join(os.Getenv("LOCALAPPDATA"), "PhantomDisplay")
	os.MkdirAll(d, 0o755)
	return d
}

func loadConfig() {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	if b, err := os.ReadFile(filepath.Join(dataDir(), "config.json")); err == nil {
		json.Unmarshal(b, &cfg)
	}
}

func saveConfig(mutate func(*config)) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	mutate(&cfg)
	b, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(filepath.Join(dataDir(), "config.json"), b, 0o644); err != nil {
		log.Println("config:", err)
	}
}

func main() {
	if f, err := os.OpenFile(filepath.Join(dataDir(), "log.txt"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		log.SetOutput(f)
	}
	background, silent, cmd := false, false, ""
	for _, a := range os.Args[1:] {
		switch a {
		case "--install":
			cmd = "install"
		case "--uninstall":
			cmd = "uninstall"
		case "--silent":
			silent = true
		case "--background":
			background = true
		}
	}
	switch cmd {
	case "install":
		exitOn(install(silent))
	case "uninstall":
		exitOn(uninstall())
	default:
		run(background)
	}
}

func exitOn(err error) {
	if err != nil {
		log.Println(err)
		msgBox(appName, err.Error())
		os.Exit(1)
	}
}

func msgBox(title, text string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	windows.MessageBox(0, m, t, windows.MB_OK|windows.MB_ICONERROR)
}

func run(background bool) {
	// Instance unique : la seconde instance demande à la première d'ouvrir sa fenêtre.
	name, _ := windows.UTF16PtrFromString(`Global\PhantomDisplay`)
	if _, err := windows.CreateMutex(nil, false, name); err == windows.ERROR_ALREADY_EXISTS {
		showExisting()
		return
	}
	loadConfig()
	port := startServer()
	instance, _ := json.Marshal(map[string]any{"port": port, "token": token})
	os.WriteFile(filepath.Join(dataDir(), "instance.json"), instance, 0o600)

	startHotkeys(func() { go deactivateAll() })
	if cfg.Hotkey != "" {
		if err := setHotkey(cfg.Hotkey); err != nil {
			log.Println("raccourci:", err)
		}
	}
	if len(cfg.Screens) > 0 && driverPresent() {
		go func() { log.Println("restauration:", applyScreens(cfg.Screens, cfg.GPU)) }()
	}
	if !background {
		openUI()
	}
	runTray()
}

// showExisting demande à l'instance en cours d'afficher sa fenêtre.
func showExisting() {
	var inst struct {
		Port  int    `json:"port"`
		Token string `json:"token"`
	}
	b, err := os.ReadFile(filepath.Join(dataDir(), "instance.json"))
	if err != nil || json.Unmarshal(b, &inst) != nil {
		return
	}
	req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/api/show", inst.Port), nil)
	req.Header.Set("X-Token", inst.Token)
	if r, err := http.DefaultClient.Do(req); err == nil {
		r.Body.Close()
	}
}

func samePath(a, b string) bool { return strings.EqualFold(filepath.Clean(a), filepath.Clean(b)) }
