package ui

import (
	"strings"
	"testing"
	"time"

	"tuissh-go/internal/settings"
	"tuissh-go/internal/store"
)

func TestWindowTitleSanitizesRemote(t *testing.T) {
	got := windowTitle("prod", "\x1b[2Jroot@evil\x07")
	if strings.ContainsAny(got, "\x1b\x07") || !strings.HasPrefix(got, "prod — ") {
		t.Fatalf("title %q", got)
	}
	if long := windowTitle("p", strings.Repeat("x", 500)); len([]rune(long)) > 100 {
		t.Fatalf("title not capped: %d", len([]rune(long)))
	}
}

func TestValidateForm(t *testing.T) {
	base := func() *app {
		return &app{fName: "n", fHost: "h", fUser: "u", fPort: "22",
			fAuth: authLabel(store.AuthPassword), fPass: "pw", fKey: "stale", fKeyPwd: "stale"}
	}
	a := base()
	p, msg := a.validateForm()
	if msg != "" || p.PrivateKeyPath != "" || p.PrivateKeyPass != "" {
		t.Fatalf("password profile must drop unused key fields: %q %+v", msg, p)
	}
	for name, mut := range map[string]func(*app){
		"bad port":    func(a *app) { a.fPort = "70000" },
		"text port":   func(a *app) { a.fPort = "ssh" },
		"no user":     func(a *app) { a.fUser = " " },
		"no password": func(a *app) { a.fPass = "" },
		"key no path": func(a *app) { a.fAuth = authLabel(store.AuthKey); a.fKey = "" },
	} {
		a := base()
		mut(a)
		if _, msg := a.validateForm(); msg == "" {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestGuardSkipsAuthWhenNotRequired(t *testing.T) {
	for name, a := range map[string]*app{
		"setting off":      {authKind: "Touch ID"},
		"no auth possible": {authKind: "", cfg: settings.Settings{RequireForCredentials: true}},
	} {
		ran := false
		a.guard("x", func() { ran = true })
		if !ran {
			t.Errorf("%s: fn must run immediately", name)
		}
	}
}

func TestElapsed(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Second: "ahora", 5 * time.Minute: "5m", 2*time.Hour + 5*time.Minute: "2h", 50 * time.Hour: "2d",
	} {
		if got := elapsed(d); got != want {
			t.Errorf("elapsed(%v) = %q, want %q", d, got, want)
		}
	}
}
