package syncro_test

import (
	"testing"

	"github.com/dreulavelle/azir/internal/syncro"
)

// Which side a message came from is what makes "back and forth" mean anything.
//
// Syncro stamps a technician name on every comment created through its API,
// including ones its own portal would mark as coming from the customer. Reading
// only that field makes a thirty-message conversation look like a monologue —
// which is exactly what it did until a long thread was put in front of it.
func TestWhichSideAMessageCameFrom(t *testing.T) {
	cases := []struct {
		name     string
		comment  syncro.Comment
		customer bool
	}{
		{
			"the portal marks a customer reply in the subject",
			syncro.Comment{Subject: "Customer Reply", TechName: "Dreu LaVelle"},
			true,
		},
		{
			"case does not matter",
			syncro.Comment{Subject: "customer reply", TechName: "Dreu LaVelle"},
			true,
		},
		{
			"a technician reply is not",
			syncro.Comment{Subject: "Reply", TechName: "Dreu LaVelle"},
			false,
		},
		{
			"no technician attributed at all is the other clear case",
			syncro.Comment{Subject: "", TechName: ""},
			true,
		},
		{
			"whitespace is not a technician",
			syncro.Comment{Subject: "Reply", TechName: "   "},
			true,
		},
		{
			"an internal note is not from the customer",
			syncro.Comment{Subject: "Internal note", TechName: "Dreu LaVelle"},
			false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := syncro.FromCustomerForTest(c.comment); got != c.customer {
				t.Errorf("got %v, want %v for %+v", got, c.customer, c.comment)
			}
		})
	}
}
