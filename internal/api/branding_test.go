package api

import (
	"testing"

	"github.com/dreulavelle/azir/internal/store"
)

// The fallbacks decide what an operator sees before they have configured
// anything, and what a reseller sees after they have configured some of it.
// Getting them wrong shows the wrong company's name on a login page.
func TestBrandingFallbacks(t *testing.T) {
	t.Run("nothing set uses what we ship", func(t *testing.T) {
		v := resolve(store.Branding{})
		if v.EffectiveName != defaultName || v.EffectiveMark != defaultMark {
			t.Errorf("got %q/%q", v.EffectiveName, v.EffectiveMark)
		}
		if v.EffectiveAccent != defaultAccent {
			t.Errorf("accent %q", v.EffectiveAccent)
		}
	})

	t.Run("a rename takes the mark with it", func(t *testing.T) {
		// Renaming to Helios should not leave an "A" in the corner. Nobody
		// would think to set the mark separately, and the one that was there
		// is the previous owner's initial.
		v := resolve(store.Branding{Name: "Helios"})
		if v.EffectiveMark != "H" {
			t.Errorf("mark is %q, want H", v.EffectiveMark)
		}
	})

	t.Run("an explicit mark wins", func(t *testing.T) {
		v := resolve(store.Branding{Name: "Helios", Mark: "HX"})
		if v.EffectiveMark != "HX" {
			t.Errorf("mark is %q", v.EffectiveMark)
		}
	})

	t.Run("whitespace is not a name", func(t *testing.T) {
		v := resolve(store.Branding{Name: "   ", Tagline: "  "})
		if v.EffectiveName != defaultName || v.EffectiveTagline != defaultTagline {
			t.Errorf("blank values were taken as set: %q / %q", v.EffectiveName, v.EffectiveTagline)
		}
	})
}

func TestColourValidation(t *testing.T) {
	for _, ok := range []string{"#7c6bff", "#000000", "#FFFFFF"} {
		if !isHexColour(ok) {
			t.Errorf("%q was rejected", ok)
		}
	}
	for _, bad := range []string{"", "7c6bff", "#7c6bf", "#7c6bfff", "red", "#zzzzzz", "#7c6bff "} {
		if isHexColour(bad) {
			t.Errorf("%q was accepted", bad)
		}
	}
}
