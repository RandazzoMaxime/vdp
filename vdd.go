package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Pilote Parsec VDD (IddCx, signé Parsec) : propriétaire, donc téléchargé chez Parsec à l'installation
// (somme SHA-256 épinglée) au lieu d'être embarqué. Il ajoute et retire les moniteurs un par un,
// sans recharger le pilote, mais ils disparaissent si l'application cesse de le « pinger ».
const (
	hwID       = `Root\Parsec\VDA`
	legacyHwID = `Root\MttVDD` // pilote des versions 0.1.x
	vddDir     = `C:\VirtualDisplayDriver`
	parsecURL  = "https://builds.parsec.app/vdd/parsec-vdd-0.45.0.0.exe"
	parsecSHA  = "e23332448fdaf5aa017cb308db5ef6855fac526a7ded05d80c039404126d5362"
	parsecDir  = `Parsec Virtual Display Driver`
	targetTag  = "psccdd0" // identifiant du moniteur Parsec dans le chemin de la cible d'affichage
	refreshHz  = 60

	ioctlAdd    = 0x22E004
	ioctlRemove = 0x22A008
	ioctlUpdate = 0x22A00C
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

// ---- périphérique (SetupAPI) ----

var displayClass = windows.GUID{Data1: 0x4D36E968, Data2: 0xE325, Data3: 0x11CE, Data4: [8]byte{0xBF, 0xC1, 0x08, 0x00, 0x2B, 0xE1, 0x03, 0x18}}

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

// eachDevice parcourt les périphériques « Display » dont l'identifiant matériel est id.
func eachDevice(id string, fn func(dis windows.DevInfo, di *windows.DevInfoData)) error {
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
		if strings.EqualFold(regString(dis, di, windows.SPDRP_HARDWAREID), id) {
			fn(dis, di)
		}
	}
}

func driverPresent() bool {
	found := false
	eachDevice(hwID, func(windows.DevInfo, *windows.DevInfoData) { found = true })
	return found
}

func removeDevices(id string) {
	eachDevice(id, func(dis windows.DevInfo, di *windows.DevInfoData) {
		windows.SetupDiCallClassInstaller(windows.DIF_REMOVE, dis, di)
	})
}

// installDriver télécharge l'installateur Parsec, vérifie sa somme puis le lance en silencieux.
// Renvoie true si c'est nous qui venons d'installer le pilote.
func installDriver() (bool, error) {
	if driverPresent() {
		return false, nil
	}
	b, err := fetch(parsecURL)
	if err != nil {
		return false, fmt.Errorf("téléchargement du pilote Parsec: %w", err)
	}
	if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != parsecSHA {
		return false, fmt.Errorf("somme SHA-256 du pilote Parsec inattendue, installation refusée")
	}
	tmp, err := os.MkdirTemp("", "vdp-parsec")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(tmp)
	exe := filepath.Join(tmp, "parsec-vdd.exe")
	if err := os.WriteFile(exe, b, 0o755); err != nil {
		return false, err
	}
	if out, err := hidden(exe, "/S").CombinedOutput(); err != nil {
		return false, fmt.Errorf("installateur Parsec: %v: %s", err, out)
	}
	for end := time.Now().Add(30 * time.Second); time.Now().Before(end); time.Sleep(500 * time.Millisecond) {
		if driverPresent() {
			return true, nil
		}
	}
	return false, fmt.Errorf("le pilote Parsec n'apparaît pas après l'installation")
}

// uninstallDriver lance le désinstalleur de Parsec (silencieux).
func uninstallDriver() {
	hidden(filepath.Join(os.Getenv("ProgramFiles"), parsecDir, "uninstall.exe"), "/S").Run()
}

// removeLegacy retire le pilote MttVDD installé par les versions 0.1.x.
func removeLegacy() {
	removeDevices(legacyHwID)
	infs, _ := filepath.Glob(filepath.Join(os.Getenv("windir"), "INF", "oem*.inf"))
	for _, f := range infs {
		if b, err := os.ReadFile(f); err == nil && strings.Contains(string(b), "MttVDD") {
			hidden("pnputil", "/delete-driver", filepath.Base(f), "/uninstall", "/force").Run()
		}
	}
	os.RemoveAll(vddDir)
}

// ---- IOCTL du pilote ----

var (
	cfgmgr       = windows.NewLazySystemDLL("cfgmgr32.dll")
	procListSize = cfgmgr.NewProc("CM_Get_Device_Interface_List_SizeW")
	procList     = cfgmgr.NewProc("CM_Get_Device_Interface_ListW")
	adapterGUID  = windows.GUID{Data1: 0x00b41627, Data2: 0x04c4, Data3: 0x429e, Data4: [8]byte{0xa2, 0x6e, 0x02, 0x65, 0xcf, 0x50, 0xc8, 0xfa}}
)

func openDriver() (windows.Handle, error) {
	var n uint32
	if r, _, _ := procListSize.Call(uintptr(unsafe.Pointer(&n)), uintptr(unsafe.Pointer(&adapterGUID)), 0, 0); r != 0 || n < 2 {
		return 0, fmt.Errorf("pilote Parsec introuvable")
	}
	buf := make([]uint16, n)
	if r, _, _ := procList.Call(uintptr(unsafe.Pointer(&adapterGUID)), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(n), 0); r != 0 {
		return 0, fmt.Errorf("pilote Parsec: liste des interfaces refusée (%d)", r)
	}
	p, _ := windows.UTF16PtrFromString(windows.UTF16ToString(buf))
	// NO_BUFFERING | OVERLAPPED | WRITE_THROUGH, comme le code de référence de Parsec
	return windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, 0x80|0x20000000|0x40000000|0x80000000, 0)
}

// ioReq vit sur le tas : le pilote peut encore y écrire après un délai dépassé.
type ioReq struct {
	ov  windows.Overlapped
	out uint32
	in  [32]byte
}

var stuck []*ioReq // requêtes abandonnées, gardées en vie

func ioctl(h windows.Handle, code uint32, in ...byte) (uint32, error) {
	r := new(ioReq)
	copy(r.in[:], in)
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(ev)
	r.ov.HEvent = ev
	if err := windows.DeviceIoControl(h, code, &r.in[0], 32, (*byte)(unsafe.Pointer(&r.out)), 4, nil, &r.ov); err != nil && err != windows.ERROR_IO_PENDING {
		return 0, err
	}
	if w, _ := windows.WaitForSingleObject(ev, 5000); w != windows.WAIT_OBJECT_0 {
		windows.CancelIoEx(h, &r.ov)
		stuck = append(stuck, r)
		return 0, fmt.Errorf("le pilote ne répond pas")
	}
	return r.out, nil
}

// ---- configuration d'affichage (CCD) ----

type luid struct {
	Low  uint32
	High int32
}

// pathInfo reproduit DISPLAYCONFIG_PATH_INFO (72 octets).
type pathInfo struct {
	SrcAdapter luid
	SrcID      uint32
	SrcMode    uint32
	SrcStatus  uint32
	TgtAdapter luid
	TgtID      uint32
	TgtMode    uint32
	Tech       uint32
	Rotation   uint32
	Scaling    uint32
	RefreshN   uint32
	RefreshD   uint32
	Scanline   uint32
	Available  int32
	TgtStatus  uint32
	Flags      uint32
}

type modeInfo [64]byte // DISPLAYCONFIG_MODE_INFO, opaque pour nous

const (
	qdcAllPaths    = 1
	qdcActivePaths = 2
	pathActive     = 1
	modeInvalid    = 0xFFFFFFFF
)

var (
	procGetSizes = user32.NewProc("GetDisplayConfigBufferSizes")
	procQueryCfg = user32.NewProc("QueryDisplayConfig")
	procCfgInfo  = user32.NewProc("DisplayConfigGetDeviceInfo")
	procSetCfg   = user32.NewProc("SetDisplayConfig")
)

func queryPaths(flags uintptr) ([]pathInfo, error) {
	var np, nm uint32
	if r, _, _ := procGetSizes.Call(flags, uintptr(unsafe.Pointer(&np)), uintptr(unsafe.Pointer(&nm))); r != 0 {
		return nil, fmt.Errorf("GetDisplayConfigBufferSizes: %d", r)
	}
	if np == 0 {
		return nil, nil
	}
	paths := make([]pathInfo, np)
	modes := make([]modeInfo, nm+1)
	if r, _, _ := procQueryCfg.Call(flags, uintptr(unsafe.Pointer(&np)), uintptr(unsafe.Pointer(&paths[0])), uintptr(unsafe.Pointer(&nm)), uintptr(unsafe.Pointer(&modes[0])), 0); r != 0 {
		return nil, fmt.Errorf("QueryDisplayConfig: %d", r)
	}
	return paths[:np], nil
}

// deviceInfo remplit un en-tête DISPLAYCONFIG_DEVICE_INFO_HEADER (type, taille, adaptateur, id) dans buf.
func deviceInfo(buf []byte, typ uint32, a luid, id uint32) {
	*(*uint32)(unsafe.Pointer(&buf[0])) = typ
	*(*uint32)(unsafe.Pointer(&buf[4])) = uint32(len(buf))
	*(*luid)(unsafe.Pointer(&buf[8])) = a
	*(*uint32)(unsafe.Pointer(&buf[16])) = id
	procCfgInfo.Call(uintptr(unsafe.Pointer(&buf[0])))
}

func targetPath(p pathInfo) string {
	var b [420]byte
	deviceInfo(b[:], 2 /* TARGET_NAME */, p.TgtAdapter, p.TgtID)
	return strings.ToLower(windows.UTF16ToString((*[128]uint16)(unsafe.Pointer(&b[164]))[:]))
}

func gdiName(p pathInfo) string {
	var b [84]byte
	deviceInfo(b[:], 1 /* SOURCE_NAME */, p.SrcAdapter, p.SrcID)
	return windows.UTF16ToString((*[32]uint16)(unsafe.Pointer(&b[20]))[:])
}

type target struct {
	A  luid
	ID uint32
}

func (p pathInfo) target() target { return target{p.TgtAdapter, p.TgtID} }

// parsecTargets liste les moniteurs Parsec branchés, avec un chemin libre ou actif pour chacun.
func parsecTargets() (map[target]bool, error) {
	all, err := queryPaths(qdcAllPaths)
	if err != nil {
		return nil, err
	}
	out := map[target]bool{}
	for _, p := range all {
		if p.Available != 0 && strings.Contains(targetPath(p), targetTag) {
			out[p.target()] = true
		}
	}
	return out, nil
}

// activePath renvoie le chemin actif de t, s'il y en a un.
func activePath(t target) (pathInfo, bool) {
	act, _ := queryPaths(qdcActivePaths)
	for _, p := range act {
		if p.target() == t {
			return p, true
		}
	}
	return pathInfo{}, false
}

// activate étend le bureau sur t. Windows le fait seul pour certains moniteurs, mais en garde
// d'autres « désactivés » dans sa base : on force alors un chemin actif vers la cible.
func activate(t target) error {
	if _, ok := activePath(t); ok {
		return nil
	}
	all, err := queryPaths(qdcAllPaths)
	if err != nil {
		return err
	}
	act, _ := queryPaths(qdcActivePaths)
	used := map[[2]uint32]bool{}
	paths := make([]pathInfo, len(act), len(act)+1)
	for i, p := range act {
		used[[2]uint32{p.SrcAdapter.Low, p.SrcID}] = true
		p.SrcMode, p.TgtMode = modeInvalid, modeInvalid
		paths[i] = p
	}
	for _, p := range all {
		if p.target() == t && !used[[2]uint32{p.SrcAdapter.Low, p.SrcID}] {
			p.SrcMode, p.TgtMode, p.Flags = modeInvalid, modeInvalid, p.Flags|pathActive
			paths = append(paths, p)
			break
		}
	}
	if len(paths) == len(act) {
		return fmt.Errorf("aucun chemin libre vers le moniteur virtuel")
	}
	// SDC_APPLY | USE_SUPPLIED_DISPLAY_CONFIG | SAVE_TO_DATABASE | ALLOW_CHANGES
	if r, _, _ := procSetCfg.Call(uintptr(len(paths)), uintptr(unsafe.Pointer(&paths[0])), 0, 0, 0x80|0x20|0x200|0x400); r != 0 {
		return fmt.Errorf("SetDisplayConfig: %d", r)
	}
	return nil
}

// ---- écrans (user32) ----

var (
	user32            = windows.NewLazySystemDLL("user32.dll")
	procChangeDisplay = user32.NewProc("ChangeDisplaySettingsExW")
	procEnumSettings  = user32.NewProc("EnumDisplaySettingsW")
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

// modeIs dit si l'écran affiche déjà p : évite un changement de mode inutile.
func modeIs(name string, p preset) bool {
	dm := devMode{Size: uint16(unsafe.Sizeof(devMode{}))}
	n, _ := windows.UTF16PtrFromString(name)
	r, _, _ := procEnumSettings.Call(uintptr(unsafe.Pointer(n)), 0xFFFFFFFF /* ENUM_CURRENT_SETTINGS */, uintptr(unsafe.Pointer(&dm)))
	return r != 0 && dm.PelsWidth == uint32(p.W) && dm.PelsHeight == uint32(p.H)
}

func setMode(name string, p preset) error {
	if modeIs(name, p) {
		return nil
	}
	dm := devMode{Size: uint16(unsafe.Sizeof(devMode{})), Fields: dmPelsWH, PelsWidth: uint32(p.W), PelsHeight: uint32(p.H), DisplayFrequency: refreshHz}
	n, _ := windows.UTF16PtrFromString(name)
	r, _, _ := procChangeDisplay.Call(uintptr(unsafe.Pointer(n)), uintptr(unsafe.Pointer(&dm)), 0, 1 /* CDS_UPDATEREGISTRY */, 0)
	if int32(r) != 0 {
		return fmt.Errorf("ChangeDisplaySettingsEx(%s, %dx%d): code %d", name, p.W, p.H, int32(r))
	}
	return nil
}

// ---- opérations ----

type screen struct {
	idx uint32 // index renvoyé par le pilote
	t   target
	p   preset // résolution demandée
}

var (
	opMu    sync.Mutex // une seule opération à la fois
	devMu   sync.Mutex // protège dev et screens
	dev     windows.Handle
	screens []screen
)

func activeCount() int {
	devMu.Lock()
	defer devMu.Unlock()
	return len(screens)
}

// lostDriver oublie le pilote (déconnecté ou arrêté) : ses écrans ont disparu avec lui.
func lostDriver(h windows.Handle) {
	devMu.Lock()
	defer devMu.Unlock()
	if dev == h {
		dev, screens = 0, nil
	}
	windows.CloseHandle(h)
}

// handle ouvre le pilote et lance le « ping » qui garde les écrans en vie (au moins toutes les 100 ms).
func handle() (windows.Handle, error) {
	devMu.Lock()
	defer devMu.Unlock()
	if dev != 0 {
		return dev, nil
	}
	h, err := openDriver()
	if err != nil {
		return 0, err
	}
	dev = h
	go func() {
		for fails := 0; fails < 5; time.Sleep(100 * time.Millisecond) {
			if _, err := ioctl(h, ioctlUpdate); err != nil {
				fails++
			} else {
				fails = 0
			}
		}
		lostDriver(h)
	}()
	return h, nil
}

func addScreen(h windows.Handle) (screen, error) {
	before, err := parsecTargets()
	if err != nil {
		return screen{}, err
	}
	idx, err := ioctl(h, ioctlAdd)
	if err != nil {
		return screen{}, err
	}
	s := screen{idx: idx}
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		now, _ := parsecTargets()
		for t := range now {
			if !before[t] {
				s.t = t
			}
		}
		if s.t != (target{}) {
			break
		}
	}
	if s.t == (target{}) {
		ioctl(h, ioctlRemove, byte(idx>>8), byte(idx))
		return screen{}, fmt.Errorf("le moniteur virtuel n'apparaît pas")
	}
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		if _, ok := activePath(s.t); ok {
			break
		}
	}
	if err := activate(s.t); err != nil {
		ioctl(h, ioctlRemove, byte(idx>>8), byte(idx))
		return screen{}, err
	}
	return s, nil
}

func applyScreens(ids []string) error {
	opMu.Lock()
	defer opMu.Unlock()
	if !driverPresent() {
		return fmt.Errorf("pilote non installé")
	}
	want := make([]preset, len(ids))
	for i, id := range ids {
		p, ok := presetByID(id)
		if !ok {
			return fmt.Errorf("preset inconnu: %q", id)
		}
		want[i] = p
	}
	h, err := handle()
	if err != nil {
		return err
	}
	devMu.Lock()
	cur := append([]screen(nil), screens...)
	devMu.Unlock()
	for len(cur) > len(want) { // retire depuis la fin
		s := cur[len(cur)-1]
		if _, err := ioctl(h, ioctlRemove, byte(s.idx>>8), byte(s.idx)); err != nil {
			return err
		}
		for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) { // Windows réorganise le bureau après le retrait
			if _, ok := activePath(s.t); !ok {
				break
			}
		}
		cur = cur[:len(cur)-1]
		devMu.Lock()
		screens = append([]screen(nil), cur...)
		devMu.Unlock()
	}
	for len(cur) < len(want) {
		s, err := addScreen(h)
		if err != nil {
			return err
		}
		cur = append(cur, s)
		devMu.Lock()
		screens = append([]screen(nil), cur...)
		devMu.Unlock()
	}
	var firstErr error
	for i := range cur {
		if err := ensureMode(cur[i].t, want[i]); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("écran %d: %w", i+1, err)
		}
		cur[i].p = want[i]
	}
	devMu.Lock()
	screens = cur
	devMu.Unlock()
	return firstErr
}

// ensureMode applique la résolution puis la vérifie : juste après un retrait, Windows peut encore renommer les sources.
func ensureMode(t target, want preset) (err error) {
	for try := 0; try < 4; try++ {
		if p, ok := activePath(t); !ok {
			err = fmt.Errorf("écran inactif")
		} else if err = setMode(gdiName(p), want); err == nil && modeIs(gdiName(p), want) {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err == nil {
		err = fmt.Errorf("résolution %s non appliquée", want.ID)
	}
	return err
}

func deactivateAll() error {
	saveConfig(func(c *config) { c.Screens = nil })
	return applyScreens(nil)
}
