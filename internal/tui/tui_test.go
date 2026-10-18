package tui

import (
	"testing"
	"time"

	"github.com/teaspotato/jules/internal/api"
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

func ids(ss []api.Session) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s.ID)
	}
	return out
}
