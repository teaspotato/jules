package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New("test-key")
	c.BaseURL = srv.URL + "/v1alpha"
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

func TestErrorParsing(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Goog-Api-Key"); got != "test-key" {
			t.Errorf("api key header = %q", got)
		}
		w.WriteHeader(401)
		w.Write([]byte(`{"error":{"code":401,"message":"bad creds","status":"UNAUTHENTICATED",
			"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"API_KEY_SERVICE_BLOCKED"}]}}`))
	})
	_, err := c.ListSourcesPage(context.Background(), ListOptions{})
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *Error, got %v", err)
	}
	if apiErr.Reason != "API_KEY_SERVICE_BLOCKED" || apiErr.Status != "UNAUTHENTICATED" || !IsAuth(err) {
		t.Fatalf("unexpected error: %+v", apiErr)
	}
	if !strings.Contains(apiErr.Hint(), "Jules web app") {
		t.Fatalf("hint = %q", apiErr.Hint())
	}
}

func TestRetriesOn5xxAnd429(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(503)
		case 2:
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
		default:
			w.Write([]byte(`{"id":"1","state":"COMPLETED"}`))
		}
	})
	s, err := c.GetSession(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	if s.State != StateCompleted || calls.Load() != 3 {
		t.Fatalf("state=%s calls=%d", s.State, calls.Load())
	}
}

func TestNoRetryOn4xx(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(404)
	})
	_, err := c.GetSession(context.Background(), "nope")
	if !IsNotFound(err) || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestPagination(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1alpha/sessions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("filter"); got != "archived = true OR archived = false" {
			t.Errorf("filter = %q", got)
		}
		switch r.URL.Query().Get("pageToken") {
		case "":
			w.Write([]byte(`{"sessions":[{"id":"1"},{"id":"2"}],"nextPageToken":"p2"}`))
		case "p2":
			w.Write([]byte(`{"sessions":[{"id":"3"}]}`))
		}
	})
	ss, err := c.ListSessions(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 3 || ss[2].ID != "3" {
		t.Fatalf("got %+v", ss)
	}
}

func TestSendMessageAndApprove(t *testing.T) {
	var paths []string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if strings.HasSuffix(r.URL.Path, ":sendMessage") {
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["prompt"] != "hello" {
				t.Errorf("body = %v", body)
			}
		}
		w.Write([]byte(`{}`))
	})
	ctx := context.Background()
	if err := c.SendMessage(ctx, "sessions/9", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := c.ApprovePlan(ctx, "9"); err != nil {
		t.Fatal(err)
	}
	want := []string{"POST /v1alpha/sessions/9:sendMessage", "POST /v1alpha/sessions/9:approvePlan"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("paths = %v", paths)
	}
}

func TestActivitiesSinceFallsBackOnBadFilter(t *testing.T) {
	var filtered, full int
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter") != "" {
			filtered++
			w.WriteHeader(400)
			w.Write([]byte(`{"error":{"code":400,"status":"INVALID_ARGUMENT"}}`))
			return
		}
		full++
		w.Write([]byte(`{"activities":[{"name":"sessions/1/activities/a","id":"a"}]}`))
	})
	acts, err := c.ListActivitiesSince(context.Background(), "1", time.Now())
	if err != nil || len(acts) != 1 || filtered != 1 || full != 1 {
		t.Fatalf("acts=%d err=%v filtered=%d full=%d", len(acts), err, filtered, full)
	}
}

func loadFixture(t *testing.T) []Activity {
	t.Helper()
	b, err := os.ReadFile("testdata/activities.json")
	if err != nil {
		t.Fatal(err)
	}
	var r struct{ Activities []Activity }
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r.Activities
}

func TestActivityKindsAndRaw(t *testing.T) {
	acts := loadFixture(t)
	var kinds []string
	for _, a := range acts {
		kinds = append(kinds, a.Kind())
	}
	want := "planGenerated,planApproved,progressUpdated,progressUpdated,agentMessaged,unknown,sessionCompleted"
	if got := strings.Join(kinds, ","); got != want {
		t.Fatalf("kinds = %s", got)
	}
	// Unknown fields survive a round trip.
	out, _ := json.Marshal(acts[5])
	if !strings.Contains(string(out), "somethingNew") {
		t.Fatalf("raw not preserved: %s", out)
	}
	plan, approved := LatestPlan(acts)
	if plan == nil || plan.ID != "plan1" || !approved {
		t.Fatalf("plan=%v approved=%v", plan, approved)
	}
	cs := LatestChangeSet(acts)
	if cs == nil || cs.GitPatch.SuggestedCommitMessage != "Fix XSS in escapeHtml" {
		t.Fatalf("latest change set = %+v", cs)
	}
}

func TestTimelineRefreshDedupes(t *testing.T) {
	acts := loadFixture(t)
	var lastFilter string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		lastFilter = r.URL.Query().Get("filter")
		// Simulate the server returning an overlap with what we hold.
		json.NewEncoder(w).Encode(map[string]any{"activities": acts[len(acts)-2:]})
	})
	tl := NewTimeline("42", acts[:len(acts)-1])
	fresh, err := tl.Refresh(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 1 || fresh[0].ID != "a7" || len(tl.Activities) != len(acts) {
		t.Fatalf("fresh=%d total=%d", len(fresh), len(tl.Activities))
	}
	if !strings.HasPrefix(lastFilter, `create_time > "2026-10-16T08:44:58`) {
		t.Fatalf("filter = %q", lastFilter)
	}
}

func TestNames(t *testing.T) {
	cases := map[string]string{
		"abc":          "sessions/abc",
		"sessions/abc": "sessions/abc",
	}
	for in, want := range cases {
		if got := SessionName(in); got != want {
			t.Errorf("SessionName(%q) = %q", in, got)
		}
	}
	src := map[string]string{
		"owner/repo":                "sources/github/owner/repo",
		"github/owner/repo":         "sources/github/owner/repo",
		"sources/github/owner/repo": "sources/github/owner/repo",
	}
	for in, want := range src {
		if got := SourceName(in); got != want {
			t.Errorf("SourceName(%q) = %q", in, got)
		}
	}
	s := Session{SourceContext: &SourceContext{Source: "sources/github/acme/app", GitHubRepoContext: &GitHubRepoContext{StartingBranch: "main"}}}
	if s.Repo() != "acme/app" || s.Branch() != "main" {
		t.Fatalf("repo=%q branch=%q", s.Repo(), s.Branch())
	}
}
