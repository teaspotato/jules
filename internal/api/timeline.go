package api

import (
	"context"
	"sort"
	"time"
)

// Timeline holds a session's activities and fetches only what's new.
//
// Polling uses the server-side create_time filter with a small overlap
// window, then de-duplicates by activity name, so activities created in the
// same instant as the last one we saw are never missed.
type Timeline struct {
	SessionID  string
	Activities []Activity
	seen       map[string]bool
}

// overlap guards against clock granularity on create_time.
const overlap = 2 * time.Second

// NewTimeline starts a timeline, optionally seeded from cache.
func NewTimeline(sessionID string, cached []Activity) *Timeline {
	t := &Timeline{SessionID: sessionID, seen: map[string]bool{}}
	t.add(cached)
	return t
}

func key(a Activity) string {
	if a.Name != "" {
		return a.Name
	}
	return a.ID
}

// Merge adds activities fetched elsewhere and returns the ones not seen
// before, oldest first.
func (t *Timeline) Merge(acts []Activity) []Activity { return t.add(acts) }

// Since is the create_time to pass to ListActivitiesSince for the next poll.
func (t *Timeline) Since() time.Time {
	last := t.Last()
	if last.IsZero() {
		return last
	}
	return last.Add(-overlap)
}

func (t *Timeline) add(acts []Activity) []Activity {
	var fresh []Activity
	for _, a := range acts {
		k := key(a)
		if k != "" && t.seen[k] {
			continue
		}
		t.seen[k] = true
		fresh = append(fresh, a)
	}
	if len(fresh) == 0 {
		return nil
	}
	t.Activities = append(t.Activities, fresh...)
	sort.SliceStable(t.Activities, func(i, j int) bool {
		return t.Activities[i].CreateTime.Before(t.Activities[j].CreateTime)
	})
	sort.SliceStable(fresh, func(i, j int) bool {
		return fresh[i].CreateTime.Before(fresh[j].CreateTime)
	})
	return fresh
}

// Last returns the create time of the newest activity, or zero.
func (t *Timeline) Last() time.Time {
	if len(t.Activities) == 0 {
		return time.Time{}
	}
	return t.Activities[len(t.Activities)-1].CreateTime
}

// Refresh fetches activities newer than the last one held and returns those
// not seen before, oldest first.
func (t *Timeline) Refresh(ctx context.Context, c *Client) ([]Activity, error) {
	acts, err := c.ListActivitiesSince(ctx, t.SessionID, t.Since())
	if err != nil {
		return nil, err
	}
	return t.add(acts), nil
}
