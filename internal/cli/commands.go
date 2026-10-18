package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/teaspotato/jules/internal/api"
	"github.com/teaspotato/jules/internal/auth"
	"github.com/teaspotato/jules/internal/browser"
	"github.com/teaspotato/jules/internal/render"
)

func termWidth(f *os.File) int {
	w, _, err := term.GetSize(int(f.Fd()))
	if err != nil {
		return 0
	}
	return w
}

func readStdin() (string, error) {
	if isatty := term.IsTerminal(int(os.Stdin.Fd())); isatty {
		fmt.Fprintln(os.Stderr, "Reading from stdin; end with Ctrl-D.")
	}
	b, err := io.ReadAll(os.Stdin)
	return strings.TrimSpace(string(b)), err
}

// ---- sources ---------------------------------------------------------------

func newSourcesCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "sources",
		Short: "List connected GitHub repositories",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			srcs, err := c.ListSources(cmd.Context())
			if err != nil {
				return err
			}
			a.cache.PutSources(srcs)
			if a.jsonOut {
				return a.printJSON(srcs)
			}
			t := a.theme()
			for _, s := range srcs {
				line := s.Slug()
				if g := s.GitHubRepo; g != nil {
					if g.DefaultBranch != nil {
						line += t.Faint.Render(" @" + g.DefaultBranch.DisplayName)
					}
					if g.IsPrivate {
						line += t.Faint.Render(" private")
					}
				}
				fmt.Fprintln(a.out, line)
			}
			return nil
		},
	}
}

// ---- sessions --------------------------------------------------------------

// stateAliases lets scripts say --state awaiting instead of the enum.
var stateAliases = map[string][]string{
	"awaiting":  {api.StateAwaitingPlanApproval, api.StateAwaitingUserFeedback},
	"attention": {api.StateAwaitingPlanApproval, api.StateAwaitingUserFeedback},
	"approval":  {api.StateAwaitingPlanApproval},
	"input":     {api.StateAwaitingUserFeedback},
	"feedback":  {api.StateAwaitingUserFeedback},
	"active":    {api.StateQueued, api.StatePlanning, api.StateInProgress, api.StateAwaitingPlanApproval, api.StateAwaitingUserFeedback, api.StatePaused},
	"running":   {api.StateQueued, api.StatePlanning, api.StateInProgress},
	"done":      {api.StateCompleted},
	"completed": {api.StateCompleted},
	"failed":    {api.StateFailed},
	"paused":    {api.StatePaused},
}

func expandStates(in []string) (map[string]bool, error) {
	if len(in) == 0 {
		return nil, nil
	}
	m := map[string]bool{}
	for _, raw := range in {
		for _, s := range strings.Split(raw, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if al, ok := stateAliases[strings.ToLower(s)]; ok {
				for _, st := range al {
					m[st] = true
				}
				continue
			}
			up := strings.ToUpper(strings.ReplaceAll(s, "-", "_"))
			switch up {
			case api.StateQueued, api.StatePlanning, api.StateAwaitingPlanApproval, api.StateAwaitingUserFeedback,
				api.StateInProgress, api.StatePaused, api.StateFailed, api.StateCompleted:
				m[up] = true
			default:
				return nil, withCode(ExitUsage, fmt.Errorf("unknown state %q", s))
			}
		}
	}
	return m, nil
}

// SessionFilter is the client-side filter shared with the TUI.
type sessionFilter struct {
	states map[string]bool
	repo   string
	text   string
}

func (f sessionFilter) match(s api.Session) bool {
	if f.states != nil && !f.states[s.State] {
		return false
	}
	if f.repo != "" && !strings.EqualFold(s.Repo(), f.repo) && !strings.HasSuffix(strings.ToLower(s.Repo()), "/"+strings.ToLower(f.repo)) {
		return false
	}
	if f.text != "" {
		hay := strings.ToLower(s.Title + "\n" + s.Prompt + "\n" + s.ID)
		if !strings.Contains(hay, strings.ToLower(f.text)) {
			return false
		}
	}
	return true
}

func newSessionsCmd(a *app) *cobra.Command {
	var (
		states   []string
		repo     string
		search   string
		limit    int
		archived bool
	)
	cmd := &cobra.Command{
		Use:     "sessions",
		Aliases: []string{"ls", "list"},
		Short:   "List sessions (needs-attention first)",
		Example: `  jules sessions --state awaiting
  jules sessions --repo owner/repo --json | jq -r '.[].id'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := expandStates(states)
			if err != nil {
				return err
			}
			c, err := a.api()
			if err != nil {
				return err
			}
			all, err := c.ListSessions(cmd.Context(), archived)
			if err != nil {
				return err
			}
			if !archived {
				a.cache.PutSessions(all)
			}
			f := sessionFilter{states: st, repo: repo, text: search}
			var out []api.Session
			for _, s := range all {
				if f.match(s) {
					out = append(out, s)
				}
			}
			sortSessions(out)
			if limit > 0 && len(out) > limit {
				out = out[:limit]
			}
			if a.jsonOut {
				if out == nil {
					out = []api.Session{}
				}
				return a.printJSON(out)
			}
			t := a.theme()
			w := a.width()
			repoW := render.RepoWidth(out)
			for _, s := range out {
				fmt.Fprintln(a.out, t.Faint.Render(fmt.Sprintf("%-20s ", s.ID))+t.SessionRow(s, w-21, repoW, search))
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVarP(&states, "state", "s", nil, "filter by state: awaiting, approval, input, active, running, completed, failed, or an API state name")
	cmd.Flags().StringVarP(&repo, "repo", "r", "", "filter by repository (owner/repo or repo)")
	cmd.Flags().StringVarP(&search, "search", "q", "", "filter by text in title, prompt or id")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "show at most n sessions")
	cmd.Flags().BoolVar(&archived, "archived", false, "include archived sessions")
	return cmd
}

// sortSessions puts sessions needing attention first, then newest updated.
func sortSessions(ss []api.Session) {
	sort.SliceStable(ss, func(i, j int) bool {
		ai, aj := api.NeedsAttention(ss[i].State), api.NeedsAttention(ss[j].State)
		if ai != aj {
			return ai
		}
		return ss[i].UpdateTime.After(ss[j].UpdateTime)
	})
}

// ---- show ------------------------------------------------------------------

func newShowCmd(a *app) *cobra.Command {
	var (
		follow, diffs, full bool
		interval            time.Duration
	)
	cmd := &cobra.Command{
		Use:   "show <session>",
		Short: "Print a session's timeline",
		Long: `Print a session's timeline: prompt, plan, messages, progress and artifacts.

With --follow, keep printing new activities as they arrive (like tail -f)
until the session completes or fails. With --json, print the session and then
one activity per line (NDJSON).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := a.resolveSession(ctx, args[0])
			if err != nil {
				return err
			}
			c, _ := a.api()
			tl := api.NewTimeline(s.ID, nil)
			if _, err := tl.Refresh(ctx, c); err != nil {
				return err
			}
			a.cache.PutActivities(s.ID, tl.Activities)

			t := a.theme()
			o := render.Options{Width: a.width(), ExpandDiffs: diffs, ExpandBash: full, FullPrompt: full, PrevPatch: new(string)}
			if a.jsonOut {
				if err := a.printNDJSON(map[string]any{"session": s}); err != nil {
					return err
				}
				for _, act := range tl.Activities {
					if err := a.printNDJSON(map[string]any{"activity": act}); err != nil {
						return err
					}
				}
			} else {
				fmt.Fprintln(a.out, t.SessionHeader(s, o.Width))
				fmt.Fprintln(a.out)
				o.Approved = render.ApprovedPlans(tl.Activities)
				fmt.Fprintln(a.out, t.Timeline(s, tl.Activities, o))
			}
			if !follow {
				return nil
			}
			return a.follow(ctx, s, tl, o, interval, func(acts []api.Activity) error {
				for _, act := range acts {
					if a.jsonOut {
						if err := a.printNDJSON(map[string]any{"activity": act}); err != nil {
							return err
						}
						continue
					}
					if act.PlanApproved != nil {
						o.Approved[act.PlanApproved.PlanID] = true
					}
					if s := t.Activity(act, o); s != "" {
						fmt.Fprintln(a.out)
						fmt.Fprintln(a.out, s)
					}
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new activities until the session ends")
	cmd.Flags().BoolVarP(&diffs, "diff", "d", false, "show full diffs instead of file summaries")
	cmd.Flags().BoolVar(&full, "full", false, "show the full prompt and bash output")
	cmd.Flags().DurationVar(&interval, "interval", 4*time.Second, "poll interval for --follow")
	return cmd
}

// printNDJSON writes v as one compact JSON line.
func (a *app) printNDJSON(v any) error {
	return json.NewEncoder(a.out).Encode(v)
}

// follow polls until the session reaches a terminal state, calling emit for
// each batch of new activities. It backs off on errors.
func (a *app) follow(ctx context.Context, s *api.Session, tl *api.Timeline, o render.Options, interval time.Duration, emit func([]api.Activity) error) error {
	c, _ := a.api()
	state := s.State
	backoff := interval
	for !api.IsTerminal(state) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		fresh, err := tl.Refresh(ctx, c)
		if err == nil {
			var cur *api.Session
			cur, err = c.GetSession(ctx, s.ID)
			if err == nil {
				state = cur.State
			}
		}
		if err != nil {
			if api.IsAuth(err) || api.IsNotFound(err) {
				return err
			}
			fmt.Fprintln(a.errOut, "jules: poll failed, retrying:", err)
			backoff = min(backoff*2, time.Minute)
			continue
		}
		backoff = interval
		if len(fresh) > 0 {
			a.cache.PutActivities(s.ID, tl.Activities)
			if err := emit(fresh); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---- new -------------------------------------------------------------------

func newNewCmd(a *app) *cobra.Command {
	var (
		repo, branch, title string
		approve, autoPR     bool
		follow              bool
	)
	cmd := &cobra.Command{
		Use:   "new [prompt]",
		Short: "Start a new session",
		Example: `  jules new --repo owner/repo "Add a --verbose flag"
  cat task.md | jules new --repo owner/repo --approve-plan --auto-pr`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			prompt, err := readArgOrStdin(args, readStdin)
			if err != nil {
				return err
			}
			if strings.TrimSpace(prompt) == "" {
				return withCode(ExitUsage, errors.New("prompt is empty"))
			}
			c, err := a.api()
			if err != nil {
				return err
			}
			req := api.CreateSessionRequest{Prompt: prompt, Title: title, RequirePlanApproval: approve}
			if autoPR {
				req.AutomationMode = api.AutomationAutoCreatePR
			}
			if repo != "" {
				if branch == "" {
					src, err := c.GetSource(ctx, repo)
					if err != nil {
						return fmt.Errorf("looking up %s: %w", repo, err)
					}
					if src.GitHubRepo != nil && src.GitHubRepo.DefaultBranch != nil {
						branch = src.GitHubRepo.DefaultBranch.DisplayName
					}
				}
				req.SourceContext = &api.SourceContext{
					Source:            api.SourceName(repo),
					GitHubRepoContext: &api.GitHubRepoContext{StartingBranch: branch},
				}
			}
			s, err := c.CreateSession(ctx, req)
			if err != nil {
				return err
			}
			a.cache.AddPrompt(prompt)
			if a.jsonOut {
				if err := a.printNDJSON(s); err != nil {
					return err
				}
			} else {
				t := a.theme()
				fmt.Fprintln(a.out, t.OK.Render("✓ Started ")+t.Bold.Render(s.ID)+"  "+t.Link.Render(s.URL))
			}
			if follow {
				tl := api.NewTimeline(s.ID, nil)
				t := a.theme()
				o := render.Options{Width: a.width(), PrevPatch: new(string), Approved: map[string]bool{}}
				return a.follow(ctx, s, tl, o, 4*time.Second, func(acts []api.Activity) error {
					for _, act := range acts {
						if a.jsonOut {
							a.printNDJSON(map[string]any{"activity": act})
							continue
						}
						if s := t.Activity(act, o); s != "" {
							fmt.Fprintln(a.out)
							fmt.Fprintln(a.out, s)
						}
					}
					return nil
				})
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&repo, "repo", "r", "", "repository (owner/repo); omit to start without a repo")
	cmd.Flags().StringVarP(&branch, "branch", "b", "", "starting branch (default: the repo's default branch)")
	cmd.Flags().StringVarP(&title, "title", "t", "", "session title (generated if omitted)")
	cmd.Flags().BoolVar(&approve, "approve-plan", false, "require plan approval before Jules starts work")
	cmd.Flags().BoolVar(&autoPR, "auto-pr", false, "open a pull request automatically when done")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow the session's activity after creating it")
	return cmd
}

// ---- send ------------------------------------------------------------------

func newSendCmd(a *app) *cobra.Command {
	var (
		wait    bool
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "send <session> [message]",
		Short: "Send a message to a session",
		Long: `Send a message to a session. The message is read from stdin when omitted.

The API returns nothing for a sent message; with --wait, jules polls until the
agent responds (any new agent activity after your message) and prints it.

Recently completed sessions can be messaged: Jules resumes them (the state goes
back to IN_PROGRESS) and replies. Very old sessions may not resume; one finished
months earlier turned FAILED without replying.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			msg, err := readArgOrStdin(args[1:], readStdin)
			if err != nil {
				return err
			}
			if strings.TrimSpace(msg) == "" {
				return withCode(ExitUsage, errors.New("message is empty"))
			}
			s, err := a.resolveSession(ctx, args[0])
			if err != nil {
				return err
			}
			c, _ := a.api()
			var tl *api.Timeline
			if wait {
				// Snapshot first so only replies after this point count.
				tl = api.NewTimeline(s.ID, nil)
				if _, err := tl.Refresh(ctx, c); err != nil {
					return err
				}
			}
			if err := c.SendMessage(ctx, s.ID, msg); err != nil {
				return err
			}
			t := a.theme()
			if !a.jsonOut {
				fmt.Fprintln(a.errOut, t.OK.Render("✓ Sent")+" to "+s.ID)
			}
			if !wait {
				if a.jsonOut {
					return a.printNDJSON(map[string]any{"sent": true, "session": s.Name})
				}
				return nil
			}
			reply, err := waitForReply(ctx, c, tl, s.State, timeout)
			if err != nil {
				if a.jsonOut {
					for _, act := range reply {
						a.printNDJSON(map[string]any{"activity": act})
					}
				}
				return err
			}
			if a.jsonOut {
				for _, act := range reply {
					a.printNDJSON(map[string]any{"activity": act})
				}
				return nil
			}
			o := render.Options{Width: a.width(), PrevPatch: new(string), Approved: map[string]bool{}}
			for _, act := range reply {
				if act.UserMessaged != nil {
					continue
				}
				if s := t.Activity(act, o); s != "" {
					fmt.Fprintln(a.out, s)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&wait, "wait", "w", false, "wait for the agent's response and print it")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "give up waiting after this long")
	return cmd
}

// waitForReply polls until an agent activity appears after the user's
// message, returning everything new (the echo of the user's message
// included). Since sendMessage returns no ID, the reply is matched by
// order: the first agent-originated activity after our userMessaged.
//
// startState is the session's state when the message was sent. A finished
// session that is messaged stays COMPLETED for a minute or so before Jules
// resumes it, so COMPLETED only ends the wait once the session has resumed.
func waitForReply(ctx context.Context, c *api.Client, tl *api.Timeline, startState string, timeout time.Duration) ([]api.Activity, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var got []api.Activity
	sawEcho := false
	resumed := !api.IsTerminal(startState)
	delay := 2 * time.Second
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return got, withCode(ExitTimeout, fmt.Errorf("no reply within %s", timeout))
			}
			return got, ctx.Err()
		case <-time.After(delay):
		}
		fresh, err := tl.Refresh(ctx, c)
		if err != nil {
			if api.IsAuth(err) || api.IsNotFound(err) {
				return got, err
			}
			delay = min(delay*2, 30*time.Second)
			continue
		}
		delay = min(delay+time.Second, 5*time.Second)
		for _, act := range fresh {
			got = append(got, act)
			if act.UserMessaged != nil || act.Originator == "user" {
				sawEcho = true
				continue
			}
			if act.PlanApproved != nil {
				continue
			}
			// Ignore in-flight progress updates until our message echo is
			// recorded, so an active session doesn't return before seeing it.
			if act.ProgressUpdated != nil && !sawEcho {
				continue
			}
			if act.SessionFailed != nil {
				reason := act.SessionFailed.Reason
				if reason == "" {
					reason = fmt.Sprintf("session %s failed", tl.SessionID)
				}
				return got, withCode(ExitFailed, errors.New(reason))
			}
			return got, nil
		}
		// A session can reach a terminal or paused state without emitting a
		// reply activity, so watch its state too.
		if s, err := c.GetSession(ctx, tl.SessionID); err == nil {
			if !api.IsTerminal(s.State) {
				resumed = true
			}
			switch s.State {
			case api.StateFailed:
				if resumed || startState != api.StateFailed {
					return got, withCode(ExitFailed, fmt.Errorf("session %s failed without replying", s.ID))
				}
			case api.StateCompleted:
				if resumed {
					return got, nil
				}
			case api.StatePaused:
				return got, fmt.Errorf("session %s is paused", s.ID)
			}
		}
	}
}

// ---- approve ---------------------------------------------------------------

func newApproveCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "approve <session>",
		Short: "Approve the pending plan",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.resolveSession(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if s.State != api.StateAwaitingPlanApproval {
				fmt.Fprintf(a.errOut, "jules: warning: session is %s, not awaiting plan approval\n", render.StateLabel(s.State))
			}
			c, _ := a.api()
			if err := c.ApprovePlan(cmd.Context(), s.ID); err != nil {
				return err
			}
			if a.jsonOut {
				return a.printNDJSON(map[string]any{"approved": true, "session": s.Name})
			}
			fmt.Fprintln(a.out, a.theme().OK.Render("✓ Plan approved")+" for "+s.ID)
			return nil
		},
	}
}

// ---- wait ------------------------------------------------------------------

func newWaitCmd(a *app) *cobra.Command {
	var (
		until    string
		timeout  time.Duration
		interval time.Duration
	)
	cmd := &cobra.Command{
		Use:   "wait <session>",
		Short: "Block until a session reaches a state",
		Long: `Block until a session reaches a state, then print it.

--until completed     wait for COMPLETED or FAILED (default)
--until needs-input   also stop when the session needs approval or input
--until <STATE>       stop at that API state (or completion/failure)

Exit codes: 0 reached, 5 session failed, 6 timed out.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			stop := map[string]bool{api.StateCompleted: true, api.StateFailed: true}
			switch strings.ToLower(until) {
			case "", "completed", "done", "terminal":
			case "needs-input", "attention", "awaiting", "input":
				stop[api.StateAwaitingPlanApproval] = true
				stop[api.StateAwaitingUserFeedback] = true
			default:
				st, err := expandStates([]string{until})
				if err != nil {
					return err
				}
				for k := range st {
					stop[k] = true
				}
			}
			s, err := a.resolveSession(ctx, args[0])
			if err != nil {
				return err
			}
			c, _ := a.api()
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			delay := interval
			for !stop[s.State] {
				select {
				case <-ctx.Done():
					if errors.Is(ctx.Err(), context.DeadlineExceeded) {
						return withCode(ExitTimeout, fmt.Errorf("session still %s after %s", render.StateLabel(s.State), timeout))
					}
					return ctx.Err()
				case <-time.After(delay):
				}
				cur, err := c.GetSession(ctx, s.ID)
				if err != nil {
					if api.IsAuth(err) || api.IsNotFound(err) {
						return err
					}
					delay = min(delay*2, time.Minute)
					continue
				}
				delay = interval
				s = cur
			}
			if a.jsonOut {
				a.printNDJSON(s)
			} else {
				fmt.Fprintln(a.out, s.ID+" "+a.theme().StateBadge(s.State))
			}
			if s.State == api.StateFailed {
				return withCode(ExitFailed, fmt.Errorf("session %s failed", s.ID))
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&until, "until", "u", "completed", "completed | needs-input | a state name")
	cmd.Flags().DurationVar(&timeout, "timeout", 24*time.Hour, "give up after this long (exit 6)")
	cmd.Flags().DurationVar(&interval, "interval", 5*time.Second, "poll interval")
	return cmd
}

// ---- patch -----------------------------------------------------------------

func newPatchCmd(a *app) *cobra.Command {
	var showMsg bool
	cmd := &cobra.Command{
		Use:   "patch <session>",
		Short: "Print the latest change set as a unified diff",
		Example: `  jules patch 3186 | git apply
  jules patch 3186 > fix.patch`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := a.resolveSession(ctx, args[0])
			if err != nil {
				return err
			}
			cs := sessionChangeSet(s)
			if cs == nil {
				c, _ := a.api()
				acts, err := c.ListActivities(ctx, s.ID)
				if err != nil {
					return err
				}
				cs = api.LatestChangeSet(acts)
			}
			if cs == nil || cs.GitPatch == nil {
				return withCode(ExitNotFound, fmt.Errorf("session %s has no changes yet", s.ID))
			}
			if a.jsonOut {
				return a.printNDJSON(cs)
			}
			if showMsg && cs.GitPatch.SuggestedCommitMessage != "" {
				fmt.Fprintln(a.errOut, cs.GitPatch.SuggestedCommitMessage)
			}
			patch := cs.GitPatch.UnidiffPatch
			if !strings.HasSuffix(patch, "\n") {
				patch += "\n"
			}
			if a.tty() {
				fmt.Fprint(a.out, a.theme().Diff(patch, a.width())+"\n")
				return nil
			}
			_, err = io.WriteString(a.out, patch)
			return err
		},
	}
	cmd.Flags().BoolVarP(&showMsg, "message", "m", false, "also print the suggested commit message to stderr")
	return cmd
}

// sessionChangeSet prefers the final change set in the session's outputs.
func sessionChangeSet(s *api.Session) *api.ChangeSet {
	for i := len(s.Outputs) - 1; i >= 0; i-- {
		if cs := s.Outputs[i].ChangeSet; cs != nil && cs.GitPatch != nil {
			return cs
		}
	}
	return nil
}

// ---- archive / unarchive / delete -----------------------------------------

func newArchiveCmd(a *app, archive bool) *cobra.Command {
	use, short, done := "archive", "Archive sessions", "Archived"
	if !archive {
		use, short, done = "unarchive", "Unarchive sessions", "Unarchived"
	}
	return &cobra.Command{
		Use:   use + " <session>...",
		Short: short,
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			for _, arg := range args {
				s, err := a.resolveSession(cmd.Context(), arg)
				if err != nil {
					return err
				}
				if archive {
					err = c.ArchiveSession(cmd.Context(), s.ID)
				} else {
					err = c.UnarchiveSession(cmd.Context(), s.ID)
				}
				if err != nil {
					return err
				}
				if a.jsonOut {
					a.printNDJSON(map[string]any{strings.ToLower(done): true, "session": s.Name})
				} else {
					fmt.Fprintln(a.out, a.theme().OK.Render("✓ "+done)+" "+s.ID)
				}
			}
			return nil
		},
	}
}

func newDeleteCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <session>...",
		Short: "Permanently delete sessions",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			var in *bufio.Reader
			if !yes {
				if !term.IsTerminal(int(os.Stdin.Fd())) {
					return withCode(ExitUsage, errors.New("refusing to delete without --yes when stdin is not a terminal"))
				}
				in = bufio.NewReader(os.Stdin)
			}
			for _, arg := range args {
				s, err := a.resolveSession(cmd.Context(), arg)
				if err != nil {
					return err
				}
				if !yes {
					fmt.Fprintf(a.errOut, "Delete %s (%s)? This cannot be undone. [y/N] ", s.ID, s.DisplayTitle())
					line, _ := in.ReadString('\n')
					if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "y") {
						fmt.Fprintln(a.errOut, "skipped")
						continue
					}
				}
				if err := c.DeleteSession(cmd.Context(), s.ID); err != nil {
					return err
				}
				if a.jsonOut {
					a.printNDJSON(map[string]any{"deleted": true, "session": s.Name})
				} else {
					fmt.Fprintln(a.out, a.theme().OK.Render("✓ Deleted")+" "+s.ID)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

// ---- open ------------------------------------------------------------------

func newOpenCmd(a *app) *cobra.Command {
	var pr bool
	cmd := &cobra.Command{
		Use:   "open <session>",
		Short: "Open a session (or its PR) in the browser",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.resolveSession(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			url := s.URL
			if pr {
				prs := s.PullRequests()
				if len(prs) == 0 {
					return withCode(ExitNotFound, fmt.Errorf("session %s has no pull request", s.ID))
				}
				url = prs[len(prs)-1].URL
			}
			if a.jsonOut || !a.tty() {
				fmt.Fprintln(a.out, url)
				return nil
			}
			return browser.Open(url)
		},
	}
	cmd.Flags().BoolVarP(&pr, "pr", "p", false, "open the pull request instead")
	return cmd
}

// ---- auth ------------------------------------------------------------------

func newAuthCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "auth", Short: "Manage the Jules API key"}
	login := &cobra.Command{
		Use:   "login",
		Short: "Store an API key in the OS keychain (validates it first)",
		Long: `Store an API key in the OS keychain. Create a key in the Jules web app
(jules.google.com, Settings, API keys). The key is read from stdin, so it
never appears in your shell history.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var key string
			if term.IsTerminal(int(os.Stdin.Fd())) {
				fmt.Fprint(a.errOut, "Jules API key: ")
				b, err := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Fprintln(a.errOut)
				if err != nil {
					return err
				}
				key = string(b)
			} else {
				b, err := io.ReadAll(os.Stdin)
				if err != nil {
					return err
				}
				key = string(b)
			}
			key = strings.TrimSpace(key)
			if key == "" {
				return withCode(ExitUsage, errors.New("empty key"))
			}
			c := api.New(key)
			if _, err := c.ListSourcesPage(cmd.Context(), api.ListOptions{PageSize: 1}); err != nil {
				return withCode(ExitAuth, fmt.Errorf("key rejected: %w", err))
			}
			if err := auth.Store(key); err != nil {
				return fmt.Errorf("saving to keychain: %w (set %s instead)", err, auth.EnvVar)
			}
			fmt.Fprintln(a.out, a.theme().OK.Render("✓ Key verified and saved to the OS keychain"))
			return nil
		},
	}
	logout := &cobra.Command{
		Use:   "logout",
		Short: "Remove the key from the OS keychain",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := auth.Delete(); err != nil {
				return err
			}
			fmt.Fprintln(a.out, "Removed the key from the OS keychain.")
			return nil
		},
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Show where the key comes from and check it works",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			key, origin, err := auth.Key()
			if err != nil {
				return withCode(ExitAuth, err)
			}
			c, _ := a.api()
			start := time.Now()
			_, err = c.ListSourcesPage(cmd.Context(), api.ListOptions{PageSize: 1})
			lat := time.Since(start).Round(time.Millisecond)
			if a.jsonOut {
				return a.printNDJSON(map[string]any{"source": string(origin), "key": auth.Mask(key), "valid": err == nil, "latencyMs": lat.Milliseconds()})
			}
			t := a.theme()
			fmt.Fprintf(a.out, "key   %s from %s\n", auth.Mask(key), origin)
			if err != nil {
				fmt.Fprintln(a.out, "check "+t.Bad.Render("failed"))
				return withCode(ExitAuth, err)
			}
			fmt.Fprintf(a.out, "check %s (%s)\n", t.OK.Render("ok"), lat)
			return nil
		},
	}
	cmd.AddCommand(login, logout, status)
	return cmd
}
