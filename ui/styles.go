package ui

import (
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"strings"
)

var (
	TitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#fc8403"))
	InfoStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	OkStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#fc8403"))
	//BarStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("111"))
	TipStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Italic(true)
	SeparatorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#fc8403"))
	ToolStyle      = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#fc8403")).
			BorderLeft(true).
			BorderForeground(lipgloss.Color("111"))
		//PaddingLeft(1)
	AddStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#fc8403"))            // Soft green
	DeleteStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))                // Soft red
	FileStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("229"))                // Yellow/Gold for paths
	KeyStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))                // Yellow for stats keys
	ValueStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#fc8403")) // Light yellow bold for values

	// ── Config / Selector styles ────────────────────────────────────────────
	SelectorBorder = lipgloss.NewStyle()
	//Border(lipgloss.RoundedBorder()).
	//BorderForeground(lipgloss.Color("#fc8403")).
	//Padding(0, 1)
	SelectorHeader = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#fc8403"))
	SelectedBg = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#fc8403")).
			Background(lipgloss.Color("#2d2d2d"))
	RadioOn   = lipgloss.NewStyle().Foreground(lipgloss.Color("#fc8403")).SetString("◉")
	RadioOff  = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).SetString("○")
	DimStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("239"))
	GrayStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	// HookStyle renders the custom status-bar hook line.
	HookStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#fc8403"))
	BlueStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#89b4fa"))
	HintStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("237")).Italic(true)
	SuccessBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("42")).
			Foreground(lipgloss.Color("42")).
			Padding(0, 1)
	InfoBox = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("250")).
		Foreground(lipgloss.Color("250")).
		Padding(0, 1)
	WarnBox = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("220")).
		Foreground(lipgloss.Color("220")).
		Padding(0, 1)
)

func Banner(compact bool) string {
	lines := []string{
		"█▀▀ █ █ █▀▀ █ █ █ █ █▀▀█ █ █ █▀▀█ █▀▀ █   █",
		"█▀▀ █ █ █   █▀▄ ▀▄▀ █  █ █ █ █▄▄▀ █   █   █",
		"▀   ▀▀▀ ▀▀▀ ▀ ▀  ▀  ▀▀▀▀ ▀▀▀ ▀ ▀▀ ▀▀▀ ▀▀▀ ▀",
	}
	if compact {
		lines = []string{
			"█▀▀ █ █ █▀▀",
			"█▀▀ ▀▄▀ █  ",
			"▀    ▀  ▀▀▀",
		}
	}

	colors := []string{"#fc8403"}

	var rendered []string
	for i, line := range lines {
		c := colors[i%len(colors)]
		rendered = append(rendered,
			lipgloss.NewStyle().
				Foreground(lipgloss.Color(c)).
				Render(line),
		)
	}

	content := strings.Join(rendered, "\n")

	border := lipgloss.NewStyle().
		Padding(1, 1).
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#fc8403"))
		//BorderForeground(lipgloss.Color("#BD93F9"))

	return border.Render(content)
}

func formatStartupHeader(compact bool, info []string, width int) string {
	banner := TitleStyle.Render(Banner(compact))
	separator := SeparatorStyle.Render(strings.Repeat("─", width))
	details := strings.Join(info, "\n")
	if !compact {
		return banner + "\n" + separator + "\n" + details
	}
	const gap = 2
	infoWidth := width - lipgloss.Width(banner) - gap
	if infoWidth < 20 {
		return ClampBlock(banner+"\n"+details+"\n"+separator, width)
	}
	details = lipgloss.NewStyle().Width(infoWidth).Render(details)
	header := lipgloss.JoinHorizontal(lipgloss.Top, banner, strings.Repeat(" ", gap), details)
	return ClampBlock(header, width) + "\n" + separator
}

var MdRenderer *glamour.TermRenderer

func InitMarkdownRenderer() error {
	compactJSON := []byte(`{
    "document": {
        "margin": 0,
        "padding": 0
    },
    "paragraph": {
        "margin_above": 0,
        "margin_below": 0,
        "padding": 0
    },
    "heading": {
        "margin_above": 1,
        "margin_below": 0,
        "padding": 0,
        "block_prefix": ""
    },
    "list": {
        "margin_above": 0,
        "margin_below": 0,
        "padding": 0,
        "block_prefix": ""
    },
    "code_block": {
        "margin_above": 0,
        "margin_below": 0,
        "padding": 0
    },
    "table": {
        "margin_above": 0,
        "margin_below": 0,
        "padding": 0
    },
    "blockquote": {
        "margin_above": 0,
        "margin_below": 0,
        "padding": 0
    },
    "item": {
        "margin_above": 0,
        "margin_below": 0,
        "padding": 0
    }
}`)
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithStylesFromJSONBytes(compactJSON),
		glamour.WithWordWrap(0),
	)
	if err != nil {
		return err
	}
	MdRenderer = r
	return nil
}
