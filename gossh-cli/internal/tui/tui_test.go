package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"gossh-cli/internal/store"
	"gossh-cli/internal/theme"
)

func testModel(t *testing.T, w, h int) *Model {
	t.Helper()
	m := &Model{
		store:    store.WithDataPath(filepath.Join(t.TempDir(), "profiles.enc")),
		connIdx:  -1,
		authWait: map[int]func() tea.Cmd{},
		width:    w, height: h,
	}
	m.setTheme(theme.Or("dracula"))
	m.filter = m.st.input("buscar…", 20)
	m.profiles = []store.ConnectionProfile{
		{Name: "prod", Host: "example.com", Port: 22, User: "root", AuthMethod: store.AuthPassword, Password: "x"},
		{Name: "staging", Host: "10.0.0.5", Port: 2222, User: "deploy", AuthMethod: store.AuthKey, PrivateKeyPath: "~/.ssh/id"},
	}
	return m
}

func TestViewFillsScreenExactly(t *testing.T) {
	for _, sz := range [][2]int{{120, 36}, {80, 24}, {60, 20}, {200, 50}} {
		m := testModel(t, sz[0], sz[1])
		v := m.View()
		lines := strings.Split(v.Content, "\n")
		if len(lines) != sz[1] {
			t.Errorf("%dx%d: %d lines", sz[0], sz[1], len(lines))
		}
		for i, l := range lines {
			if w := lipgloss.Width(l); w != sz[0] {
				t.Errorf("%dx%d: line %d is %d wide", sz[0], sz[1], i, w)
				break
			}
		}
	}
}

func TestEveryModalRendersInsideScreen(t *testing.T) {
	m := testModel(t, 100, 30)
	m.sess = nil
	mods := map[string]modal{
		"form":     newFormModal(m, 0),
		"theme":    newThemeModal(m),
		"settings": newSettingsModal(m),
		"help":     newHelpModal(),
		"snippets": newSnippetsModal(m),
		"backup":   newBackupModal(m, true),
		"confirm":  newConfirm("t", "m", "d", "ok", nil, nil),
		"message":  newMessageModal("Error", "algo\nfalló"),
	}
	for name, md := range mods {
		m.modal = md
		md.init(m)
		v := m.View()
		lines := strings.Split(v.Content, "\n")
		if len(lines) != 30 {
			t.Errorf("%s: %d lines, want 30", name, len(lines))
		}
		for _, l := range lines {
			if w := lipgloss.Width(l); w != 100 {
				t.Errorf("%s: line width %d, want 100", name, w)
				break
			}
		}
	}
}

func TestFormValidation(t *testing.T) {
	m := testModel(t, 100, 30)
	f := newFormModal(m, -1)
	if _, msg := f.validate(); msg == "" {
		t.Fatal("empty form must not validate")
	}
	f.ins[fName].SetValue("a")
	f.ins[fHost].SetValue("h")
	f.ins[fUser].SetValue("u")
	if _, msg := f.validate(); !strings.Contains(msg, "contraseña") {
		t.Fatalf("password required: %q", msg)
	}
	f.ins[fPass].SetValue("pw")
	p, msg := f.validate()
	if msg != "" || p.Port != 22 {
		t.Fatalf("valid form (empty port = 22): %q %+v", msg, p)
	}
	f.ins[fPort].SetValue("70000")
	if _, msg := f.validate(); !strings.Contains(msg, "puerto") {
		t.Fatalf("bad port accepted: %q", msg)
	}
	f.ins[fPort].SetValue("22")
	f.auth = 1 // key
	if _, msg := f.validate(); !strings.Contains(msg, "llave") {
		t.Fatalf("key path required: %q", msg)
	}
}

func TestFilterAndCursor(t *testing.T) {
	m := testModel(t, 100, 30)
	m.filter.SetValue("stag")
	if f := m.filtered(); len(f) != 1 || f[0] != 1 {
		t.Fatalf("filter: %v", f)
	}
	m.filter.SetValue("")
	m.moveCursor(5)
	if m.cursor != 1 {
		t.Fatalf("cursor clamps to last: %d", m.cursor)
	}
	m.moveCursor(-9)
	if m.cursor != 0 {
		t.Fatalf("cursor clamps to first: %d", m.cursor)
	}
}

func TestSidebarKeys(t *testing.T) {
	m := testModel(t, 100, 30)
	press := func(s string) {
		var k tea.KeyPressMsg
		switch s {
		case "down":
			k = tea.KeyPressMsg{Code: tea.KeyDown}
		default:
			k = tea.KeyPressMsg{Code: rune(s[0]), Text: s}
		}
		m.Update(k)
	}
	press("down")
	if m.cursor != 1 {
		t.Fatal("down must move")
	}
	press("t")
	if _, ok := m.modal.(*themeModal); !ok {
		t.Fatal("t opens the theme picker")
	}
	// live preview then cancel restores the theme
	before := m.th.Name
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.th.Name == before {
		t.Fatal("moving in the picker previews the theme")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.th.Name != before || m.modal != nil {
		t.Fatal("esc restores the theme and closes")
	}
	press("?")
	if _, ok := m.modal.(*helpModal); !ok {
		t.Fatal("? opens help")
	}
}

func TestCompletePath(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"alpha.txt", "alpine.txt", "beta.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := completePath(filepath.Join(dir, "be")); got != filepath.Join(dir, "beta.txt") {
		t.Errorf("single match: %q", got)
	}
	if got := completePath(filepath.Join(dir, "al")); got != filepath.Join(dir, "alp") {
		t.Errorf("ambiguous advances to the common prefix: %q", got)
	}
	if got := completePath(filepath.Join(dir, "alp")); got != filepath.Join(dir, "alp") {
		t.Errorf("ambiguous with no longer prefix must not change: %q", got)
	}
	if got := completePath(filepath.Join(dir, "alph")); got != filepath.Join(dir, "alpha.txt") {
		t.Errorf("alph: %q", got)
	}
	if got := completePath(filepath.Join(dir, "zz")); got != filepath.Join(dir, "zz") {
		t.Errorf("no match: %q", got)
	}
}

func TestElapsed(t *testing.T) {
	cases := map[string]string{"30s": "<1m", "5m": "5m", "61m": "1h01m"}
	for in, want := range cases {
		d, _ := parseDur(in)
		if got := elapsed(d); got != want {
			t.Errorf("%s: %q want %q", in, got, want)
		}
	}
}

func parseDur(s string) (d time.Duration, err error) { return time.ParseDuration(s) }
