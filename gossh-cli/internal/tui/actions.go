package tui

import (
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"gossh-cli/internal/theme"
)

// action is one command of the app. The same table drives the key bindings
// of both focus areas and the help/command palette.
type action struct {
	label   string
	sKey    string // key in the sidebar ("" = none)
	lKey    string // key after the leader (Ctrl+Q) ("" = none)
	needSes bool
	run     func(m *Model) tea.Cmd
}

var actions []action

func init() {
	actions = []action{
		{"Conectar", "enter", "", false, func(m *Model) tea.Cmd { return m.connectTo(m.cursor) }},
		{"Ir a la terminal", "tab", "", true, func(m *Model) tea.Cmd { m.focus = focusTerm; return nil }},
		{"Nueva conexión", "n", "", false, func(m *Model) tea.Cmd { return m.openModal(newFormModal(m, -1)) }},
		{"Editar conexión", "e", "", false, func(m *Model) tea.Cmd { return m.editProfile(m.cursor) }},
		{"Duplicar conexión", "c", "", false, func(m *Model) tea.Cmd { return m.duplicate(m.cursor) }},
		{"Eliminar conexión", "d", "", false, func(m *Model) tea.Cmd { return m.confirmDelete(m.cursor) }},
		{"Buscar", "/", "", false, func(m *Model) tea.Cmd { m.filtering = true; return m.filter.Focus() }},
		{"Desconectar", "x", "d", true, func(m *Model) tea.Cmd { m.endSession("Desconectado"); return nil }},
		{"Subir archivo (SFTP)", "u", "u", true, func(m *Model) tea.Cmd { return m.openModal(newPathModal(m, true)) }},
		{"Editar archivo remoto", "o", "e", true, func(m *Model) tea.Cmd { return m.openModal(newPathModal(m, false)) }},
		{"Snippets", "s", "s", false, func(m *Model) tea.Cmd { return m.openModal(newSnippetsModal(m)) }},
		{"Cambiar tema…", "t", "t", false, func(m *Model) tea.Cmd { return m.openModal(newThemeModal(m)) }},
		{"Siguiente tema", "T", "T", false, func(m *Model) tea.Cmd { return m.cycleTheme(1) }},
		{"Importar backup", "i", "r", false, func(m *Model) tea.Cmd { return m.openModal(newBackupModal(m, false)) }},
		{"Exportar backup", "E", "x", false, func(m *Model) tea.Cmd { return m.exportBackup() }},
		{"Ajustes", ",", ",", false, func(m *Model) tea.Cmd { return m.openModal(newSettingsModal(m)) }},
		{"Bloquear", "l", "l", false, func(m *Model) tea.Cmd { return m.lock() }},
		{"Mostrar/ocultar barra lateral", "b", "b", false, func(m *Model) tea.Cmd { m.sidebarHidden = !m.sidebarHidden; m.fitSession(); return nil }},
		{"Ayuda y comandos", "?", "?", false, func(m *Model) tea.Cmd { return m.openModal(newHelpModal()) }},
		{"Salir", "q", "", false, func(m *Model) tea.Cmd { return m.quitRequest() }},
	}
}

func (m *Model) sidebarKey(k tea.KeyPressMsg) tea.Cmd {
	if m.filtering {
		switch k.String() {
		case "esc":
			m.filter.SetValue("")
			m.filter.Blur()
			m.filtering = false
			return nil
		case "enter":
			m.filter.Blur()
			m.filtering = false
			return nil
		case "up", "down":
			// fall through to navigation below
		default:
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(k)
			if f := m.filtered(); len(f) > 0 {
				found := false
				for _, i := range f {
					found = found || i == m.cursor
				}
				if !found {
					m.cursor = f[0]
				}
			}
			return cmd
		}
	}
	key := k.String()
	switch key {
	case "up", "k":
		m.moveCursor(-1)
		return nil
	case "down", "j":
		m.moveCursor(1)
		return nil
	case "home", "g":
		if f := m.filtered(); len(f) > 0 {
			m.cursor = f[0]
		}
		return nil
	case "end", "G":
		if f := m.filtered(); len(f) > 0 {
			m.cursor = f[len(f)-1]
		}
		return nil
	case "ctrl+c":
		return m.quitRequest()
	case "esc":
		if m.filter.Value() != "" {
			m.filter.SetValue("")
		}
		return nil
	}
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		return m.connectTo(int(key[0] - '1'))
	}
	for _, a := range actions {
		if a.sKey == key {
			if a.needSes && m.sess == nil {
				return m.notify(noteInfo, "No hay sesión activa: conecta primero")
			}
			if a.sKey == "enter" || a.sKey == "e" || a.sKey == "c" || a.sKey == "d" {
				if _, ok := m.current(); !ok {
					return nil
				}
			}
			return a.run(m)
		}
	}
	return nil
}

// terminalKey sends keys to the remote shell, except the leader (Ctrl+Q).
func (m *Model) terminalKey(k tea.KeyPressMsg) tea.Cmd {
	key := k.String()
	if m.leader {
		m.leader = false
		switch key {
		case "ctrl+q":
			m.sess.sendKey(k) // literal Ctrl+Q
			return nil
		case "q", "esc":
			m.focus = focusSidebar
			return nil
		case "pgup":
			m.sess.scrollBy(m.sess.rows / 2)
			return nil
		case "pgdown":
			m.sess.scrollBy(-m.sess.rows / 2)
			return nil
		}
		for _, a := range actions {
			if a.lKey != "" && a.lKey == key {
				return a.run(m)
			}
		}
		return m.notify(noteInfo, "Ctrl+Q ? muestra los comandos")
	}
	switch key {
	case "ctrl+q":
		m.leader = true
		return nil
	case "shift+pgup":
		m.sess.scrollBy(m.sess.rows / 2)
		return nil
	case "shift+pgdown":
		m.sess.scrollBy(-m.sess.rows / 2)
		return nil
	}
	m.sess.sendKey(k)
	return nil
}

func (m *Model) quitRequest() tea.Cmd {
	if m.sess == nil {
		return m.shutdown()
	}
	return m.openModal(newConfirm("¿Salir de "+appName+"?",
		"Hay una sesión SSH abierta.", "Se cerrará la conexión.", "Salir",
		func(m *Model) tea.Cmd { return m.shutdown() }, nil))
}

func (m *Model) editProfile(idx int) tea.Cmd {
	if idx < 0 || idx >= len(m.profiles) {
		return nil
	}
	return m.guard("Editar "+m.profiles[idx].Name, func() tea.Cmd {
		return m.openModal(newFormModal(m, idx))
	})
}

func (m *Model) confirmDelete(idx int) tea.Cmd {
	if idx < 0 || idx >= len(m.profiles) {
		return nil
	}
	p := m.profiles[idx]
	return m.openModal(newConfirm("¿Eliminar «"+p.Name+"»?",
		"Se borrará la conexión «"+p.Name+"» ("+p.User+"@"+p.Host+").",
		"Esta acción no se puede deshacer.", "Eliminar",
		func(m *Model) tea.Cmd { return m.deleteProfile(idx) }, nil))
}

func (m *Model) cycleTheme(delta int) tea.Cmd {
	all := theme.All()
	i := (theme.Index(m.th.Name) + delta + len(all)) % len(all)
	m.setTheme(all[i])
	return m.persistTheme(all[i].Name)
}

// ---- SFTP ----

func (m *Model) doUpload(local, remote string) tea.Cmd {
	client := m.sess.client
	local = expandHome(strings.TrimSpace(local))
	remote = strings.TrimSpace(remote)
	if strings.HasSuffix(remote, "/") {
		remote += filepath.Base(local)
	}
	m.status = "Subiendo " + filepath.Base(local) + "…"
	send := m.send
	return func() tea.Msg {
		if client.Exists(remote) {
			reply := make(chan bool, 1)
			send(askMsg{c: newConfirm("¿Reemplazar archivo remoto?", remote+" ya existe.",
				"Se sobrescribirá con «"+filepath.Base(local)+"».", "Reemplazar", nil, nil), reply: reply})
			if !<-reply {
				return uploadDoneMsg{skip: true}
			}
		}
		return uploadDoneMsg{remote: remote, err: client.UploadFile(local, remote)}
	}
}

func (m *Model) readRemote(path string) tea.Cmd {
	client := m.sess.client
	path = strings.TrimSpace(path)
	m.status = "Leyendo " + path + "…"
	return func() tea.Msg {
		content, err := client.ReadRemoteFile(path)
		return remoteReadMsg{path, content, err}
	}
}

func (m *Model) writeRemote(path, content string) tea.Cmd {
	if m.sess == nil {
		return m.notify(noteError, "Sin conexión: no se pudo guardar "+path)
	}
	client := m.sess.client
	m.status = "Guardando " + path + "…"
	return func() tea.Msg {
		return remoteWrittenMsg{path, client.WriteRemoteFile(path, content)}
	}
}
