package main

import (
	"testing"
	"unsafe"
)

func TestHotkey(t *testing.T) {
	m, vk, err := parseHotkey("Ctrl+Alt+K")
	if err != nil || m != 3 || vk != 'K' {
		t.Fatalf("Ctrl+Alt+K: %d %d %v", m, vk, err)
	}
	if _, vk, _ := parseHotkey("Shift+F12"); vk != 0x7B {
		t.Fatalf("F12 = %#x", vk)
	}
	for _, bad := range []string{"K", "Ctrl+", "Ctrl+A+B", "Ctrl+Foo", "Ctrl+F99"} {
		if _, _, err := parseHotkey(bad); err == nil {
			t.Errorf("%q devrait être refusé", bad)
		}
	}
}

func TestNewer(t *testing.T) {
	cases := []struct {
		l, c string
		want bool
	}{{"v0.2.0", "0.1.9", true}, {"v0.1.0", "0.1.0", false}, {"v0.1.0", "0.2.0", false}, {"v1.0.0", "dev", false}}
	for _, c := range cases {
		if newer(c.l, c.c) != c.want {
			t.Errorf("newer(%s,%s)", c.l, c.c)
		}
	}
}

func TestStructSizes(t *testing.T) {
	if s := unsafe.Sizeof(devMode{}); s != 220 {
		t.Errorf("DEVMODEW = %d, attendu 220", s)
	}
	if s := unsafe.Sizeof(pathInfo{}); s != 72 {
		t.Errorf("DISPLAYCONFIG_PATH_INFO = %d, attendu 72", s)
	}
	if s := unsafe.Sizeof(displayDevice{}); s != 840 {
		t.Errorf("DISPLAY_DEVICEW = %d, attendu 840", s)
	}
}
