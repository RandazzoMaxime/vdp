package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows/registry"
)

const (
	taskLogon = "PhantomDisplay"      // démarrage à l'ouverture de session, sans UAC
	taskOpen  = "PhantomDisplay-Open" // lancé à la demande par le raccourci du menu Démarrer
	uninstKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\PhantomDisplay`
)

func installDir() string   { return filepath.Join(os.Getenv("ProgramFiles"), appName) }
func installedExe() string { return filepath.Join(installDir(), exeName) }
func startMenuLink() string {
	return filepath.Join(os.Getenv("ProgramData"), `Microsoft\Windows\Start Menu\Programs`, appName+".lnk")
}

func isInstalled() bool {
	self, _ := os.Executable()
	return samePath(self, installedExe())
}

func hidden(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000 /* CREATE_NO_WINDOW */}
	return c
}

func killOthers() {
	hidden("taskkill", "/F", "/IM", exeName, "/FI", fmt.Sprintf("PID ne %d", os.Getpid())).Run()
}

// install copie l'exe, installe le pilote, crée les tâches planifiées, le raccourci et l'entrée de désinstallation.
func install(silent bool) error {
	self, _ := os.Executable()
	dir := installDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	killOthers()
	if !samePath(self, installedExe()) {
		b, err := os.ReadFile(self)
		if err != nil {
			return err
		}
		for i := 0; ; i++ { // l'ancienne instance peut mettre quelques secondes à libérer l'exe
			if err = os.WriteFile(installedExe(), b, 0o755); err == nil {
				break
			}
			if i == 30 {
				return fmt.Errorf("copie de l'exécutable: %w", err)
			}
			time.Sleep(time.Second)
		}
	}
	if err := installDriver(dir); err != nil {
		return fmt.Errorf("installation du pilote: %w", err)
	}
	exe := installedExe()
	if out, err := hidden("schtasks", "/Create", "/F", "/TN", taskLogon, "/SC", "ONLOGON", "/RL", "HIGHEST", "/TR", fmt.Sprintf(`"%s" --background`, exe)).CombinedOutput(); err != nil {
		return fmt.Errorf("tâche planifiée: %v: %s", err, out)
	}
	if out, err := hidden("schtasks", "/Create", "/F", "/TN", taskOpen, "/SC", "ONCE", "/ST", "00:00", "/RL", "HIGHEST", "/TR", fmt.Sprintf(`"%s"`, exe)).CombinedOutput(); err != nil {
		return fmt.Errorf("tâche planifiée: %v: %s", err, out)
	}
	ps := fmt.Sprintf(`$s=(New-Object -ComObject WScript.Shell).CreateShortcut('%s');$s.TargetPath='schtasks.exe';$s.Arguments='/Run /TN %s';$s.IconLocation='%s,0';$s.WindowStyle=7;$s.Save()`, startMenuLink(), taskOpen, exe)
	if out, err := hidden("powershell", "-NoProfile", "-Command", ps).CombinedOutput(); err != nil {
		return fmt.Errorf("raccourci: %v: %s", err, out)
	}
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, uninstKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	k.SetStringValue("DisplayName", appName)
	k.SetStringValue("DisplayVersion", version)
	k.SetStringValue("Publisher", "Maxime")
	k.SetStringValue("DisplayIcon", exe)
	k.SetStringValue("InstallLocation", dir)
	k.SetStringValue("UninstallString", fmt.Sprintf(`"%s" --uninstall`, exe))
	task := taskOpen
	if silent {
		task = taskLogon
	}
	if err := hidden("schtasks", "/Run", "/TN", task).Run(); err != nil {
		return exec.Command(exe).Start()
	}
	return nil
}

func uninstall() error {
	killOthers()
	hidden("schtasks", "/Delete", "/F", "/TN", taskLogon).Run()
	hidden("schtasks", "/Delete", "/F", "/TN", taskOpen).Run()
	os.Remove(startMenuLink())
	removeDevices()
	infs, _ := filepath.Glob(filepath.Join(os.Getenv("windir"), "INF", "oem*.inf"))
	for _, f := range infs {
		if b, err := os.ReadFile(f); err == nil && strings.Contains(string(b), "MttVDD") {
			hidden("pnputil", "/delete-driver", filepath.Base(f), "/uninstall", "/force").Run()
		}
	}
	registry.DeleteKey(registry.LOCAL_MACHINE, uninstKey)
	os.RemoveAll(vddDir)
	// l'exe en cours d'exécution ne peut pas se supprimer lui-même
	return exec.Command("cmd", "/c", fmt.Sprintf(`ping -n 3 127.0.0.1 >nul & rmdir /s /q "%s"`, installDir())).Start()
}

// ---- mise à jour ----

type release struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

var httpc = &http.Client{Timeout: 5 * time.Minute}

func latestRelease() (*release, error) {
	resp, err := httpc.Get("https://api.github.com/repos/" + repo + "/releases/latest")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub: %s", resp.Status)
	}
	var r release
	return &r, json.NewDecoder(resp.Body).Decode(&r)
}

func semver(s string) (v [3]int) {
	for i, p := range strings.SplitN(strings.TrimPrefix(s, "v"), ".", 3) {
		v[i], _ = strconv.Atoi(strings.SplitN(p, "-", 2)[0])
	}
	return
}

func newer(latest, cur string) bool {
	if cur == "dev" {
		return false
	}
	a, b := semver(latest), semver(cur)
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

func (r *release) asset(name string) string {
	for _, a := range r.Assets {
		if a.Name == name {
			return a.URL
		}
	}
	return ""
}

func fetch(url string) ([]byte, error) {
	resp, err := httpc.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// applyUpdate télécharge la release, vérifie son SHA-256 puis lance son installateur silencieux.
// L'appelant doit quitter ensuite pour libérer l'exe installé.
func applyUpdate(r *release) error {
	exeURL, shaURL := r.asset(exeName), r.asset(exeName+".sha256")
	if exeURL == "" || shaURL == "" {
		return fmt.Errorf("release %s incomplète (exe ou .sha256 manquant)", r.Tag)
	}
	sum, err := fetch(shaURL)
	if err != nil {
		return err
	}
	exe, err := fetch(exeURL)
	if err != nil {
		return err
	}
	got := sha256.Sum256(exe)
	if f := strings.Fields(string(sum)); len(f) == 0 || !strings.EqualFold(f[0], hex.EncodeToString(got[:])) {
		return fmt.Errorf("somme SHA-256 invalide, mise à jour refusée")
	}
	tmp, err := os.MkdirTemp("", "phantom-update")
	if err != nil {
		return err
	}
	path := filepath.Join(tmp, exeName)
	if err := os.WriteFile(path, exe, 0o755); err != nil {
		return err
	}
	return exec.Command(path, "--install", "--silent").Start()
}
