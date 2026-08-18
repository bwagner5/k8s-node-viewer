package ui

import (
	"strings"

	"github.com/sahilm/fuzzy"

	"github.com/oxidecomputer/k8s-node-viewer/internal/theme"
)

// maxSuggestions is how many completion rows the popup shows at once.
const maxSuggestions = 8

// cmdBar is the k9s-style ":" line plus its completion popup.
//
// The interaction contract, chosen so muscle memory from k9s carries over:
//   - ":" opens it; Esc closes it.
//   - Typing filters completions fuzzily but never rewrites what you typed.
//   - Tab (or Up/Down then Enter) accepts a completion.
//   - Enter on a command that needs an argument does not error — it opens the
//     picker for that argument. ":nodepool<Enter>" is the fast path to
//     choosing a nodepool, which is exactly what this was asked to do.
type cmdBar struct {
	active bool
	input  string
	// hl is the highlighted completion; hlActive means the user has moved into
	// the list, so Enter should accept the highlight rather than the raw line.
	hl       int
	hlActive bool
	items    []string
	// picking means we are completing an argument for cmd, not a command name.
	picking bool
	cmd     *command
}

func (b *cmdBar) open(prefill string) {
	b.active, b.input = true, prefill
	b.picking, b.cmd = false, nil
	b.resetHighlight()
}

func (b *cmdBar) close() {
	b.active, b.input, b.items = false, "", nil
	b.picking, b.cmd = false, nil
	b.resetHighlight()
}

func (b *cmdBar) resetHighlight() { b.hl, b.hlActive = 0, false }

// pick puts the bar into argument-completion mode for a command.
func (b *cmdBar) pick(cmd *command) {
	b.active, b.picking, b.cmd = true, true, cmd
	b.input = cmd.name + " "
	b.resetHighlight()
}

// word returns the token being completed and the prefix before it.
func (b *cmdBar) word() (prefix, token string) {
	if i := strings.IndexByte(b.input, ' '); i >= 0 {
		return b.input[:i+1], strings.TrimLeft(b.input[i+1:], " ")
	}
	return "", b.input
}

// refresh recomputes the completion list from the current input. Called after
// every keystroke; ranking is fuzzy so ":np spt" finds "spot-batch".
func (b *cmdBar) refresh(m *Model) {
	prefix, token := b.word()
	var pool []string
	if prefix == "" {
		pool = commandNames(m.demo != nil)
		b.cmd = nil
	} else {
		name := strings.TrimSpace(prefix)
		cmd, ok := lookup(strings.ToLower(name))
		if !ok {
			b.items = nil
			return
		}
		b.cmd = cmd
		pool = m.candidates(cmd)
	}
	b.items = rank(pool, token)
	if b.hl >= len(b.items) {
		b.resetHighlight()
	}
}

// rank filters and orders candidates against a partial token.
func rank(pool []string, token string) []string {
	if token == "" {
		return pool
	}
	matches := fuzzy.Find(token, pool)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m.Str)
	}
	return out
}

// accept substitutes the highlighted completion into the input. It returns true
// if the input is now a complete command line ready to run.
func (b *cmdBar) accept() bool {
	if len(b.items) == 0 {
		return false
	}
	choice := b.items[clampInt(b.hl, 0, len(b.items)-1)]
	prefix, _ := b.word()
	if prefix == "" {
		// Completed a command name. If it takes an argument, keep the bar open
		// with a trailing space so completions for the argument appear.
		cmd, ok := lookup(choice)
		b.input = choice
		if ok && cmd.arg != argNone {
			b.input += " "
			b.resetHighlight()
			return false
		}
		return true
	}
	b.input = prefix + choice
	return true
}

func (b *cmdBar) move(delta int) {
	if len(b.items) == 0 {
		return
	}
	b.hlActive = true
	b.hl = (b.hl + delta + len(b.items)) % len(b.items)
}

func (b *cmdBar) insert(s string) {
	b.input += s
	b.resetHighlight()
}

func (b *cmdBar) backspace() {
	if b.input == "" {
		return
	}
	r := []rune(b.input)
	b.input = string(r[:len(r)-1])
	b.resetHighlight()
}

// height is how many rows the bar would like. The caller may grant fewer on a
// short terminal, so render takes the granted height rather than reading this.
func (b *cmdBar) height() int {
	if !b.active {
		return 0
	}
	return 1 + min(len(b.items), maxSuggestions)
}

// render returns exactly `total` rows: completion popup rows followed by the
// prompt row, which is always the last one and never dropped.
func (b *cmdBar) render(w, total int) []string {
	t := theme.Current
	rows := clampInt(min(len(b.items), maxSuggestions), 0, max(0, total-1))
	c := newCanvas(w, rows+1, t.Panel, t.PanelFg)

	// Scroll the window so the highlight stays visible in a long list.
	start := 0
	if b.hl >= rows {
		start = b.hl - rows + 1
	}
	for i := 0; i < rows; i++ {
		idx := start + i
		if idx >= len(b.items) {
			break
		}
		fg, bg := t.PanelFg, t.Panel
		if idx == b.hl && b.hlActive {
			fg, bg = contrastOn(t.Accent), t.Accent
		} else if idx == b.hl {
			bg = theme.Mix(t.Panel, t.Accent, 0.22)
		}
		c.rect(0, i, w, 1, bg)
		c.text(2, i, shorten(b.items[idx], w-12), fg, idx == b.hl)
		if idx == b.hl {
			c.text(0, i, "▶", t.Accent, true)
		}
	}
	if rest := len(b.items) - (start + rows); rest > 0 && rows > 0 {
		c.textRight(0, rows-1, w-1, "+"+itoa(rest)+" more", t.Dim, false)
	}

	// Prompt row.
	y := rows
	c.rect(0, y, w, 1, t.Accent)
	fg := contrastOn(t.Accent)
	hint := ""
	if b.cmd != nil && b.cmd.argHint != "" && !strings.Contains(strings.TrimSpace(b.input), " ") {
		hint = "  " + b.cmd.argHint
	}
	c.text(0, y, " :"+b.input+"█"+hint, fg, true)
	if b.cmd != nil {
		c.textRight(0, y, w-1, b.cmd.help+" ", theme.Mix(fg, t.Accent, 0.35), false)
	} else {
		c.textRight(0, y, w-1, "tab complete · enter run · esc cancel ", theme.Mix(fg, t.Accent, 0.35), false)
	}
	return c.Lines()
}

// itoa avoids pulling strconv into the render path for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
