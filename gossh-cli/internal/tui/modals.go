package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"gossh-cli/internal/store"
)

// modal is a dialog drawn over the UI. While one is open it gets every key.
type modal interface {
	init(m *Model) tea.Cmd
	update(m *Model, msg tea.Msg) tea.Cmd
	view(m *Model, width int) string
}

func (m *Model) openModal(md modal) tea.Cmd {
	m.modal = md
	m.leader = false
	return md.init(m)
}

func (m *Model) closeModal() { m.modal = nil }

func (m *Model) modalWidth(pref int) int { return max(min(pref, m.width-4), 30) }

// frame draws the dialog box around inner.
func (m *Model) frame(title, inner string, width int) string {
	t := m.st.accent.Background(lipgloss.Color(m.th.BgAlt)).Bold(true).Render(title)
	body := lipgloss.NewStyle().Background(lipgloss.Color(m.th.BgAlt)).Render(inner)
	return m.st.modal.Width(width).Render(t + "\n\n" + body)
}

func (m *Model) bgAlt() lipgloss.Style {
	return lipgloss.NewStyle().Background(lipgloss.Color(m.th.BgAlt)).Foreground(lipgloss.Color(m.th.Fg))
}

func (m *Model) dim() lipgloss.Style { return m.bgAlt().Foreground(lipgloss.Color(m.th.Muted)) }

// keyHints renders "enter guardar · esc cancelar" style help.
func (m *Model) keyHints(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, m.st.key.Render(pairs[i])+m.dim().Render(" "+pairs[i+1]))
	}
	return strings.Join(parts, m.dim().Render("  "))
}

func (m *Model) errLine(msg string, w int) string {
	if msg == "" {
		return ""
	}
	return "\n" + m.bgAlt().Foreground(lipgloss.Color(m.th.Danger)).Width(w).Render(msg)
}

// ---- message ----

type messageModal struct{ title, text string }

func newMessageModal(title, text string) *messageModal { return &messageModal{title, text} }
func (mm *messageModal) init(*Model) tea.Cmd           { return nil }
func (mm *messageModal) update(m *Model, msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "enter", "esc", "q", "space":
			m.closeModal()
		}
	}
	return nil
}
func (mm *messageModal) view(m *Model, width int) string {
	w := m.modalWidth(width)
	body := m.bgAlt().Width(w-6).Render(mm.text) + "\n\n" + m.keyHints("enter", "cerrar")
	title := mm.title
	return m.frame(title, body, w)
}

// ---- confirm ----

type confirmModal struct {
	title, message, detail, yes string
	onYes, onNo                 func(*Model) tea.Cmd
}

func newConfirm(title, message, detail, yes string, onYes, onNo func(*Model) tea.Cmd) *confirmModal {
	return &confirmModal{title, message, detail, yes, onYes, onNo}
}

func (c *confirmModal) init(*Model) tea.Cmd { return nil }
func (c *confirmModal) update(m *Model, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "y", "Y", "enter":
		m.closeModal()
		if c.onYes != nil {
			return c.onYes(m)
		}
	case "n", "N", "esc", "ctrl+c":
		m.closeModal()
		if c.onNo != nil {
			return c.onNo(m)
		}
	}
	return nil
}
func (c *confirmModal) view(m *Model, width int) string {
	w := m.modalWidth(width)
	body := m.bgAlt().Width(w - 6).Render(c.message)
	if c.detail != "" {
		body += "\n\n" + m.dim().Width(w-6).Render(c.detail)
	}
	body += "\n\n" + m.keyHints("enter", c.yes, "esc", "cancelar")
	return m.frame(c.title, body, w)
}

// ---- keyboard-interactive prompt (OTP, second factor…) ----

type promptModal struct {
	instruction, question string
	in                    textinput.Model
	reply                 chan promptReply
}

func newPromptModal(m *Model, p promptMsg) *promptModal {
	in := m.st.input("", 40)
	if !p.echo {
		in.EchoMode = textinput.EchoPassword
	}
	return &promptModal{instruction: p.instruction, question: p.question, in: in, reply: p.reply}
}

func (p *promptModal) init(*Model) tea.Cmd { return p.in.Focus() }
func (p *promptModal) update(m *Model, msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "enter":
			p.reply <- promptReply{p.in.Value(), true}
			p.in.SetValue("")
			m.closeModal()
			return nil
		case "esc", "ctrl+c":
			p.reply <- promptReply{"", false}
			p.in.SetValue("")
			m.closeModal()
			return nil
		}
	}
	var cmd tea.Cmd
	p.in, cmd = p.in.Update(msg)
	return cmd
}
func (p *promptModal) view(m *Model, width int) string {
	w := m.modalWidth(width)
	title := p.instruction
	if strings.TrimSpace(title) == "" {
		title = "El servidor solicita información"
	}
	p.in.SetWidth(w - 8)
	body := m.dim().Render(p.question) + "\n" + p.in.View() + "\n\n" + m.keyHints("enter", "aceptar", "esc", "cancelar")
	return m.frame(title, body, w)
}

// ---- path completion for local file fields ----

// completePath completes a local path with Tab: a single match is filled in
// (directories get a trailing slash), several advance to their common prefix.
func completePath(p string) string {
	if p == "" {
		return p
	}
	exp := expandHome(p)
	matches, err := filepath.Glob(escapeGlob(exp) + "*")
	if err != nil || len(matches) == 0 {
		return p
	}
	sort.Strings(matches)
	pick := matches[0]
	if len(matches) > 1 {
		pick = commonPrefix(matches)
	} else if st, err := os.Stat(pick); err == nil && st.IsDir() {
		pick += "/"
	}
	if len(pick) <= len(exp) {
		return p
	}
	if strings.HasPrefix(p, "~/") {
		if h, _ := os.UserHomeDir(); h != "" && strings.HasPrefix(pick, h) {
			return "~" + strings.TrimPrefix(pick, h)
		}
	}
	return pick
}

func escapeGlob(s string) string {
	return strings.NewReplacer(`\`, `\\`, "*", `\*`, "?", `\?`, "[", `\[`).Replace(s)
}

func commonPrefix(ss []string) string {
	p := ss[0]
	for _, s := range ss[1:] {
		for !strings.HasPrefix(s, p) {
			p = p[:len(p)-1]
		}
	}
	return p
}

// ---- generic stack of text fields ----

type fields struct {
	labels []string
	ins    []textinput.Model
	focus  int
	// pathField marks fields that complete local paths with Tab.
	pathField map[int]bool
}

func (f *fields) setFocus(i int) tea.Cmd {
	f.focus = (i + len(f.ins)) % len(f.ins)
	var cmd tea.Cmd
	for j := range f.ins {
		if j == f.focus {
			cmd = f.ins[j].Focus()
		} else {
			f.ins[j].Blur()
		}
	}
	return cmd
}

// key handles navigation; it returns handled=false for keys the focused
// input should receive.
func (f *fields) key(k tea.KeyPressMsg) (cmd tea.Cmd, handled bool) {
	switch k.String() {
	case "tab":
		if f.pathField[f.focus] {
			cur := f.ins[f.focus].Value()
			if next := completePath(cur); next != cur {
				f.ins[f.focus].SetValue(next)
				f.ins[f.focus].CursorEnd()
				return nil, true
			}
		}
		return f.setFocus(f.focus + 1), true
	case "shift+tab", "up":
		return f.setFocus(f.focus - 1), true
	case "down":
		return f.setFocus(f.focus + 1), true
	}
	return nil, false
}

func (f *fields) update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	f.ins[f.focus], cmd = f.ins[f.focus].Update(msg)
	return cmd
}

func (f *fields) view(m *Model, w int) string {
	var rows []string
	lw := 0
	for _, l := range f.labels {
		lw = max(lw, lipgloss.Width(l))
	}
	for i := range f.ins {
		f.ins[i].SetWidth(max(w-lw-8, 10))
		lab := m.dim()
		if i == f.focus {
			lab = m.bgAlt().Foreground(lipgloss.Color(m.th.Accent)).Bold(true)
		}
		rows = append(rows, lab.Width(lw+1).Render(f.labels[i])+f.ins[i].View())
	}
	return strings.Join(rows, "\n")
}

// ---- profile form ----

var authChoices = []struct {
	label  string
	method store.AuthMethod
}{
	{"Contraseña", store.AuthPassword},
	{"Llave privada", store.AuthKey},
	{"Llave y contraseña", store.AuthBoth},
	{"Interactivo (teclado)", store.AuthInteractive},
}

func usesPassword(m store.AuthMethod) bool { return m != store.AuthKey }
func usesKey(m store.AuthMethod) bool      { return m == store.AuthKey || m == store.AuthBoth }

// Form field slots.
const (
	fName = iota
	fHost
	fPort
	fUser
	fAuth // not a text input: selector
	fPass
	fKey
	fKeyPass
)

type formModal struct {
	idx   int
	ins   [8]textinput.Model
	auth  int
	focus int // slot of the focused field
	err   string
}

func newFormModal(m *Model, idx int) *formModal {
	f := &formModal{idx: idx}
	mk := func(ph string) textinput.Model { return m.st.input(ph, 30) }
	f.ins[fName] = mk("Producción")
	f.ins[fHost] = mk("servidor.ejemplo.com")
	f.ins[fPort] = mk("22")
	f.ins[fUser] = mk("root")
	f.ins[fPass] = mk("")
	f.ins[fPass].EchoMode = textinput.EchoPassword
	f.ins[fKey] = mk("~/.ssh/id_ed25519")
	f.ins[fKeyPass] = mk("")
	f.ins[fKeyPass].EchoMode = textinput.EchoPassword
	if idx >= 0 && idx < len(m.profiles) {
		p := m.profiles[idx]
		f.ins[fName].SetValue(p.Name)
		f.ins[fHost].SetValue(p.Host)
		f.ins[fPort].SetValue(itoa(int(p.Port)))
		f.ins[fUser].SetValue(p.User)
		f.ins[fPass].SetValue(p.Password)
		f.ins[fKey].SetValue(p.PrivateKeyPath)
		f.ins[fKeyPass].SetValue(p.PrivateKeyPass)
		for i, c := range authChoices {
			if c.method == p.AuthMethod {
				f.auth = i
			}
		}
	}
	return f
}

func (f *formModal) method() store.AuthMethod { return authChoices[f.auth].method }

// slots lists the visible fields in order.
func (f *formModal) slots() []int {
	s := []int{fName, fHost, fPort, fUser, fAuth}
	if usesPassword(f.method()) {
		s = append(s, fPass)
	}
	if usesKey(f.method()) {
		s = append(s, fKey, fKeyPass)
	}
	return s
}

func (f *formModal) setFocus(slot int) tea.Cmd {
	f.focus = slot
	var cmd tea.Cmd
	for i := range f.ins {
		f.ins[i].Blur()
	}
	if slot != fAuth {
		cmd = f.ins[slot].Focus()
	}
	return cmd
}

func (f *formModal) move(delta int) tea.Cmd {
	s := f.slots()
	pos := 0
	for i, v := range s {
		if v == f.focus {
			pos = i
		}
	}
	return f.setFocus(s[(pos+delta+len(s))%len(s)])
}

func (f *formModal) init(m *Model) tea.Cmd { return f.setFocus(fName) }

func (f *formModal) update(m *Model, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		if f.focus != fAuth {
			var cmd tea.Cmd
			f.ins[f.focus], cmd = f.ins[f.focus].Update(msg)
			return cmd
		}
		return nil
	}
	switch k.String() {
	case "esc", "ctrl+c":
		m.closeModal()
		return nil
	case "ctrl+s":
		return f.save(m)
	case "shift+tab", "up":
		return f.move(-1)
	case "down":
		return f.move(1)
	case "tab":
		if f.focus == fKey {
			cur := f.ins[fKey].Value()
			if next := completePath(cur); next != cur {
				f.ins[fKey].SetValue(next)
				f.ins[fKey].CursorEnd()
				return nil
			}
		}
		return f.move(1)
	case "enter":
		s := f.slots()
		if f.focus == s[len(s)-1] {
			return f.save(m)
		}
		return f.move(1)
	}
	if f.focus == fAuth {
		switch k.String() {
		case "left", "h", "shift+space":
			f.auth = (f.auth + len(authChoices) - 1) % len(authChoices)
		case "right", "l", "space":
			f.auth = (f.auth + 1) % len(authChoices)
		}
		return nil
	}
	var cmd tea.Cmd
	f.ins[f.focus], cmd = f.ins[f.focus].Update(msg)
	return cmd
}

func (f *formModal) validate() (store.ConnectionProfile, string) {
	portText := strings.TrimSpace(f.ins[fPort].Value())
	if portText == "" {
		portText = "22"
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 {
		return store.ConnectionProfile{}, "El puerto debe ser un número entre 1 y 65535."
	}
	mt := f.method()
	np := store.ConnectionProfile{
		Name: strings.TrimSpace(f.ins[fName].Value()), Host: strings.TrimSpace(f.ins[fHost].Value()),
		Port: uint16(port), User: strings.TrimSpace(f.ins[fUser].Value()), AuthMethod: mt,
	}
	if usesPassword(mt) {
		np.Password = f.ins[fPass].Value()
	}
	if usesKey(mt) {
		np.PrivateKeyPath = strings.TrimSpace(f.ins[fKey].Value())
		np.PrivateKeyPass = f.ins[fKeyPass].Value()
	}
	switch {
	case np.Name == "" || np.Host == "":
		return np, "Nombre y host son obligatorios."
	case np.User == "":
		return np, "El usuario es obligatorio."
	case (mt == store.AuthPassword || mt == store.AuthInteractive) && np.Password == "":
		return np, "Indica la contraseña de la cuenta."
	case mt == store.AuthKey && np.PrivateKeyPath == "":
		return np, "Indica la ruta de la llave privada."
	case mt == store.AuthBoth && np.PrivateKeyPath == "" && np.Password == "":
		return np, "Indica una llave privada, una contraseña o ambas."
	}
	return np, ""
}

func (f *formModal) save(m *Model) tea.Cmd {
	np, msg := f.validate()
	if msg != "" {
		f.err = msg
		return nil
	}
	next := append([]store.ConnectionProfile(nil), m.profiles...)
	if f.idx >= 0 && f.idx < len(next) {
		next[f.idx] = np
	} else {
		next = append(next, np)
		m.cursor = len(next) - 1
	}
	if err := m.saveProfiles(next); err != nil {
		f.err = "Error guardando: " + err.Error()
		return nil
	}
	m.closeModal()
	return nil
}

func (f *formModal) view(m *Model, width int) string {
	w := m.modalWidth(width)
	title := "Nueva conexión"
	if f.idx >= 0 && f.idx < len(m.profiles) {
		title = "Editar " + m.profiles[f.idx].Name
	}
	const lw = 15
	row := func(slot int, label string) string {
		lab := m.dim()
		if f.focus == slot {
			lab = m.bgAlt().Foreground(lipgloss.Color(m.th.Accent)).Bold(true)
		}
		var val string
		if slot == fAuth {
			val = m.bgAlt().Render("◀ ") +
				m.bgAlt().Foreground(lipgloss.Color(m.th.Accent2)).Bold(f.focus == fAuth).Render(authChoices[f.auth].label) +
				m.bgAlt().Render(" ▶")
		} else {
			f.ins[slot].SetWidth(max(w-lw-8, 10))
			val = f.ins[slot].View()
		}
		return lab.Width(lw).Render(label) + val
	}
	rows := []string{
		row(fName, "Nombre"), row(fHost, "Host"), row(fPort, "Puerto"), row(fUser, "Usuario"),
		"", row(fAuth, "Autenticación"),
	}
	if usesPassword(f.method()) {
		rows = append(rows, row(fPass, "Contraseña"))
	}
	if usesKey(f.method()) {
		rows = append(rows, row(fKey, "Llave privada"), row(fKeyPass, "Passphrase"))
	}
	body := strings.Join(rows, "\n") + m.errLine(f.err, w-6) + "\n\n" +
		m.keyHints("tab", "siguiente", "ctrl+s", "guardar", "esc", "cancelar")
	return m.frame(title, body, w)
}

// ---- remote path (upload / edit remote) ----

type pathModal struct {
	upload bool
	f      fields
	err    string
}

func newPathModal(m *Model, upload bool) *pathModal {
	p := &pathModal{upload: upload, f: fields{pathField: map[int]bool{}}}
	home := ""
	if m.sess != nil {
		home = m.sess.client.Home()
	}
	if upload {
		p.f.labels = []string{"Archivo local", "Destino remoto"}
		p.f.ins = []textinput.Model{m.st.input("~/archivo.txt", 40), m.st.input(home+"/", 40)}
		p.f.pathField[0] = true
		p.f.ins[1].SetValue(home + "/")
	} else {
		p.f.labels = []string{"Archivo remoto"}
		p.f.ins = []textinput.Model{m.st.input(home+"/.bashrc", 40)}
		p.f.ins[0].SetValue(home + "/")
	}
	return p
}

func (p *pathModal) init(m *Model) tea.Cmd { return p.f.setFocus(0) }

func (p *pathModal) update(m *Model, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return p.f.update(msg)
	}
	switch k.String() {
	case "esc", "ctrl+c":
		m.closeModal()
		return nil
	case "enter":
		if m.sess == nil {
			m.closeModal()
			return nil
		}
		if p.upload {
			local := expandHome(strings.TrimSpace(p.f.ins[0].Value()))
			st, err := os.Stat(local)
			switch {
			case strings.TrimSpace(local) == "":
				p.err = "Indica el archivo local."
				return nil
			case err != nil:
				p.err = "No se puede leer el archivo: " + err.Error()
				return nil
			case st.IsDir():
				p.err = "Eso es una carpeta; elige un archivo."
				return nil
			}
			remote := strings.TrimSpace(p.f.ins[1].Value())
			if remote == "" {
				p.err = "Indica el destino remoto."
				return nil
			}
			m.closeModal()
			return m.doUpload(local, remote)
		}
		path := strings.TrimSpace(p.f.ins[0].Value())
		if path == "" || strings.HasSuffix(path, "/") {
			p.err = "Indica la ruta de un archivo."
			return nil
		}
		m.closeModal()
		return m.readRemote(path)
	}
	if cmd, handled := p.f.key(k); handled {
		return cmd
	}
	return p.f.update(msg)
}

func (p *pathModal) view(m *Model, width int) string {
	w := m.modalWidth(width)
	title, hint := "Editar archivo remoto", "Ruta del archivo en el servidor (límite 2 MiB, solo texto UTF-8)"
	if p.upload {
		title, hint = "Subir archivo", "Tab completa la ruta local · destino terminado en / conserva el nombre"
	}
	body := m.dim().Width(w-6).Render(hint) + "\n\n" + p.f.view(m, w) + m.errLine(p.err, w-6) + "\n\n" +
		m.keyHints("enter", "aceptar", "tab", "completar/siguiente", "esc", "cancelar")
	return m.frame(title, body, w)
}

// ---- remote editor ----

type editorModal struct {
	path string
	ta   textarea.Model
}

func newEditorModal(m *Model, path, content string) *editorModal {
	e := &editorModal{path: path}
	e.ta = m.st.area(max(m.width-12, 30), max(m.height-12, 5))
	e.ta.SetValue(content)
	e.ta.MoveToBegin()
	return e
}

func (e *editorModal) init(m *Model) tea.Cmd { return e.ta.Focus() }
func (e *editorModal) update(m *Model, msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc":
			m.closeModal()
			return nil
		case "ctrl+s":
			path, content := e.path, e.ta.Value()
			m.closeModal()
			return m.writeRemote(path, content)
		}
	}
	var cmd tea.Cmd
	e.ta, cmd = e.ta.Update(msg)
	return cmd
}
func (e *editorModal) view(m *Model, width int) string {
	w := max(m.width-6, 40)
	e.ta.SetWidth(w - 6)
	e.ta.SetHeight(max(m.height-12, 5))
	body := e.ta.View() + "\n\n" + m.keyHints("ctrl+s", "guardar", "esc", "cancelar")
	return m.frame(e.path, body, w)
}

// ---- backup export / import ----

type backupModal struct {
	export bool
	f      fields
	busy   bool
	err    string
}

func newBackupModal(m *Model, export bool) *backupModal {
	b := &backupModal{export: export, f: fields{pathField: map[int]bool{0: true}}}
	home, _ := os.UserHomeDir()
	if export {
		b.f.labels = []string{"Guardar en", "Contraseña", "Repetir"}
		b.f.ins = []textinput.Model{m.st.input("~/gossh-backup.json", 40), m.st.input("", 40), m.st.input("", 40)}
		b.f.ins[0].SetValue(filepath.Join(home, "gossh-backup.json"))
		b.f.ins[1].EchoMode, b.f.ins[2].EchoMode = textinput.EchoPassword, textinput.EchoPassword
	} else {
		b.f.labels = []string{"Archivo", "Contraseña"}
		b.f.ins = []textinput.Model{m.st.input("~/gossh-backup.json", 40), m.st.input("", 40)}
		b.f.ins[1].EchoMode = textinput.EchoPassword
	}
	return b
}

func (b *backupModal) init(m *Model) tea.Cmd {
	if b.export {
		return b.f.setFocus(1) // path is prefilled
	}
	return b.f.setFocus(0)
}

func (b *backupModal) update(m *Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case exportDoneMsg:
		b.busy = false
		if msg.err != nil {
			b.err = msg.err.Error()
			return nil
		}
		m.closeModal()
		return m.notify(noteSuccess, "Backup exportado a "+msg.path)
	case importDoneMsg:
		b.busy = false
		if msg.err != nil {
			b.err = msg.err.Error()
			return nil
		}
		return b.finishImport(m, msg.b)
	case tea.KeyPressMsg:
		if b.busy {
			return nil
		}
		switch msg.String() {
		case "esc", "ctrl+c":
			m.closeModal()
			return nil
		case "enter":
			if b.f.focus < len(b.f.ins)-1 {
				return b.f.setFocus(b.f.focus + 1)
			}
			return b.submit(m)
		}
		if cmd, handled := b.f.key(msg); handled {
			return cmd
		}
	}
	return b.f.update(msg)
}

func (b *backupModal) submit(m *Model) tea.Cmd {
	path := expandHome(strings.TrimSpace(b.f.ins[0].Value()))
	pass := b.f.ins[1].Value()
	if path == "" {
		b.err = "Indica la ruta del archivo."
		return nil
	}
	if b.export {
		if pass != b.f.ins[2].Value() {
			b.err = "Las contraseñas no coinciden."
			return nil
		}
		if len([]rune(pass)) < store.MinBackupPassphraseChars {
			b.err = fmt.Sprintf("La contraseña debe tener al menos %d caracteres.", store.MinBackupPassphraseChars)
			return nil
		}
		run := func(*Model) tea.Cmd { return b.runExport(m, path, pass) }
		if _, err := os.Stat(path); err == nil {
			return m.openModal(newConfirm("¿Reemplazar archivo?", path+" ya existe.", "", "Reemplazar",
				func(m *Model) tea.Cmd { m.modal = b; return run(m) },
				func(m *Model) tea.Cmd { m.modal = b; return nil }))
		}
		return run(m)
	}
	b.busy, b.err = true, ""
	return func() tea.Msg {
		data, err := readBackupFile(path)
		if err != nil {
			return importDoneMsg{err: err}
		}
		bk, err := store.ImportBackup(data, pass)
		return importDoneMsg{b: bk, err: err}
	}
}

func (b *backupModal) runExport(m *Model, path, pass string) tea.Cmd {
	b.busy, b.err = true, ""
	bk := store.Backup{
		Profiles: append([]store.ConnectionProfile(nil), m.profiles...),
		Snippets: append([]store.Snippet(nil), m.snippets...),
	}
	return func() tea.Msg { // Argon2 is slow: off the UI loop
		js, err := store.ExportBackup(bk, pass)
		if err == nil {
			err = store.WritePrivate(path, []byte(js))
		}
		return exportDoneMsg{path, err}
	}
}

func (b *backupModal) finishImport(m *Model, imported store.Backup) tea.Cmd {
	added, skipped := store.MergeImported(m.profiles, imported.Profiles)
	profiles := store.AppendNew(m.profiles, imported.Profiles)
	snippets := store.AppendNewSnippets(m.snippets, imported.Snippets)
	snAdded := len(snippets) - len(m.snippets)
	if err := m.saveProfiles(profiles); err != nil {
		b.err = "Error guardando: " + err.Error()
		return nil
	}
	if snAdded > 0 {
		if err := m.store.SaveSnippets(snippets); err != nil {
			b.err = "Error guardando snippets: " + err.Error()
			return nil
		}
		m.snippets = snippets
	}
	m.closeModal()
	msg := fmt.Sprintf("%d importadas, %d duplicadas omitidas", added, skipped)
	if len(imported.Snippets) > 0 {
		msg += fmt.Sprintf(" · %d snippets nuevos", snAdded)
	}
	return m.notify(noteSuccess, msg)
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

func (b *backupModal) view(m *Model, width int) string {
	w := m.modalWidth(width)
	title := "Importar backup"
	if b.export {
		title = "Exportar backup cifrado"
	}
	hint := "Contraseña del backup (mín. 12 caracteres). Tab completa rutas."
	body := m.dim().Width(w-6).Render(hint) + "\n\n" + b.f.view(m, w) + m.errLine(b.err, w-6)
	if b.busy {
		body += "\n\n" + m.bgAlt().Foreground(lipgloss.Color(m.th.Warning)).Render("Trabajando… (Argon2id puede tardar unos segundos)")
	} else {
		label := "importar"
		if b.export {
			label = "exportar"
		}
		body += "\n\n" + m.keyHints("enter", label, "esc", "cancelar")
	}
	return m.frame(title, body, w)
}

// exportBackup opens the export dialog after authenticating when needed.
func (m *Model) exportBackup() tea.Cmd {
	if len(m.profiles) == 0 {
		return m.notify(noteInfo, "No hay conexiones para exportar")
	}
	return m.guard("Exportar las conexiones guardadas", func() tea.Cmd {
		return m.openModal(newBackupModal(m, true))
	})
}
