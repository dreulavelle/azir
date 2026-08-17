package syncro

import "regexp"

// done matches the statuses that mean nobody is working on this any more.
//
// Syncro lets an account invent its own status names, so there is no fixed list
// to check against and the word is read for intent instead. Word boundaries
// matter more than they look: without them "Incomplete" reads as complete and a
// live ticket disappears out of the queue.
//
// The interface carries the same rule (web/src/ui.tsx, isDone) and the two have
// to agree. A ticket the queue hides but the filter still counts is worse than
// either behaviour on its own, because the number on screen stops describing
// the list under it.
var done = regexp.MustCompile(`(?i)\b(resolv|close|complete|done|cancel)`)

// IsDone reports whether a ticket status means the work is finished.
func IsDone(status string) bool { return done.MatchString(status) }
