package ui

import (
	"strings"
	"testing"
)

// TestStackedMetersAreRails pins the separation, not the glyph choice: every
// meter that has another meter directly above or below it must be drawn as a
// half-height bar. Background fills on adjacent rows have no edge between them
// and merge into one block of two colours.
func TestStackedMetersAreRails(t *testing.T) {
	rail := string(railCell)

	t.Run("header", func(t *testing.T) {
		m := newTestModel(t, 160, 44, 14)
		m.snap.Totals.Pending, m.snap.Totals.Unschedulable = 28, 7
		settle(m)
		// Rows 1..3 are cpu, mem and pending — all three stacked, all three rails.
		for i, row := range strings.Split(m.View(), "\n")[1:headerHeight] {
			if !strings.Contains(row, rail) {
				t.Errorf("header meter row %d is not a rail: %q", i+1, row)
			}
		}
	})

	t.Run("dense table", func(t *testing.T) {
		m := newTestModel(t, 160, 30, 20)
		m.setMode(ModeDense)
		settle(m)
		rows := 0
		for _, line := range strings.Split(m.View(), "\n") {
			if strings.Contains(line, "ip-10-0-0-") && strings.Contains(line, rail) {
				rows++
			}
		}
		if rows < 5 {
			t.Errorf("only %d dense rows drew rails; every row's meters stack against its neighbours", rows)
		}
	})

	t.Run("detail pane", func(t *testing.T) {
		m := agedTestModel(t, 160, 44, 14)
		m.describe = &stubDescriber{detail: sampleDetail("")}
		m.setCursor(2)
		openPane(t, m)
		out := m.View()
		if !strings.Contains(out, "CAPACITY") {
			t.Fatalf("pane did not render its capacity block:\n%s", out)
		}
		if !strings.Contains(out, rail) {
			t.Error("the capacity meters stack four deep and are not rails")
		}
	})
}

// TestCardMetersStayFilled is the other half of the rule: a card meter carries
// its label, its absolute figures and its percentage *on* the bar, which a glyph
// bar cannot support — a rail there would be shredded by its own text.
func TestCardMetersStayFilled(t *testing.T) {
	m := newTestModel(t, 160, 44, 14)
	settle(m)
	for _, line := range strings.Split(m.View(), "\n") {
		// Card meter rows are the ones carrying the cpu figures inside a box.
		if strings.Contains(line, "┃ cpu") && strings.Contains(line, string(railCell)) {
			t.Errorf("card meter row was drawn as a rail, so its figures cannot sit on it: %q", line)
		}
	}
}
