package api

import (
	"strings"
	"testing"

	"github.com/dreulavelle/azir/internal/assistant"
	"github.com/dreulavelle/azir/internal/store"
)

// The prompt is assembled from three parts and the order is load-bearing: what
// an operator wrote comes after the instructions it is meant to qualify, and
// the facts of the request come last so nothing an operator wrote can push them
// out of the model's attention.
func TestPromptAssembly(t *testing.T) {
	t.Run("an empty prompt uses what Azir ships", func(t *testing.T) {
		got := prompt(store.AssistantConfig{}, "Dreu", "ticket 4210")
		if !strings.Contains(got, "You are Azir") {
			t.Error("the shipped instructions are missing")
		}
		if !strings.Contains(got, assistant.InjectionDefence) {
			t.Error("the injection defence is missing from the default")
		}
	})

	t.Run("an edited prompt replaces the shipped one", func(t *testing.T) {
		got := prompt(store.AssistantConfig{SystemPrompt: "Only speak in haiku."}, "Dreu", "")
		if strings.Contains(got, "You are Azir") {
			t.Error("the shipped instructions survived being overridden")
		}
		if !strings.Contains(got, "Only speak in haiku.") {
			t.Error("the edited instructions are missing")
		}
	})

	t.Run("the request's facts always come last", func(t *testing.T) {
		got := prompt(store.AssistantConfig{
			SystemPrompt: "Base.",
			HousePrompt:  "House.",
		}, "Dreu", "ticket 4210")

		base := strings.Index(got, "Base.")
		house := strings.Index(got, "House.")
		who := strings.Index(got, "Dreu")
		if base < 0 || house < 0 || who < 0 {
			t.Fatalf("a part is missing from %q", got)
		}
		if !(base < house && house < who) {
			t.Errorf("wrong order: base=%d house=%d context=%d", base, house, who)
		}
		if !strings.Contains(got, "ticket 4210") {
			t.Error("the prompt does not say what the technician is looking at")
		}
	})
}
