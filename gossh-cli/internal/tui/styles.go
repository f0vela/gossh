package tui

import (
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"

	"gossh-cli/internal/theme"
)

// styles are the lipgloss styles derived from the active theme.
type styles struct {
	th theme.Theme

	base      lipgloss.Style // plain text on the window background
	muted     lipgloss.Style
	accent    lipgloss.Style
	accent2   lipgloss.Style
	bold      lipgloss.Style
	success   lipgloss.Style
	warning   lipgloss.Style
	danger    lipgloss.Style
	info      lipgloss.Style
	sidebar   lipgloss.Style // sidebar background
	row       lipgloss.Style
	rowSel    lipgloss.Style
	rowSelDim lipgloss.Style // selected row while the sidebar is not focused
	modal     lipgloss.Style
	key       lipgloss.Style // key-cap in hints
	hint      lipgloss.Style
	status    lipgloss.Style
}

func newStyles(th theme.Theme) styles {
	c := theme.C
	bg := lipgloss.NewStyle().Background(c(th.Bg)).Foreground(c(th.Fg))
	return styles{
		th:      th,
		base:    bg,
		muted:   bg.Foreground(c(th.Muted)),
		accent:  bg.Foreground(c(th.Accent)),
		accent2: bg.Foreground(c(th.Accent2)),
		bold:    bg.Bold(true),
		success: bg.Foreground(c(th.Success)),
		warning: bg.Foreground(c(th.Warning)),
		danger:  bg.Foreground(c(th.Danger)),
		info:    bg.Foreground(c(th.Info)),
		sidebar: lipgloss.NewStyle().Background(c(th.BgAlt)).Foreground(c(th.Fg)),
		row:     lipgloss.NewStyle().Background(c(th.BgAlt)).Foreground(c(th.Fg)),
		rowSel: lipgloss.NewStyle().Background(c(th.Surface)).Foreground(c(th.Accent)).
			Bold(true),
		rowSelDim: lipgloss.NewStyle().Background(c(th.Surface)).Foreground(c(th.Fg)),
		modal: lipgloss.NewStyle().Background(c(th.BgAlt)).Foreground(c(th.Fg)).
			Border(lipgloss.RoundedBorder()).BorderForeground(c(th.Accent)).BorderBackground(c(th.BgAlt)).
			Padding(1, 2),
		key:    lipgloss.NewStyle().Background(c(th.Surface)).Foreground(c(th.Accent)).Bold(true).Padding(0, 1),
		hint:   lipgloss.NewStyle().Background(c(th.BgAlt)).Foreground(c(th.Muted)),
		status: lipgloss.NewStyle().Background(c(th.BgAlt)).Foreground(c(th.Muted)),
	}
}

// onBg returns a style that paints on the given background.
func (s styles) onBg(bg string) lipgloss.Style {
	return lipgloss.NewStyle().Background(theme.C(bg)).Foreground(theme.C(s.th.Fg))
}

// input returns a textinput themed for use inside a modal.
func (s styles) input(placeholder string, width int) textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = placeholder
	ti.SetWidth(width)
	ti.SetStyles(s.inputStyles())
	return ti
}

func (s styles) inputStyles() textinput.Styles {
	c := theme.C
	field := lipgloss.NewStyle().Background(c(s.th.Surface)).Foreground(c(s.th.Fg))
	ph := lipgloss.NewStyle().Background(c(s.th.Surface)).Foreground(c(s.th.Muted))
	st := textinput.Styles{}
	st.Focused = textinput.StyleState{Text: field, Placeholder: ph, Suggestion: ph, Prompt: field}
	st.Blurred = textinput.StyleState{Text: field.Foreground(c(s.th.Muted)), Placeholder: ph, Suggestion: ph, Prompt: field}
	st.Cursor.Color = c(s.th.Accent)
	return st
}

// area returns a themed textarea.
func (s styles) area(w, h int) textarea.Model {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.CharLimit = 0
	ta.SetWidth(w)
	ta.SetHeight(h)
	c := theme.C
	st := ta.Styles()
	f := lipgloss.NewStyle().Background(c(s.th.Surface)).Foreground(c(s.th.Fg))
	for _, ss := range []*textarea.StyleState{&st.Focused, &st.Blurred} {
		ss.Base = f
		ss.Text = f
		ss.CursorLine = f
		ss.EndOfBuffer = f.Foreground(c(s.th.Surface))
		ss.Placeholder = f.Foreground(c(s.th.Muted))
		ss.Prompt = f
	}
	st.Cursor.Color = c(s.th.Accent)
	ta.SetStyles(st)
	return ta
}
