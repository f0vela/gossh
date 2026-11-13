package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"gossh-cli/internal/settings"
	"gossh-cli/internal/theme"
)

// ---- theme picker with live preview ----

type themeModal struct {
	orig string // theme to restore on cancel
	cur  int
}

func newThemeModal(m *Model) *themeModal {
	return &themeModal{orig: m.th.Name, cur: theme.Index(m.th.Name)}
}

func (t *themeModal) init(*Model) tea.Cmd { return nil }

func (t *themeModal) update(m *Model, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	all := theme.All()
	move := func(d int) {
		t.cur = (t.cur + d + len(all)) % len(all)
		m.setTheme(all[t.cur]) // live preview
	}
	switch k.String() {
	case "up", "k":
		move(-1)
	case "down", "j":
		move(1)
	case "pgup":
		move(-5)
	case "pgdown":
		move(5)
	case "home", "g":
		t.cur = len(all) - 1
		move(1)
	case "enter":
		m.closeModal()
		return m.persistTheme(all[t.cur].Name)
	case "esc", "q", "ctrl+c":
		m.setTheme(theme.Or(t.orig))
		m.closeModal()
	}
	return nil
}

func (t *themeModal) view(m *Model, width int) string {
	w := m.modalWidth(width)
	all := theme.All()
	const maxRows = 12
	start := 0
	if t.cur >= maxRows {
		start = t.cur - maxRows + 1
	}
	var rows []string
	for i := start; i < len(all) && i < start+maxRows; i++ {
		th := all[i]
		sw := ""
		for _, c := range []string{th.Accent, th.Accent2, th.Success, th.Warning, th.Danger, th.Info} {
			sw += lipgloss.NewStyle().Foreground(theme.C(c)).Background(theme.C(th.Bg)).Render("██")
		}
		mark := "  "
		st := m.bgAlt()
		if i == t.cur {
			mark = "▸ "
			st = lipgloss.NewStyle().Background(lipgloss.Color(m.th.Surface)).Foreground(lipgloss.Color(m.th.Accent)).Bold(true)
		}
		cur := ""
		if th.Name == t.orig {
			cur = " (actual)"
		}
		label := mark + th.Label + cur
		gap := max(w-6-lipgloss.Width(label)-lipgloss.Width(sw)-1, 1)
		rows = append(rows, st.Render(label+strings.Repeat(" ", gap))+sw+st.Render(" "))
	}
	body := strings.Join(rows, "\n") + "\n\n" + m.keyHints("↑↓", "vista previa", "enter", "aplicar", "esc", "cancelar")
	return m.frame("Tema", body, w)
}

// ---- settings ----

type settingsModal struct{ cur int }

func newSettingsModal(*Model) *settingsModal { return &settingsModal{} }
func (s *settingsModal) init(*Model) tea.Cmd { return nil }

func (s *settingsModal) update(m *Model, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "esc", "q", "ctrl+c":
		m.closeModal()
	case "up", "k":
		s.cur = max(s.cur-1, 0)
	case "down", "j":
		s.cur = min(s.cur+1, 2)
	case "enter", "space", "left", "right":
		switch s.cur {
		case 0:
			if m.authKind == "" {
				return nil
			}
			want := !m.cfg.RequireForCredentials
			return m.changeSetting(func(c *settings.Settings) { c.RequireForCredentials = want })
		case 1:
			if m.authKind == "" {
				return nil
			}
			want := !m.cfg.RequireOnLaunch
			return m.changeSetting(func(c *settings.Settings) { c.RequireOnLaunch = want })
		case 2:
			d := 1
			if k.String() == "left" {
				d = -1
			}
			return m.cycleTheme(d)
		}
	}
	return nil
}

func (s *settingsModal) view(m *Model, width int) string {
	w := m.modalWidth(width)
	var intro string
	if m.authKind == "" {
		intro = m.bgAlt().Foreground(lipgloss.Color(m.th.Warning)).Width(w - 6).Render(
			"Este equipo no tiene Touch ID ni contraseña de usuario disponible, así que no se puede pedir autenticación.")
	} else {
		intro = m.dim().Width(w - 6).Render("Se pedirá " + m.authKind + " (o la contraseña de macOS si falla). " +
			"Cambiar estas opciones también exige autenticarse.")
	}
	kind := m.authKind
	if kind == "" {
		kind = "autenticación"
	}
	row := func(i int, title, hint, val string) string {
		st := m.bgAlt().Width(w - 6)
		if i == s.cur {
			st = lipgloss.NewStyle().Background(lipgloss.Color(m.th.Surface)).Foreground(lipgloss.Color(m.th.Accent)).Width(w - 6)
		}
		return st.Render(val+" "+title) + "\n" + m.dim().Width(w-6).Render("    "+hint)
	}
	box := func(on bool) string {
		if on {
			return "[x]"
		}
		return "[ ]"
	}
	body := intro + "\n\n" +
		row(0, "Pedir "+kind+" para conectar", "Antes de usar, editar o exportar credenciales guardadas.", box(m.cfg.RequireForCredentials)) + "\n" +
		row(1, "Bloquear al abrir", "No descifra ni muestra las conexiones hasta autenticarte (Ctrl+Q l la bloquea).", box(m.cfg.RequireOnLaunch)) + "\n" +
		row(2, "Tema: "+m.th.Label, "←/→ para cambiar; t abre el selector con vista previa.", "◆") + "\n\n" +
		m.keyHints("enter/espacio", "cambiar", "esc", "cerrar")
	return m.frame("Ajustes", body, w)
}

// ---- help / command palette ----

type helpModal struct{ cur int }

func newHelpModal() *helpModal           { return &helpModal{} }
func (h *helpModal) init(*Model) tea.Cmd { return nil }

func (h *helpModal) update(m *Model, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "esc", "q", "?", "ctrl+c":
		m.closeModal()
	case "up", "k":
		h.cur = max(h.cur-1, 0)
	case "down", "j":
		h.cur = min(h.cur+1, len(actions)-1)
	case "enter":
		a := actions[h.cur]
		if a.needSes && m.sess == nil {
			return m.notify(noteInfo, "No hay sesión activa: conecta primero")
		}
		m.closeModal()
		return a.run(m)
	}
	return nil
}

func (h *helpModal) view(m *Model, width int) string {
	w := m.modalWidth(width)
	rows := []string{
		m.dim().Width(w - 6).Render("Con la terminal enfocada todo va al servidor; Ctrl+Q es la tecla líder."),
		"",
		m.bgAlt().Bold(true).Render(pad("Acción", w-6-24)) + m.bgAlt().Bold(true).Render(pad("Barra lateral", 14)) + m.bgAlt().Bold(true).Render("Ctrl+Q +"),
	}
	maxRows := max(m.height-19, 5)
	start := 0
	if h.cur >= maxRows {
		start = h.cur - maxRows + 1
	}
	for i := start; i < len(actions) && i < start+maxRows; i++ {
		a := actions[i]
		st := m.bgAlt()
		if i == h.cur {
			st = lipgloss.NewStyle().Background(lipgloss.Color(m.th.Surface)).Foreground(lipgloss.Color(m.th.Accent)).Bold(true)
		}
		line := pad(a.label, w-6-24) + pad(dash(a.sKey), 14) + dash(a.lKey)
		rows = append(rows, st.Width(w-6).Render(line))
	}
	rows = append(rows, "", m.dim().Width(w-6).Render(
		"↑↓/jk mover · 1-9 conectar · Ctrl+Q q vuelve a la barra · Shift+PgUp/PgDn historial · Ctrl+Q Ctrl+Q envía Ctrl+Q."))
	rows = append(rows, "", m.keyHints("enter", "ejecutar", "esc", "cerrar"))
	return m.frame("Ayuda y comandos", strings.Join(rows, "\n"), w)
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func pad(s string, w int) string {
	if n := lipgloss.Width(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}
