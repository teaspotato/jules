package render

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/teaspotato/jules/internal/api"
)

func fixture(t *testing.T) []api.Activity {
	t.Helper()
	b, err := os.ReadFile("../api/testdata/activities.json")
	if err != nil {
		t.Fatal(err)
	}
	var r struct{ Activities []api.Activity }
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r.Activities
}

// plain renders without colours (a bytes.Buffer is not a TTY).
func plain() *Theme { return NewTheme(&bytes.Buffer{}) }

func TestTimelinePlain(t *testing.T) {
	acts := fixture(t)
	s := &api.Session{ID: "42", Prompt: "Fix the XSS bug"}
	out := plain().Timeline(s, acts, Options{Width: 80})

	for _, want := range []string{
		"● You", "Fix the XSS bug",
		"▤ Plan", "(approved)", "1. Fix the escape function", "2. Add a test",
		"✓ Plan approved",
		"Replaced the insecure escapeHtml implementation.",
		"▣ media image/png",
		"● Jules", "update the docs",
		"◇ Unsupported activity", "somethingNew",
		"━━ Session completed ━━", "commit: Fix XSS in escapeHtml",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("timeline missing %q\n%s", want, out)
		}
	}
	// The identical cumulative diff on a3/a4 is shown once, plus once on
	// completion.
	if n := strings.Count(out, "± Changes"); n != 2 {
		t.Errorf("change sets shown %d times, want 2\n%s", n, out)
	}
	for i, l := range strings.Split(out, "\n") {
		if strings.HasSuffix(l, " ") {
			t.Errorf("line %d has trailing spaces: %q", i, l)
		}
	}
}

func TestTimelineCacheMatches(t *testing.T) {
	acts := fixture(t)
	s := &api.Session{ID: "42"}
	th := plain()
	want := th.Timeline(s, acts, Options{Width: 80})
	cache := map[string]string{}
	first := th.Timeline(s, acts, Options{Width: 80, Cache: cache})
	second := th.Timeline(s, acts, Options{Width: 80, Cache: cache})
	if first != want || second != want {
		t.Fatal("cached rendering differs from uncached")
	}
}

func TestDiffStats(t *testing.T) {
	patch := "diff --git a/x.go b/x.go\nnew file mode 100644\nindex 0..1\n--- /dev/null\n+++ b/x.go\n@@ -0,0 +1,2 @@\n+a\n+b\n" +
		"diff --git a/y.go b/y.go\nindex 1..2 100644\n--- a/y.go\n+++ b/y.go\n@@ -1,2 +1,1 @@\n-old\n-older\n+new\n"
	st := DiffStats(patch)
	if len(st) != 2 || st[0].Path != "x.go" || st[0].Add != 2 || st[1].Path != "y.go" || st[1].Del != 2 || st[1].Add != 1 {
		t.Fatalf("stats = %+v", st)
	}
	plainPatch := "--- a/z\n+++ b/z\n@@ -1 +1 @@\n-x\n+y\n"
	st = DiffStats(plainPatch)
	if len(st) != 1 || st[0].Path != "z" || st[0].Add != 1 || st[0].Del != 1 {
		t.Fatalf("plain stats = %+v", st)
	}
}

func TestBashCollapses(t *testing.T) {
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, "line")
	}
	out := plain().Bash(&api.BashOutput{Command: "make", Output: strings.Join(lines, "\n"), ExitCode: 2}, false, 80)
	if !strings.Contains(out, "exit 2") || !strings.Contains(out, "12 earlier lines hidden") {
		t.Fatalf("bash = %s", out)
	}
}

func TestStateLabels(t *testing.T) {
	if StateLabel(api.StateAwaitingUserFeedback) != "needs input" || StateLabel("NEW_THING") != "new_thing" {
		t.Fatal("labels")
	}
}
