package main

import (
	"bytes"
	"embed"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Pilote MIT de VirtualDrivers/Virtual-Display-Driver, signé SignPath (voir driver/README.md).
//
//go:embed driver/MttVDD.dll driver/MttVDD.inf driver/mttvdd.cat
var driverFS embed.FS

const (
	hwID        = `Root\MttVDD`
	pipeName    = `\\.\pipe\MTTVirtualDisplayPipe`
	vddDir      = `C:\VirtualDisplayDriver`
	vddAdapter  = "Virtual Display Driver"
	refreshRate = 60
)

type preset struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	W, H  int    `json:"-"`
}

var presets = []preset{
	{"1080", "1080p", 1920, 1080},
	{"4k", "4K", 3840, 2160},
	{"wide", "3440 wide", 3440, 1440},
}

func presetByID(id string) (preset, bool) {
	for _, p := range presets {
		if p.ID == id {
			return p, true
		}
	}
	return preset{}, false
}

// settingsXML produit vdd_settings.xml. Les résolutions sont communes à tous les moniteurs du
// pilote : le preset de chaque écran est appliqué ensuite par ChangeDisplaySettingsEx.
func settingsXML(count int, gpu string) []byte {
	if gpu == "" {
		gpu = "default"
	}
	var esc bytes.Buffer
	xml.EscapeText(&esc, []byte(gpu))
	var b bytes.Buffer
	fmt.Fprintf(&b, "<?xml version='1.0' encoding='utf-8'?>\n<vdd_settings>\n")
	fmt.Fprintf(&b, "  <monitors><count>%d</count></monitors>\n", count)
	fmt.Fprintf(&b, "  <gpu><friendlyname>%s</friendlyname></gpu>\n", esc.String())
	fmt.Fprintf(&b, "  <global><g_refresh_rate>%d</g_refresh_rate></global>\n  <resolutions>\n", refreshRate)
	for _, p := range presets {
		fmt.Fprintf(&b, "    <resolution><width>%d</width><height>%d</height><refresh_rate>%d</refresh_rate></resolution>\n", p.W, p.H, refreshRate)
	}
	fmt.Fprintf(&b, "  </resolutions>\n  <options><HardwareCursor>true</HardwareCursor><logging>false</logging><debuglogging>false</debuglogging></options>\n</vdd_settings>\n")
	return b.Bytes()
}

func writeSettings(count int, gpu string) error {
	if err := os.MkdirAll(vddDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(vddDir, "vdd_settings.xml"), settingsXML(count, gpu), 0o644)
}

// ---- périphérique (SetupAPI) ----

var displayClass = windows.GUID{Data1: 0x4D36E968, Data2: 0xE325, Data3: 0x11CE, Data4: [8]byte{0xBF, 0xC1, 0x08, 0x00, 0x2B, 0xE1, 0x03, 0x18}}

type propChangeParams struct {
	hdr         windows.ClassInstallHeader
	StateChange uint32
	Scope       uint32
	HwProfile   uint32
}

func regString(dis windows.DevInfo, di *windows.DevInfoData, prop windows.SPDRP) string {
	v, err := windows.SetupDiGetDeviceRegistryProperty(dis, di, prop)
	if err != nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case []string:
		if len(t) > 0 {
			return t[0]
		}
	}
	return ""
}

// eachDisplayDevice parcourt les périphériques « Display » présents.
func eachDisplayDevice(fn func(dis windows.DevInfo, di *windows.DevInfoData)) error {
	dis, err := windows.SetupDiGetClassDevsEx(&displayClass, "", 0, windows.DIGCF_PRESENT, 0, "")
	if err != nil {
		return err
	}
	defer windows.SetupDiDestroyDeviceInfoList(dis)
	for i := 0; ; i++ {
		di, err := windows.SetupDiEnumDeviceInfo(dis, i)
		if err != nil {
			return nil
		}
		fn(dis, di)
	}
}

func isVDD(dis windows.DevInfo, di *windows.DevInfoData) bool {
	return strings.EqualFold(regString(dis, di, windows.SPDRP_HARDWAREID), hwID)
}

func driverPresent() bool {
	found := false
	eachDisplayDevice(func(dis windows.DevInfo, di *windows.DevInfoData) { found = found || isVDD(dis, di) })
	return found
}

const (
	dicsEnable  = 1
	dicsDisable = 2
)

func setDeviceState(change uint32) error {
	var firstErr error
	err := eachDisplayDevice(func(dis windows.DevInfo, di *windows.DevInfoData) {
		if !isVDD(dis, di) {
			return
		}
		p := propChangeParams{hdr: *windows.MakeClassInstallHeader(windows.DIF_PROPERTYCHANGE), StateChange: change, Scope: 1 /* DICS_FLAG_GLOBAL */}
		e := windows.SetupDiSetClassInstallParams(dis, di, &p.hdr, uint32(unsafe.Sizeof(p)))
		if e == nil {
			e = windows.SetupDiCallClassInstaller(windows.DIF_PROPERTYCHANGE, dis, di)
		}
		if e != nil && firstErr == nil {
			firstErr = e
		}
	})
	if err != nil {
		return err
	}
	return firstErr
}

func removeDevices() error {
	return eachDisplayDevice(func(dis windows.DevInfo, di *windows.DevInfoData) {
		if isVDD(dis, di) {
			windows.SetupDiCallClassInstaller(windows.DIF_REMOVE, dis, di)
		}
	})
}

// createDevice crée le périphérique racine Root\MttVDD (équivalent de « devcon install »).
func createDevice() error {
	dis, err := windows.SetupDiCreateDeviceInfoListEx(&displayClass, 0, "")
	if err != nil {
		return err
	}
	defer windows.SetupDiDestroyDeviceInfoList(dis)
	di, err := windows.SetupDiCreateDeviceInfo(dis, "Display", &displayClass, "", 0, windows.DICD_GENERATE_ID)
	if err != nil {
		return err
	}
	id := utf16.Encode([]rune(hwID + "\x00\x00"))
	if err := windows.SetupDiSetDeviceRegistryProperty(dis, di, windows.SPDRP_HARDWAREID,
		unsafe.Slice((*byte)(unsafe.Pointer(&id[0])), len(id)*2)); err != nil {
		return err
	}
	return windows.SetupDiCallClassInstaller(windows.DIF_REGISTERDEVICE, dis, di)
}

var procUpdateDriver = windows.NewLazySystemDLL("newdev.dll").NewProc("UpdateDriverForPlugAndPlayDevicesW")

func updateDriver(inf string) error {
	h, _ := windows.UTF16PtrFromString(hwID)
	f, _ := windows.UTF16PtrFromString(inf)
	var reboot int32
	r, _, e := procUpdateDriver.Call(0, uintptr(unsafe.Pointer(h)), uintptr(unsafe.Pointer(f)), 1 /* INSTALLFLAG_FORCE */, uintptr(unsafe.Pointer(&reboot)))
	if r == 0 {
		return fmt.Errorf("UpdateDriverForPlugAndPlayDevices: %w", e)
	}
	return nil
}

// installDriver extrait le pilote embarqué dans dir, crée le périphérique si besoin et l'installe.
func installDriver(dir string) error {
	dst := filepath.Join(dir, "driver")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, n := range []string{"MttVDD.dll", "MttVDD.inf", "mttvdd.cat"} {
		b, _ := driverFS.ReadFile("driver/" + n)
		if err := os.WriteFile(filepath.Join(dst, n), b, 0o644); err != nil {
			return err
		}
	}
	if err := writeSettings(1, ""); err != nil {
		return err
	}
	if !driverPresent() {
		if err := createDevice(); err != nil {
			return fmt.Errorf("création du périphérique: %w", err)
		}
	}
	if err := updateDriver(filepath.Join(dst, "MttVDD.inf")); err != nil {
		return err
	}
	return setDeviceState(dicsDisable) // aucun écran tant que l'utilisateur n'en demande pas
}

// gpuNames liste les cartes graphiques physiques (le pilote choisit celle qui rend les écrans virtuels).
func gpuNames() []string {
	out := []string{}
	eachDisplayDevice(func(dis windows.DevInfo, di *windows.DevInfoData) {
		n := regString(dis, di, windows.SPDRP_DEVICEDESC)
		l := strings.ToLower(n)
		if n == "" || strings.Contains(l, "virtual") || strings.Contains(l, "parsec") || strings.Contains(l, "mobile monitor") || strings.Contains(l, "basic") {
			return
		}
		out = append(out, n)
	})
	return out
}

// ---- pipe du pilote ----

func sendPipe(cmd string) error {
	u := utf16.Encode([]rune(cmd))
	b := unsafe.Slice((*byte)(unsafe.Pointer(&u[0])), len(u)*2)
	var err error
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
		var f *os.File
		if f, err = os.OpenFile(pipeName, os.O_WRONLY, 0); err == nil {
			_, err = f.Write(b)
			f.Close()
			return err
		}
	}
	return fmt.Errorf("pipe du pilote injoignable: %w", err)
}

// ---- écrans (user32) ----

var (
	user32            = windows.NewLazySystemDLL("user32.dll")
	procEnumDisplays  = user32.NewProc("EnumDisplayDevicesW")
	procChangeDisplay = user32.NewProc("ChangeDisplaySettingsExW")
)

type displayDevice struct {
	Cb           uint32
	DeviceName   [32]uint16
	DeviceString [128]uint16
	StateFlags   uint32
	DeviceID     [128]uint16
	DeviceKey    [128]uint16
}

type devMode struct {
	DeviceName                        [32]uint16
	SpecVersion, DriverVersion, Size  uint16
	DriverExtra                       uint16
	Fields                            uint32
	PositionX, PositionY              int32
	DisplayOrientation, FixedOutput   uint32
	Color, Duplex, YRes, TTOption, Co int16
	FormName                          [32]uint16
	LogPixels                         uint16
	BitsPerPel, PelsWidth, PelsHeight uint32
	DisplayFlags, DisplayFrequency    uint32
	ICMMethod, ICMIntent, MediaType   uint32
	DitherType, Reserved1, Reserved2  uint32
	PanningWidth, PanningHeight       uint32
}

const dmPelsWH = 0x80000 | 0x100000 | 0x400000 // largeur | hauteur | fréquence

// virtualDisplays renvoie les noms \\.\DISPLAYn des moniteurs du pilote rattachés au bureau.
func virtualDisplays() []string {
	var out []string
	for i := uint32(0); ; i++ {
		d := displayDevice{Cb: uint32(unsafe.Sizeof(displayDevice{}))}
		if r, _, _ := procEnumDisplays.Call(0, uintptr(i), uintptr(unsafe.Pointer(&d)), 0); r == 0 {
			break
		}
		if d.StateFlags&1 != 0 /* ATTACHED_TO_DESKTOP */ && windows.UTF16ToString(d.DeviceString[:]) == vddAdapter {
			out = append(out, windows.UTF16ToString(d.DeviceName[:]))
		}
	}
	sort.Strings(out)
	return out
}

func setMode(name string, p preset) error {
	dm := devMode{Size: uint16(unsafe.Sizeof(devMode{})), Fields: dmPelsWH, PelsWidth: uint32(p.W), PelsHeight: uint32(p.H), DisplayFrequency: refreshRate}
	n, _ := windows.UTF16PtrFromString(name)
	r, _, _ := procChangeDisplay.Call(uintptr(unsafe.Pointer(n)), uintptr(unsafe.Pointer(&dm)), 0, 1 /* CDS_UPDATEREGISTRY */, 0)
	if int32(r) != 0 {
		return fmt.Errorf("ChangeDisplaySettingsEx(%s, %dx%d): code %d", name, p.W, p.H, int32(r))
	}
	return nil
}

// ---- opérations ----

var opMu sync.Mutex // une seule opération sur le pilote à la fois

func applyScreens(ids []string, gpu string) error {
	opMu.Lock()
	defer opMu.Unlock()
	if !driverPresent() {
		return fmt.Errorf("pilote non installé")
	}
	if len(ids) == 0 {
		return setDeviceState(dicsDisable)
	}
	want := make([]preset, len(ids))
	for i, id := range ids {
		p, ok := presetByID(id)
		if !ok {
			return fmt.Errorf("preset inconnu: %q", id)
		}
		want[i] = p
	}
	if err := writeSettings(len(ids), gpu); err != nil {
		return err
	}
	if err := setDeviceState(dicsEnable); err != nil {
		return err
	}
	// SETDISPLAYCOUNT réécrit le nombre et recharge le pilote, qui relit résolutions et GPU.
	if err := sendPipe(fmt.Sprintf("SETDISPLAYCOUNT %d", len(ids))); err != nil {
		return err
	}
	var names []string
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(400 * time.Millisecond) {
		if names = virtualDisplays(); len(names) == len(ids) {
			break
		}
	}
	if len(names) != len(ids) {
		return fmt.Errorf("%d écran(s) virtuel(s) attendu(s), %d détecté(s)", len(ids), len(names))
	}
	for i, n := range names {
		if err := setMode(n, want[i]); err != nil {
			return err
		}
	}
	return nil
}

func deactivateAll() error {
	saveConfig(func(c *config) { c.Screens = nil })
	opMu.Lock()
	defer opMu.Unlock()
	return setDeviceState(dicsDisable)
}
