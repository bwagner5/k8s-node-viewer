package theme

import (
	"testing"

	"github.com/lucasb-eyer/go-colorful"
)

// TestUtilRampRunsTowardGreen pins the meaning of the utilisation colour, which
// is a product decision and not a palette detail: full is good here, so the ramp
// ends green and never passes through the reds and ambers that mean trouble
// everywhere else on screen.
func TestUtilRampRunsTowardGreen(t *testing.T) {
	for _, th := range []Theme{Dark, Light} {
		t.Run(th.Name, func(t *testing.T) {
			full, err := colorful.Hex(string(th.Util(1)))
			if err != nil {
				t.Fatalf("full-scale colour is not a hex colour: %v", err)
			}
			if h, s, _ := full.Hsl(); h < 90 || h > 165 || s < 0.3 {
				t.Errorf("100%% utilisation is hue %.0f sat %.2f, want a saturated green (90–165)", h, s)
			}

			// Nothing on the ramp may read as an alarm: red and amber are reserved
			// for failure and for the unschedulable backlog.
			for i := 0; i <= 20; i++ {
				f := float64(i) / 20
				c, err := colorful.Hex(string(th.Util(f)))
				if err != nil {
					t.Fatalf("ramp at %.2f is not a hex colour: %v", f, err)
				}
				h, s, _ := c.Hsl()
				if s > 0.25 && (h < 60 || h > 330) {
					t.Errorf("ramp at %.2f is hue %.0f: a warm alarm colour has no business on this ramp", f, h)
				}
			}

			// Contrast against the page has to climb — not luminance, which moves
			// the other way on a light background. This is what keeps the fill level
			// readable on a projector that has flattened every hue.
			bg, err := colorful.Hex(string(th.Bg))
			if err != nil {
				t.Fatalf("background is not a hex colour: %v", err)
			}
			bgL, _, _ := bg.Lab()
			prev := -1.0
			for i := 0; i <= 20; i++ {
				f := float64(i) / 20
				c, _ := colorful.Hex(string(th.Util(f)))
				l, _, _ := c.Lab()
				contrast := l - bgL
				if contrast < 0 {
					contrast = -contrast
				}
				if contrast < prev-0.01 {
					t.Fatalf("contrast against the page fell at %.2f: %.3f after %.3f", f, contrast, prev)
				}
				prev = contrast
			}
		})
	}
}
