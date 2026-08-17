package syncro

import "testing"

// Who said what has to be stated, not inferred. Syncro stamps the API key's
// owner on every comment made through the API — including the customer's own
// replies — so a caller reading the tech field sees the MSP's name on the
// customer's message. The assistant reads these; half a conversation attributed
// to the wrong side produces a confident summary of something that never
// happened.
func TestCommentAttribution(t *testing.T) {
	cases := []struct {
		name    string
		comment Comment
		want    string
	}{
		{
			"Syncro's own convention wins over the tech field",
			Comment{Subject: "Customer Reply", TechName: "Dreu LaVelle"},
			"customer",
		},
		{
			"an ordinary reply is ours even with a blank tech",
			Comment{Subject: "Reply", TechName: "Dreu LaVelle"},
			"technician",
		},
		{
			"a comment with no tech at all came from outside",
			Comment{Subject: "", TechName: ""},
			"customer",
		},
		{
			"an internal note is always ours",
			Comment{Subject: "Internal note", TechName: "Dreu LaVelle", Hidden: true},
			"technician",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := "technician"
			if fromCustomer(tc.comment) {
				got = "customer"
			}
			if got != tc.want {
				t.Errorf("comment %+v read as %q, want %q", tc.comment, got, tc.want)
			}
		})
	}
}
