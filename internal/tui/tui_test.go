package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/teaspotato/jules/internal/api"
	"github.com/teaspotato/jules/internal/cache"
)

func TestListSortsAttentionFirstAndFilters(t *testing.T) {
	now := time.Now()
	l := newList()
	l.resize(80, 20)
	l.setSessions([]api.Session{
		{ID: "1", Title: "old done", State: api.StateCompleted, UpdateTime: now},
		{ID: "2", Title: "needs me", State: api.StateAwaitingPlanApproval, UpdateTime: now.Add(-time.Hour)},
		{ID: "3", Title: "failed thing", State: api.StateFailed, UpdateTime: now.Add(-time.Minute)},
	})
	if l.visible[0].ID != "2" || l.visible[1].ID != "1" {
		t.Fatalf("order = %v", ids(l.visible))
	}
	l.filter = 4 // failed
	l.apply()
	if len(l.visible) != 1 || l.visible[0].ID != "3" {
		t.Fatalf("failed filter = %v", ids(l.visible))
	}
	l.filter = 0
	l.search.SetValue("done")
	l.apply()
	if len(l.visible) != 1 || l.visible[0].ID != "1" {
		t.Fatalf("search = %v", ids(l.visible))
	}
}

func TestReconcileDropsEchoedMessage(t *testing.T) {
	sm := newSessionModel(api.Session{ID: "1"}, nil)
	sm.pending = []pending{{text: "please add tests"}}
	sm.waiting = true
	sm.reconcile([]api.Activity{{Name: "a", Originator: "user", UserMessaged: &api.UserMessaged{UserMessage: "please add tests"}}})
	if len(sm.pending) != 0 || !sm.waiting {
		t.Fatalf("pending=%d waiting=%v", len(sm.pending), sm.waiting)
	}
	sm.reconcile([]api.Activity{{Name: "b", Originator: "agent", AgentMessaged: &api.AgentMessaged{AgentMessage: "ok"}}})
	if sm.waiting {
		t.Fatal("still waiting after agent reply")
	}
}

func TestSessionInitialPromptWhileLoading(t *testing.T) {
	m := newModel(t.Context(), api.New("k"), cache.Cache{})
	m.width, m.height = 80, 24
	m.openSession(api.Session{ID: "1", Prompt: "Fix the login redirect", State: api.StateInProgress})
	got := m.View()
	if !strings.Contains(got, "Fix the login redirect") || !strings.Contains(got, "Loading timeline…") {
		t.Fatalf("expected prompt and loading indicator in view:\n%s", got)
	}
}

func ids(ss []api.Session) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s.ID)
	}
	return out
}

func TestSelectionTargets(t *testing.T) {
	l := newList()
	l.resize(80, 20)
	now := time.Now()
	l.setSessions([]api.Session{
		{ID: "a", State: api.StateCompleted, UpdateTime: now},
		{ID: "b", State: api.StateCompleted, UpdateTime: now.Add(-time.Minute)},
		{ID: "c", State: api.StateCompleted, UpdateTime: now.Add(-2 * time.Minute)},
		{ID: "d", State: api.StateCompleted, UpdateTime: now.Add(-3 * time.Minute)},
	})
	// Nothing selected: the current row.
	if got := ids(l.targets()); len(got) != 1 || got[0] != "a" {
		t.Fatalf("targets = %v", got)
	}
	// Visual range a..c survives a refresh, plus a separate mark on d.
	l.visual, l.anchor, l.anchorID = true, 0, "a"
	l.move(2)
	l.setSessions(l.all)
	l.marked["d"] = true
	if got := ids(l.targets()); len(got) != 4 {
		t.Fatalf("targets = %v", got)
	}
	l.remove([]string{"a", "d"})
	l.clearSelection()
	if got := ids(l.visible); len(got) != 2 || got[0] != "b" {
		t.Fatalf("after delete = %v", got)
	}
}
