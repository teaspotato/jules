package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/teaspotato/jules/internal/api"
	"github.com/teaspotato/jules/internal/cache"
)

func TestSessionID(t *testing.T) {
	cases := map[string]string{
		"123":                                  "123",
		"sessions/123":                         "123",
		"https://jules.google.com/session/123": "123",
		"https://jules.google.com/session/123/?x":  "123",
		"https://jules.google.com/session/123#top": "123",
	}
	for in, want := range cases {
		if got := sessionID(in); got != want {
			t.Errorf("sessionID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExpandStates(t *testing.T) {
	st, err := expandStates([]string{"awaiting,failed"})
	if err != nil {
		t.Fatal(err)
	}
	if !st[api.StateAwaitingPlanApproval] || !st[api.StateAwaitingUserFeedback] || !st[api.StateFailed] || len(st) != 3 {
		t.Fatalf("states = %v", st)
	}
	if _, err := expandStates([]string{"bogus"}); err == nil {
		t.Fatal("want error for unknown state")
	}
}

// run executes the CLI against a fake API and returns stdout.
func run(t *testing.T, h http.HandlerFunc, args ...string) (string, error) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := api.New("k")
	c.BaseURL = srv.URL
	var out, errOut bytes.Buffer
	a := &app{out: &out, errOut: &errOut, client: c, cache: cache.Cache{}, version: "test"}
	root := newRoot(a)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestSessionsJSONFilterAndSort(t *testing.T) {
	out, err := run(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"sessions":[
			{"id":"1","state":"COMPLETED","updateTime":"2026-10-18T00:00:00Z","sourceContext":{"source":"sources/github/a/b"}},
			{"id":"2","state":"AWAITING_USER_FEEDBACK","updateTime":"2026-10-01T00:00:00Z","sourceContext":{"source":"sources/github/a/b"}},
			{"id":"3","state":"FAILED","updateTime":"2026-10-17T00:00:00Z","sourceContext":{"source":"sources/github/c/d"}}
		]}`))
	}, "sessions", "--repo", "a/b", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var ss []map[string]any
	if err := json.Unmarshal([]byte(out), &ss); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if len(ss) != 2 || ss[0]["id"] != "2" || ss[1]["id"] != "1" {
		t.Fatalf("got %v", ss)
	}
}

func TestPrefixResolutionAndPatch(t *testing.T) {
	patch := "diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1 +1 @@\n-a\n+b\n"
	out, err := run(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sessions/318":
			w.WriteHeader(404)
		case "/sessions":
			w.Write([]byte(`{"sessions":[{"id":"3186","name":"sessions/3186"},{"id":"999"}]}`))
		case "/sessions/3186/activities":
			json.NewEncoder(w).Encode(map[string]any{"activities": []any{
				map[string]any{"name": "x", "progressUpdated": map[string]any{}, "artifacts": []any{
					map[string]any{"changeSet": map[string]any{"gitPatch": map[string]any{"unidiffPatch": patch}}},
				}},
			}})
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}, "patch", "318")
	if err != nil {
		t.Fatal(err)
	}
	if out != patch {
		t.Fatalf("patch output = %q", out)
	}
}

func TestShowNDJSON(t *testing.T) {
	out, err := run(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sessions/7":
			w.Write([]byte(`{"id":"7","name":"sessions/7","state":"COMPLETED"}`))
		case "/sessions/7/activities":
			w.Write([]byte(`{"activities":[{"name":"sessions/7/activities/a","agentMessaged":{"agentMessage":"hi"},"future":true}]}`))
		}
	}, "show", "7", "--json")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], `{"session":`) || !strings.Contains(lines[1], `"future":true`) {
		t.Fatalf("ndjson = %s", out)
	}
}

func TestSendRefusesFinishedSession(t *testing.T) {
	sent := false
	_, err := run(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ":sendMessage") {
			sent = true
		}
		w.Write([]byte(`{"id":"5","name":"sessions/5","state":"COMPLETED"}`))
	}, "send", "5", "hello")
	if err == nil || sent || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err=%v sent=%v", err, sent)
	}
}

func TestWaitForReplySkipsInFlightProgress(t *testing.T) {
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sessions/5":
			w.Write([]byte(`{"id":"5","name":"sessions/5","state":"IN_PROGRESS"}`))
		case "/sessions/5/activities":
			polls++
			if polls == 1 {
				// In-flight progress update before userMessaged is recorded.
				w.Write([]byte(`{"activities":[{"id":"p1","progressUpdated":{"title":"running tests"}}]}`))
				return
			}
			w.Write([]byte(`{"activities":[
				{"id":"p1","progressUpdated":{"title":"running tests"}},
				{"id":"u1","originator":"user","userMessaged":{"userMessage":"hi"}},
				{"id":"a1","originator":"agent","agentMessaged":{"agentMessage":"done"}}
			]}`))
		}
	}))
	t.Cleanup(srv.Close)
	c := api.New("k")
	c.BaseURL = srv.URL
	tl := api.NewTimeline("5", nil)
	got, err := waitForReply(t.Context(), c, tl, 10*1e9)
	if err != nil {
		t.Fatal(err)
	}
	if polls < 2 || len(got) != 3 || got[len(got)-1].ID != "a1" {
		t.Fatalf("polls=%d got=%+v", polls, got)
	}
}
