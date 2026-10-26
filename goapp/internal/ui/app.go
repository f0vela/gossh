// Package ui implements the desktop app with MyGo native UI: a macOS-style
// source-list sidebar with the connections and a central panel hosting a
// libghostty-vt terminal for the SSH session.
package ui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"gossh-go/internal/biometric"
	"gossh-go/internal/settings"
	"gossh-go/internal/sshx"
	"gossh-go/internal/store"
)

const appTitle = "gossh"

type app struct {
	win      *mygo.Window
	st       *store.SecureStore
	profiles []store.ConnectionProfile

	selected   string // sidebar id: profile index as string
	filter     string // sidebar search text (empty shows every profile)
	splitW     float32
	status     string // persistent status-bar text
	connecting bool
	connIdx    int  // profile of the live session, -1 when none
	dialogBusy bool // a native file dialog is open

	// security settings (Touch ID / macOS password)
	cfgStore     *settings.Store
	cfg          settings.Settings
	authKind     string // "Touch ID", "contraseña de macOS" or "" when unavailable
	locked       bool   // app locked: nothing decrypted, nothing shown
	authBusy     bool   // a system authentication prompt is open
	settingsOpen bool

	// terminal focus: refocus asks the next frame to give the terminal the
	// keyboard back after a modal closed; wasModal remembers the last frame.
	refocus  bool
	wasModal bool

	sidebarHidden bool
	connectedAt   time.Time // start of the live session, for the sidebar's elapsed time
	notes         []note

	client *sshx.Client
	term   *terminal.Terminal

	// profile form modal
	formOpen                    bool
	formIdx                     int
	fName, fHost, fPort, fUser  string
	fAuth, fPass, fKey, fKeyPwd string // fAuth is one of authChoices' labels
	formErr                     string
	delOpen                     bool
	delIdx                      int

	// backup password modal
	bkOpen          bool
	bkExport        bool
	bkPass, bkPass2 string
	bkData          []byte
	bkErr           string

	// keyboard-interactive question (OTP, 2FA…) asked by the server
	promptOpen     bool
	promptTitle    string
	promptQuestion string
	promptEcho     bool
	promptVal      string
	promptCh       chan promptResult

	// snippets modal: snEdit is -2 while listing, -1 for a new snippet, else
	// the index being edited
	snippets []store.Snippet
	snOpen   bool
	snEdit   int
	snName   string
	snText   string
	snEnter  bool
	snFilter string
	snErr    string
	snDelete int // index awaiting delete confirmation, -1 none

	// remote path modal (upload target / edit-remote open)
	pathOpen    bool
	pathUpload  bool
	pathVal     string
	uploadLocal string
	editorOpen  bool
	editorPath  string
	editorText  string
}

var (
	iconServer  = svg(`<rect width="20" height="8" x="2" y="2" rx="2"/><rect width="20" height="8" x="2" y="14" rx="2"/><line x1="6" y1="6" x2="6.01" y2="6"/><line x1="6" y1="18" x2="6.01" y2="18"/>`)
	iconPlus    = svg(`<path d="M5 12h14"/><path d="M12 5v14"/>`)
	iconImport  = svg(`<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/>`)
	iconExport  = svg(`<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/>`)
	iconPlug    = svg(`<path d="M12 22v-5"/><path d="M9 8V2"/><path d="M15 8V2"/><path d="M18 8v5a4 4 0 0 1-4 4h-4a4 4 0 0 1-4-4V8Z"/>`)
	iconUnplug  = svg(`<path d="m19 5 3-3"/><path d="m2 22 3-3"/><path d="M6.3 20.3a2.4 2.4 0 0 0 3.4 0L12 18l-6-6-2.3 2.3a2.4 2.4 0 0 0 0 3.4Z"/><path d="M7.5 13.5 10 11"/><path d="M10.5 16.5 13 14"/><path d="m12 6 6 6 2.3-2.3a2.4 2.4 0 0 0 0-3.4l-2.6-2.6a2.4 2.4 0 0 0-3.4 0Z"/>`)
	iconUpload  = svg(`<path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z"/><path d="M14 2v4a2 2 0 0 0 2 2h4"/><path d="m9 15 3-3 3 3"/><path d="M12 12v6"/>`)
	iconPencil  = svg(`<path d="M17 3a2.85 2.83 0 1 1 4 4L7.5 20.5 2 22l1.5-5.5Z"/>`)
	iconSidebar = svg(`<rect width="18" height="18" x="3" y="3" rx="2"/><path d="M9 3v18"/>`)
	iconCheck   = svg(`<path d="M20 6 9 17l-5-5"/>`)
	iconLock    = svg(`<rect width="18" height="11" x="3" y="11" rx="2" ry="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/>`)
	iconSnippet = svg(`<path d="m16 18 6-6-6-6"/><path d="m8 6-6 6 6 6"/>`)
	iconCopy    = svg(`<rect width="14" height="14" x="8" y="8" rx="2" ry="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/>`)
	iconTrash   = svg(`<path d="M3 6h18"/><path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6"/><path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2"/>`)
	iconFolder  = svg(`<path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"/>`)
)

func svg(shapes string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">` + shapes + `</svg>`))
}

type promptResult struct {
	answer string
	ok     bool
}

// note is a transient message shown as a toast. The view drains them: only
// the view has the ui.Context that toasts need.
type note struct {
	kind string // "error", "success" or "" (info)
	text string
}

const (
	noteError   = "error"
	noteSuccess = "success"
	noteInfo    = ""
)

func (a *app) notify(kind, text string) {
	a.notes = append(a.notes, note{kind, text})
	if a.win != nil {
		a.win.Invalidate()
	}
}

// authChoices maps the form's labels to the stored auth methods.
var authChoices = []struct {
	label  string
	method store.AuthMethod
}{
	{"Contraseña", store.AuthPassword},
	{"Llave privada", store.AuthKey},
	{"Llave y contraseña", store.AuthBoth},
	{"Interactivo (teclado)", store.AuthInteractive},
}

func authLabel(m store.AuthMethod) string {
	for _, c := range authChoices {
		if c.method == m {
			return c.label
		}
	}
	return authChoices[0].label
}

func authMethodOf(label string) store.AuthMethod {
	for _, c := range authChoices {
		if c.label == label {
			return c.method
		}
	}
	return store.AuthPassword
}

func authLabels() []string {
	out := make([]string, len(authChoices))
	for i, c := range authChoices {
		out[i] = c.label
	}
	return out
}

// usesPassword / usesKey tell which credential fields an auth method needs.
func usesPassword(m store.AuthMethod) bool { return m != store.AuthKey }
func usesKey(m store.AuthMethod) bool      { return m == store.AuthKey || m == store.AuthBoth }

// iconBtn builds a flat icon-only toolbar button with a11y label and
// tooltip, like macOS toolbars: no border, a soft fill on hover.
func iconBtn(c *ui.Context, icon *ui.SVG, label string) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c).Label(label).Tooltip(label).
		Size(30, 28).Radius(8).Center().
		Children(func() { ui.Icon(c, icon).Size(16, 16).TextColor(t.TextMuted) })
	if b.Hovered() {
		b.Background(t.SurfaceHover)
	}
	return b
}

// sshConn adapts *sshx.Client to the terminal plugin's Conn contract:
// io.ReadWriteCloser plus Resize(cols, rows).
type sshConn struct{ c *sshx.Client }

func (s sshConn) Read(p []byte) (int, error)  { return s.c.Stdout().Read(p) }
func (s sshConn) Write(p []byte) (int, error) { return s.c.Stdin().Write(p) }
func (s sshConn) Close() error                { s.c.Close(); return nil }
func (s sshConn) Resize(cols, rows int) error { return s.c.Resize(cols, rows) }

// Run starts the app.
func Run() error {
	st, err := store.New()
	if err != nil {
		return err
	}
	a := &app{st: st, splitW: 264, selected: "", delIdx: -1, connIdx: -1, snEdit: -2, snDelete: -1}

	mygo.App.WhenReady(func() {
		a.initSecurity()
		mygo.App.SetMenu(a.menu())
		if !a.locked {
			a.loadProfiles()
		}
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title:         appTitle,
			Width:         1100,
			Height:        700,
			TitleBarStyle: mygo.TitleBarHiddenInset,
			Transparent:   true,
			Vibrancy:      mygo.VibrancySidebar,
			Content:       ui.View(a.view),
		})
		a.win.OnClosed(func() { a.disconnect() })
		go a.tickElapsed()
		if a.locked {
			a.unlock()
		}
	})
	return mygo.App.Run()
}

// ---- security: Touch ID / lock / settings ----

// initSecurity reads the settings and what the Mac can authenticate with.
// A corrupt settings file locks the app (fail closed) instead of resetting
// the protections it held.
func (a *app) initSecurity() {
	switch {
	case biometric.Biometrics():
		a.authKind = "Touch ID"
	case biometric.Available():
		a.authKind = "contraseña de macOS"
	}
	cs, err := settings.New()
	if err != nil {
		a.notify(noteError, "ajustes: "+err.Error())
		return
	}
	a.cfgStore = cs
	cfg, err := cs.Load()
	if err != nil {
		a.notify(noteError, "No se pudieron leer los ajustes (se bloquea por seguridad): "+err.Error())
		cfg.RequireOnLaunch = true
	}
	a.cfg = cfg
	a.locked = cfg.RequireOnLaunch && a.authKind != ""
}

// tickElapsed repaints twice a minute while connected so the sidebar's
// elapsed time keeps moving.
func (a *app) tickElapsed() {
	for range time.Tick(30 * time.Second) {
		mygo.RunOnMain(func() {
			if a.client != nil && a.win != nil {
				a.win.Invalidate()
			}
		})
	}
}

// refreshMenu rebuilds the menu bar: its «Conexiones» menu carries the ⌘1…⌘9
// shortcuts, so it must follow every change to the list.
func (a *app) refreshMenu() { mygo.App.SetMenu(a.menu()) }

func (a *app) loadProfiles() {
	loaded, err := a.st.LoadProfiles()
	if err != nil {
		a.notify(noteError, err.Error())
		return
	}
	defer a.refreshMenu()
	a.profiles = loaded.Profiles
	if loaded.Notice != "" {
		a.notify(noteError, loaded.Notice)
	}
	if sn, err := a.st.LoadSnippets(); err != nil {
		a.notify(noteError, err.Error())
	} else {
		a.snippets = sn
	}
}

// authenticate asks macOS for Touch ID (or the account password) from any
// thread and runs ok on the UI thread when it succeeds. Cancelling is
// silent; any other failure is shown. At most one prompt is open.
func (a *app) authenticate(reason string, ok func()) {
	if a.authBusy {
		return
	}
	a.authBusy = true
	go func() {
		err := biometric.Authenticate(reason)
		mygo.RunOnMain(func() {
			a.authBusy = false
			switch {
			case err == nil:
				ok()
			case !errors.Is(err, biometric.ErrCancelled):
				a.notify(noteError, err.Error())
			}
			a.win.Invalidate()
		})
	}()
}

// guard runs fn at once, or after authentication when the user asked for
// credentials to be protected.
func (a *app) guard(reason string, fn func()) {
	if !a.cfg.RequireForCredentials || a.authKind == "" {
		fn()
		return
	}
	a.authenticate(reason, fn)
}

func (a *app) unlock() {
	a.authenticate("Desbloquear gossh", func() {
		a.locked = false
		if a.profiles == nil {
			a.loadProfiles()
		} else {
			a.refreshMenu()
		}
	})
}

func (a *app) lock() {
	if a.cfg.RequireOnLaunch && a.authKind != "" {
		a.locked = true
		a.settingsOpen = false
		a.snOpen = false
		a.refreshMenu() // no connection shortcuts while locked
		a.win.Invalidate()
	}
}

// changeSetting applies a settings change after authenticating, so a
// passer-by at an unlocked Mac cannot switch the protection off.
func (a *app) changeSetting(apply func(*settings.Settings)) {
	a.authenticate("Cambiar los ajustes de seguridad de gossh", func() {
		next := a.cfg
		apply(&next)
		if a.cfgStore == nil {
			a.notify(noteError, "No se pudieron guardar los ajustes")
			return
		}
		if err := a.cfgStore.Save(next); err != nil {
			a.notify(noteError, "Error guardando ajustes: "+err.Error())
			return
		}
		a.cfg = next
	})
}

// menu is the macOS menu bar: the app menu gets «Ajustes…» (⌘,) and
// «Bloquear» (⌘L); the rest are the standard menus.
func (a *app) menu() *mygo.Menu {
	conns := []*mygo.MenuItem{}
	if !a.locked {
		for i, p := range a.profiles {
			idx := i
			it := &mygo.MenuItem{Label: p.Name, Click: func(*mygo.MenuItem, *mygo.Window) { a.connectTo(idx) }}
			if i < 9 {
				it.Accelerator = "CmdOrCtrl+" + strconv.Itoa(i+1)
			}
			conns = append(conns, it)
		}
	}
	if len(conns) == 0 {
		conns = append(conns, &mygo.MenuItem{Label: "Sin conexiones", Disabled: true})
	}
	return mygo.NewMenu([]*mygo.MenuItem{
		{Label: appTitle, Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleAbout},
			mygo.Separator(),
			{Label: "Ajustes…", Accelerator: "CmdOrCtrl+,", Click: func(*mygo.MenuItem, *mygo.Window) {
				if !a.locked {
					a.settingsOpen = true
					a.win.Invalidate()
				}
			}},
			{Label: "Snippets…", Accelerator: "CmdOrCtrl+Shift+S", Click: func(*mygo.MenuItem, *mygo.Window) {
				if !a.locked {
					a.openSnippets()
				}
			}},
			{Label: "Mostrar u ocultar barra lateral", Accelerator: "CmdOrCtrl+Ctrl+S", Click: func(*mygo.MenuItem, *mygo.Window) {
				if !a.locked {
					a.toggleSidebar()
				}
			}},
			{Label: "Bloquear gossh", Accelerator: "CmdOrCtrl+L", Click: func(*mygo.MenuItem, *mygo.Window) { a.lock() }},
			mygo.Separator(),
			{Role: mygo.RoleServices},
			mygo.Separator(),
			{Role: mygo.RoleHide},
			{Role: mygo.RoleHideOthers},
			{Role: mygo.RoleUnhide},
			mygo.Separator(),
			{Role: mygo.RoleQuit},
		}},
		{Role: mygo.RoleFileMenu},
		{Role: mygo.RoleEditMenu},
		{Label: "Conexiones", Submenu: conns},
		{Role: mygo.RoleViewMenu},
		{Role: mygo.RoleWindowMenu},
	})
}

func (a *app) toggleSidebar() {
	a.sidebarHidden = !a.sidebarHidden
	a.win.Invalidate()
}

// ---- view ----

// designTheme nudges the system theme toward the DESIGN.md palette:
// charcoal accents, calm cool greys and the design's status colors.
// Dark systems keep the framework's own dark palette.
func designTheme(src *ui.Theme) *ui.Theme {
	t := *src
	if t.Dark {
		return &t
	}
	t.Background = ui.Hex("#f9f9ff") // app canvas
	t.Surface = ui.Hex("#f0f3ff")    // sidebar surface & control faces
	t.SurfaceHover = ui.Hex("#e8eeff")
	t.SurfacePressed = ui.Hex("#dfe8ff")
	t.Border = ui.Hex("#c6c6cd")
	t.Text = ui.Hex("#111c2d")
	t.TextMuted = ui.Hex("#45474c")
	t.Accent = ui.Hex("#000000") // primary buttons: black
	t.AccentHover = ui.Hex("#141b2c")
	t.AccentPressed = ui.Hex("#273143")
	t.Success = ui.Hex("#34d27b")
	t.Warning = ui.Hex("#f79009")
	t.Danger = ui.Hex("#f04438")
	t.Focus = ui.Hex("#1570ef")
	t.Selection = ui.Hex("#b2ddff")
	return &t
}

func (a *app) view(c *ui.Context) {
	for _, n := range a.notes {
		t := ui.Toast{Title: n.text, Type: n.kind}
		if n.kind == noteError {
			t.Timeout = 10e9 // errors need time to be read
		}
		c.AddToast(t)
	}
	a.notes = nil

	if a.locked {
		a.lockView(c)
		return
	}
	a.clearIdleSecrets()
	modal := a.formOpen || a.bkOpen || a.pathOpen || a.editorOpen || a.settingsOpen || a.promptOpen || a.delOpen || a.snOpen
	if a.wasModal && !modal && a.term != nil {
		a.refocus = true
	}
	a.wasModal = modal
	c.SetTheme(designTheme(c.Theme()))
	p := paletteOf(c.Theme())
	// Over the window's vibrancy the sidebar draws nothing, so the material
	// shows through; everything else paints its own background.
	p.glass = c.Vibrancy()
	if p.glass {
		c.Root().Background(ui.Transparent)
	}
	ui.Column(c).Fill().Children(func() {
		if a.sidebarHidden {
			ui.Column(c).Fill().Padding(8).Background(p.win).Children(func() { a.mainPane(c, p) })
			return
		}
		ui.Split(c, &a.splitW,
			func() { a.sidebarPane(c, p) },
			func() { a.mainPane(c, p) },
		).Fill()
	})
	a.modals(c)
}

// lockView replaces the whole window while the app is locked.
func (a *app) lockView(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Center().Gap(10).DragWindow().Children(func() {
		ui.Icon(c, iconLock).Size(44, 44).TextColor(t.TextMuted)
		ui.Text(c, "gossh está bloqueado").Bold().FontSize(17)
		ui.Text(c, "Usa "+a.authKind+" para ver tus conexiones.").TextColor(t.TextMuted)
		ui.Spacer(c).Height(6)
		if ui.PrimaryButton(c, "Desbloquear").Disabled(a.authBusy).Clicked() {
			a.unlock()
		}
	})
}

// clearIdleSecrets drops passwords and passphrases from memory as soon as
// the modal that held them is closed (by any means: button, Esc, click out).
func (a *app) clearIdleSecrets() {
	if !a.formOpen {
		a.fPass, a.fKeyPwd, a.formErr = "", "", ""
	}
	if !a.bkOpen {
		a.bkPass, a.bkPass2, a.bkData, a.bkErr = "", "", nil, ""
	}
	if !a.editorOpen {
		a.editorText = ""
	}
	if !a.promptOpen {
		a.promptVal = ""
		a.answerPrompt("", false) // dismissed (Esc / click out) = cancel
	}
}

// answerPrompt delivers the user's answer to the connecting goroutine that
// is waiting on it, once; later calls are no-ops.
func (a *app) answerPrompt(answer string, ok bool) {
	if a.promptCh == nil {
		return
	}
	a.promptCh <- promptResult{answer, ok} // buffered: never blocks
	a.promptCh = nil
	a.promptOpen = false
}

// askPrompt runs on the connect goroutine: it opens the question modal on
// the main thread and blocks until the user answers or cancels.
func (a *app) askPrompt(instruction, question string, echo bool) (string, bool) {
	ch := make(chan promptResult, 1)
	mygo.RunOnMain(func() {
		a.promptCh = ch
		a.promptTitle = strings.TrimSpace(instruction)
		a.promptQuestion = strings.TrimSpace(question)
		a.promptEcho = echo
		a.promptVal = ""
		a.promptOpen = true
		a.win.Invalidate()
	})
	r := <-ch
	return r.answer, r.ok
}

// confirmDialog shows a native question sheet from any goroutine and
// reports whether the user chose the first (confirming) button.
func (a *app) confirmDialog(title, message, detail, yes string) bool {
	var res mygo.MessageResult
	mygo.RunOnMain(func() {
		res, _ = mygo.Dialog.Message(mygo.MessageOptions{
			Parent: a.win, Type: mygo.MessageQuestion,
			Title: title, Message: message, Detail: detail,
			Buttons: []string{yes, "Cancelar"},
		})
	})
	return res.Button == 0
}

// pal holds the window's own colors, which the system theme does not
// have: the sidebar surface, the canvas, pills for session state and
// hairlines. Lights follow DESIGN.md, darks extend the old recessed look.
type pal struct {
	win, sidebar, card, cardBorder, selected, selBorder, hair, chip    ui.Color
	sidebarBorder                                                      ui.Color
	glass                                                              bool     // sidebar shows the window's vibrancy
	hostChip, hostChipText                                             ui.Color // connection address chip
	okPill, okPillText, warnPill, warnPillText, idlePill, idlePillText ui.Color
}

func paletteOf(t *ui.Theme) pal {
	if t.Dark {
		return pal{
			win: ui.Hex("#16181d"), card: ui.Hex("#1f2229"), cardBorder: ui.Hex("#2b2f37"),
			selected: ui.Hex("#2a2e37"), selBorder: ui.Hex("#363b46"), hair: ui.Hex("#262a31"),
			chip:    ui.Hex("#2a2e37"),
			sidebar: ui.Hex("#191c22"), sidebarBorder: ui.Hex("#262a31"),
			hostChip: ui.Hex("#1e2a4a"), hostChipText: ui.Hex("#9ec1ff"),
			okPill: ui.Hex("#15301f"), okPillText: ui.Hex("#6bdd92"),
			warnPill: ui.Hex("#3a2d12"), warnPillText: ui.Hex("#fdb022"),
			idlePill: ui.Hex("#262a31"), idlePillText: ui.Hex("#9a9ba1"),
		}
	}
	return pal{
		win: ui.Hex("#f9f9ff"), card: ui.Hex("#ffffff"), cardBorder: ui.Hex("#eef0f8"),
		selected: ui.Hex("#ffffff"), selBorder: ui.Hex("#eef0f8"), hair: ui.Hex("#e8eaf4"),
		chip:    ui.Hex("#e8eeff"),
		sidebar: ui.Hex("#f0f3ff"), sidebarBorder: ui.Hex("#e8eaf4"),
		hostChip: ui.Hex("#e8eeff"), hostChipText: ui.Hex("#2563eb"),
		okPill: ui.Hex("#ecfdf3"), okPillText: ui.Hex("#027a48"),
		warnPill: ui.Hex("#fffaeb"), warnPillText: ui.Hex("#b54708"),
		idlePill: ui.Hex("#e8eeff"), idlePillText: ui.Hex("#45474c"),
	}
}

// termThemes colors the terminal like the card it sits in, so the
// rounded card and the terminal read as one surface.
func termThemes() (light, dark *terminal.Theme) {
	light, dark = terminal.LightTheme(), terminal.DarkTheme()
	light.Background = ui.Hex("#fafafb")
	dark.Background = ui.Hex("#0f172a")
	return light, dark
}

func (a *app) headerHeight(c *ui.Context) float32 {
	if h := c.TitleBar().Height; h > 44 {
		return h
	}
	return 44
}

// sidebarControls are the flat icons of the sidebar header (and of the card
// header while the sidebar is hidden, where only the toggle shows).
func (a *app) sidebarControls(c *ui.Context, full bool) {
	if iconBtn(c, iconSidebar, "Mostrar u ocultar barra lateral").Clicked() {
		a.toggleSidebar()
	}
	if !full {
		return
	}
	if iconBtn(c, iconImport, "Importar backup").Clicked() {
		a.importBackup()
	}
	if iconBtn(c, iconExport, "Exportar backup").Clicked() {
		a.exportBackup()
	}
	if iconBtn(c, iconSnippet, "Snippets (⇧⌘S)").Clicked() {
		a.openSnippets()
	}
	if iconBtn(c, iconPlus, "Nueva conexión").Clicked() {
		a.openForm(-1)
	}
}

// sidebarPane is the left column: the traffic lights' row with its icons,
// then the connection list. It sits on its own sidebar surface, split
// from the workspace by a hairline border.
func (a *app) sidebarPane(c *ui.Context, p pal) {
	bar := c.TitleBar()
	col := ui.Column(c).Fill().BorderEndWidth(1).BorderColor(p.sidebarBorder)
	if !p.glass {
		col.Background(p.sidebar)
	}
	col.Children(func() {
		ui.Row(c).Height(a.headerHeight(c)).Padding(0, 10, 0, bar.Left+10).Gap(2).DragWindow().Center().Children(func() {
			ui.Spacer(c)
			a.sidebarControls(c, true)
		})
		a.sidebarView(c, p)
	})
}

func (a *app) hairline(c *ui.Context, p pal, inset float32) {
	ui.Box(c).FillWidth().Height(1).MarginX(inset).Background(p.hair)
}

// mainPane is the workspace side of the split: the toolbar with the
// connection's identity and actions, then the session (or its empty
// state) and the status footer, all sitting on the canvas.
func (a *app) mainPane(c *ui.Context, p pal) {
	ui.Column(c).Fill().Background(p.win).Children(func() {
		a.cardHeader(c, p)
		a.hairline(c, p, 0)
		ui.Column(c).FillWidth().Grow(1).Children(func() { a.centerView(c) })
		a.hairline(c, p, 0)
		a.statusBar(c, p)
	})
}

// statePill is the capsule next to the connection's name: its color tells
// the session state at a glance (design's ping-badge palette).
func (a *app) statePill(c *ui.Context, p pal) {
	t := c.Theme()
	bg, fg, dot, label := p.idlePill, p.idlePillText, p.idlePillText, "En espera"
	switch {
	case a.client != nil:
		bg, fg, dot, label = p.okPill, p.okPillText, t.Success, "Conectado · "+elapsed(time.Since(a.connectedAt))
	case a.connecting:
		bg, fg, dot, label = p.warnPill, p.warnPillText, t.Warning, "Conectando…"
	case a.connIdx >= 0:
		dot, label = t.Danger, "Sesión terminada"
	}
	ui.Row(c).Gap(6).Padding(3, 9).Radius(9999).Background(bg).Center().Children(func() {
		ui.Box(c).Size(6, 6).Radius(3).Background(dot)
		ui.Text(c, label).FontSize(11).FontWeight(500).TextColor(fg).SingleLine()
	})
}

// current is the profile the card talks about: the live session's, else
// the selected one.
func (a *app) current() (store.ConnectionProfile, bool) {
	if a.connIdx >= 0 && a.connIdx < len(a.profiles) {
		return a.profiles[a.connIdx], true
	}
	if i := a.selIdx(); i >= 0 {
		return a.profiles[i], true
	}
	return store.ConnectionProfile{}, false
}

// cardHeader: connection name, address chip and session pill on the
// left; the actions (connect, upload, edit remote) on the right.
func (a *app) cardHeader(c *ui.Context, p pal) {
	left := float32(16)
	if a.sidebarHidden {
		left = c.TitleBar().Left + 10 // clear the traffic lights
	}
	ui.Row(c).Height(a.headerHeight(c)).Padding(0, 14, 0, left).Gap(10).DragWindow().Center().Children(func() {
		if a.sidebarHidden {
			a.sidebarControls(c, false)
		}
		if pr, ok := a.current(); ok {
			mono := pr.User + "@" + pr.Host
			if pr.Port != 22 {
				mono += ":" + strconv.Itoa(int(pr.Port))
			}
			ui.Text(c, pr.Name).Font("JetBrains Mono, SF Mono, Menlo, monospace").FontSize(14).FontWeight(700).SingleLine().Shrink(1)
			ui.Row(c).Padding(2, 8).Radius(6).Background(p.hostChip).Center().Children(func() {
				ui.Text(c, mono).Font("JetBrains Mono, SF Mono, Menlo, monospace").FontSize(11).TextColor(c.Theme().Text).SingleLine()
			})
			a.statePill(c, p)
		} else {
			ui.Text(c, appTitle).FontSize(15).FontWeight(600).SingleLine()
			a.statePill(c, p)
		}

		ui.Spacer(c)

		if ui.Button(c, "").Label("Snippets").Tooltip("Snippets guardados (⇧⌘S)").Children(func() {
			ui.Icon(c, iconSnippet)
			ui.Text(c, "Snippets").SingleLine()
		}).Clicked() {
			a.openSnippets()
		}
		upBtn := ui.Button(c, "").Label("Subir archivo").Tooltip("Subir archivo por SFTP").Children(func() {
			ui.Icon(c, iconUpload)
			ui.Text(c, "Subir").SingleLine()
		})
		if upBtn.Disabled(a.client == nil).Clicked() {
			a.uploadFile()
		}
		editBtn := ui.Button(c, "").Label("Editar remoto").Tooltip("Editar un archivo remoto").Children(func() {
			ui.Icon(c, iconPencil)
			ui.Text(c, "Editar").SingleLine()
		})
		if editBtn.Disabled(a.client == nil).Clicked() {
			a.pathUpload = false
			a.pathVal = a.client.Home() + "/"
			a.pathOpen = true
		}
		connBtn := ui.PrimaryButton(c, "").Children(func() {
			switch {
			case a.client != nil:
				ui.Icon(c, iconUnplug)
				ui.Text(c, "Desconectar").SingleLine()
			case a.connecting:
				ui.Spinner(c)
				ui.Text(c, "Conectando…").SingleLine()
			default:
				ui.Icon(c, iconPlug)
				ui.Text(c, "Conectar").SingleLine()
			}
		})
		if connBtn.Disabled(a.client == nil && (a.selIdx() < 0 || a.connecting)).Clicked() {
			a.connectToggle()
		}
	})
}

// sidebarView is the connection list: the ⌘K-style filter field, a
// section label with the visible count, then compact rows with a status
// dot (green while the session is live). The selected row is a lifted
// white pill.
func (a *app) sidebarView(c *ui.Context, p pal) {
	t := c.Theme()
	q := strings.ToLower(strings.TrimSpace(a.filter))
	ui.Column(c).Fill().Padding(0, 10).Gap(8).Children(func() {
		ui.TextInput(c, &a.filter).Placeholder("Filtrar servidores…")
		ui.Row(c).FillWidth().Padding(2, 8, 0, 8).Children(func() {
			ui.Text(c, "SERVIDORES SSH").FontSize(10).FontWeight(600).LetterSpacing(0.6).TextColor(t.TextMuted)
			ui.Spacer(c)
			ui.Text(c, strconv.Itoa(len(a.profiles))).FontSize(10).TextColor(t.TextMuted)
		})
		visible := 0
		ui.Scroll(c).Fill().Children(func() {
			ui.Column(c).FillWidth().Gap(1).FocusGroup(ui.Vertical).Children(func() {
				for i, pr := range a.profiles {
					idx := i
					if q != "" && !strings.Contains(strings.ToLower(pr.Name+" "+pr.Host), q) {
						continue
					}
					visible++
					sel := strconv.Itoa(i) == a.selected
					live := idx == a.connIdx
					row := ui.ButtonBase(c).Label(pr.Name).FillWidth().Height(36).
						Padding(0, 10).Radius(10).Selected(sel)
					if sel {
						row.Background(p.selected).Border(1, p.selBorder)
					} else if row.Hovered() {
						row.Background(p.card)
					}
					row.Children(func() {
						ui.Row(c).FillWidth().Gap(8).Center().Children(func() {
							ic := ui.Icon(c, iconServer).Size(15, 15).TextColor(t.TextMuted)
							if live {
								ic.TextColor(t.Success)
							}
							ui.Text(c, pr.Name).FontSize(13).SingleLine().Grow(1).Shrink(1)
							ui.Box(c).Size(7, 7).Radius(4).
								Background(func() ui.Color {
									if live {
										return t.Success
									}
									return p.hair
								}())
						})
					})
					row.ContextMenu(func(m *ui.Menu) { a.profileMenu(m, idx) })
					if row.Clicked() {
						a.selected = strconv.Itoa(idx)
					}
					if row.DoubleClicked() {
						a.connectTo(idx)
					}
				}
				if len(a.profiles) == 0 {
					ui.Text(c, "Sin conexiones").FontSize(12).TextColor(t.TextMuted)
				} else if visible == 0 {
					ui.Text(c, "Ningún servidor coincide").FontSize(12).TextColor(t.TextMuted)
				}
			})
		})
	})
}

// elapsed formats a session age compactly: "ahora", "5m", "2h", "3d".
func elapsed(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "ahora"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	}
	return strconv.Itoa(int(d.Hours()/24)) + "d"
}

func (a *app) centerView(c *ui.Context) {
	t := c.Theme()
	switch {
	case a.term != nil:
		// AutoFocus: the keyboard goes to the terminal as soon as a session
		// opens, so the user can type without clicking it first.
		ui.Column(c).Fill().Children(func() {
			tv := terminal.View(c, a.term).Fill().AutoFocus()
			if a.refocus {
				tv.Focus()
				a.refocus = false
			}
		})
	case a.connecting:
		ui.Column(c).Fill().Center().Gap(12).Children(func() {
			ui.Spinner(c)
			ui.Text(c, a.status).TextColor(t.TextMuted)
		})
	case len(a.profiles) == 0:
		a.emptyState(c, "Aún no tienes conexiones",
			"Crea tu primera conexión SSH o importa un backup cifrado.", true)
	case a.selIdx() < 0:
		a.emptyState(c, "Selecciona una conexión",
			"Elige un servidor en la barra lateral para ver sus detalles y conectarte.", false)
	default:
		a.profileCard(c, a.profiles[a.selIdx()])
	}
}

func (a *app) emptyState(c *ui.Context, title, hint string, withAction bool) {
	t := c.Theme()
	ui.Column(c).Fill().Center().Gap(12).Children(func() {
		// the mock's tinted icon in a rounded square
		ui.Row(c).Justify(ui.Center).Children(func() {
			ui.Box(c).Size(80, 80).Radius(24).Background(paletteOf(c.Theme()).hostChip).Border(1, paletteOf(c.Theme()).cardBorder).Center().Children(func() {
				ui.Box(c).Size(60, 60).Radius(18).Background(ui.Hex("#dbe5ff")).Center().Children(func() {
					ui.Icon(c, iconServer).Size(30, 30).TextColor(ui.Hex("#2563eb"))
				})
			})
		})
		ui.Text(c, title).FontWeight(600).FontSize(28).LetterSpacing(-0.5)
		ui.Text(c, hint).TextColor(t.TextMuted).FontSize(15).MaxWidth(420).TextAlign(ui.Center)
		if withAction {
			ui.Spacer(c).Height(6)
			if ui.PrimaryButton(c, "Nueva conexión").Clicked() {
				a.openForm(-1)
			}
		}
	})
}

// profileCard is the detail pane of the selected, not connected, profile:
// an identity header, a metrics strip on a recessed panel and the actions,
// like the mock's connection sheet.
func (a *app) profileCard(c *ui.Context, p store.ConnectionProfile) {
	t := c.Theme()
	tint := paletteOf(c.Theme())
	metric := func(label, value string) {
		ui.Column(c).Gap(2).Children(func() {
			ui.Text(c, label).FontSize(11).FontWeight(500).TextColor(t.TextMuted)
			ui.Text(c, value).Font("JetBrains Mono, SF Mono, Menlo, monospace").FontSize(13).Selectable().Shrink(1)
		})
	}
	ui.Column(c).Fill().Center().Children(func() {
		ui.Column(c).Width(560).Padding(20).Gap(14).
			Background(tint.card).Border(1, tint.cardBorder).Radius(16).Children(func() {
			ui.Row(c).Gap(12).Center().Children(func() {
				ui.Box(c).Size(40, 40).Radius(10).Background(tint.hostChip).Center().Children(func() {
					ui.Icon(c, iconServer).Size(20, 20).TextColor(tint.hostChipText)
				})
				ui.Column(c).Gap(1).Grow(1).Children(func() {
					ui.Row(c).Gap(8).Center().Children(func() {
						ui.Text(c, p.Name).FontWeight(600).FontSize(16).SingleLine().Shrink(1)
						if a.connIdx == a.selIdx() {
							ui.Box(c).Size(7, 7).Radius(4).Background(t.Success)
						}
					})
					ui.Text(c, p.User+"@"+p.Host).Font("JetBrains Mono, SF Mono, Menlo, monospace").FontSize(12).TextColor(t.TextMuted).SingleLine().Shrink(1)
				})
			})
			ui.Column(c).FillWidth().Padding(12, 14).Gap(10).Background(tint.chip).Radius(8).Children(func() {
				ui.Row(c).FillWidth().Gap(28).Children(func() {
					metric("Puerto SSH", strconv.Itoa(int(p.Port)))
					metric("Autenticación", authLabel(p.AuthMethod))
					if usesKey(p.AuthMethod) && p.PrivateKeyPath != "" {
						metric("Llave privada", p.PrivateKeyPath)
					}
				})
			})
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "").Label("Parámetros").Tooltip("Editar conexión").
					Children(func() {
						ui.Icon(c, iconPencil)
						ui.Text(c, "Parámetros").SingleLine()
					}).Clicked() {
					a.openForm(a.selIdx())
				}
				connLabel := "Iniciar sesión SSH"
				if a.connecting {
					connLabel = "Conectando…"
				}
				if ui.PrimaryButton(c, "").Label(connLabel).
					Children(func() {
						if a.connecting {
							ui.Spinner(c)
						} else {
							ui.Icon(c, iconPlug)
						}
						ui.Text(c, connLabel).SingleLine()
					}).Disabled(a.connecting).Clicked() {
					a.connectTo(a.selIdx())
				}
			})
		})
	})
}

// statusBar is the card's footer: a state dot and message on the left,
// the session's address as a chip on the right.
func (a *app) statusBar(c *ui.Context, p pal) {
	t := c.Theme()
	dot := t.TextMuted
	text := a.status
	switch {
	case a.client != nil:
		dot = t.Success
		if text == "" && a.connIdx >= 0 && a.connIdx < len(a.profiles) {
			text = "Conectado a " + a.profiles[a.connIdx].Name
		}
	case a.connecting:
		dot = t.Warning
	}
	if text == "" {
		text = "Sin conexión"
	}
	ui.Row(c).Height(30).Padding(0, 10, 0, 14).Gap(8).Center().Children(func() {
		ui.Box(c).Size(7, 7).Radius(4).Background(dot)
		ui.Text(c, text).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(1)
		ui.Spacer(c)
		if a.client != nil && a.connIdx >= 0 && a.connIdx < len(a.profiles) {
			pr := a.profiles[a.connIdx]
			ui.Row(c).Padding(2, 8).Radius(6).Background(p.chip).Center().Children(func() {
				ui.Text(c, pr.User+"@"+pr.Host).FontSize(11).TextColor(t.TextMuted).SingleLine()
			})
		}
	})
}

// ---- modals ----

func (a *app) modals(c *ui.Context) {
	t := c.Theme()
	errLine := func(msg string) {
		if msg != "" {
			ui.Text(c, msg).TextColor(t.Danger).FontSize(12).Wrap()
		}
	}

	// Delete confirm (delIdx is the profile the menu acted on).
	if a.delIdx >= 0 && a.delIdx < len(a.profiles) {
		p := a.profiles[a.delIdx]
		switch ui.AlertDialog(c, &a.delOpen, "¿Eliminar «"+p.Name+"»?",
			"Se borrará la conexión «"+p.Name+"» ("+p.User+"@"+p.Host+"). Esta acción no se puede deshacer.",
			"Cancelar", "Eliminar") {
		case 1:
			a.deleteProfile(a.delIdx)
		}
	} else {
		a.delOpen = false
	}

	// Profile form.
	ui.Modal(c, &a.formOpen, func() {
		title := "Nueva conexión"
		if a.formIdx >= 0 && a.formIdx < len(a.profiles) {
			title = "Editar " + a.profiles[a.formIdx].Name
		}
		ui.Text(c, title).Bold().FontSize(15)
		ui.Form(c, func() {
			ui.Fieldset(c, "Servidor", func() {
				ui.Field(c, "Nombre", func() { ui.TextInput(c, &a.fName).AutoFocus().Placeholder("Producción") })
				ui.Field(c, "Host", func() { ui.TextInput(c, &a.fHost).Placeholder("servidor.ejemplo.com") })
				ui.Field(c, "Puerto", func() { ui.TextInput(c, &a.fPort) })
				ui.Field(c, "Usuario", func() { ui.TextInput(c, &a.fUser) })
			})
			ui.Fieldset(c, "Autenticación", func() {
				ui.Field(c, "Método", func() { ui.Select(c, &a.fAuth, authLabels()) })
				m := authMethodOf(a.fAuth)
				if usesPassword(m) {
					ui.Field(c, "Contraseña", func() { ui.TextInput(c, &a.fPass).Password() })
				}
				if usesKey(m) {
					ui.Field(c, "Llave privada", func() {
						ui.Row(c).Gap(6).Children(func() {
							ui.TextInput(c, &a.fKey).Placeholder("~/.ssh/id_ed25519").Grow(1)
							if ui.Button(c, "").Label("Elegir llave").Tooltip("Elegir llave…").
								Children(func() { ui.Icon(c, iconFolder) }).Clicked() {
								a.pickKey()
							}
						})
					})
					ui.Field(c, "Passphrase", func() { ui.TextInput(c, &a.fKeyPwd).Password() })
				}
			})
		})
		errLine(a.formErr)
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "Cancelar").Clicked() {
				a.formOpen = false
			}
			if ui.PrimaryButton(c, "Guardar").Clicked() {
				a.saveForm()
			}
		})
	}).Width(460)

	// Backup password.
	ui.Modal(c, &a.bkOpen, func() {
		if a.bkExport {
			ui.Text(c, "Exportar backup cifrado").Bold().FontSize(15)
		} else {
			ui.Text(c, "Importar backup").Bold().FontSize(15)
		}
		ui.Text(c, "Contraseña del backup (mín. 12 caracteres)").TextColor(t.TextMuted).FontSize(12)
		ui.Form(c, func() {
			ui.Field(c, "Contraseña", func() { ui.TextInput(c, &a.bkPass).Password().AutoFocus() })
			if a.bkExport {
				ui.Field(c, "Repetir", func() { ui.TextInput(c, &a.bkPass2).Password() })
			}
		})
		errLine(a.bkErr)
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "Cancelar").Clicked() {
				a.bkOpen = false
			}
			label := "Importar"
			if a.bkExport {
				label = "Exportar"
			}
			if ui.PrimaryButton(c, label).Clicked() {
				a.finishBackup()
			}
		})
	}).Width(420)

	// Server question (keyboard-interactive: OTP, second factor…).
	ui.Modal(c, &a.promptOpen, func() {
		title := a.promptTitle
		if title == "" {
			title = "El servidor solicita información"
		}
		ui.Text(c, title).Bold().FontSize(15).Wrap()
		ui.Field(c, a.promptQuestion, func() {
			in := ui.TextInput(c, &a.promptVal).AutoFocus()
			if !a.promptEcho {
				in.Password()
			}
		})
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "Cancelar").Clicked() {
				a.answerPrompt("", false)
			}
			if ui.PrimaryButton(c, "Aceptar").Clicked() {
				a.answerPrompt(a.promptVal, true)
				a.promptVal = ""
			}
		})
	}).Width(420)

	// Settings.
	ui.Modal(c, &a.settingsOpen, func() {
		ui.Text(c, "Ajustes de seguridad").Bold().FontSize(15)
		if a.authKind == "" {
			ui.Text(c, "Este Mac no tiene Touch ID ni contraseña de usuario configurada, "+
				"así que no se puede pedir autenticación.").TextColor(t.Warning).FontSize(12).Wrap()
		} else {
			ui.Text(c, "Se pedirá "+a.authKind+" (o la contraseña de macOS si falla). "+
				"La contraseña de cada servidor se escribe solo la primera vez, al crear la conexión.").
				TextColor(t.TextMuted).FontSize(12).Wrap()
		}
		setting := func(title, hint string, on bool, set func(*settings.Settings, bool)) {
			v := on
			ui.Row(c).Gap(12).Center().Children(func() {
				ui.Column(c).Gap(2).Grow(1).Children(func() {
					ui.Text(c, title)
					ui.Text(c, hint).FontSize(11).TextColor(t.TextMuted).Wrap()
				})
				if ui.Switch(c, &v).Disabled(a.authKind == "").Changed() {
					want := v
					a.changeSetting(func(s *settings.Settings) { set(s, want) })
				}
			})
		}
		ui.Divider(c)
		setting("Pedir "+a.authKindOr("autenticación")+" para conectar",
			"Antes de usar, editar o exportar credenciales guardadas.",
			a.cfg.RequireForCredentials, func(s *settings.Settings, v bool) { s.RequireForCredentials = v })
		setting("Bloquear la app al abrirla",
			"No descifra ni muestra tus conexiones hasta autenticarte. ⌘L la bloquea de nuevo.",
			a.cfg.RequireOnLaunch, func(s *settings.Settings, v bool) { s.RequireOnLaunch = v })
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.PrimaryButton(c, "Listo").Clicked() {
				a.settingsOpen = false
			}
		})
	}).Width(460)

	a.snippetsModal(c)

	// Remote path.
	ui.Modal(c, &a.pathOpen, func() {
		title, hint := "Editar archivo remoto", "Ruta del archivo en el servidor"
		if a.pathUpload {
			title, hint = "Subir archivo", "Ruta de destino (termina en / para usar el nombre original)"
		}
		ui.Text(c, title).Bold().FontSize(15)
		ui.Text(c, hint).TextColor(t.TextMuted).FontSize(12)
		ui.TextInput(c, &a.pathVal).AutoFocus()
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "Cancelar").Clicked() {
				a.pathOpen = false
			}
			if ui.PrimaryButton(c, "Aceptar").Clicked() {
				a.pathOpen = false
				if a.pathUpload {
					a.doUpload()
				} else {
					a.openRemoteEditor()
				}
			}
		})
	}).Width(460)

	// Remote editor.
	ui.Modal(c, &a.editorOpen, func() {
		ui.Text(c, a.editorPath).Bold().FontSize(15).SingleLine()
		ui.TextArea(c, &a.editorText).Width(600).Height(380)
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "Cancelar").Clicked() {
				a.editorOpen = false
			}
			if ui.PrimaryButton(c, "Guardar").Clicked() {
				a.editorOpen = false
				a.writeRemote()
			}
		})
	})
}

// profileMenu is the right-click menu of a sidebar item. The menu is a
// native NSMenu (Apple spacing/alignment); label glyphs render unevenly,
// so items stay clean text.
func (a *app) profileMenu(m *ui.Menu, idx int) {
	if m.Item("Conectar").Chosen() {
		a.connectTo(idx)
	}
	if m.Item("Editar").Chosen() {
		a.selected = strconv.Itoa(idx)
		a.openForm(idx)
	}
	if m.Item("Duplicar").Chosen() {
		a.duplicateProfile(idx)
	}
	m.Separator()
	if m.Item("Eliminar").Chosen() {
		a.selected = strconv.Itoa(idx)
		a.delIdx = idx
		a.delOpen = true
	}
}

func (a *app) duplicateProfile(idx int) {
	if idx < 0 || idx >= len(a.profiles) {
		return
	}
	p := a.profiles[idx]
	p.Name += "-copia"
	a.profiles = append(a.profiles, p)
	if err := a.st.SaveProfiles(a.profiles); err != nil {
		a.notify(noteError, "Error guardando: "+err.Error())
		return
	}
	a.selected = strconv.Itoa(len(a.profiles) - 1)
	a.refreshMenu()
}

func (a *app) authKindOr(fallback string) string {
	if a.authKind == "" {
		return fallback
	}
	return a.authKind
}

// ---- selection helpers ----

func (a *app) selIdx() int {
	i, err := strconv.Atoi(a.selected)
	if err != nil || i < 0 || i >= len(a.profiles) {
		return -1
	}
	return i
}

// ---- connect / disconnect ----

func (a *app) connectToggle() {
	if a.client != nil {
		a.disconnect()
		return
	}
	a.connectTo(a.selIdx())
}

// connectTo opens a session to profile idx, closing the live one first.
func (a *app) connectTo(idx int) {
	if a.locked || idx < 0 || idx >= len(a.profiles) || a.connecting || a.authBusy {
		return
	}
	a.guard("Conectar a "+a.profiles[idx].Name, func() { a.startConnect(idx) })
}

// startConnect opens the session once any authentication has succeeded.
func (a *app) startConnect(idx int) {
	if idx >= len(a.profiles) || a.connecting {
		return
	}
	if a.client != nil {
		a.disconnect()
	}
	a.selected = strconv.Itoa(idx)
	p := a.profiles[idx]
	a.connecting = true
	a.status = "Conectando a " + p.Name + "…"

	go func() {
		confirm := func(info sshx.HostKeyInfo) bool {
			return a.confirmDialog("Host desconocido",
				fmt.Sprintf("Host key %s de %s:%d", info.KeyType, info.Host, info.Port),
				fmt.Sprintf("Huella: %s\n\n¿Confiar y guardar en known_hosts?", info.Fingerprint),
				"Confiar")
		}
		client, err := sshx.Connect(p, 120, 32, confirm, a.askPrompt)
		mygo.RunOnMain(func() {
			a.connecting = false
			if err != nil {
				a.status = ""
				a.notify(noteError, err.Error())
				return
			}
			light, dark := termThemes()
			t, terr := terminal.New(terminal.Options{
				Conn:      sshConn{client},
				Theme:     light,
				DarkTheme: dark,
				OnTitle: func(title string) {
					mygo.RunOnMain(func() { a.win.SetTitle(windowTitle(p.Name, title)) })
				},
				OnExit: func(int) {
					mygo.RunOnMain(func() {
						a.disconnect()
						a.status = "Sesión terminada"
						a.win.Invalidate()
					})
				},
			})
			if terr != nil {
				client.Close()
				a.status = ""
				a.notify(noteError, "terminal: "+terr.Error())
				return
			}
			a.client = client
			a.term = t
			a.connIdx = idx
			a.connectedAt = time.Now()
			a.status = "Conectado a " + p.Name
			a.win.SetTitle(windowTitle(p.Name, ""))
			a.win.Invalidate()
		})
	}()
}

// windowTitle composes the title from the profile and the title the remote
// program set. The remote string is untrusted: control characters are
// stripped and its length capped, and the profile name always leads so a
// server cannot impersonate another host in the title bar.
func windowTitle(profile, remote string) string {
	remote = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, remote)
	if r := []rune(remote); len(r) > 80 {
		remote = string(r[:80]) + "…"
	}
	if strings.TrimSpace(remote) == "" {
		return profile + " — " + appTitle
	}
	return profile + " — " + remote
}

func (a *app) disconnect() {
	if a.term != nil {
		_ = a.term.Close() // closes conn → client.Close()
		a.term = nil
	}
	a.client = nil
	a.connIdx = -1
	a.status = ""
	if a.win != nil {
		a.win.SetTitle(appTitle)
		a.win.Invalidate()
	}
}

// ---- SFTP ----

// Native file dialogs block until dismissed and need the main thread to
// show them, so they must never run on it (view/click handlers do): they
// run on a goroutine and apply their result back with RunOnMain.
// dialogBusy keeps a second click from stacking another dialog.

func (a *app) uploadFile() {
	if a.client == nil || a.dialogBusy {
		return
	}
	a.dialogBusy = true
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: "Archivo a subir"})
		mygo.RunOnMain(func() {
			a.dialogBusy = false
			if err != nil || len(paths) == 0 || a.client == nil {
				return
			}
			a.uploadLocal = paths[0]
			a.pathVal = a.client.Home() + "/" + filepath.Base(a.uploadLocal)
			a.pathUpload = true
			a.pathOpen = true
			a.win.Invalidate()
		})
	}()
}

func (a *app) doUpload() {
	local, remote := a.uploadLocal, strings.TrimSpace(a.pathVal)
	if strings.HasSuffix(remote, "/") {
		remote += filepath.Base(local)
	}
	client := a.client
	if client == nil {
		return
	}
	a.status = "Subiendo " + filepath.Base(local) + "…"
	go func() {
		if client.Exists(remote) && !a.confirmDialog("¿Reemplazar archivo remoto?",
			remote+" ya existe.", "Se sobrescribirá con «"+filepath.Base(local)+"».", "Reemplazar") {
			mygo.RunOnMain(func() { a.status = ""; a.win.Invalidate() })
			return
		}
		err := client.UploadFile(local, remote)
		mygo.RunOnMain(func() {
			a.status = ""
			if err != nil {
				a.notify(noteError, err.Error())
			} else {
				a.notify(noteSuccess, "Subido a "+remote)
			}
		})
	}()
}

func (a *app) openRemoteEditor() {
	path := strings.TrimSpace(a.pathVal)
	client := a.client
	if client == nil {
		return
	}
	go func() {
		content, err := client.ReadRemoteFile(path)
		mygo.RunOnMain(func() {
			if err != nil {
				a.notify(noteError, err.Error())
				return
			}
			a.editorPath = path
			a.editorText = content
			a.editorOpen = true
			a.win.Invalidate()
		})
	}()
}

func (a *app) writeRemote() {
	path, content := a.editorPath, a.editorText
	client := a.client
	if client == nil {
		a.notify(noteError, "Sin conexión: no se pudo guardar "+path)
		return
	}
	go func() {
		err := client.WriteRemoteFile(path, content)
		mygo.RunOnMain(func() {
			if err != nil {
				a.notify(noteError, err.Error())
			} else {
				a.notify(noteSuccess, "Guardado "+path)
			}
		})
	}()
}

// ---- profile CRUD ----

func (a *app) openForm(idx int) {
	if idx >= 0 && idx < len(a.profiles) && a.cfg.RequireForCredentials && !a.authBusy {
		a.guard("Editar "+a.profiles[idx].Name, func() { a.showForm(idx) })
		return
	}
	a.showForm(idx)
}

func (a *app) showForm(idx int) {
	a.formIdx = idx
	a.formErr = ""
	if idx >= 0 {
		p := a.profiles[idx]
		a.fName, a.fHost, a.fUser = p.Name, p.Host, p.User
		a.fPort = strconv.Itoa(int(p.Port))
		a.fAuth = authLabel(p.AuthMethod)
		a.fPass, a.fKey, a.fKeyPwd = p.Password, p.PrivateKeyPath, p.PrivateKeyPass
	} else {
		a.fName, a.fHost, a.fUser = "", "", ""
		a.fPort = "22"
		a.fAuth = authLabel(store.AuthPassword)
		a.fPass, a.fKey, a.fKeyPwd = "", "", ""
	}
	a.formOpen = true
}

func (a *app) pickKey() {
	if a.dialogBusy {
		return
	}
	a.dialogBusy = true
	home, _ := os.UserHomeDir()
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent: a.win, Title: "Llave privada",
			DefaultPath:     filepath.Join(home, ".ssh"),
			ShowHiddenFiles: true,
		})
		mygo.RunOnMain(func() {
			a.dialogBusy = false
			if err == nil && len(paths) > 0 {
				a.fKey = paths[0]
			}
			a.win.Invalidate()
		})
	}()
}

// validateForm checks the form and returns the profile, or a message for
// the user. Nothing is silently defaulted: a bad port is an error, not 22.
func (a *app) validateForm() (store.ConnectionProfile, string) {
	port, err := strconv.Atoi(strings.TrimSpace(a.fPort))
	if err != nil || port <= 0 || port > 65535 {
		return store.ConnectionProfile{}, "El puerto debe ser un número entre 1 y 65535."
	}
	m := authMethodOf(a.fAuth)
	np := store.ConnectionProfile{
		Name: strings.TrimSpace(a.fName), Host: strings.TrimSpace(a.fHost),
		Port: uint16(port), User: strings.TrimSpace(a.fUser),
		AuthMethod: m,
	}
	if usesPassword(m) {
		np.Password = a.fPass
	}
	if usesKey(m) {
		np.PrivateKeyPath = strings.TrimSpace(a.fKey)
		np.PrivateKeyPass = a.fKeyPwd
	}
	switch {
	case np.Name == "" || np.Host == "":
		return np, "Nombre y host son obligatorios."
	case np.User == "":
		return np, "El usuario es obligatorio."
	case m == store.AuthPassword && np.Password == "",
		m == store.AuthInteractive && np.Password == "":
		return np, "Indica la contraseña de la cuenta."
	case m == store.AuthKey && np.PrivateKeyPath == "":
		return np, "Indica la ruta de la llave privada."
	case m == store.AuthBoth && np.PrivateKeyPath == "" && np.Password == "":
		return np, "Indica una llave privada, una contraseña o ambas."
	}
	return np, ""
}

func (a *app) saveForm() {
	np, msg := a.validateForm()
	if msg != "" {
		a.formErr = msg
		return
	}
	if a.formIdx >= 0 {
		a.profiles[a.formIdx] = np
	} else {
		a.profiles = append(a.profiles, np)
	}
	if err := a.st.SaveProfiles(a.profiles); err != nil {
		a.formErr = "Error guardando: " + err.Error()
		return
	}
	a.formOpen = false
	a.refreshMenu()
}

func (a *app) deleteProfile(idx int) {
	if idx < 0 || idx >= len(a.profiles) {
		return
	}
	if idx == a.connIdx {
		a.disconnect()
	} else if idx < a.connIdx {
		a.connIdx--
	}
	a.profiles = append(a.profiles[:idx], a.profiles[idx+1:]...)
	a.selected = ""
	a.delIdx = -1
	if err := a.st.SaveProfiles(a.profiles); err != nil {
		a.notify(noteError, "Error guardando: "+err.Error())
	}
	a.refreshMenu()
}

// ---- backup ----

func (a *app) exportBackup() {
	if len(a.profiles) == 0 {
		a.notify(noteInfo, "No hay conexiones para exportar")
		return
	}
	a.guard("Exportar las conexiones guardadas", func() {
		a.bkExport = true
		a.bkPass, a.bkPass2, a.bkErr = "", "", ""
		a.bkOpen = true
	})
}

func (a *app) importBackup() {
	if a.dialogBusy {
		return
	}
	a.dialogBusy = true
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent: a.win, Title: "Backup gossh",
			Filters: []mygo.FileFilter{{Name: "Backup", Extensions: []string{"json"}}},
		})
		var data []byte
		if err == nil && len(paths) > 0 {
			data, err = readBackupFile(paths[0])
		}
		mygo.RunOnMain(func() {
			a.dialogBusy = false
			switch {
			case err != nil:
				a.notify(noteError, err.Error())
			case data != nil:
				a.bkData = data
				a.bkExport = false
				a.bkPass, a.bkErr = "", ""
				a.bkOpen = true
				a.win.Invalidate()
			}
		})
	}()
}

// readBackupFile reads a backup, refusing files too large to be one.
func readBackupFile(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("Error leyendo: %w", err)
	}
	if st.Size() > store.MaxBackupFileSize {
		return nil, fmt.Errorf("El archivo es demasiado grande para ser un backup de gossh")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("Error leyendo: %w", err)
	}
	return data, nil
}

func (a *app) finishBackup() {
	if a.bkExport {
		if a.bkPass != a.bkPass2 {
			a.bkErr = "Las contraseñas no coinciden."
			return
		}
		if a.dialogBusy {
			return
		}
		a.dialogBusy = true
		bk, pass := store.Backup{
			Profiles: append([]store.ConnectionProfile(nil), a.profiles...),
			Snippets: append([]store.Snippet(nil), a.snippets...),
		}, a.bkPass
		go func() { // Argon2 and the save dialog both block: off the UI thread
			jsonStr, err := store.ExportBackup(bk, pass)
			var path string
			if err == nil {
				path, err = mygo.Dialog.Save(mygo.SaveDialogOptions{
					Parent: a.win, Title: "Exportar backup", DefaultPath: "gossh-backup.json",
				})
			}
			if err == nil && path != "" {
				err = store.WritePrivate(path, []byte(jsonStr))
			}
			mygo.RunOnMain(func() {
				a.dialogBusy = false
				switch {
				case err != nil:
					a.bkErr = err.Error()
				case path != "": // "" = user cancelled the save dialog: keep the modal
					a.bkOpen = false
					a.notify(noteSuccess, "Backup exportado a "+path)
				}
				a.win.Invalidate()
			})
		}()
		return
	}

	imported, err := store.ImportBackup(a.bkData, a.bkPass)
	if err != nil {
		a.bkErr = err.Error()
		return
	}
	added, skipped := store.MergeImported(a.profiles, imported.Profiles)
	profiles := store.AppendNew(a.profiles, imported.Profiles)
	snippets := store.AppendNewSnippets(a.snippets, imported.Snippets)
	snAdded := len(snippets) - len(a.snippets)
	if err := a.st.SaveProfiles(profiles); err != nil {
		a.bkErr = "Error guardando: " + err.Error()
		return
	}
	a.profiles = profiles
	if snAdded > 0 {
		if err := a.st.SaveSnippets(snippets); err != nil {
			a.bkErr = "Error guardando snippets: " + err.Error()
			return
		}
		a.snippets = snippets
	}
	a.bkOpen = false
	a.refreshMenu()
	msg := fmt.Sprintf("%d importadas, %d duplicadas omitidas", added, skipped)
	if len(imported.Snippets) > 0 {
		msg += fmt.Sprintf(" · %d snippets nuevos", snAdded)
	}
	a.notify(noteSuccess, msg)
}

// ---- snippets ----

func (a *app) openSnippets() {
	if a.locked {
		return
	}
	a.snEdit, a.snDelete, a.snErr, a.snFilter = -2, -1, "", ""
	a.snOpen = true
	a.win.Invalidate()
}

func (a *app) editSnippet(idx int) {
	a.snEdit, a.snErr = idx, ""
	if idx >= 0 && idx < len(a.snippets) {
		sn := a.snippets[idx]
		a.snName, a.snText, a.snEnter = sn.Name, sn.Text, sn.Enter
	} else {
		a.snName, a.snText, a.snEnter = "", "", false
	}
}

func (a *app) saveSnippet() {
	name := strings.TrimSpace(a.snName)
	if name == "" {
		a.snErr = "Ponle un nombre al snippet."
		return
	}
	if strings.TrimSpace(a.snText) == "" {
		a.snErr = "El snippet está vacío."
		return
	}
	sn := store.Snippet{Name: name, Text: a.snText, Enter: a.snEnter}
	next := append([]store.Snippet(nil), a.snippets...)
	if a.snEdit >= 0 && a.snEdit < len(next) {
		next[a.snEdit] = sn
	} else {
		next = append(next, sn)
	}
	if err := a.st.SaveSnippets(next); err != nil {
		a.snErr = "Error guardando: " + err.Error()
		return
	}
	a.snippets = next
	a.snEdit = -2
}

func (a *app) deleteSnippet(idx int) {
	if idx < 0 || idx >= len(a.snippets) {
		return
	}
	next := append(append([]store.Snippet(nil), a.snippets[:idx]...), a.snippets[idx+1:]...)
	if err := a.st.SaveSnippets(next); err != nil {
		a.notify(noteError, "Error guardando: "+err.Error())
		return
	}
	a.snippets = next
}

// insertSnippet pastes the snippet into the live session. Paste is
// bracketed when the remote program asks for it, so a multi-line snippet
// does not run line by line; Enter is sent only if the snippet asks.
func (a *app) insertSnippet(sn store.Snippet) {
	if a.term == nil || a.client == nil {
		return
	}
	a.term.Paste(sn.Text)
	if sn.Enter {
		_, _ = a.client.Stdin().Write([]byte("\r"))
	}
	a.snOpen = false
}

func (a *app) snippetsModal(c *ui.Context) {
	t := c.Theme()
	mono := "JetBrains Mono, SF Mono, Menlo, monospace"
	ui.Modal(c, &a.snOpen, func() {
		if a.snEdit != -2 {
			title := "Nuevo snippet"
			if a.snEdit >= 0 {
				title = "Editar snippet"
			}
			ui.Text(c, title).Bold().FontSize(15)
			ui.Field(c, "Nombre", func() { ui.TextInput(c, &a.snName).AutoFocus().Placeholder("Reiniciar nginx") })
			ui.Field(c, "Texto", func() {
				ui.TextArea(c, &a.snText).FillWidth().Height(150).Font(mono).Placeholder("sudo systemctl restart nginx")
			})
			ui.Row(c).Gap(12).Center().Children(func() {
				ui.Column(c).Gap(2).Grow(1).Children(func() {
					ui.Text(c, "Ejecutar al insertar")
					ui.Text(c, "Pulsa Enter después de pegarlo. Desactivado, solo se escribe.").FontSize(11).TextColor(t.TextMuted).Wrap()
				})
				ui.Switch(c, &a.snEnter)
			})
			if a.snErr != "" {
				ui.Text(c, a.snErr).TextColor(t.Danger).FontSize(12).Wrap()
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Cancelar").Clicked() {
					a.snEdit = -2
				}
				if ui.PrimaryButton(c, "Guardar").Clicked() {
					a.saveSnippet()
				}
			})
			return
		}

		ui.Row(c).Gap(8).Center().Children(func() {
			ui.Text(c, "Snippets").Bold().FontSize(15).Grow(1)
			if ui.Button(c, "Nuevo").Children(func() {
				ui.Icon(c, iconPlus)
				ui.Text(c, "Nuevo").SingleLine()
			}).Clicked() {
				a.editSnippet(-1)
			}
		})
		live := a.term != nil
		hint := "Conéctate a un servidor para insertar un snippet en la terminal. También puedes copiarlos."
		if live {
			hint = "Insertar pega el texto en la terminal activa."
		}
		ui.Text(c, hint).FontSize(12).TextColor(t.TextMuted).Wrap()
		if len(a.snippets) > 0 {
			ui.TextInput(c, &a.snFilter).Placeholder("Filtrar snippets…")
		}
		q := strings.ToLower(strings.TrimSpace(a.snFilter))
		ui.Scroll(c).FillWidth().Height(320).Children(func() {
			ui.Column(c).FillWidth().Gap(6).Children(func() {
				shown := 0
				for i, sn := range a.snippets {
					idx, sn := i, sn
					if q != "" && !strings.Contains(strings.ToLower(sn.Name+" "+sn.Text), q) {
						continue
					}
					shown++
					ui.Row(c).FillWidth().Gap(8).Padding(8, 10).Radius(10).Center().
						Background(paletteOf(t).card).Border(1, paletteOf(t).cardBorder).Children(func() {
						ui.Column(c).Gap(2).Grow(1).Shrink(1).Children(func() {
							ui.Text(c, sn.Name).FontWeight(600).SingleLine()
							first, _, _ := strings.Cut(sn.Text, "\n")
							ui.Text(c, first).Font(mono).FontSize(11).TextColor(t.TextMuted).SingleLine()
						})
						if ui.Button(c, "").Label("Insertar").Disabled(!live).Children(func() {
							ui.Text(c, "Insertar").SingleLine()
						}).Clicked() {
							a.insertSnippet(sn)
						}
						if iconBtn(c, iconCopy, "Copiar").Clicked() {
							if err := mygo.Clipboard.Write(transfer.TextData(sn.Text)); err != nil {
								a.notify(noteError, "No se pudo copiar: "+err.Error())
							} else {
								a.notify(noteSuccess, "Copiado «"+sn.Name+"»")
							}
						}
						if iconBtn(c, iconPencil, "Editar").Clicked() {
							a.editSnippet(idx)
						}
						if iconBtn(c, iconTrash, "Eliminar").Clicked() {
							a.snDelete = idx
						}
					})
				}
				if len(a.snippets) == 0 {
					ui.Text(c, "Aún no tienes snippets. Guarda aquí los comandos que usas siempre.").
						TextColor(t.TextMuted).Wrap()
				} else if shown == 0 {
					ui.Text(c, "Ningún snippet coincide").FontSize(12).TextColor(t.TextMuted)
				}
			})
		})
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.PrimaryButton(c, "Listo").Clicked() {
				a.snOpen = false
			}
		})
	}).Width(520)

	if a.snDelete >= 0 && a.snDelete < len(a.snippets) {
		open := true
		switch ui.AlertDialog(c, &open, "¿Eliminar «"+a.snippets[a.snDelete].Name+"»?",
			"Esta acción no se puede deshacer.", "Cancelar", "Eliminar") {
		case 1:
			a.deleteSnippet(a.snDelete)
			a.snDelete = -1
		default:
			if !open {
				a.snDelete = -1
			}
		}
	}
}
