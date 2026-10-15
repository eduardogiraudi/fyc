// basilar latex renderer so you don't see \rightarrow when the agent explain something
package ui

import (
	"regexp"
	"sort"
	"strings"
)

var (
	reInlineLatex = regexp.MustCompile(`\\\((.+?)\\\)|\$([^$\n]+?)\$`)

	reDisplayLatex = regexp.MustCompile(`(?s)\\\[(.+?)\\\]|\$\$(.+?)\$\$`)

	reSupBrace = regexp.MustCompile(`\^\{([^{}]*)\}`)
	reSubBrace = regexp.MustCompile(`_\{([^{}]*)\}`)
	reSup      = regexp.MustCompile(`\^([^\s{])`)
	reSub      = regexp.MustCompile(`_([^\s{])`)

	reCmd = regexp.MustCompile(`\\([a-zA-Z]+)`)

	reMathbb = regexp.MustCompile(`\\mathbb\{([A-Za-z])\}`)

	reEnv = regexp.MustCompile(`(?s)\\begin\{[^}]+\}(.*?)\\end\{[^}]+\}`)

	reNewline = regexp.MustCompile(`\\\\`)
)

var latexSymbolsSorted []string

func init() {
	latexSymbolsSorted = make([]string, 0, len(latexSymbols))
	for k := range latexSymbols {
		latexSymbolsSorted = append(latexSymbolsSorted, k)
	}

	sort.Slice(latexSymbolsSorted, func(i, j int) bool {
		return len(latexSymbolsSorted[i]) > len(latexSymbolsSorted[j])
	})
}

func preprocessLatex(s string) string {
	s = reDisplayLatex.ReplaceAllStringFunc(s, func(m string) string {
		inner := extractLatexInner(m, reDisplayLatex)
		rendered := renderLatexExpr(inner)
		return "\n\n  " + rendered + "\n\n"
	})
	s = reInlineLatex.ReplaceAllStringFunc(s, func(m string) string {
		inner := extractLatexInner(m, reInlineLatex)
		return renderLatexExpr(inner)
	})
	return s
}

func extractLatexInner(m string, re *regexp.Regexp) string {
	groups := re.FindStringSubmatch(m)
	for _, g := range groups[1:] {
		if g != "" {
			return strings.TrimSpace(g)
		}
	}
	return m
}

func renderLatexExpr(expr string) string {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return expr
	}

	for _, k := range latexSymbolsSorted {
		expr = strings.ReplaceAll(expr, k, latexSymbols[k])
	}

	expr = reEnv.ReplaceAllStringFunc(expr, func(m string) string {
		groups := reEnv.FindStringSubmatch(m)
		if len(groups) < 2 {
			return m
		}
		inner := strings.TrimSpace(groups[1])
		inner = reNewline.ReplaceAllString(inner, "\n  ")
		inner = strings.ReplaceAll(inner, "&", "  ")
		return inner
	})

	expr = expandMathVariant(expr)

	expr = expandText(expr)

	expr = expandOverset(expr)
	expr = expandUnderset(expr)

	expr = expandFrac(expr)

	expr = expandSqrt(expr)

	expr = reSupBrace.ReplaceAllStringFunc(expr, func(m string) string {
		inner := m[2 : len(m)-1]
		return toSuperscript(inner)
	})
	expr = reSubBrace.ReplaceAllStringFunc(expr, func(m string) string {
		inner := m[2 : len(m)-1]
		return toSubscript(inner)
	})

	expr = reSup.ReplaceAllStringFunc(expr, func(m string) string {
		return toSuperscript(string([]rune(m)[1:]))
	})
	expr = reSub.ReplaceAllStringFunc(expr, func(m string) string {
		return toSubscript(string([]rune(m)[1:]))
	})

	expr = reMathbb.ReplaceAllStringFunc(expr, func(m string) string {
		groups := reMathbb.FindStringSubmatch(m)
		if len(groups) < 2 {
			return m
		}
		return mathbbChar(groups[1])
	})

	expr = reCmd.ReplaceAllStringFunc(expr, func(m string) string {
		name := m[1:]

		if v, ok := latexSymbols[m]; ok {
			return v
		}
		return name
	})
	expr = strings.ReplaceAll(expr, "{", "")
	expr = strings.ReplaceAll(expr, "}", "")

	return expr
}

func expandFrac(s string) string {
	for {
		idx := strings.Index(s, `\frac{`)
		if idx == -1 {
			break
		}
		start := idx + len(`\frac`)
		num, afterNum := extractBraces(s, start)
		if afterNum == -1 {
			break
		}
		den, afterDen := extractBraces(s, afterNum)
		if afterDen == -1 {
			break
		}
		replacement := "(" + renderLatexExpr(num) + ")/(" + renderLatexExpr(den) + ")"
		s = s[:idx] + replacement + s[afterDen:]
	}
	return s
}

func expandSqrt(s string) string {
	for {
		idx := strings.Index(s, `\sqrt{`)
		if idx == -1 {

			break
		}
		start := idx + len(`\sqrt`)
		inner, after := extractBraces(s, start)
		if after == -1 {
			break
		}
		replacement := "√(" + renderLatexExpr(inner) + ")"
		s = s[:idx] + replacement + s[after:]
	}

	s = strings.ReplaceAll(s, `\sqrt`, "√")
	return s
}

var mathVariants = []string{
	`\mathbf`, `\mathit`, `\mathrm`, `\mathsf`, `\mathtt`, `\mathcal`,
	`\mathfrak`, `\boldsymbol`, `\bm`, `\vec`, `\hat`, `\tilde`,
	`\bar`, `\dot`, `\ddot`, `\overline`, `\underline`,
}

func expandMathVariant(s string) string {
	for _, cmd := range mathVariants {
		for {
			idx := strings.Index(s, cmd+"{")
			if idx == -1 {
				break
			}
			start := idx + len(cmd)
			inner, after := extractBraces(s, start)
			if after == -1 {
				break
			}

			var prefix, suffix string
			switch cmd {
			case `\vec`:
				suffix = "⃗"
			case `\hat`:
				suffix = "̂"
			case `\tilde`:
				suffix = "̃"
			case `\bar`, `\overline`:
				suffix = "̄"
			case `\dot`:
				suffix = "̇"
			case `\ddot`:
				suffix = "̈"
			}
			_ = prefix
			s = s[:idx] + renderLatexExpr(inner) + suffix + s[after:]
		}
	}
	return s
}

func expandText(s string) string {
	for {
		idx := strings.Index(s, `\text{`)
		if idx == -1 {
			break
		}
		start := idx + len(`\text`)
		inner, after := extractBraces(s, start)
		if after == -1 {
			break
		}

		s = s[:idx] + strings.TrimSpace(inner) + s[after:]
	}
	return s
}

func expandOverset(s string) string {
	for {
		idx := strings.Index(s, `\overset{`)
		if idx == -1 {
			break
		}
		start := idx + len(`\overset`)
		top, afterTop := extractBraces(s, start)
		if afterTop == -1 {
			break
		}
		base, afterBase := extractBraces(s, afterTop)
		if afterBase == -1 {
			break
		}
		renderedTop := renderLatexExpr(top)
		renderedBase := renderLatexExpr(base)
		sup := toSuperscript(renderedTop)

		s = s[:idx] + renderedBase + sup + s[afterBase:]
	}
	return s
}

func expandUnderset(s string) string {
	for {
		idx := strings.Index(s, `\underset{`)
		if idx == -1 {
			break
		}
		start := idx + len(`\underset`)
		bot, afterBot := extractBraces(s, start)
		if afterBot == -1 {
			break
		}
		base, afterBase := extractBraces(s, afterBot)
		if afterBase == -1 {
			break
		}
		renderedBot := renderLatexExpr(bot)
		renderedBase := renderLatexExpr(base)
		sub := toSubscript(renderedBot)
		s = s[:idx] + renderedBase + sub + s[afterBase:]
	}
	return s
}

func extractBraces(s string, pos int) (string, int) {
	if pos >= len(s) || s[pos] != '{' {
		return "", -1
	}
	depth := 0
	for i := pos; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[pos+1 : i], i + 1
			}
		}
	}
	return "", -1
}

var latexSymbols = map[string]string{

	`\alpha`: "α", `\beta`: "β", `\gamma`: "γ", `\delta`: "δ",
	`\epsilon`: "ε", `\varepsilon`: "ε", `\zeta`: "ζ", `\eta`: "η",
	`\theta`: "θ", `\vartheta`: "θ", `\iota`: "ι", `\kappa`: "κ",
	`\lambda`: "λ", `\mu`: "μ", `\nu`: "ν", `\xi`: "ξ",
	`\pi`: "π", `\varpi`: "π", `\rho`: "ρ", `\varrho`: "ρ",
	`\sigma`: "σ", `\varsigma`: "ς", `\tau`: "τ", `\upsilon`: "υ",
	`\phi`: "φ", `\varphi`: "φ", `\chi`: "χ", `\psi`: "ψ", `\omega`: "ω",

	`\Gamma`: "Γ", `\Delta`: "Δ", `\Theta`: "Θ", `\Lambda`: "Λ",
	`\Xi`: "Ξ", `\Pi`: "Π", `\Sigma`: "Σ", `\Phi`: "Φ",
	`\Psi`: "Ψ", `\Omega`: "Ω",

	`\times`: "×", `\div`: "÷", `\pm`: "±", `\mp`: "∓",
	`\cdot`: "·", `\circ`: "∘", `\oplus`: "⊕", `\otimes`: "⊗",

	`\leq`: "≤", `\le`: "≤", `\geq`: "≥", `\ge`: "≥",
	`\neq`: "≠", `\ne`: "≠", `\approx`: "≈", `\equiv`: "≡",
	`\sim`: "∼", `\simeq`: "≃", `\cong`: "≅", `\propto`: "∝",
	`\ll`: "≪", `\gg`: "≫",

	`\subset`: "⊂", `\supset`: "⊃", `\subseteq`: "⊆", `\supseteq`: "⊇",
	`\in`: "∈", `\notin`: "∉", `\ni`: "∋",
	`\cup`: "∪", `\cap`: "∩", `\setminus`: "∖", `\emptyset`: "∅", `\varnothing`: "∅",

	`\to`: "→", `\gets`: "←",
	`\leftarrow`: "←", `\rightarrow`: "→", `\leftrightarrow`: "↔",
	`\Leftarrow`: "⇐", `\Rightarrow`: "⇒", `\Leftrightarrow`: "⇔",
	`\mapsto`: "↦", `\uparrow`: "↑", `\downarrow`: "↓",
	`\nearrow`: "↗", `\searrow`: "↘", `\nwarrow`: "↖", `\swarrow`: "↙",

	`\forall`: "∀", `\exists`: "∃", `\nexists`: "∄", `\neg`: "¬",
	`\land`: "∧", `\lor`: "∨", `\top`: "⊤", `\bot`: "⊥",
	`\vdash`: "⊢", `\models`: "⊨",

	`\infty`: "∞", `\nabla`: "∇", `\partial`: "∂",
	`\int`: "∫", `\iint`: "∬", `\iiint`: "∭", `\oint`: "∮",
	`\sum`: "∑", `\prod`: "∏", `\coprod`: "∐",

	`\lim`: "lim", `\log`: "log", `\ln`: "ln", `\exp`: "exp",
	`\sin`: "sin", `\cos`: "cos", `\tan`: "tan",
	`\arcsin`: "arcsin", `\arccos`: "arccos", `\arctan`: "arctan",
	`\sinh`: "sinh", `\cosh`: "cosh", `\tanh`: "tanh",
	`\max`: "max", `\min`: "min", `\sup`: "sup", `\inf`: "inf",
	`\det`: "det", `\dim`: "dim", `\ker`: "ker", `\gcd`: "gcd",

	`\hbar`: "ℏ", `\ell`: "ℓ", `\Re`: "ℜ", `\Im`: "ℑ",
	`\aleph`: "ℵ", `\wp`: "℘",
	`\ldots`: "…", `\cdots`: "⋯", `\dots`: "…", `\vdots`: "⋮", `\ddots`: "⋱",
	`\quad`: " ", `\qquad`: "  ", `\,`: "\u2009", `\;`: " ", `\:`: " ",
	`\|`: "‖", `\langle`: "⟨", `\rangle`: "⟩",
	`\lfloor`: "⌊", `\rfloor`: "⌋", `\lceil`: "⌈", `\rceil`: "⌉",
	`\left(`: "(", `\right)`: ")", `\left[`: "[", `\right]`: "]",
	`\left\{`: "{", `\right\}`: "}",
	`\left|`: "|", `\right|`: "|",
	`\because`: "∵", `\therefore`: "∴",
	`\angle`: "∠", `\triangle`: "△",
	`\perp`: "⊥", `\parallel`: "∥",
	`\prime`: "′", `\''`: "″",
}

var superscriptMap = map[rune]rune{
	'0': '⁰', '1': '¹', '2': '²', '3': '³', '4': '⁴',
	'5': '⁵', '6': '⁶', '7': '⁷', '8': '⁸', '9': '⁹',
	'a': 'ᵃ', 'b': 'ᵇ', 'c': 'ᶜ', 'd': 'ᵈ', 'e': 'ᵉ',
	'f': 'ᶠ', 'g': 'ᵍ', 'h': 'ʰ', 'i': 'ⁱ', 'j': 'ʲ',
	'k': 'ᵏ', 'l': 'ˡ', 'm': 'ᵐ', 'n': 'ⁿ', 'o': 'ᵒ',
	'p': 'ᵖ', 'r': 'ʳ', 's': 'ˢ', 't': 'ᵗ', 'u': 'ᵘ',
	'v': 'ᵛ', 'w': 'ʷ', 'x': 'ˣ', 'y': 'ʸ', 'z': 'ᶻ',
	'+': '⁺', '-': '⁻', '=': '⁼', '(': '⁽', ')': '⁾',
	'A': 'ᴬ', 'B': 'ᴮ', 'D': 'ᴰ', 'E': 'ᴱ', 'G': 'ᴳ',
	'H': 'ᴴ', 'I': 'ᴵ', 'J': 'ᴶ', 'K': 'ᴷ', 'L': 'ᴸ',
	'M': 'ᴹ', 'N': 'ᴺ', 'O': 'ᴼ', 'P': 'ᴾ', 'R': 'ᴿ',
	'T': 'ᵀ', 'U': 'ᵁ', 'V': 'ⱽ', 'W': 'ᵂ',
}

var subscriptMap = map[rune]rune{
	'0': '₀', '1': '₁', '2': '₂', '3': '₃', '4': '₄',
	'5': '₅', '6': '₆', '7': '₇', '8': '₈', '9': '₉',
	'a': 'ₐ', 'e': 'ₑ', 'i': 'ᵢ', 'o': 'ₒ', 'u': 'ᵤ',
	'n': 'ₙ', 'k': 'ₖ', 'j': 'ⱼ', 'r': 'ᵣ', 's': 'ₛ',
	'x': 'ₓ', '+': '₊', '-': '₋', '=': '₌', '(': '₍', ')': '₎',
	't': 'ₜ', 'v': 'ᵥ', 'β': 'ᵦ', 'γ': 'ᵧ', 'ρ': 'ᵨ',
}

func toSuperscript(s string) string {
	var b strings.Builder
	for _, r := range s {
		if mapped, ok := superscriptMap[r]; ok {
			b.WriteRune(mapped)
		} else {
			b.WriteString("^(")
			b.WriteRune(r)
			b.WriteString(")")
		}
	}
	return b.String()
}

func toSubscript(s string) string {
	var b strings.Builder
	for _, r := range s {
		if mapped, ok := subscriptMap[r]; ok {
			b.WriteRune(mapped)
		} else {
			b.WriteString("_(")
			b.WriteRune(r)
			b.WriteString(")")
		}
	}
	return b.String()
}
func mathbbChar(letter string) string {
	switch letter {
	case "R":
		return "ℝ"
	case "C":
		return "ℂ"
	case "Z":
		return "ℤ"
	case "N":
		return "ℕ"
	case "Q":
		return "ℚ"
	case "P":
		return "ℙ"
	case "H":
		return "ℍ"
	case "F":
		return "𝔽"
	case "k", "K":
		return "𝕜"
	default:
		return `\mathbb{` + letter + "}"
	}
}
