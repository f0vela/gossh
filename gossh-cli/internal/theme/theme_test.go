package theme

import (
	"regexp"
	"testing"
)

var hex = regexp.MustCompile(`^#[0-9a-f]{6}$`)

func TestAllThemesAreComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, th := range All() {
		if seen[th.Name] {
			t.Fatalf("duplicate theme %q", th.Name)
		}
		seen[th.Name] = true
		for name, c := range map[string]string{
			"Bg": th.Bg, "BgAlt": th.BgAlt, "Surface": th.Surface, "Fg": th.Fg, "Muted": th.Muted,
			"Border": th.Border, "Accent": th.Accent, "Accent2": th.Accent2, "Success": th.Success,
			"Warning": th.Warning, "Danger": th.Danger, "Info": th.Info,
		} {
			if !hex.MatchString(c) {
				t.Errorf("%s.%s = %q is not #rrggbb", th.Name, name, c)
			}
		}
		for i, c := range th.ANSI {
			if !hex.MatchString(c) {
				t.Errorf("%s.ANSI[%d] = %q is not #rrggbb", th.Name, i, c)
			}
		}
	}
	for _, want := range []string{"dracula", "tokyonight", "catppuccin-mocha", "catppuccin-latte", "nord", "gruvbox"} {
		if !seen[want] {
			t.Errorf("missing theme %q", want)
		}
	}
}

func TestGet(t *testing.T) {
	for _, in := range []string{"dracula", "Dracula", "tokyo-night", "Tokyo Night", "catppuccin_mocha", "CATPPUCCIN MOCHA"} {
		if _, ok := Get(in); !ok {
			t.Errorf("Get(%q) not found", in)
		}
	}
	if _, ok := Get("nope"); ok {
		t.Error("unknown theme found")
	}
	if Or("nope").Name != Default {
		t.Error("Or must fall back to the default")
	}
	if all := All(); all[Index("dracula")].Name != "dracula" {
		t.Error("Index mismatch")
	}
}
