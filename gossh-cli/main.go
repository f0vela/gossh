// gossh: terminal (Bubble Tea) SSH manager, feature-for-feature with the
// gossh desktop app and sharing its encrypted store.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"gossh-cli/internal/settings"
	"gossh-cli/internal/theme"
	"gossh-cli/internal/tui"
)

const version = "0.1.0"

const usage = `gossh — gestor SSH en la terminal

Uso:
  gossh [--theme NOMBRE]     abre la interfaz
  gossh theme list           lista los temas (con muestra de color)
  gossh theme set NOMBRE     guarda el tema por defecto
  gossh theme current        muestra el tema guardado
  gossh --version

Dentro de la app: t abre el selector de temas con vista previa, T pasa al siguiente.
`

func main() {
	themeFlag := flag.String("theme", "", "tema para esta sesión (ver: gossh theme list)")
	showVersion := flag.Bool("version", false, "muestra la versión")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()

	if *showVersion {
		fmt.Println("gossh", version)
		return
	}
	if args := flag.Args(); len(args) > 0 {
		if args[0] == "theme" {
			os.Exit(themeCmd(args[1:]))
		}
		flag.Usage()
		os.Exit(2)
	}
	if *themeFlag != "" {
		if _, ok := theme.Get(*themeFlag); !ok {
			fmt.Fprintf(os.Stderr, "gossh: tema desconocido %q. Disponibles: %s\n", *themeFlag, strings.Join(theme.Names(), ", "))
			os.Exit(2)
		}
	}

	m, err := tui.New(tui.Options{Theme: *themeFlag})
	if err != nil {
		fmt.Fprintln(os.Stderr, "gossh:", err)
		os.Exit(1)
	}
	p := tea.NewProgram(m)
	m.SetSender(p.Send)
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "gossh:", err)
		os.Exit(1)
	}
}

func themeCmd(args []string) int {
	cs, err := settings.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gossh:", err)
		return 1
	}
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list":
		cfg, _ := cs.Load()
		cur := theme.Or(cfg.Theme).Name
		for _, t := range theme.All() {
			sw := ""
			for _, c := range []string{t.Bg, t.Accent, t.Accent2, t.Success, t.Warning, t.Danger, t.Info, t.Fg} {
				sw += lipgloss.NewStyle().Background(theme.C(c)).Render("  ")
			}
			mark := " "
			if t.Name == cur {
				mark = "*"
			}
			lipgloss.Printf("%s %-22s %s  %s\n", mark, t.Name, sw, t.Label)
		}
	case "current":
		cfg, _ := cs.Load()
		fmt.Println(theme.Or(cfg.Theme).Name)
	case "set":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "uso: gossh theme set NOMBRE")
			return 2
		}
		t, ok := theme.Get(args[1])
		if !ok {
			fmt.Fprintf(os.Stderr, "tema desconocido %q. Disponibles: %s\n", args[1], strings.Join(theme.Names(), ", "))
			return 2
		}
		cfg, err := cs.Load()
		if err != nil {
			fmt.Fprintln(os.Stderr, "gossh: ajustes ilegibles, no se sobrescriben:", err)
			return 1
		}
		cfg.Theme = t.Name
		if err := cs.Save(cfg); err != nil {
			fmt.Fprintln(os.Stderr, "gossh:", err)
			return 1
		}
		fmt.Println("Tema guardado:", t.Label)
	default:
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	return 0
}
