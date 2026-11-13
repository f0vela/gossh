package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"gossh-cli/internal/store"
)

// View renders the whole screen.
func (m *Model) View() tea.View {
	var v tea.View
	v.AltScreen = true
	v.BackgroundColor = lipgloss.Color(m.th.Bg)
	v.ForegroundColor = lipgloss.Color(m.th.Fg)
	v.WindowTitle = m.windowTitle()
	if m.width < 20 || m.height < 8 {
		v.SetContent("terminal demasiado pequeña")
		return v
	}

	var screen string
	if m.locked {
		screen = m.lockView()
	} else {
		screen = lipgloss.JoinVertical(lipgloss.Left, m.body(), m.statusBar())
		if m.modal != nil {
			screen = m.overlay(screen)
		} else if m.focus == focusTerm && m.sess != nil && m.sess.scroll == 0 && !m.sess.emu.CursorHidden() {
			p := m.sess.emu.CursorPosition()
			v.Cursor = tea.NewCursor(m.sidebarWidth()+2+p.X, 3+p.Y)
		}
	}
	v.SetContent(screen)
	return v
}

func (m *Model) windowTitle() string {
	if m.sess != nil && m.connIdx >= 0 && m.connIdx < len(m.profiles) {
		t := m.profiles[m.connIdx].Name
		if r := m.sess.remoteTitle(); strings.TrimSpace(r) != "" {
			t += " — " + r
		} else {
			t += " — " + appName
		}
		return t
	}
	return appName
}

func (m *Model) body() string {
	h := m.height - 1
	main := m.mainPane(m.mainWidth(), h)
	if sw := m.sidebarWidth(); sw > 0 {
		return lipgloss.JoinHorizontal(lipgloss.Top, m.sidebarPane(sw, h), main)
	}
	return main
}

// fill pads/crops content to exactly w x h painted with style.
func fill(st lipgloss.Style, w, h int, content string) string {
	return st.Width(w).Height(h).MaxWidth(w).MaxHeight(h).Render(content)
}

func (m *Model) overlay(base string) string {
	box := m.modal.view(m, 64)
	bw, bh := lipgloss.Width(box), lipgloss.Height(box)
	x, y := max((m.width-bw)/2, 0), max((m.height-bh)/2, 0)
	return lipgloss.NewCompositor(
		lipgloss.NewLayer(base),
		lipgloss.NewLayer(box).X(x).Y(y).Z(1),
	).Render()
}

// ---- sidebar ----

func (m *Model) sidebarPane(w, h int) string {
	st := m.st
	bg := lipgloss.Color(m.th.BgAlt)
	line := func(s lipgloss.Style, text string) string { return fill(s, w, 1, text) }

	title := st.sidebar.Foreground(lipgloss.Color(m.th.Accent)).Bold(true)
	rows := []string{
		line(title, " ▍"+appName),
		line(st.sidebar.Foreground(lipgloss.Color(m.th.Muted)), fmt.Sprintf(" %d conexiones", len(m.profiles))),
	}
	if m.filtering || m.filter.Value() != "" {
		m.filter.SetWidth(w - 4)
		in := m.filter.View()
		rows = append(rows, st.sidebar.Width(w).Render(" "+in+" "))
	} else {
		rows = append(rows, line(st.sidebar, ""))
	}

	f := m.filtered()
	avail := (h - len(rows) - 1) / 2
	if avail < 1 {
		avail = 1
	}
	pos := 0
	for i, idx := range f {
		if idx == m.cursor {
			pos = i
		}
	}
	if pos < m.top {
		m.top = pos
	}
	if pos >= m.top+avail {
		m.top = pos - avail + 1
	}
	m.top = max(min(m.top, max(len(f)-avail, 0)), 0)

	if len(f) == 0 {
		msg := "Sin conexiones.\n n  crea una"
		if len(m.profiles) > 0 {
			msg = "Sin resultados."
		}
		for _, l := range strings.Split(msg, "\n") {
			rows = append(rows, line(st.sidebar.Foreground(lipgloss.Color(m.th.Muted)), " "+l))
		}
	}
	for i := m.top; i < len(f) && i < m.top+avail; i++ {
		idx := f[i]
		p := m.profiles[idx]
		sel := idx == m.cursor
		rs := st.row
		sub := st.sidebar.Foreground(lipgloss.Color(m.th.Muted))
		if sel {
			if m.focus == focusSidebar {
				rs = st.rowSel
			} else {
				rs = st.rowSelDim
			}
			sub = st.rowSelDim.Foreground(lipgloss.Color(m.th.Muted))
		}
		marker := " "
		if sel {
			marker = "▎"
		}
		num := ""
		if idx < 9 {
			num = fmt.Sprintf("%d", idx+1)
		}
		right := m.dimOn(rs, num)
		if idx == m.connIdx && m.sess != nil {
			right = rs.Foreground(lipgloss.Color(m.th.Success)).Render("● " + elapsed(time.Since(m.connectedAt)))
		} else if idx == m.connIdx || (m.connecting && sel) {
			right = rs.Foreground(lipgloss.Color(m.th.Warning)).Render("…")
		}
		name := truncate(p.Name, w-lipgloss.Width(right)-4)
		gap := max(w-lipgloss.Width(marker+" "+name)-lipgloss.Width(right)-1, 1)
		l1 := rs.Render(marker+" "+name+strings.Repeat(" ", gap)) + right + rs.Render(" ")
		addr := fmt.Sprintf("%s@%s", p.User, p.Host)
		if p.Port != 22 {
			addr += fmt.Sprintf(":%d", p.Port)
		}
		l2 := sub.Width(w).Render("  " + truncate(addr, w-3))
		rows = append(rows, l1, l2)
	}
	content := strings.Join(rows, "\n")
	return lipgloss.NewStyle().Background(bg).Width(w).Height(h).MaxWidth(w).MaxHeight(h).Render(content)
}

func (m *Model) dimOn(base lipgloss.Style, s string) string {
	return base.Foreground(lipgloss.Color(m.th.Muted)).Render(s)
}

func truncate(s string, w int) string {
	if w < 1 {
		return ""
	}
	r := []rune(s)
	if lipgloss.Width(s) <= w {
		return s
	}
	for len(r) > 0 && lipgloss.Width(string(r))+1 > w {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

// ---- main card ----

func (m *Model) mainPane(w, h int) string {
	st := m.st
	border := m.th.Border
	if m.focus == focusTerm && m.sess != nil {
		border = m.th.Accent
	}
	card := lipgloss.NewStyle().Background(lipgloss.Color(m.th.Bg)).
		Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(border)).BorderBackground(lipgloss.Color(m.th.Bg)).
		Padding(0, 1)
	iw, ih := w-4, h-2
	var content string
	header := m.cardHeader(iw)
	sep := st.muted.Render(strings.Repeat("─", iw))
	var inner string
	switch {
	case m.sess != nil && m.connIdx >= 0:
		inner = m.sess.render(m.th)
	case len(m.profiles) == 0:
		inner = m.emptyState(iw, ih-2, "No hay conexiones", "Pulsa n para crear la primera, o i para importar un backup.")
	default:
		p, _ := m.current()
		inner = m.profileCard(iw, ih-2, p)
	}
	content = header + "\n" + sep + "\n" + fill(st.base, iw, ih-2, inner)
	return card.Width(w).Height(h).Render(content)
}

func (m *Model) cardHeader(w int) string {
	st := m.st
	p, ok := m.current()
	if m.sess != nil && m.connIdx >= 0 && m.connIdx < len(m.profiles) {
		p, ok = m.profiles[m.connIdx], true
	}
	var left string
	if ok {
		addr := fmt.Sprintf("%s@%s", p.User, p.Host)
		if p.Port != 22 {
			addr += fmt.Sprintf(":%d", p.Port)
		}
		left = st.bold.Render(p.Name) + st.muted.Render("  "+addr)
	} else {
		left = st.bold.Render(appName)
	}
	var pill string
	switch {
	case m.connecting:
		pill = st.warning.Render("◌ conectando")
	case m.sess != nil:
		pill = st.success.Render("● conectado")
		if m.leader {
			pill = st.accent2.Bold(true).Render("CTRL+Q …") + st.base.Render("  ") + pill
		} else if m.sess.scroll > 0 {
			pill = st.warning.Render(fmt.Sprintf("↑%d  ", m.sess.scroll)) + pill
		}
	default:
		pill = st.muted.Render("○ desconectado")
	}
	gap := max(w-lipgloss.Width(left)-lipgloss.Width(pill), 1)
	return left + st.base.Render(strings.Repeat(" ", gap)) + pill
}

func (m *Model) emptyState(w, h int, title, hint string) string {
	st := m.st
	t := st.bold.Foreground(lipgloss.Color(m.th.Accent)).Render(title)
	body := t + "\n" + st.muted.Render(hint)
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, body, lipgloss.WithWhitespaceStyle(st.base))
}

func (m *Model) profileCard(w, h int, p store.ConnectionProfile) string {
	st := m.st
	label := func(k, v string) string {
		return st.muted.Render(pad(k, 16)) + st.base.Render(v)
	}
	auth := authChoices[0].label
	for _, c := range authChoices {
		if c.method == p.AuthMethod {
			auth = c.label
		}
	}
	lines := []string{
		st.bold.Foreground(lipgloss.Color(m.th.Accent)).Render(p.Name), "",
		label("Host", p.Host),
		label("Puerto", itoa(int(p.Port))),
		label("Usuario", p.User),
		label("Autenticación", auth),
	}
	if usesKey(p.AuthMethod) {
		lines = append(lines, label("Llave privada", p.PrivateKeyPath))
	}
	if m.connecting {
		lines = append(lines, "", st.warning.Render("Conectando…"))
	} else {
		lines = append(lines, "",
			st.muted.Render("Pulsa ")+st.key.Render("enter")+st.muted.Render(" para conectar · ")+
				st.key.Render("e")+st.muted.Render(" editar · ")+st.key.Render("?")+st.muted.Render(" ayuda"))
	}
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, strings.Join(lines, "\n"),
		lipgloss.WithWhitespaceStyle(st.base))
}

// ---- status bar ----

func (m *Model) statusBar() string {
	st := m.st.status
	right := m.th.Label + "  ? ayuda "
	var left string
	switch {
	case m.toast != nil:
		c := m.th.Info
		switch m.toast.kind {
		case noteSuccess:
			c = m.th.Success
		case noteError:
			c = m.th.Danger
		}
		left = " " + st.Foreground(lipgloss.Color(c)).Render(oneLine(m.toast.text))
	case m.leader:
		left = " " + st.Foreground(lipgloss.Color(m.th.Accent2)).Render(
			"Ctrl+Q → q barra · d desconectar · u subir · e editar remoto · s snippets · t tema · x exportar · r importar · ? ayuda")
	case m.status != "":
		left = " " + st.Foreground(lipgloss.Color(m.th.Warning)).Render(m.status)
	case m.focus == focusTerm && m.sess != nil:
		left = " " + st.Render("Ctrl+Q: comandos · Shift+PgUp/PgDn: historial")
	default:
		left = " " + st.Render("↑↓ mover · enter conectar · n nueva · e editar · d borrar · s snippets · t tema · q salir")
	}
	left = truncate(left, max(m.width-lipgloss.Width(right)-1, 1))
	gap := max(m.width-lipgloss.Width(left)-lipgloss.Width(right), 0)
	return st.Width(m.width).Render(left + st.Render(strings.Repeat(" ", gap)) + st.Render(right))
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
}

func (m *Model) lockView() string {
	st := m.st
	title := st.bold.Foreground(lipgloss.Color(m.th.Accent)).Render("🔒 " + appName + " bloqueado")
	hint := st.muted.Render("Enter para desbloquear con " + m.authKind + " · q para salir")
	body := title + "\n\n" + hint
	if m.authBusy {
		body = title + "\n\n" + st.warning.Render("Esperando autenticación…")
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, body, lipgloss.WithWhitespaceStyle(st.base))
}
