// Package theme defines the color themes of gossh.
package theme

import (
	"image/color"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
)

// Theme is a named palette. Colors are hex strings (#rrggbb).
type Theme struct {
	Name  string // slug used on the command line and in settings.json
	Label string // display name

	Bg, BgAlt, Surface             string // window, panels/sidebar, selected rows & inputs
	Fg, Muted, Border              string
	Accent, Accent2                string // primary accent and secondary accent
	Success, Warning, Danger, Info string

	// ANSI is the 16-color palette applied to the remote terminal
	// (black, red, green, yellow, blue, magenta, cyan, white + bright).
	ANSI [16]string
}

// C converts a hex string to a color.
func C(hex string) color.Color { return lipgloss.Color(hex) }

const Default = "tokyonight"

var themes = []Theme{
	{
		Name: "tokyonight", Label: "Tokyo Night",
		Bg: "#1a1b26", BgAlt: "#16161e", Surface: "#292e42",
		Fg: "#c0caf5", Muted: "#565f89", Border: "#3b4261",
		Accent: "#7aa2f7", Accent2: "#bb9af7",
		Success: "#9ece6a", Warning: "#e0af68", Danger: "#f7768e", Info: "#7dcfff",
		ANSI: [16]string{"#15161e", "#f7768e", "#9ece6a", "#e0af68", "#7aa2f7", "#bb9af7", "#7dcfff", "#a9b1d6",
			"#414868", "#f7768e", "#9ece6a", "#e0af68", "#7aa2f7", "#bb9af7", "#7dcfff", "#c0caf5"},
	},
	{
		Name: "tokyonight-storm", Label: "Tokyo Night Storm",
		Bg: "#24283b", BgAlt: "#1f2335", Surface: "#2f334d",
		Fg: "#c0caf5", Muted: "#565f89", Border: "#3b4261",
		Accent: "#7aa2f7", Accent2: "#bb9af7",
		Success: "#9ece6a", Warning: "#e0af68", Danger: "#f7768e", Info: "#7dcfff",
		ANSI: [16]string{"#1d202f", "#f7768e", "#9ece6a", "#e0af68", "#7aa2f7", "#bb9af7", "#7dcfff", "#a9b1d6",
			"#414868", "#f7768e", "#9ece6a", "#e0af68", "#7aa2f7", "#bb9af7", "#7dcfff", "#c0caf5"},
	},
	{
		Name: "dracula", Label: "Dracula",
		Bg: "#282a36", BgAlt: "#21222c", Surface: "#44475a",
		Fg: "#f8f8f2", Muted: "#6272a4", Border: "#44475a",
		Accent: "#bd93f9", Accent2: "#ff79c6",
		Success: "#50fa7b", Warning: "#f1fa8c", Danger: "#ff5555", Info: "#8be9fd",
		ANSI: [16]string{"#21222c", "#ff5555", "#50fa7b", "#f1fa8c", "#bd93f9", "#ff79c6", "#8be9fd", "#f8f8f2",
			"#6272a4", "#ff6e6e", "#69ff94", "#ffffa5", "#d6acff", "#ff92df", "#a4ffff", "#ffffff"},
	},
	{
		Name: "catppuccin-mocha", Label: "Catppuccin Mocha",
		Bg: "#1e1e2e", BgAlt: "#181825", Surface: "#313244",
		Fg: "#cdd6f4", Muted: "#6c7086", Border: "#45475a",
		Accent: "#cba6f7", Accent2: "#f5c2e7",
		Success: "#a6e3a1", Warning: "#f9e2af", Danger: "#f38ba8", Info: "#89dceb",
		ANSI: [16]string{"#45475a", "#f38ba8", "#a6e3a1", "#f9e2af", "#89b4fa", "#f5c2e7", "#94e2d5", "#bac2de",
			"#585b70", "#f38ba8", "#a6e3a1", "#f9e2af", "#89b4fa", "#f5c2e7", "#94e2d5", "#a6adc8"},
	},
	{
		Name: "catppuccin-macchiato", Label: "Catppuccin Macchiato",
		Bg: "#24273a", BgAlt: "#1e2030", Surface: "#363a4f",
		Fg: "#cad3f5", Muted: "#6e738d", Border: "#494d64",
		Accent: "#c6a0f6", Accent2: "#f5bde6",
		Success: "#a6da95", Warning: "#eed49f", Danger: "#ed8796", Info: "#91d7e3",
		ANSI: [16]string{"#494d64", "#ed8796", "#a6da95", "#eed49f", "#8aadf4", "#f5bde6", "#8bd5ca", "#b8c0e0",
			"#5b6078", "#ed8796", "#a6da95", "#eed49f", "#8aadf4", "#f5bde6", "#8bd5ca", "#a5adcb"},
	},
	{
		Name: "catppuccin-frappe", Label: "Catppuccin Frappé",
		Bg: "#303446", BgAlt: "#292c3c", Surface: "#414559",
		Fg: "#c6d0f5", Muted: "#737994", Border: "#51576d",
		Accent: "#ca9ee6", Accent2: "#f4b8e4",
		Success: "#a6d189", Warning: "#e5c890", Danger: "#e78284", Info: "#99d1db",
		ANSI: [16]string{"#51576d", "#e78284", "#a6d189", "#e5c890", "#8caaee", "#f4b8e4", "#81c8be", "#b5bfe2",
			"#626880", "#e78284", "#a6d189", "#e5c890", "#8caaee", "#f4b8e4", "#81c8be", "#a5adce"},
	},
	{
		Name: "catppuccin-latte", Label: "Catppuccin Latte (claro)",
		Bg: "#eff1f5", BgAlt: "#e6e9ef", Surface: "#ccd0da",
		Fg: "#4c4f69", Muted: "#8c8fa1", Border: "#bcc0cc",
		Accent: "#8839ef", Accent2: "#ea76cb",
		Success: "#40a02b", Warning: "#df8e1d", Danger: "#d20f39", Info: "#04a5e5",
		ANSI: [16]string{"#5c5f77", "#d20f39", "#40a02b", "#df8e1d", "#1e66f5", "#ea76cb", "#179299", "#acb0be",
			"#6c6f85", "#d20f39", "#40a02b", "#df8e1d", "#1e66f5", "#ea76cb", "#179299", "#bcc0cc"},
	},
	{
		Name: "nord", Label: "Nord",
		Bg: "#2e3440", BgAlt: "#272c36", Surface: "#3b4252",
		Fg: "#d8dee9", Muted: "#616e88", Border: "#434c5e",
		Accent: "#88c0d0", Accent2: "#81a1c1",
		Success: "#a3be8c", Warning: "#ebcb8b", Danger: "#bf616a", Info: "#8fbcbb",
		ANSI: [16]string{"#3b4252", "#bf616a", "#a3be8c", "#ebcb8b", "#81a1c1", "#b48ead", "#88c0d0", "#e5e9f0",
			"#4c566a", "#bf616a", "#a3be8c", "#ebcb8b", "#81a1c1", "#b48ead", "#8fbcbb", "#eceff4"},
	},
	{
		Name: "gruvbox", Label: "Gruvbox Dark",
		Bg: "#282828", BgAlt: "#1d2021", Surface: "#3c3836",
		Fg: "#ebdbb2", Muted: "#928374", Border: "#504945",
		Accent: "#fabd2f", Accent2: "#d3869b",
		Success: "#b8bb26", Warning: "#fe8019", Danger: "#fb4934", Info: "#83a598",
		ANSI: [16]string{"#282828", "#cc241d", "#98971a", "#d79921", "#458588", "#b16286", "#689d6a", "#a89984",
			"#928374", "#fb4934", "#b8bb26", "#fabd2f", "#83a598", "#d3869b", "#8ec07c", "#ebdbb2"},
	},
	{
		Name: "solarized-dark", Label: "Solarized Dark",
		Bg: "#002b36", BgAlt: "#00212b", Surface: "#073642",
		Fg: "#93a1a1", Muted: "#586e75", Border: "#0b4553",
		Accent: "#268bd2", Accent2: "#6c71c4",
		Success: "#859900", Warning: "#b58900", Danger: "#dc322f", Info: "#2aa198",
		ANSI: [16]string{"#073642", "#dc322f", "#859900", "#b58900", "#268bd2", "#d33682", "#2aa198", "#eee8d5",
			"#002b36", "#cb4b16", "#586e75", "#657b83", "#839496", "#6c71c4", "#93a1a1", "#fdf6e3"},
	},
	{
		Name: "solarized-light", Label: "Solarized Light (claro)",
		Bg: "#fdf6e3", BgAlt: "#eee8d5", Surface: "#e4ddc8",
		Fg: "#586e75", Muted: "#93a1a1", Border: "#d3cbb7",
		Accent: "#268bd2", Accent2: "#6c71c4",
		Success: "#859900", Warning: "#b58900", Danger: "#dc322f", Info: "#2aa198",
		ANSI: [16]string{"#073642", "#dc322f", "#859900", "#b58900", "#268bd2", "#d33682", "#2aa198", "#eee8d5",
			"#002b36", "#cb4b16", "#586e75", "#657b83", "#839496", "#6c71c4", "#93a1a1", "#fdf6e3"},
	},
	{
		Name: "onedark", Label: "One Dark",
		Bg: "#282c34", BgAlt: "#21252b", Surface: "#2c313c",
		Fg: "#abb2bf", Muted: "#5c6370", Border: "#3e4451",
		Accent: "#61afef", Accent2: "#c678dd",
		Success: "#98c379", Warning: "#e5c07b", Danger: "#e06c75", Info: "#56b6c2",
		ANSI: [16]string{"#282c34", "#e06c75", "#98c379", "#e5c07b", "#61afef", "#c678dd", "#56b6c2", "#abb2bf",
			"#5c6370", "#e06c75", "#98c379", "#e5c07b", "#61afef", "#c678dd", "#56b6c2", "#ffffff"},
	},
	{
		Name: "rosepine", Label: "Rosé Pine",
		Bg: "#191724", BgAlt: "#13111e", Surface: "#26233a",
		Fg: "#e0def4", Muted: "#6e6a86", Border: "#403d52",
		Accent: "#c4a7e7", Accent2: "#ebbcba",
		Success: "#9ccfd8", Warning: "#f6c177", Danger: "#eb6f92", Info: "#31748f",
		ANSI: [16]string{"#26233a", "#eb6f92", "#31748f", "#f6c177", "#9ccfd8", "#c4a7e7", "#ebbcba", "#e0def4",
			"#6e6a86", "#eb6f92", "#31748f", "#f6c177", "#9ccfd8", "#c4a7e7", "#ebbcba", "#e0def4"},
	},
	{
		Name: "kanagawa", Label: "Kanagawa",
		Bg: "#1f1f28", BgAlt: "#16161d", Surface: "#2a2a37",
		Fg: "#dcd7ba", Muted: "#727169", Border: "#363646",
		Accent: "#7e9cd8", Accent2: "#957fb8",
		Success: "#98bb6c", Warning: "#e6c384", Danger: "#e82424", Info: "#7fb4ca",
		ANSI: [16]string{"#16161d", "#c34043", "#76946a", "#c0a36e", "#7e9cd8", "#957fb8", "#6a9589", "#c8c093",
			"#727169", "#e82424", "#98bb6c", "#e6c384", "#7fb4ca", "#938aa9", "#7aa89f", "#dcd7ba"},
	},
	{
		Name: "monokai", Label: "Monokai",
		Bg: "#272822", BgAlt: "#1e1f1c", Surface: "#3e3d32",
		Fg: "#f8f8f2", Muted: "#75715e", Border: "#49483e",
		Accent: "#a6e22e", Accent2: "#ae81ff",
		Success: "#a6e22e", Warning: "#e6db74", Danger: "#f92672", Info: "#66d9ef",
		ANSI: [16]string{"#272822", "#f92672", "#a6e22e", "#f4bf75", "#66d9ef", "#ae81ff", "#a1efe4", "#f8f8f2",
			"#75715e", "#f92672", "#a6e22e", "#f4bf75", "#66d9ef", "#ae81ff", "#a1efe4", "#f9f8f5"},
	},
	{
		Name: "github-light", Label: "GitHub Light (claro)",
		Bg: "#ffffff", BgAlt: "#f6f8fa", Surface: "#eaeef2",
		Fg: "#24292f", Muted: "#6e7781", Border: "#d0d7de",
		Accent: "#0969da", Accent2: "#8250df",
		Success: "#1a7f37", Warning: "#9a6700", Danger: "#cf222e", Info: "#0550ae",
		ANSI: [16]string{"#24292f", "#cf222e", "#116329", "#4d2d00", "#0969da", "#8250df", "#1b7c83", "#6e7781",
			"#57606a", "#a40e26", "#1a7f37", "#633c01", "#218bff", "#a475f9", "#3192aa", "#8c959f"},
	},
}

// All returns every theme in display order.
func All() []Theme { return append([]Theme(nil), themes...) }

// Names returns the theme slugs, sorted.
func Names() []string {
	out := make([]string, len(themes))
	for i, t := range themes {
		out[i] = t.Name
	}
	sort.Strings(out)
	return out
}

// Get finds a theme by slug or label, ignoring case, spaces and dashes.
func Get(name string) (Theme, bool) {
	k := norm(name)
	for _, t := range themes {
		if norm(t.Name) == k || norm(t.Label) == k {
			return t, true
		}
	}
	return Theme{}, false
}

// Or returns the named theme or the default one.
func Or(name string) Theme {
	if t, ok := Get(name); ok {
		return t
	}
	t, _ := Get(Default)
	return t
}

// Index is the position of the named theme in All(), or 0.
func Index(name string) int {
	for i, t := range themes {
		if t.Name == Or(name).Name {
			return i
		}
	}
	return 0
}

func norm(s string) string {
	return strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.ToLower(s))
}
