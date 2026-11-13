package tui

import (
	"image/color"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"gossh-cli/internal/sshx"
	"gossh-cli/internal/theme"
)

// session is a live SSH shell rendered through a VT emulator.
type session struct {
	client *sshx.Client
	emu    *vt.SafeEmulator
	cols   int
	rows   int

	title   atomic.Value // string set by the remote program (untrusted)
	pending atomic.Bool  // a redraw message is already queued
	scroll  int          // lines scrolled back into history (0 = live)
	closeMu sync.Once
}

type outputMsg struct{ s *session }
type sessionEndMsg struct{ s *session }

func newSession(c *sshx.Client, cols, rows int, send func(tea.Msg)) *session {
	s := &session{client: c, emu: vt.NewSafeEmulator(cols, rows), cols: cols, rows: rows}
	s.emu.SetScrollbackSize(5000)
	s.emu.SetCallbacks(vt.Callbacks{Title: func(t string) { s.title.Store(t) }})

	// remote → emulator
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := c.Stdout().Read(buf)
			if n > 0 {
				_, _ = s.emu.Write(buf[:n])
				if s.pending.CompareAndSwap(false, true) {
					send(outputMsg{s})
				}
			}
			if err != nil {
				send(sessionEndMsg{s})
				return
			}
		}
	}()
	// emulator → remote (typed keys, paste, terminal query replies)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := s.emu.Read(buf)
			if n > 0 {
				if _, werr := c.Stdin().Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return s
}

func (s *session) close() {
	s.closeMu.Do(func() {
		s.client.Close()
		_ = s.emu.Close()
	})
}

func (s *session) resize(cols, rows int) {
	if cols < 1 || rows < 1 || (cols == s.cols && rows == s.rows) {
		return
	}
	s.cols, s.rows = cols, rows
	s.emu.Resize(cols, rows)
	_ = s.client.Resize(cols, rows)
}

// remoteTitle is the title the remote program set, stripped of control
// characters and capped: it is untrusted text shown in our UI.
func (s *session) remoteTitle() string {
	t, _ := s.title.Load().(string)
	t = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, t)
	if r := []rune(t); len(r) > 60 {
		t = string(r[:60]) + "…"
	}
	return t
}

func (s *session) scrollBy(delta int) {
	max := s.emu.ScrollbackLen()
	s.scroll += delta
	if s.scroll > max {
		s.scroll = max
	}
	if s.scroll < 0 {
		s.scroll = 0
	}
}

func (s *session) sendKey(k tea.KeyPressMsg) {
	s.scroll = 0
	s.emu.SendKey(uv.KeyPressEvent(k))
}

func (s *session) paste(text string) {
	s.scroll = 0
	s.emu.Paste(text)
}

// themed maps a cell color to the theme: default colors become the theme's
// foreground/background and the 16 ANSI colors use the theme palette.
func themed(c color.Color, def string, th theme.Theme) color.Color {
	switch v := c.(type) {
	case nil:
		return theme.C(def)
	case ansi.BasicColor:
		return theme.C(th.ANSI[int(v)&15])
	case ansi.IndexedColor:
		if int(v) < 16 {
			return theme.C(th.ANSI[int(v)])
		}
	}
	return c
}

// render paints the visible terminal (live screen or scrolled history) with
// the theme's colors. It returns the ANSI text of exactly rows lines.
func (s *session) render(th theme.Theme) string {
	sb := s.emu.ScrollbackLen()
	top := sb - s.scroll
	if top < 0 {
		top = 0
	}
	var out strings.Builder
	var run strings.Builder
	for r := 0; r < s.rows; r++ {
		idx := top + r
		var prev *uv.Style
		flush := func() {
			if run.Len() == 0 {
				return
			}
			out.WriteString(prev.Styled(run.String()))
			run.Reset()
		}
		for x := 0; x < s.cols; {
			var cell *uv.Cell
			if idx < sb {
				cell = s.emu.ScrollbackCellAt(x, idx)
			} else {
				cell = s.emu.CellAt(x, idx-sb)
			}
			st := uv.Style{}
			content, w := " ", 1
			if cell != nil {
				st = cell.Style
				if cell.Content != "" {
					content = cell.Content
				}
				if cell.Width > 1 {
					w = cell.Width
				}
			}
			st.Fg = themed(st.Fg, th.Fg, th)
			st.Bg = themed(st.Bg, th.Bg, th)
			if prev == nil || !prev.Equal(&st) {
				flush()
				cp := st
				prev = &cp
			}
			run.WriteString(content)
			x += w
		}
		flush()
		out.WriteString(ansi.ResetStyle)
		if r < s.rows-1 {
			out.WriteByte('\n')
		}
	}
	return out.String()
}
