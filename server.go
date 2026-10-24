package main

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

//go:embed ui/index.html
var indexHTML string

var (
	token   = randomHex()
	srvPort int
)

func randomHex() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// startServer sert l'interface sur 127.0.0.1. Le jeton (inconnu des pages web tierces) et le contrôle
// de l'en-tête Host protègent l'API contre le CSRF et le DNS rebinding.
func startServer() int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		exitOn(err)
	}
	srvPort = ln.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" || r.URL.Query().Get("t") != token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(strings.ReplaceAll(indexHTML, "{{TOKEN}}", token)))
	})
	mux.HandleFunc("/api/", api)
	host := fmt.Sprintf("127.0.0.1:%d", srvPort)
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != host {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	return srvPort
}

func api(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Token") != token || r.Method != "POST" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var in struct {
		Screens []string `json:"screens"`
		GPU     string   `json:"gpu"`
		Hotkey  string   `json:"hotkey"`
	}
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in)

	var out any
	var err error
	switch r.URL.Path {
	case "/api/state":
		out = state()
	case "/api/show":
		go openUI()
	case "/api/apply":
		if err = applyScreens(in.Screens, in.GPU); err == nil {
			saveConfig(func(c *config) { c.Screens, c.GPU = in.Screens, in.GPU })
		}
	case "/api/deactivate":
		err = deactivateAll()
	case "/api/hotkey":
		if err = setHotkey(in.Hotkey); err == nil {
			saveConfig(func(c *config) { c.Hotkey = in.Hotkey })
		}
	case "/api/install":
		self, _ := os.Executable()
		if err = exec.Command(self, "--install").Start(); err == nil {
			go func() { time.Sleep(500 * time.Millisecond); os.Exit(0) }()
		}
	case "/api/update/check":
		var rel *release
		if rel, err = latestRelease(); err == nil {
			out = map[string]any{"latest": rel.Tag, "newer": newer(rel.Tag, version)}
		}
	case "/api/update/apply":
		var rel *release
		if rel, err = latestRelease(); err == nil {
			if err = applyUpdate(rel); err == nil {
				go func() { time.Sleep(500 * time.Millisecond); os.Exit(0) }()
			}
		}
	case "/api/quit":
		go func() { time.Sleep(200 * time.Millisecond); os.Exit(0) }()
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"data": out})
}

func state() map[string]any {
	cfgMu.Lock()
	c := cfg
	cfgMu.Unlock()
	if c.Screens == nil {
		c.Screens = []string{}
	}
	return map[string]any{
		"version":   version,
		"installed": isInstalled(),
		"driver":    driverPresent(),
		"active":    len(virtualDisplays()),
		"screens":   c.Screens,
		"hotkey":    c.Hotkey,
		"gpu":       c.GPU,
		"gpus":      gpuNames(),
		"presets":   presets,
	}
}
