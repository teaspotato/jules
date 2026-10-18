package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/teaspotato/jules/internal/api"
)

// sessionID extracts an ID from a full name, a web URL, or a bare ID/prefix.
func sessionID(arg string) string {
	arg = strings.TrimSpace(arg)
	if i := strings.Index(arg, "/session/"); i >= 0 {
		arg = arg[i+len("/session/"):]
		arg, _, _ = strings.Cut(arg, "?")
		arg, _, _ = strings.Cut(arg, "#")
		arg = strings.Trim(arg, "/")
	}
	return strings.TrimPrefix(arg, "sessions/")
}

// resolveSession accepts a full resource name, a bare ID, a jules.google.com
// URL, or a unique ID prefix.
func (a *app) resolveSession(ctx context.Context, arg string) (*api.Session, error) {
	c, err := a.api()
	if err != nil {
		return nil, err
	}
	id := sessionID(arg)
	if id == "" {
		return nil, withCode(ExitUsage, fmt.Errorf("empty session id"))
	}
	s, err := c.GetSession(ctx, id)
	if err == nil {
		return s, nil
	}
	var apiErr *api.Error
	if !api.IsNotFound(err) && !(errors.As(err, &apiErr) && apiErr.StatusCode == 400) {
		return nil, err
	}
	// Prefix match over cached sessions first, then the live list.
	if cached, ok := a.cache.Sessions(); ok {
		if m := matchPrefix(cached, id); len(m) == 1 {
			return c.GetSession(ctx, m[0].ID)
		}
	}
	all, err := c.ListSessions(ctx, true)
	if err != nil {
		return nil, err
	}
	m := matchPrefix(all, id)
	switch len(m) {
	case 0:
		return nil, withCode(ExitNotFound, fmt.Errorf("no session matches %q", arg))
	case 1:
		return &m[0], nil
	}
	var ids []string
	for i, s := range m {
		if i == 5 {
			ids = append(ids, "…")
			break
		}
		ids = append(ids, s.ID+" ("+s.DisplayTitle()+")")
	}
	return nil, withCode(ExitUsage, fmt.Errorf("%q is ambiguous: %s", arg, strings.Join(ids, ", ")))
}

func matchPrefix(ss []api.Session, prefix string) []api.Session {
	var out []api.Session
	for _, s := range ss {
		if strings.HasPrefix(s.ID, prefix) {
			out = append(out, s)
		}
	}
	return out
}

// readArgOrStdin returns the joined args, or stdin when no args are given
// (or the only arg is "-").
func readArgOrStdin(args []string, stdin func() (string, error)) (string, error) {
	if len(args) == 0 || (len(args) == 1 && args[0] == "-") {
		return stdin()
	}
	return strings.Join(args, " "), nil
}
