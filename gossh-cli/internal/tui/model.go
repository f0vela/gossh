// Package tui is the Bubble Tea interface of gossh: a profile sidebar and an
// SSH terminal card, feature-for-feature with the gossh desktop app.
package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"gossh-cli/internal/biometric"
	"gossh-cli/internal/settings"
	"gossh-cli/internal/sshx"
	"gossh-cli/internal/store"
	"gossh-cli/internal/theme"
)

const appName = "gossh"

type focusArea int

const (
	focusSidebar focusArea = iota
	focusTerm
)

type noteKind int

const (
	noteInfo noteKind = iota
	noteSuccess
	noteError
)

type toast struct {
	kind noteKind
	text string
	id   int
}

// Model is the root Bubble Tea model.
type Model struct {
	th theme.Theme
	st styles

	width, height int

	store    *store.SecureStore
	profiles []store.ConnectionProfile
	snippets []store.Snippet

	cfgStore  *settings.Store
	cfg       settings.Settings
	cfgBroken bool   // settings file unreadable: never overwrite it
	authKind  string // "Touch ID", "contraseña de macOS" or "" when unavailable
	locked    bool
	authBusy  bool

	cursor        int // index into profiles
	top           int // first visible sidebar row
	filter        textinput.Model
	filtering     bool
	sidebarHidden bool
	focus         focusArea
	leader        bool

	connecting  bool
	connIdx     int
	connectedAt time.Time
	sess        *session
	status      string

	toast  *toast
	nextID int

	modal modal
	// auth continuations waiting for the system prompt, by id
	authWait map[int]func() tea.Cmd

	send func(tea.Msg)
	quit bool
}

// Options configure New.
type Options struct {
	// Theme overrides the saved theme for this run when non-empty.
	Theme string
}

// New builds the model: it reads the settings and, unless the app is locked,
// the encrypted store.
func New(opt Options) (*Model, error) {
	st, err := store.New()
	if err != nil {
		return nil, err
	}
	m := &Model{store: st, connIdx: -1, authWait: map[int]func() tea.Cmd{}}

	switch {
	case biometric.Biometrics():
		m.authKind = "Touch ID"
	case biometric.Available():
		m.authKind = "contraseña de macOS"
	}
	var startNotes []string
	if cs, err := settings.New(); err != nil {
		m.cfgBroken = true
		startNotes = append(startNotes, "ajustes: "+err.Error())
	} else {
		m.cfgStore = cs
		cfg, err := cs.Load()
		if err != nil {
			// Fail closed: a corrupt file must not switch the protections off.
			m.cfgBroken = true
			cfg.RequireOnLaunch = true
			startNotes = append(startNotes, "No se pudieron leer los ajustes (se bloquea por seguridad): "+err.Error())
		}
		m.cfg = cfg
	}
	m.locked = m.cfg.RequireOnLaunch && m.authKind != ""

	name := m.cfg.Theme
	if opt.Theme != "" {
		name = opt.Theme
	}
	m.setTheme(theme.Or(name))

	m.filter = m.st.input("buscar…", 20)
	if !m.locked {
		startNotes = append(startNotes, m.loadProfiles()...)
	}
	if len(startNotes) > 0 {
		m.toast = &toast{kind: noteError, text: strings.Join(startNotes, " · "), id: m.newID()}
	}
	return m, nil
}

// SetSender wires the program so goroutines can post messages.
func (m *Model) SetSender(f func(tea.Msg)) { m.send = f }

func (m *Model) newID() int { m.nextID++; return m.nextID }

func (m *Model) setTheme(t theme.Theme) {
	m.th = t
	m.st = newStyles(t)
	m.filter.SetStyles(m.st.inputStyles())
}

// loadProfiles decrypts the store; it returns messages to show.
func (m *Model) loadProfiles() (notes []string) {
	loaded, err := m.store.LoadProfiles()
	if err != nil {
		return []string{err.Error()}
	}
	m.profiles = loaded.Profiles
	if loaded.Notice != "" {
		notes = append(notes, loaded.Notice)
	}
	if sn, err := m.store.LoadSnippets(); err != nil {
		notes = append(notes, err.Error())
	} else {
		m.snippets = sn
	}
	return notes
}

// ---- messages ----

type (
	connectedMsg struct {
		idx    int
		client *sshx.Client
		err    error
	}
	hostKeyMsg struct {
		info  sshx.HostKeyInfo
		reply chan bool
	}
	promptMsg struct {
		instruction, question string
		echo                  bool
		reply                 chan promptReply
	}
	askMsg struct {
		c     *confirmModal
		reply chan bool
	}
	authDoneMsg struct {
		id  int
		err error
	}
	toastExpireMsg struct{ id int }
	tickMsg        time.Time
	uploadDoneMsg  struct {
		remote string
		err    error
		skip   bool
	}
	remoteReadMsg struct {
		path, content string
		err           error
	}
	remoteWrittenMsg struct {
		path string
		err  error
	}
	exportDoneMsg struct {
		path string
		err  error
	}
	importDoneMsg struct {
		b   store.Backup
		err error
	}
)

type promptReply struct {
	answer string
	ok     bool
}

// Init starts the clock and, when locked, the unlock prompt.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tea.Tick(30*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })}
	if m.locked {
		cmds = append(cmds, m.unlockCmd())
	}
	return tea.Batch(cmds...)
}

// Update routes every message.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.fitSession()
		return m, nil
	case tickMsg:
		return m, tea.Tick(30*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
	case toastExpireMsg:
		if m.toast != nil && m.toast.id == msg.id {
			m.toast = nil
		}
		return m, nil
	case outputMsg:
		if msg.s == m.sess {
			msg.s.pending.Store(false)
		}
		return m, nil
	case sessionEndMsg:
		if msg.s == m.sess {
			m.endSession("Sesión terminada")
		} else {
			msg.s.close()
		}
		return m, nil
	case connectedMsg:
		return m, m.onConnected(msg)
	case hostKeyMsg:
		return m, m.openModal(newConfirm("Host desconocido",
			fmt.Sprintf("Host key %s de %s:%d", msg.info.KeyType, msg.info.Host, msg.info.Port),
			"Huella: "+msg.info.Fingerprint+"\n\n¿Confiar y guardar en known_hosts?",
			"Confiar", func(m *Model) tea.Cmd { msg.reply <- true; return nil }, func(m *Model) tea.Cmd { msg.reply <- false; return nil }))
	case promptMsg:
		return m, m.openModal(newPromptModal(m, msg))
	case askMsg:
		c := msg.c
		c.onYes = func(*Model) tea.Cmd { msg.reply <- true; return nil }
		c.onNo = func(*Model) tea.Cmd { msg.reply <- false; return nil }
		return m, m.openModal(c)
	case authDoneMsg:
		m.authBusy = false
		fn := m.authWait[msg.id]
		delete(m.authWait, msg.id)
		switch {
		case msg.err == nil && fn != nil:
			return m, fn()
		case msg.err != nil && !isCancel(msg.err):
			return m, m.notify(noteError, msg.err.Error())
		}
		return m, nil
	case uploadDoneMsg:
		m.status = ""
		switch {
		case msg.skip:
		case msg.err != nil:
			return m, m.notify(noteError, msg.err.Error())
		default:
			return m, m.notify(noteSuccess, "Subido a "+msg.remote)
		}
		return m, nil
	case remoteReadMsg:
		m.status = ""
		if msg.err != nil {
			return m, m.notify(noteError, msg.err.Error())
		}
		return m, m.openModal(newEditorModal(m, msg.path, msg.content))
	case remoteWrittenMsg:
		m.status = ""
		if msg.err != nil {
			return m, m.notify(noteError, msg.err.Error())
		}
		return m, m.notify(noteSuccess, "Guardado "+msg.path)
	case exportDoneMsg, importDoneMsg:
		if bm, ok := m.modal.(*backupModal); ok {
			return m, bm.update(m, msg)
		}
		return m, nil
	}

	if m.locked {
		if k, ok := msg.(tea.KeyPressMsg); ok {
			switch k.String() {
			case "q", "ctrl+c":
				return m, m.shutdown()
			case "enter", "space":
				return m, m.unlockCmd()
			}
		}
		return m, nil
	}

	if m.modal != nil {
		return m, m.modal.update(m, msg)
	}

	switch msg := msg.(type) {
	case tea.PasteMsg:
		if m.focus == focusTerm && m.sess != nil {
			m.sess.paste(msg.Content)
		} else if m.filtering {
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(msg)
			return m, cmd
		}
		return m, nil
	case tea.KeyPressMsg:
		if m.focus == focusTerm && m.sess != nil {
			return m, m.terminalKey(msg)
		}
		return m, m.sidebarKey(msg)
	}
	return m, nil
}

func (m *Model) shutdown() tea.Cmd {
	if m.sess != nil {
		m.sess.close()
		m.sess = nil
	}
	m.quit = true
	return tea.Quit
}

// ---- notifications ----

// notify shows a toast, or a message dialog for long/multi-line errors.
func (m *Model) notify(kind noteKind, text string) tea.Cmd {
	if kind == noteError && m.modal == nil &&
		(strings.Contains(text, "\n") || len([]rune(text)) > max(20, m.width-30)) {
		return m.openModal(newMessageModal("Error", text))
	}
	id := m.newID()
	m.toast = &toast{kind: kind, text: text, id: id}
	d := 4 * time.Second
	if kind == noteError {
		d = 10 * time.Second
	}
	return tea.Tick(d, func(time.Time) tea.Msg { return toastExpireMsg{id} })
}

// ---- authentication (Touch ID / macOS password) ----

func isCancel(err error) bool { return err == biometric.ErrCancelled }

// authenticate asks the system to verify the user, then runs ok.
func (m *Model) authenticate(reason string, ok func() tea.Cmd) tea.Cmd {
	if m.authBusy {
		return nil
	}
	m.authBusy = true
	id := m.newID()
	m.authWait[id] = ok
	return func() tea.Msg { return authDoneMsg{id, biometric.Authenticate(reason)} }
}

// guard runs fn at once, or after authentication when the user asked for
// saved credentials to be protected.
func (m *Model) guard(reason string, fn func() tea.Cmd) tea.Cmd {
	if !m.cfg.RequireForCredentials || m.authKind == "" {
		return fn()
	}
	return m.authenticate(reason, fn)
}

func (m *Model) unlockCmd() tea.Cmd {
	return m.authenticate("Desbloquear "+appName, func() tea.Cmd {
		m.locked = false
		if m.profiles == nil {
			if notes := m.loadProfiles(); len(notes) > 0 {
				return m.notify(noteError, strings.Join(notes, " · "))
			}
		}
		return nil
	})
}

func (m *Model) lock() tea.Cmd {
	if !(m.cfg.RequireOnLaunch && m.authKind != "") {
		return m.notify(noteInfo, "Activa «Bloquear al abrir» en Ajustes (,) para poder bloquear")
	}
	m.locked = true
	m.modal = nil
	m.leader = false
	return nil
}

// changeSetting applies a security settings change after authenticating, so
// a passer-by at an unlocked Mac cannot switch the protection off.
func (m *Model) changeSetting(apply func(*settings.Settings)) tea.Cmd {
	return m.authenticate("Cambiar los ajustes de seguridad de "+appName, func() tea.Cmd {
		next := m.cfg
		apply(&next)
		if err := m.saveSettings(next); err != nil {
			return m.notify(noteError, "Error guardando ajustes: "+err.Error())
		}
		m.cfg = next
		return nil
	})
}

func (m *Model) saveSettings(s settings.Settings) error {
	if m.cfgStore == nil || m.cfgBroken {
		return fmt.Errorf("el archivo de ajustes no se pudo leer; no se sobrescribe")
	}
	return m.cfgStore.Save(s)
}

// persistTheme stores the chosen theme (not a security setting: no auth).
func (m *Model) persistTheme(name string) tea.Cmd {
	next := m.cfg
	next.Theme = name
	if err := m.saveSettings(next); err != nil {
		return m.notify(noteError, "Tema aplicado, pero no se guardó: "+err.Error())
	}
	m.cfg = next
	return m.notify(noteSuccess, "Tema: "+m.th.Label)
}

// ---- profiles ----

func (m *Model) filtered() []int {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	out := make([]int, 0, len(m.profiles))
	for i, p := range m.profiles {
		if q == "" || strings.Contains(strings.ToLower(p.Name+" "+p.Host+" "+p.User), q) {
			out = append(out, i)
		}
	}
	return out
}

func (m *Model) current() (store.ConnectionProfile, bool) {
	if m.cursor < 0 || m.cursor >= len(m.profiles) {
		return store.ConnectionProfile{}, false
	}
	return m.profiles[m.cursor], true
}

// moveCursor moves the selection by delta within the filtered list.
func (m *Model) moveCursor(delta int) {
	f := m.filtered()
	if len(f) == 0 {
		return
	}
	pos := 0
	for i, idx := range f {
		if idx == m.cursor {
			pos = i
		}
	}
	pos = min(max(pos+delta, 0), len(f)-1)
	m.cursor = f[pos]
}

func (m *Model) saveProfiles(next []store.ConnectionProfile) error {
	if err := m.store.SaveProfiles(next); err != nil {
		return err
	}
	m.profiles = next
	return nil
}

func (m *Model) duplicate(idx int) tea.Cmd {
	if idx < 0 || idx >= len(m.profiles) {
		return nil
	}
	p := m.profiles[idx]
	p.Name += "-copia"
	next := append(append([]store.ConnectionProfile(nil), m.profiles...), p)
	if err := m.saveProfiles(next); err != nil {
		return m.notify(noteError, "Error guardando: "+err.Error())
	}
	m.cursor = len(m.profiles) - 1
	return nil
}

func (m *Model) deleteProfile(idx int) tea.Cmd {
	if idx < 0 || idx >= len(m.profiles) {
		return nil
	}
	if idx == m.connIdx {
		m.endSession("")
	} else if idx < m.connIdx {
		m.connIdx--
	}
	next := append(append([]store.ConnectionProfile(nil), m.profiles[:idx]...), m.profiles[idx+1:]...)
	var cmd tea.Cmd
	if err := m.saveProfiles(next); err != nil {
		cmd = m.notify(noteError, "Error guardando: "+err.Error())
	}
	m.cursor = min(m.cursor, len(m.profiles)-1)
	m.cursor = max(m.cursor, 0)
	return cmd
}

// ---- connection ----

// termSize is the size of the terminal area inside the card.
func (m *Model) termSize() (cols, rows int) {
	return max(m.mainWidth()-4, 10), max(m.height-1-2-2, 3)
}

func (m *Model) sidebarWidth() int {
	if m.sidebarHidden || m.width < 70 {
		return 0
	}
	return min(32, max(24, m.width/4))
}

func (m *Model) mainWidth() int { return m.width - m.sidebarWidth() }

func (m *Model) fitSession() {
	if m.sess != nil {
		c, r := m.termSize()
		m.sess.resize(c, r)
	}
}

func (m *Model) connectTo(idx int) tea.Cmd {
	if idx < 0 || idx >= len(m.profiles) || m.connecting {
		return nil
	}
	if m.sess != nil && m.connIdx == idx {
		m.focus = focusTerm
		return nil
	}
	return m.guard("Conectar a "+m.profiles[idx].Name, func() tea.Cmd { return m.startConnect(idx) })
}

func (m *Model) startConnect(idx int) tea.Cmd {
	if m.sess != nil {
		m.endSession("")
	}
	m.cursor = idx
	p := m.profiles[idx]
	m.connecting = true
	m.status = "Conectando a " + p.Name + "…"
	cols, rows := m.termSize()
	send := m.send
	return func() tea.Msg {
		confirm := func(info sshx.HostKeyInfo) bool {
			reply := make(chan bool, 1)
			send(hostKeyMsg{info, reply})
			return <-reply
		}
		prompt := func(instruction, question string, echo bool) (string, bool) {
			reply := make(chan promptReply, 1)
			send(promptMsg{instruction, question, echo, reply})
			r := <-reply
			return r.answer, r.ok
		}
		c, err := sshx.Connect(p, cols, rows, confirm, prompt)
		return connectedMsg{idx, c, err}
	}
}

func (m *Model) onConnected(msg connectedMsg) tea.Cmd {
	m.connecting = false
	if msg.err != nil {
		m.status = ""
		return m.notify(noteError, msg.err.Error())
	}
	cols, rows := m.termSize()
	m.sess = newSession(msg.client, cols, rows, m.send)
	m.connIdx = msg.idx
	m.connectedAt = time.Now()
	m.focus = focusTerm
	m.status = ""
	return nil
}

// endSession closes the live session; a non-empty reason is shown in the
// status bar.
func (m *Model) endSession(reason string) {
	if m.sess != nil {
		m.sess.close()
		m.sess = nil
	}
	m.connIdx = -1
	m.focus = focusSidebar
	m.leader = false
	m.status = reason
}

// ---- helpers ----

func expandHome(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, rest)
	}
	return p
}

func itoa(n int) string { return strconv.Itoa(n) }

// elapsed formats a duration like the desktop app does.
func elapsed(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
