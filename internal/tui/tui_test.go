package tui

import (
	"errors"
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
	}, time.Time{})
	// Opens on blocked: awaiting approval and failed, attention first.
	if got := ids(l.visible); len(got) != 2 || got[0] != "2" || got[1] != "3" {
		t.Fatalf("blocked = %v", got)
	}
	l.filter = tabDone
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
	l.filter = tabDone
	now := time.Now()
	l.setSessions([]api.Session{
		{ID: "a", State: api.StateCompleted, UpdateTime: now},
		{ID: "b", State: api.StateCompleted, UpdateTime: now.Add(-time.Minute)},
		{ID: "c", State: api.StateCompleted, UpdateTime: now.Add(-2 * time.Minute)},
		{ID: "d", State: api.StateCompleted, UpdateTime: now.Add(-3 * time.Minute)},
	}, time.Time{})
	// Nothing selected: the current row.
	if got := ids(l.targets()); len(got) != 1 || got[0] != "a" {
		t.Fatalf("targets = %v", got)
	}
	// Visual range a..c survives a refresh, plus a separate mark on d.
	l.visual, l.anchor, l.anchorID = true, 0, "a"
	l.move(2)
	l.setSessions(l.all, time.Time{})
	l.marked["d"] = true
	if got := ids(l.targets()); len(got) != 4 {
		t.Fatalf("targets = %v", got)
	}
	l.clearSelection()
	for _, id := range []string{"a", "d"} {
		l.startOp(actDelete, id)
	}
	l.finishOp(actDelete, "a", nil)
	if j := l.finishOp(actDelete, "d", nil); j == nil || j.done != 2 {
		t.Fatalf("job = %+v", j)
	}
	if got := ids(l.visible); len(got) != 2 || got[0] != "b" {
		t.Fatalf("after delete = %v", got)
	}
}

func TestTabsAndArchive(t *testing.T) {
	l := newList()
	l.setSessions([]api.Session{
		{ID: "q", State: api.StateQueued},
		{ID: "p", State: api.StatePaused},
		{ID: "c", State: api.StateCompleted},
		{ID: "x", State: api.StateFailed, Archived: true},
		{ID: "n", State: "SOMETHING_NEW"},
	}, time.Time{})
	want := map[string]int{"q": tabWorking, "n": tabWorking, "p": tabBlocked, "c": tabDone, "x": tabArchived}
	for _, s := range l.all {
		if got := tabOf(s); got != want[s.ID] {
			t.Errorf("tabOf(%s) = %s", s.ID, tabNames[got])
		}
	}
	before := time.Now()
	l.startOp(actArchive, "c")
	if l.progress() != "archiving 0/1" {
		t.Fatalf("progress = %q", l.progress())
	}
	l.finishOp(actArchive, "c", nil)
	// A fetch that began before the archive landed must not undo it.
	stale := []api.Session{{ID: "c", State: api.StateCompleted}, {ID: "x", State: api.StateFailed, Archived: true}}
	l.setSessions(stale, before.Add(-time.Second))
	l.filter = tabArchived
	l.apply()
	if got := ids(l.visible); len(got) != 2 {
		t.Fatalf("archived = %v", got)
	}
}

func TestEditsExpire(t *testing.T) {
	l := newList()
	l.setSessions([]api.Session{{ID: "a", State: api.StateCompleted}}, time.Time{})
	l.startOp(actDelete, "a")
	l.finishOp(actDelete, "a", nil)
	// A later fetch reflects the delete; its edit is dropped, so a session
	// that somehow comes back is shown again.
	l.setSessions([]api.Session{{ID: "a", State: api.StateCompleted}}, time.Now().Add(time.Second))
	if len(l.edits) != 0 || len(l.all) != 1 {
		t.Fatalf("edits = %v, all = %v", l.edits, ids(l.all))
	}
	// A failed call leaves the session alone.
	l.startOp(actArchive, "a")
	if j := l.finishOp(actArchive, "a", errors.New("boom")); j == nil || j.failed != 1 || l.all[0].Archived {
		t.Fatalf("job = %+v", j)
	}
}

func TestWatchRingsOnBlocked(t *testing.T) {
	m := &model{busy: map[string]int{}}
	working := []api.Session{{ID: "a", State: api.StateInProgress, Title: "Fix it"}}
	if m.watch(working) != nil {
		t.Fatal("rang before the first live fetch")
	}
	m.groups = map[string]string{}
	if m.watch(working) != nil {
		t.Fatal("rang on first sight")
	}
	if m.watch([]api.Session{{ID: "a", State: api.StateAwaitingPlanApproval, Title: "Fix it"}}) == nil {
		t.Fatal("no bell when a working session became blocked")
	}
	if !strings.Contains(m.toast, "Fix it") {
		t.Fatalf("toast = %q", m.toast)
	}
}
