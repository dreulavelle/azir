package store_test

import (
	"testing"

	"github.com/dreulavelle/azir/internal/store"
)

// Which model a technician may ask for is a permission, so it is checked in one
// place and tested like one. The failure that matters is the permissive one: a
// stored preference must never become a way past a policy that has since
// tightened.
func TestModelPolicy(t *testing.T) {
	cases := []struct {
		name    string
		cfg     store.AssistantConfig
		model   string
		allowed bool
	}{
		{"empty means the default", store.AssistantConfig{Model: "a"}, "", true},
		{"the default itself is always fine", store.AssistantConfig{Model: "a"}, "a", true},
		{"fixed refuses anything else", store.AssistantConfig{Model: "a", ModelChoice: store.ModelFixed}, "b", false},
		{"an unset policy is fixed", store.AssistantConfig{Model: "a"}, "b", false},
		{
			"listed allows what is on the list",
			store.AssistantConfig{Model: "a", ModelChoice: store.ModelListed, AllowedModels: []string{"b", "c"}},
			"b", true,
		},
		{
			"listed refuses what is not",
			store.AssistantConfig{Model: "a", ModelChoice: store.ModelListed, AllowedModels: []string{"b", "c"}},
			"d", false,
		},
		{
			"a model removed from the list stops being allowed",
			store.AssistantConfig{Model: "a", ModelChoice: store.ModelListed, AllowedModels: []string{"c"}},
			"b", false,
		},
		{"free allows anything", store.AssistantConfig{Model: "a", ModelChoice: store.ModelFree}, "anything", true},
		{"surrounding space does not smuggle a model past", store.AssistantConfig{Model: "a"}, "  b  ", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.MayUse(tc.model); got != tc.allowed {
				t.Errorf("MayUse(%q) = %v, want %v", tc.model, got, tc.allowed)
			}
		})
	}
}
