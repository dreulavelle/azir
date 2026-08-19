package bulk

/*
Fields Azir recognises by name.

Azir does not decide what a phone system can be set to — the plugin publishes
that, and everything is built from what it publishes. These are the handful
Azir looks for anyway, to do something more than show a form: split a name into
its parts, mark a badly-configured extension in a list, warn before a
spreadsheet sets two fields that describe one thing.

Every one of them is optional. A plugin that publishes none of these still
works; its extensions simply have no marks in the list and their names are one
field rather than two. That is the contract — Azir asks, it does not require.

Gathered here rather than written as string literals across four files, because
a name that appears in four places is a name that will be right in three of
them. That has already happened once: a default naming "Enabled" where the
field is "enabled" was refused along with every other default in the same
request, and a new extension came out configured the way nobody wanted.
*/
const (
	// FieldFirst and FieldLast are the parts of a person's name. Where a phone
	// system has them, the form offers them and the display name is what it
	// builds from the two.
	FieldFirst = "FirstName"
	FieldLast  = "LastName"

	// FieldEmail is what appears beside an extension in a list.
	FieldEmail = "EmailAddress"

	// The settings worth marking in a list of extensions, because they are
	// what somebody scans one to find.
	FieldRecording = "RecordCalls"
	FieldVoicemail = "VMEnabled"
	// FieldTunnel blocks remote connections that are not tunnelled, and
	// FieldAudio has the phone system carry the audio. Both ship the wrong way
	// round for a hosted deployment.
	FieldTunnel = "BlockTunnel"
	FieldAudio  = "PbxDeliversAudio"
)

/*
Splits reports whether a phone system offers the parts of a name separately.

Where it does, the display name is the two of them joined and setting both is a
contradiction — so a form offers the parts, and the display name stays for the
sheet, which has always had one column for it. Answered from the published
fields rather than assumed, because a phone system that only has a display name
is one where the display name is the field.
*/
func Splits(specs []Spec) bool {
	for _, spec := range specs {
		if string(spec.Field) == FieldFirst {
			return true
		}
	}
	return false
}
