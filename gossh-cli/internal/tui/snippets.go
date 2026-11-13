package tui

import (
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"gossh-cli/internal/store"
)

type snippetsModal struct {
	filter textinput.Model
	cur    int // index into the filtered list
	// edit state; editing is false while listing
	editing bool
	edIdx   int // -1 new
	name    textinput.Model
	text    textarea.Model
	enter   bool
	focus   int // 0 name, 1 text, 2 enter toggle
	err     string
}

func newSnippetsModal(m *Model) *snippetsModal {
	return &snippetsModal{filter: m.st.input("buscar snippet…", 40), edIdx: -1}
}

func (s *snippetsModal) init(m *Model) tea.Cmd { return s.filter.Focus() }

func (s *snippetsModal) visible(m *Model) []int {
	q := strings.ToLower(strings.TrimSpace(s.filter.Value()))
	var out []int
	for i, sn := range m.snippets {
		if q == "" || strings.Contains(strings.ToLower(sn.Name+" "+sn.Text), q) {
			out = append(out, i)
		}
	}
	return out
}

func (s *snippetsModal) startEdit(m *Model, idx int) tea.Cmd {
	s.editing, s.edIdx, s.err, s.focus = true, idx, "", 0
	s.name = m.st.input("Reiniciar nginx", 40)
	s.text = m.st.area(40, 7)
	s.enter = false
	if idx >= 0 && idx < len(m.snippets) {
		sn := m.snippets[idx]
		s.name.SetValue(sn.Name)
		s.text.SetValue(sn.Text)
		s.enter = sn.Enter
	}
	return s.name.Focus()
}

func (s *snippetsModal) setFocus(f int) tea.Cmd {
	s.focus = (f + 3) % 3
	s.name.Blur()
	s.text.Blur()
	switch s.focus {
	case 0:
		return s.name.Focus()
	case 1:
		return s.text.Focus()
	}
	return nil
}

func (s *snippetsModal) update(m *Model, msg tea.Msg) tea.Cmd {
	k, isKey := msg.(tea.KeyPressMsg)
	if s.editing {
		return s.updateEdit(m, msg, k, isKey)
	}
	if !isKey {
		var cmd tea.Cmd
		s.filter, cmd = s.filter.Update(msg)
		return cmd
	}
	vis := s.visible(m)
	switch k.String() {
	case "esc", "ctrl+c":
		m.closeModal()
		return nil
	case "up":
		s.cur = max(s.cur-1, 0)
		return nil
	case "down":
		s.cur = min(s.cur+1, max(len(vis)-1, 0))
		return nil
	case "ctrl+n":
		return s.startEdit(m, -1)
	case "ctrl+e":
		if s.cur < len(vis) {
			return s.startEdit(m, vis[s.cur])
		}
		return nil
	case "ctrl+x":
		if s.cur < len(vis) {
			idx := vis[s.cur]
			sn := m.snippets[idx]
			return m.openModal(newConfirm("¿Eliminar «"+sn.Name+"»?", "Se borrará el snippet «"+sn.Name+"».", "", "Eliminar",
				func(m *Model) tea.Cmd {
					next := append(append([]store.Snippet(nil), m.snippets[:idx]...), m.snippets[idx+1:]...)
					if err := m.store.SaveSnippets(next); err != nil {
						return m.notify(noteError, "Error guardando: "+err.Error())
					}
					m.snippets = next
					s.cur = max(s.cur-1, 0)
					m.modal = s
					return nil
				}, func(m *Model) tea.Cmd { m.modal = s; return nil }))
		}
		return nil
	case "enter":
		if s.cur >= len(vis) {
			return nil
		}
		if m.sess == nil {
			return m.notify(noteInfo, "Conecta a un servidor para pegar el snippet")
		}
		sn := m.snippets[vis[s.cur]]
		m.closeModal()
		m.focus = focusTerm
		m.sess.paste(sn.Text)
		if sn.Enter {
			_, _ = m.sess.client.Stdin().Write([]byte("\r"))
		}
		return nil
	}
	var cmd tea.Cmd
	s.filter, cmd = s.filter.Update(msg)
	s.cur = 0
	return cmd
}

func (s *snippetsModal) updateEdit(m *Model, msg tea.Msg, k tea.KeyPressMsg, isKey bool) tea.Cmd {
	if isKey {
		switch k.String() {
		case "esc":
			s.editing = false
			return s.filter.Focus()
		case "ctrl+s":
			return s.save(m)
		case "tab":
			return s.setFocus(s.focus + 1)
		case "shift+tab":
			return s.setFocus(s.focus - 1)
		}
		if s.focus == 2 {
			switch k.String() {
			case "space", "enter", "left", "right":
				s.enter = !s.enter
			}
			return nil
		}
		if s.focus == 0 && (k.String() == "enter" || k.String() == "down") {
			return s.setFocus(1)
		}
	}
	var cmd tea.Cmd
	switch s.focus {
	case 0:
		s.name, cmd = s.name.Update(msg)
	case 1:
		s.text, cmd = s.text.Update(msg)
	}
	return cmd
}

func (s *snippetsModal) save(m *Model) tea.Cmd {
	name := strings.TrimSpace(s.name.Value())
	if name == "" {
		s.err = "Ponle un nombre al snippet."
		return nil
	}
	if strings.TrimSpace(s.text.Value()) == "" {
		s.err = "El snippet está vacío."
		return nil
	}
	sn := store.Snippet{Name: name, Text: s.text.Value(), Enter: s.enter}
	next := append([]store.Snippet(nil), m.snippets...)
	if s.edIdx >= 0 && s.edIdx < len(next) {
		next[s.edIdx] = sn
	} else {
		next = append(next, sn)
	}
	if err := m.store.SaveSnippets(next); err != nil {
		s.err = "Error guardando: " + err.Error()
		return nil
	}
	m.snippets = next
	s.editing = false
	return s.filter.Focus()
}

func (s *snippetsModal) view(m *Model, width int) string {
	w := m.modalWidth(width)
	if s.editing {
		return s.viewEdit(m, w)
	}
	vis := s.visible(m)
	rows := []string{s.filter.View(), ""}
	s.filter.SetWidth(w - 8)
	if len(vis) == 0 {
		msg := "No hay snippets. Ctrl+N crea uno."
		if strings.TrimSpace(s.filter.Value()) != "" {
			msg = "Sin resultados."
		}
		rows = append(rows, m.dim().Render(msg))
	}
	const maxRows = 8
	start := 0
	if s.cur >= maxRows {
		start = s.cur - maxRows + 1
	}
	for i := start; i < len(vis) && i < start+maxRows; i++ {
		sn := m.snippets[vis[i]]
		first := strings.SplitN(strings.TrimSpace(sn.Text), "\n", 2)[0]
		tag := ""
		if sn.Enter {
			tag = " ⏎"
		}
		label := sn.Name + m.dim().Render("  "+first+tag)
		st := m.bgAlt().Width(w - 6)
		if i == s.cur {
			st = lipgloss.NewStyle().Background(lipgloss.Color(m.th.Surface)).Foreground(lipgloss.Color(m.th.Accent)).Bold(true).Width(w - 6)
			label = sn.Name + "  " + first + tag
		}
		rows = append(rows, st.MaxWidth(w-6).Render(label))
	}
	body := strings.Join(rows, "\n") + "\n\n" +
		m.keyHints("enter", "pegar", "^n", "nuevo", "^e", "editar", "^x", "borrar", "esc", "cerrar")
	title := "Snippets"
	if m.sess == nil {
		title += " (sin sesión)"
	}
	return m.frame(title, body, w)
}

func (s *snippetsModal) viewEdit(m *Model, w int) string {
	lab := func(i int, t string) string {
		if s.focus == i {
			return m.bgAlt().Foreground(lipgloss.Color(m.th.Accent)).Bold(true).Render(t)
		}
		return m.dim().Render(t)
	}
	s.name.SetWidth(w - 8)
	s.text.SetWidth(w - 8)
	box := "[ ]"
	if s.enter {
		box = "[x]"
	}
	body := lab(0, "Nombre") + "\n" + s.name.View() + "\n\n" + lab(1, "Texto") + "\n" + s.text.View() + "\n\n" +
		lab(2, box+" Pulsar Enter tras pegar") + m.errLine(s.err, w-6) + "\n\n" +
		m.keyHints("tab", "siguiente", "ctrl+s", "guardar", "esc", "volver")
	title := "Nuevo snippet"
	if s.edIdx >= 0 {
		title = "Editar snippet"
	}
	return m.frame(title, body, w)
}
