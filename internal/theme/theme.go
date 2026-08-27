// Package theme centralises every colour and glyph choice.
//
// The target is a projector in a lit room: saturated hues, wide luminance gaps
// between adjacent states, and no reliance on hue alone to convey meaning
// (phases also differ in border glyph and label text). Swapping Current is the
// only supported way to restyle the app.
package theme

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/lucasb-eyer/go-colorful"
)

// Theme is a complete palette. Utilisation is a ramp rather than discrete
// buckets so a filling node reads as a continuous motion, not a stair-step.
type Theme struct {
	Name string

	Bg      lipgloss.Color
	Fg      lipgloss.Color
	Dim     lipgloss.Color
	Accent  lipgloss.Color
	Panel   lipgloss.Color
	PanelFg lipgloss.Color

	// Card is a node box's background and Well is the inset area inside it that
	// represents unallocated capacity. Giving a node a body colour distinct from
	// the page is what makes an idle node still read as a card rather than as
	// empty screen with a hairline around it.
	Card lipgloss.Color
	Well lipgloss.Color
	// Title is the node card's header strip.
	Title lipgloss.Color

	// UtilRamp is sampled by fraction, and it runs *toward* green: a packed node
	// is a node earning its cost, so saturated is the good end and idle is the
	// unremarkable one. It deliberately does not reach red at the top — red on
	// this screen means something is wrong (a failing pod, an unschedulable
	// backlog), and a well-utilised cluster is the opposite of that.
	//
	// It stays a continuous ramp rather than discrete buckets so a filling node
	// reads as motion, and its luminance climbs monotonically so the fill level
	// is still legible where hue is not.
	UtilRamp []string
	// Empty is the unfilled part of a meter.
	Empty lipgloss.Color

	// Phase colours, indexed by model.Phase.
	Phase []lipgloss.Color

	// Pod cells are coloured by state and nothing else. One dominant hue for
	// running (which is almost every cell, so the node interior reads as a
	// single packed mass) plus three accents that only appear when something is
	// happening. None of them sit near a Phase colour.
	PodRunning     lipgloss.Color
	PodPending     lipgloss.Color
	PodTerminating lipgloss.Color
	PodFailed      lipgloss.Color
	PodDim         lipgloss.Color

	Ok       lipgloss.Color
	Warn     lipgloss.Color
	Err      lipgloss.Color
	Flash    lipgloss.Color
	Selected lipgloss.Color
}

// Dark is the default: near-black background so saturated fills carry the
// image, which is what survives a washed-out projector.
var Dark = Theme{
	Name:    "dark",
	Bg:      "#0b0d12",
	Fg:      "#e6edf3",
	Dim:     "#7d8590",
	Accent:  "#58c8ff",
	Panel:   "#161b26",
	PanelFg: "#e6edf3",

	Card:  "#161b26",
	Well:  "#0e1119",
	Title: "#222a3a",

	// Slate → blue → cyan → green → mint: idle is quiet, full is bright green.
	UtilRamp: []string{"#4a5468", "#1f6feb", "#22b8cf", "#22c55e", "#4ade80"},
	Empty:    "#20242e",

	// Phase colours are saturated: they only ever appear as thin borders, where
	// a pastel would disappear.
	Phase: []lipgloss.Color{
		"#a371f7", // Provisioning — violet, distinct from every steady state
		"#ff9d2e", // NotReady
		"#2ea043", // Ready
		"#58a6ff", // Cordoned
		"#ffd33d", // Draining
		"#ff5c5c", // Terminating
		"#6e7681", // Gone
	},

	PodRunning:     "#5eead4", // teal: the resting colour of a healthy cluster
	PodPending:     "#8b93a7", // neutral grey: scheduled but not yet doing anything
	PodTerminating: "#f472b6", // pink: unmistakably not a node phase colour
	PodFailed:      "#f87171",
	PodDim:         "#39414f",

	Ok:       "#2ea043",
	Warn:     "#ffd33d",
	Err:      "#ff5c5c",
	Flash:    "#ffffff",
	Selected: "#ffffff",
}

// Light is for a projector that cannot be dimmed, where a dark background
// turns into a grey smear.
var Light = Theme{
	Name:    "light",
	Bg:      "#ffffff",
	Fg:      "#11161d",
	Dim:     "#5b6470",
	Accent:  "#0969da",
	Panel:   "#eef1f5",
	PanelFg: "#11161d",

	Card:  "#f4f6fa",
	Well:  "#e4e8ef",
	Title: "#dde3ec",

	// The same walk, inverted in luminance: on white it is the *dark* end that
	// carries, so the ramp starts as a pale grey and deepens into green. A full
	// meter has to be the most prominent thing on the card either way.
	UtilRamp: []string{"#c2cad4", "#79b0e8", "#2f9bb8", "#1f8a4d", "#136c36"},
	Empty:    "#dfe3e9",

	Phase: []lipgloss.Color{
		"#8250df", "#e16f24", "#1a7f37", "#0969da", "#bf8700", "#cf222e", "#8c959f",
	},

	// Mid-tone rather than pastel: on white, pastels wash out entirely.
	PodRunning:     "#2aa198",
	PodPending:     "#98a2b3",
	PodTerminating: "#bf3989",
	PodFailed:      "#cf222e",
	PodDim:         "#c7cdd6",

	Ok:       "#1a7f37",
	Warn:     "#bf8700",
	Err:      "#cf222e",
	Flash:    "#11161d",
	Selected: "#11161d",
}

// Current is the active theme. Set via Use before the first render.
var Current = Dark

// Use switches the active theme by name and reports whether it was known.
func Use(name string) bool {
	switch name {
	case "dark":
		Current = Dark
	case "light":
		Current = Light
	default:
		return false
	}
	return true
}

// Names lists selectable themes.
func Names() []string { return []string{"dark", "light"} }

// Util samples the utilisation ramp at f in [0,1].
func (t Theme) Util(f float64) lipgloss.Color { return sample(t.UtilRamp, f) }

// PhaseColor is bounds-safe so an unknown phase cannot panic mid-render.
func (t Theme) PhaseColor(phase int) lipgloss.Color {
	if phase < 0 || phase >= len(t.Phase) {
		return t.Dim
	}
	return t.Phase[phase]
}

// sample linearly interpolates a hex ramp in Lab space, which keeps the
// midpoints from going muddy the way naive RGB blending does.
func sample(ramp []string, f float64) lipgloss.Color {
	if len(ramp) == 0 {
		return lipgloss.Color("#ffffff")
	}
	if f <= 0 {
		return lipgloss.Color(ramp[0])
	}
	if f >= 1 {
		return lipgloss.Color(ramp[len(ramp)-1])
	}
	pos := f * float64(len(ramp)-1)
	i := int(pos)
	a, errA := colorful.Hex(ramp[i])
	b, errB := colorful.Hex(ramp[i+1])
	if errA != nil || errB != nil {
		return lipgloss.Color(ramp[i])
	}
	return lipgloss.Color(a.BlendLab(b, pos-float64(i)).Clamped().Hex())
}

// Mix blends two colours; used for flashes and pulses. Non-hex inputs fall
// back to a, so callers never have to error-check a colour.
func Mix(a, b lipgloss.Color, f float64) lipgloss.Color {
	ca, errA := colorful.Hex(string(a))
	cb, errB := colorful.Hex(string(b))
	if errA != nil || errB != nil {
		return a
	}
	return lipgloss.Color(ca.BlendLab(cb, clamp01(f)).Clamped().Hex())
}

// Dimmed darkens a colour toward the background by f, for fade-out animations.
func (t Theme) Dimmed(c lipgloss.Color, f float64) lipgloss.Color { return Mix(c, t.Bg, f) }

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}
