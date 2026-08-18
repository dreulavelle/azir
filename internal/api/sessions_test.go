package api_test

import (
	"net/http"
	"net/http/cookiejar"
	"testing"
	"time"
)

// signIn opens a second session as the same administrator, so there is one to
// end that is not the one asking.
func signIn(t *testing.T, url string) *http.Client {
	t.Helper()
	return signInAs(t, url, "admin@test.local", "test-password-1234")
}

// signInAs opens a session as anybody, for the tests whose subject is what a
// different account is allowed to do.
func signInAs(t *testing.T, url, email, password string) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 15 * time.Second}
	do(t, client, http.MethodPost, url+"/api/login", map[string]string{
		"email": email, "password": password,
	}, http.StatusOK)
	return client
}

func openSessions(t *testing.T, c *http.Client, url string) []map[string]any {
	t.Helper()
	out := do(t, c, http.MethodGet, url+"/api/sessions", nil, http.StatusOK)
	list, _ := out["sessions"].([]any)
	sessions := make([]map[string]any, 0, len(list))
	for _, entry := range list {
		if row, ok := entry.(map[string]any); ok {
			sessions = append(sessions, row)
		}
	}
	return sessions
}

/*
Where an account is signed in, answered without a database client.

Every sign-in has recorded the browser and the address since the table was
made, and nothing ever read either. The question is the one asked after a
laptop goes missing.
*/
func TestOpenSessionsCanBeSeenAndEnded(t *testing.T) {
	srv, client := server(t)

	sessions := openSessions(t, client, srv.URL)
	if len(sessions) == 0 {
		t.Fatal("nothing open, yet this request arrived on a session")
	}

	// The one making the request is marked, so nobody ends it wondering why
	// the page stopped working.
	var current, other map[string]any
	for _, s := range sessions {
		if s["current"] == true {
			current = s
		} else if other == nil {
			other = s
		}
	}
	if current == nil {
		t.Fatal("no session is marked as the one asking")
	}
	if current["email"] == "" {
		t.Error("a session that does not say whose it is answers nothing")
	}

	// Sign in again so there is a second one to end.
	second := signIn(t, srv.URL)
	sessions = openSessions(t, client, srv.URL)
	if len(sessions) < 2 {
		t.Fatalf("expected a second session, saw %d", len(sessions))
	}

	var target string
	for _, s := range sessions {
		if s["current"] != true {
			target = s["id"].(string)
			break
		}
	}
	if target == "" {
		t.Fatal("no other session to end")
	}
	do(t, client, http.MethodDelete, srv.URL+"/api/sessions/"+target, nil, http.StatusOK)

	// Ending it is what makes it stop working, not merely what hides the row.
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := second.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("the ended session still works: %d", res.StatusCode)
	}

	// And the one asking still does.
	do(t, client, http.MethodGet, srv.URL+"/api/me", nil, http.StatusOK)
}

// A session that has already gone says so rather than reporting success for
// something it did not do.
func TestEndingASessionTwiceSaysSo(t *testing.T) {
	srv, client := server(t)

	signIn(t, srv.URL)
	var target string
	for _, s := range openSessions(t, client, srv.URL) {
		if s["current"] != true {
			target = s["id"].(string)
			break
		}
	}
	if target == "" {
		t.Fatal("no second session")
	}

	do(t, client, http.MethodDelete, srv.URL+"/api/sessions/"+target, nil, http.StatusOK)
	do(t, client, http.MethodDelete, srv.URL+"/api/sessions/"+target, nil, http.StatusNotFound)
}
