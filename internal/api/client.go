package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the v1alpha endpoint.
const DefaultBaseURL = "https://jules.googleapis.com/v1alpha"

// Client talks to the Jules API. The zero value is not usable; use New.
type Client struct {
	BaseURL    string
	APIKey     string
	HTTP       *http.Client
	UserAgent  string
	MaxRetries int
	// Sleep is swappable for tests.
	Sleep func(context.Context, time.Duration) error
}

// New returns a client for key with sensible defaults.
func New(key string) *Client {
	return &Client{
		BaseURL:    DefaultBaseURL,
		APIKey:     key,
		HTTP:       &http.Client{Timeout: 60 * time.Second},
		UserAgent:  "jules-tui",
		MaxRetries: 4,
		Sleep:      sleepCtx,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Error is a non-2xx response from the API.
type Error struct {
	StatusCode int
	Status     string // e.g. "UNAUTHENTICATED"
	Message    string
	Reason     string // ErrorInfo.reason, e.g. "API_KEY_SERVICE_BLOCKED"
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.StatusCode)
	}
	s := fmt.Sprintf("jules api: %d", e.StatusCode)
	if e.Status != "" {
		s += " " + e.Status
	}
	if e.Reason != "" {
		s += " (" + e.Reason + ")"
	}
	return s + ": " + msg
}

// Hint is a human next step for well-known failures, or "".
func (e *Error) Hint() string {
	switch {
	case e.Reason == "API_KEY_SERVICE_BLOCKED":
		return "this key is not allowed to call the Jules API; create one in the Jules web app (Settings > API keys)"
	case e.Reason == "API_KEY_INVALID" || e.StatusCode == 401:
		return "the API key was rejected; run `jules auth login` or set JULES_API_KEY"
	case e.StatusCode == 403:
		return "the API key lacks access to this resource"
	case e.StatusCode == 429:
		return "rate limited by Jules; try again shortly"
	}
	return ""
}

// IsNotFound reports whether err is a 404 from the API.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.StatusCode == http.StatusNotFound
}

// IsAuth reports whether err is an authentication/authorization failure.
func IsAuth(err error) bool {
	var e *Error
	return errors.As(err, &e) && (e.StatusCode == 401 || e.StatusCode == 403)
}

func retryable(code int) bool {
	return code == 429 || code == 500 || code == 502 || code == 503 || code == 504
}

// do performs a request with retries on 429/5xx and transport errors, and
// decodes a JSON body into out (if non-nil).
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	u := strings.TrimRight(c.BaseURL, "/") + "/" + strings.TrimLeft(path, "/")
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var lastErr error
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		if c.APIKey != "" {
			req.Header.Set("X-Goog-Api-Key", c.APIKey)
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Accept", "application/json")
		if c.UserAgent != "" {
			req.Header.Set("User-Agent", c.UserAgent)
		}

		resp, err := c.HTTP.Do(req)
		var wait time.Duration
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = err
		} else {
			data, rerr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if rerr != nil {
				lastErr = rerr
			} else if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				if out == nil || len(bytes.TrimSpace(data)) == 0 {
					return nil
				}
				if err := json.Unmarshal(data, out); err != nil {
					return fmt.Errorf("jules api: decoding %s %s: %w", method, path, err)
				}
				return nil
			} else {
				apiErr := parseError(resp, data)
				if !retryable(resp.StatusCode) {
					return apiErr
				}
				lastErr = apiErr
				wait = apiErr.RetryAfter
			}
		}

		if attempt >= c.MaxRetries {
			return lastErr
		}
		if wait == 0 {
			// Exponential backoff with jitter: 0.5s, 1s, 2s, 4s ...
			base := 500 * time.Millisecond << attempt
			wait = base/2 + time.Duration(rand.Int64N(int64(base)))
		}
		if err := c.Sleep(ctx, wait); err != nil {
			return err
		}
	}
}

func parseError(resp *http.Response, data []byte) *Error {
	e := &Error{StatusCode: resp.StatusCode}
	var env struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
			Details []struct {
				Type   string `json:"@type"`
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &env) == nil {
		e.Message = env.Error.Message
		e.Status = env.Error.Status
		for _, d := range env.Error.Details {
			if d.Reason != "" {
				e.Reason = d.Reason
				break
			}
		}
	} else {
		e.Message = strings.TrimSpace(string(data))
	}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil {
			e.RetryAfter = time.Duration(secs) * time.Second
		}
	}
	return e
}

// SessionName normalises "abc" or "sessions/abc" to "sessions/abc".
func SessionName(id string) string {
	if strings.HasPrefix(id, "sessions/") {
		return id
	}
	return "sessions/" + id
}

// SourceName normalises "owner/repo", "github/owner/repo" or
// "sources/github/owner/repo" to the resource name.
func SourceName(s string) string {
	switch {
	case strings.HasPrefix(s, "sources/"):
		return s
	case strings.HasPrefix(s, "github/"):
		return "sources/" + s
	default:
		return "sources/github/" + s
	}
}

// Page is one page of a list call.
type Page[T any] struct {
	Items         []T
	NextPageToken string
}

// ListOptions control a list call. Filter is AIP-160 as accepted by the
// endpoint (see the discovery doc for what each supports).
type ListOptions struct {
	PageSize  int
	PageToken string
	Filter    string
}

func (o ListOptions) values() url.Values {
	v := url.Values{}
	if o.PageSize > 0 {
		v.Set("pageSize", strconv.Itoa(o.PageSize))
	}
	if o.PageToken != "" {
		v.Set("pageToken", o.PageToken)
	}
	if o.Filter != "" {
		v.Set("filter", o.Filter)
	}
	return v
}

func (c *Client) ListSourcesPage(ctx context.Context, o ListOptions) (Page[Source], error) {
	var r struct {
		Sources       []Source `json:"sources"`
		NextPageToken string   `json:"nextPageToken"`
	}
	err := c.do(ctx, http.MethodGet, "sources", o.values(), nil, &r)
	return Page[Source]{r.Sources, r.NextPageToken}, err
}

func (c *Client) GetSource(ctx context.Context, name string) (*Source, error) {
	var s Source
	if err := c.do(ctx, http.MethodGet, SourceName(name), nil, nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (c *Client) ListSessionsPage(ctx context.Context, o ListOptions) (Page[Session], error) {
	var r struct {
		Sessions      []Session `json:"sessions"`
		NextPageToken string    `json:"nextPageToken"`
	}
	err := c.do(ctx, http.MethodGet, "sessions", o.values(), nil, &r)
	return Page[Session]{r.Sessions, r.NextPageToken}, err
}

func (c *Client) GetSession(ctx context.Context, id string) (*Session, error) {
	var s Session
	if err := c.do(ctx, http.MethodGet, SessionName(id), nil, nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// CreateSessionRequest is the input for CreateSession.
type CreateSessionRequest struct {
	Prompt              string         `json:"prompt"`
	Title               string         `json:"title,omitempty"`
	SourceContext       *SourceContext `json:"sourceContext,omitempty"`
	RequirePlanApproval bool           `json:"requirePlanApproval,omitempty"`
	AutomationMode      string         `json:"automationMode,omitempty"`
}

func (c *Client) CreateSession(ctx context.Context, req CreateSessionRequest) (*Session, error) {
	var s Session
	if err := c.do(ctx, http.MethodPost, "sessions", nil, req, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// SendMessage posts a user message. The API returns nothing; the message
// shows up later as a userMessaged activity.
func (c *Client) SendMessage(ctx context.Context, id, prompt string) error {
	return c.do(ctx, http.MethodPost, SessionName(id)+":sendMessage", nil, map[string]string{"prompt": prompt}, nil)
}

func (c *Client) ApprovePlan(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, SessionName(id)+":approvePlan", nil, struct{}{}, nil)
}

func (c *Client) ArchiveSession(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, SessionName(id)+":archive", nil, struct{}{}, nil)
}

func (c *Client) UnarchiveSession(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, SessionName(id)+":unarchive", nil, struct{}{}, nil)
}

func (c *Client) DeleteSession(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, SessionName(id), nil, nil, nil)
}

func (c *Client) ListActivitiesPage(ctx context.Context, sessionID string, o ListOptions) (Page[Activity], error) {
	var r struct {
		Activities    []Activity `json:"activities"`
		NextPageToken string     `json:"nextPageToken"`
	}
	err := c.do(ctx, http.MethodGet, SessionName(sessionID)+"/activities", o.values(), nil, &r)
	return Page[Activity]{r.Activities, r.NextPageToken}, err
}

// All drains a paginated list call.
func All[T any](ctx context.Context, o ListOptions, fetch func(context.Context, ListOptions) (Page[T], error)) ([]T, error) {
	if o.PageSize == 0 {
		o.PageSize = 100
	}
	var all []T
	for {
		p, err := fetch(ctx, o)
		if err != nil {
			return all, err
		}
		all = append(all, p.Items...)
		if p.NextPageToken == "" || p.NextPageToken == o.PageToken {
			return all, nil
		}
		o.PageToken = p.NextPageToken
	}
}

func (c *Client) ListSources(ctx context.Context) ([]Source, error) {
	return All(ctx, ListOptions{}, c.ListSourcesPage)
}

// ListSessions returns every session; archived ones only when includeArchived.
func (c *Client) ListSessions(ctx context.Context, includeArchived bool) ([]Session, error) {
	o := ListOptions{}
	if includeArchived {
		o.Filter = "archived = true OR archived = false"
	}
	return All(ctx, o, c.ListSessionsPage)
}

// ListActivities returns the whole timeline of a session, oldest first.
func (c *Client) ListActivities(ctx context.Context, sessionID string) ([]Activity, error) {
	return All(ctx, ListOptions{}, func(ctx context.Context, o ListOptions) (Page[Activity], error) {
		return c.ListActivitiesPage(ctx, sessionID, o)
	})
}

// ListActivitiesSince returns activities created strictly after t, using the
// server-side create_time filter so polling stays cheap.
func (c *Client) ListActivitiesSince(ctx context.Context, sessionID string, t time.Time) ([]Activity, error) {
	if t.IsZero() {
		return c.ListActivities(ctx, sessionID)
	}
	o := ListOptions{Filter: fmt.Sprintf("create_time > %q", t.UTC().Format(time.RFC3339Nano))}
	acts, err := All(ctx, o, func(ctx context.Context, o ListOptions) (Page[Activity], error) {
		return c.ListActivitiesPage(ctx, sessionID, o)
	})
	var apiErr *Error
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadRequest {
		// The filter is documented but unverified; fall back to a full
		// listing rather than failing (callers de-duplicate).
		return c.ListActivities(ctx, sessionID)
	}
	return acts, err
}
