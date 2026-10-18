// Package render turns sessions and activities into styled terminal text.
// It is shared by the CLI (printed once) and the TUI (shown in a viewport).
package render

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/teaspotato/jules/internal/api"
)

// Theme holds every style; build one per output with NewTheme.
type Theme struct {
	R *lipgloss.Renderer

	Title, Faint, Bold, Accent, Link lipgloss.Style
	Agent, User, System              lipgloss.Style
	Add, Del, Hunk, FileHdr          lipgloss.Style
	OK, Bad, Warn                    lipgloss.Style
	Card, Banner                     lipgloss.Style
	Match                            lipgloss.Style

	color bool
	md    map[int]*glamour.TermRenderer
	dark  bool
}

// NewTheme styles output for w, honouring NO_COLOR and non-TTY writers.
func NewTheme(w io.Writer) *Theme {
	r := lipgloss.NewRenderer(w)
	if os.Getenv("NO_COLOR") != "" {
		r.SetColorProfile(termenv.Ascii)
	}
	t := &Theme{R: r, md: map[int]*glamour.TermRenderer{}}
	t.color = r.ColorProfile() != termenv.Ascii
	t.dark = r.HasDarkBackground()

	c := func(light, dark string) lipgloss.AdaptiveColor {
		return lipgloss.AdaptiveColor{Light: light, Dark: dark}
	}
	accent := c("#5A3FD9", "#A99BFF")
	green := c("#1A7F37", "#57D38C")
	red := c("#CF222E", "#FF7B72")
	amber := c("#9A6700", "#E3B341")
	blue := c("#0969DA", "#79C0FF")
	faint := c("#6E7781", "#8B949E")

	t.Title = r.NewStyle().Bold(true)
	t.Faint = r.NewStyle().Foreground(faint)
	t.Bold = r.NewStyle().Bold(true)
	t.Accent = r.NewStyle().Foreground(accent)
	t.Link = r.NewStyle().Foreground(blue).Underline(true)
	t.Agent = r.NewStyle().Foreground(accent).Bold(true)
	t.User = r.NewStyle().Foreground(blue).Bold(true)
	t.System = r.NewStyle().Foreground(faint).Bold(true)
	t.Add = r.NewStyle().Foreground(green)
	t.Del = r.NewStyle().Foreground(red)
	t.Hunk = r.NewStyle().Foreground(blue)
	t.FileHdr = r.NewStyle().Bold(true).Foreground(amber)
	t.OK = r.NewStyle().Foreground(green).Bold(true)
	t.Bad = r.NewStyle().Foreground(red).Bold(true)
	t.Warn = r.NewStyle().Foreground(amber).Bold(true)
	t.Card = r.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(faint).Padding(0, 1)
	t.Banner = r.NewStyle().Bold(true).Padding(0, 1)
	t.Match = r.NewStyle().Bold(true).Foreground(c("#1F2328", "#0D1117")).Background(amber)
	return t
}

// Color reports whether the theme emits ANSI colours.
func (t *Theme) Color() bool { return t.color }

// StateBadge is a short coloured label for a session state.
func (t *Theme) StateBadge(state string) string {
	label := StateLabel(state)
	switch state {
	case api.StateCompleted:
		return t.OK.Render(label)
	case api.StateFailed:
		return t.Bad.Render(label)
	case api.StateAwaitingPlanApproval, api.StateAwaitingUserFeedback:
		return t.Warn.Render(label)
	case api.StateInProgress, api.StatePlanning, api.StateQueued:
		return t.Accent.Render(label)
	}
	return t.Faint.Render(label)
}

// StateLabel is the human name of a state.
func StateLabel(state string) string {
	switch state {
	case api.StateQueued:
		return "queued"
	case api.StatePlanning:
		return "planning"
	case api.StateAwaitingPlanApproval:
		return "needs approval"
	case api.StateAwaitingUserFeedback:
		return "needs input"
	case api.StateInProgress:
		return "working"
	case api.StatePaused:
		return "paused"
	case api.StateFailed:
		return "failed"
	case api.StateCompleted:
		return "completed"
	case "", api.StateUnspecified:
		return "unknown"
	}
	return strings.ToLower(state)
}

// Ago is a compact relative time ("3m", "2h", "5d").
func Ago(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	if t.Year() == time.Now().Year() {
		return t.Local().Format("Jan 2")
	}
	return t.Local().Format("Jan 2006")
}

// Markdown renders md at width; plain text when colours are off.
func (t *Theme) Markdown(md string, width int) string {
	md = strings.TrimSpace(md)
	if md == "" {
		return ""
	}
	if !t.color {
		return wrap(md, width)
	}
	if width < 20 {
		width = 20
	}
	r, ok := t.md[width]
	if !ok {
		style := "light"
		if t.dark {
			style = "dark"
		}
		var err error
		r, err = glamour.NewTermRenderer(glamour.WithStandardStyle(style), glamour.WithWordWrap(width), glamour.WithEmoji())
		if err != nil {
			return wrap(md, width)
		}
		t.md[width] = r
	}
	out, err := r.Render(md)
	if err != nil {
		return wrap(md, width)
	}
	return strings.Trim(trimRight(out), "\n")
}

func wrap(s string, width int) string {
	if width <= 0 {
		return s
	}
	return ansi.Wrap(s, width, "")
}

// trimRight drops the trailing padding glamour adds to every line.
func trimRight(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
		// Padding may sit just before a trailing reset sequence.
		for strings.HasSuffix(lines[i], " \x1b[0m") {
			lines[i] = strings.TrimSuffix(lines[i], " \x1b[0m") + "\x1b[0m"
		}
	}
	return strings.Join(lines, "\n")
}

// PerLine styles each line separately, so multi-line text is not padded
// into a block the way lipgloss does for a single Render call.
func PerLine(st lipgloss.Style, s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = st.Render(l)
	}
	return strings.Join(lines, "\n")
}

// Indent prefixes every line of s.
func Indent(s, prefix string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "" {
			lines[i] = prefix + l
		} else {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

// SessionHeader is the block shown above a timeline.
func (t *Theme) SessionHeader(s *api.Session, width int) string {
	var b strings.Builder
	b.WriteString(t.Title.Render(s.DisplayTitle()))
	b.WriteString("  " + t.StateBadge(s.State))
	if s.Archived {
		b.WriteString(" " + t.Faint.Render("archived"))
	}
	b.WriteString("\n")
	meta := []string{t.Faint.Render(s.ID)}
	if repo := s.Repo(); repo != "" {
		r := repo
		if br := s.Branch(); br != "" {
			r += "@" + br
		}
		meta = append(meta, r)
	}
	meta = append(meta, "created "+Ago(s.CreateTime), "updated "+Ago(s.UpdateTime))
	b.WriteString(strings.Join(meta, t.Faint.Render(" · ")))
	for _, pr := range s.PullRequests() {
		b.WriteString("\n" + t.OK.Render("PR ") + t.Link.Render(pr.URL))
	}
	if s.URL != "" {
		b.WriteString("\n" + t.Faint.Render("web ") + t.Link.Render(s.URL))
	}
	return b.String()
}

// Session list columns: date, repo, name, status.
const (
	dateW   = 6
	statusW = 14
	colGap  = "  "
)

// RepoWidth sizes the repo column to fit ss, within sensible bounds.
func RepoWidth(ss []api.Session) int {
	w := 4
	for _, s := range ss {
		w = max(w, lipgloss.Width(s.Repo()))
	}
	return min(w, 28)
}

// SessionColumns is the faint column header matching SessionRow.
func (t *Theme) SessionColumns(width, repoW int) string {
	nameW := max(4, width-dateW-repoW-statusW-3*len(colGap))
	return t.Faint.Render(fmt.Sprintf("%*s%s%-*s%s%-*s%s%s",
		dateW, "UPDATED", colGap, repoW, "REPO", colGap, nameW, "NAME", colGap, "STATUS"))
}

// SessionRow is one line in a session list: date, repo, name, status.
// Text matching query (case-insensitive) is highlighted.
func (t *Theme) SessionRow(s api.Session, width, repoW int, query string) string {
	nameW := max(4, width-dateW-repoW-statusW-3*len(colGap))
	date := fmt.Sprintf("%*s", dateW, Ago(s.UpdateTime))
	repo := truncate(s.Repo(), repoW)
	repoCell := t.Highlight(repo, query, t.Faint) + strings.Repeat(" ", max(0, repoW-lipgloss.Width(repo)))

	pr := ""
	if len(s.PullRequests()) > 0 {
		pr = "⇡ "
	}
	name := truncate(s.DisplayTitle(), nameW-lipgloss.Width(pr))
	nameCell := t.OK.Render(pr) + t.Highlight(name, query, lipgloss.Style{}) +
		strings.Repeat(" ", max(0, nameW-lipgloss.Width(pr)-lipgloss.Width(name)))

	return t.Faint.Render(date) + colGap + repoCell + colGap + nameCell + colGap + t.StateBadge(s.State)
}

// Highlight renders s with base, marking every case-insensitive match of
// query with the Match style.
func (t *Theme) Highlight(s, query string, base lipgloss.Style) string {
	query = strings.TrimSpace(query)
	lower := strings.ToLower(s)
	if query == "" || len(lower) != len(s) {
		return base.Render(s)
	}
	q := strings.ToLower(query)
	var b strings.Builder
	for {
		i := strings.Index(lower, q)
		if i < 0 {
			b.WriteString(base.Render(s))
			return b.String()
		}
		if i > 0 {
			b.WriteString(base.Render(s[:i]))
		}
		b.WriteString(t.Match.Render(s[i : i+len(q)]))
		s, lower = s[i+len(q):], lower[i+len(q):]
		if s == "" {
			return b.String()
		}
	}
}

func padRight(s string, w int) string {
	return s + strings.Repeat(" ", max(0, w-lipgloss.Width(s)))
}

func truncate(s string, w int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if w <= 1 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}

// Options control how much of an activity is shown.
type Options struct {
	Width       int
	ExpandDiffs bool // show full diffs instead of a file summary
	ExpandBash  bool // show full bash output instead of the tail
	FullPrompt  bool // show the whole initial prompt instead of its start
	Approved    map[string]bool
	// PrevPatch is the last change set already shown. Jules attaches the
	// whole cumulative diff to most progress updates, so a patch identical
	// to PrevPatch is skipped.
	PrevPatch *string
	// Cache, when set, memoises rendered activities across redraws.
	Cache map[string]string
}

// cacheKey identifies one rendering of an activity.
func cacheKey(a api.Activity, o Options) string {
	approved := false
	if a.PlanGenerated != nil && a.PlanGenerated.Plan != nil {
		approved = o.Approved[a.PlanGenerated.Plan.ID]
	}
	prev := ""
	if o.PrevPatch != nil {
		h := fnv.New64a()
		h.Write([]byte(*o.PrevPatch))
		prev = fmt.Sprint(h.Sum64())
	}
	return fmt.Sprintf("%s|%d|%t%t%t|%s", a.Name+a.ID, o.Width, o.ExpandDiffs, o.ExpandBash, approved, prev)
}

// advancePatch moves o.PrevPatch past a's change sets without rendering.
func advancePatch(a api.Activity, o Options) {
	if o.PrevPatch == nil {
		return
	}
	for _, art := range a.Artifacts {
		if cs := art.ChangeSet; cs != nil && cs.GitPatch != nil {
			*o.PrevPatch = cs.GitPatch.UnidiffPatch
		}
	}
}

// ApprovedPlans collects plan IDs that have a planApproved activity.
func ApprovedPlans(acts []api.Activity) map[string]bool {
	m := map[string]bool{}
	for _, a := range acts {
		if a.PlanApproved != nil {
			m[a.PlanApproved.PlanID] = true
		}
	}
	return m
}

// Activity renders one timeline entry.
func (t *Theme) Activity(a api.Activity, o Options) string {
	w := o.Width
	if w <= 0 {
		w = 80
	}
	inner := max(20, w-2)
	ts := t.Faint.Render(a.CreateTime.Local().Format("15:04"))
	head := func(who lipgloss.Style, label string) string {
		return who.Render(label) + " " + ts
	}

	var b strings.Builder
	switch a.Kind() {
	case api.KindAgentMessaged:
		b.WriteString(head(t.Agent, "● Jules") + "\n")
		b.WriteString(Indent(t.Markdown(a.AgentMessaged.AgentMessage, inner), "  "))
	case api.KindUserMessaged:
		b.WriteString(head(t.User, "● You") + "\n")
		b.WriteString(Indent(t.Markdown(a.UserMessaged.UserMessage, inner), "  "))
	case api.KindPlanGenerated:
		b.WriteString(t.plan(a, o, inner, ts))
	case api.KindPlanApproved:
		b.WriteString(head(t.OK, "✓ Plan approved"))
	case api.KindProgressUpdated:
		p := a.ProgressUpdated
		title := p.Title
		if title == "" {
			title = a.Description
		}
		title = strings.TrimSpace(title)
		if title == "" && strings.TrimSpace(p.Description) == "" {
			// Jules emits bare progress updates that only carry the current
			// diff; show the diff (if new) without a heading, or nothing.
			return strings.TrimPrefix(t.artifacts(a, o, inner), "\n")
		}
		b.WriteString(t.Faint.Render("◦ ") + t.Bold.Render(title) + " " + ts)
		if d := strings.TrimSpace(p.Description); d != "" && d != title {
			b.WriteString("\n" + Indent(t.Markdown(d, inner), "  "))
		}
	case api.KindSessionCompleted:
		b.WriteString(t.OK.Render("━━ Session completed ━━") + " " + ts)
	case api.KindSessionFailed:
		b.WriteString(t.Bad.Render("━━ Session failed ━━") + " " + ts)
		if r := a.SessionFailed.Reason; r != "" {
			b.WriteString("\n" + Indent(t.Markdown(r, inner), "  "))
		}
	default:
		b.WriteString(head(t.System, "◇ Unsupported activity") + "\n")
		b.WriteString(Indent(PerLine(t.Faint, prettyJSON(a.Raw, 30)), "  "))
	}
	b.WriteString(t.artifacts(a, o, inner))
	return b.String()
}

// artifacts renders an activity's artifacts, each on new lines, skipping
// change sets identical to the last one shown.
func (t *Theme) artifacts(a api.Activity, o Options, inner int) string {
	var b strings.Builder
	for _, art := range a.Artifacts {
		if cs := art.ChangeSet; cs != nil && cs.GitPatch != nil && o.PrevPatch != nil {
			// The final change set is always shown, even if unchanged.
			if cs.GitPatch.UnidiffPatch == *o.PrevPatch && a.SessionCompleted == nil {
				continue
			}
			*o.PrevPatch = cs.GitPatch.UnidiffPatch
		}
		if s := t.Artifact(art, o, inner); s != "" {
			b.WriteString("\n" + Indent(s, "  "))
		}
	}
	return b.String()
}

func (t *Theme) plan(a api.Activity, o Options, width int, ts string) string {
	p := a.PlanGenerated.Plan
	var b strings.Builder
	label := "▤ Plan"
	if p != nil && o.Approved[p.ID] {
		label += " " + t.OK.Render("(approved)")
	}
	b.WriteString(t.Accent.Bold(true).Render(label) + " " + ts)
	if p == nil {
		return b.String()
	}
	var body strings.Builder
	for i, s := range p.Steps {
		if i > 0 {
			body.WriteString("\n")
		}
		n := s.Index + 1
		if s.Index == 0 && i > 0 {
			n = i + 1
		}
		body.WriteString(t.Bold.Render(fmt.Sprintf("%d. %s", n, strings.TrimSpace(s.Title))))
		if d := strings.TrimSpace(s.Description); d != "" {
			body.WriteString("\n" + Indent(t.Markdown(d, width-8), "   "))
		}
	}
	b.WriteString("\n" + t.Card.Width(width).Render(body.String()))
	return b.String()
}

// Artifact renders an artifact attached to an activity.
func (t *Theme) Artifact(a api.Artifact, o Options, width int) string {
	switch {
	case a.ChangeSet != nil:
		return t.ChangeSet(a.ChangeSet, o.ExpandDiffs, width)
	case a.BashOutput != nil:
		return t.Bash(a.BashOutput, o.ExpandBash, width)
	case a.Media != nil:
		n := base64.StdEncoding.DecodedLen(len(a.Media.Data))
		return t.Faint.Render(fmt.Sprintf("▣ media %s (%s)", a.Media.MimeType, humanBytes(n)))
	case len(a.Raw) > 0:
		return PerLine(t.Faint, "◇ unsupported artifact\n"+prettyJSON(a.Raw, 15))
	}
	return ""
}

// Bash renders a command and its output, tail-collapsed unless expanded.
func (t *Theme) Bash(bo *api.BashOutput, expand bool, width int) string {
	exit := t.OK.Render("exit 0")
	if bo.ExitCode != 0 {
		exit = t.Bad.Render(fmt.Sprintf("exit %d", bo.ExitCode))
	}
	var b strings.Builder
	b.WriteString(t.Faint.Render("$ ") + t.Bold.Render(truncate(bo.Command, width-12)) + "  " + exit)
	out := strings.TrimRight(bo.Output, "\n")
	if out == "" {
		return b.String()
	}
	lines := strings.Split(out, "\n")
	const tail = 8
	if !expand && len(lines) > tail {
		b.WriteString("\n" + t.Faint.Render(fmt.Sprintf("  … %d earlier lines hidden", len(lines)-tail)))
		lines = lines[len(lines)-tail:]
	}
	for _, l := range lines {
		b.WriteString("\n" + t.Faint.Render("│ ") + truncate(l, width-2))
	}
	return b.String()
}

// FileStat summarises one file in a unified diff.
type FileStat struct {
	Path      string
	Add, Del  int
	StartLine int // index into the patch lines
}

// DiffStats parses file headers and +/- counts from a unified diff.
func DiffStats(patch string) []FileStat {
	var stats []FileStat
	inHeader := false // between "diff --git" and the first hunk
	for i, l := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			path := l[len("diff --git "):]
			if _, after, ok := strings.Cut(path, " b/"); ok {
				path = after
			}
			stats = append(stats, FileStat{Path: path, StartLine: i})
			inHeader = true
		case strings.HasPrefix(l, "@@"):
			inHeader = false
		case inHeader:
			// mode/index/---/+++ lines of a git diff header.
		case strings.HasPrefix(l, "+++ "):
			// Plain unified diff with no "diff --git" headers.
			stats = append(stats, FileStat{Path: strings.TrimPrefix(strings.TrimPrefix(l, "+++ "), "b/"), StartLine: i})
		case len(stats) == 0, strings.HasPrefix(l, "--- "):
		case strings.HasPrefix(l, "+"):
			stats[len(stats)-1].Add++
		case strings.HasPrefix(l, "-"):
			stats[len(stats)-1].Del++
		}
	}
	return stats
}

// ChangeSet renders a patch: a file summary, plus the coloured diff when
// expanded.
func (t *Theme) ChangeSet(cs *api.ChangeSet, expand bool, width int) string {
	if cs.GitPatch == nil {
		return t.Faint.Render("± empty change set")
	}
	gp := cs.GitPatch
	stats := DiffStats(gp.UnidiffPatch)
	var adds, dels int
	for _, s := range stats {
		adds += s.Add
		dels += s.Del
	}
	var b strings.Builder
	b.WriteString(t.FileHdr.Render("± Changes") + " " +
		t.Faint.Render(fmt.Sprintf("%d files ", len(stats))) +
		t.Add.Render(fmt.Sprintf("+%d", adds)) + " " + t.Del.Render(fmt.Sprintf("-%d", dels)))
	if gp.BaseCommitID != "" {
		b.WriteString(t.Faint.Render("  base " + short(gp.BaseCommitID)))
	}
	if msg := strings.TrimSpace(gp.SuggestedCommitMessage); msg != "" {
		first, _, _ := strings.Cut(msg, "\n")
		b.WriteString("\n" + t.Faint.Render("commit: ") + truncate(first, width-8))
	}
	if !expand {
		for _, s := range stats {
			b.WriteString(fmt.Sprintf("\n  %s %s %s", t.Add.Render(fmt.Sprintf("%+4d", s.Add)), t.Del.Render(fmt.Sprintf("-%-4d", s.Del)), truncate(s.Path, width-14)))
		}
		return b.String()
	}
	b.WriteString("\n" + t.Diff(gp.UnidiffPatch, width))
	return b.String()
}

// Diff colours a unified diff line by line.
func (t *Theme) Diff(patch string, width int) string {
	lines := strings.Split(strings.TrimRight(patch, "\n"), "\n")
	for i, l := range lines {
		l = strings.ReplaceAll(l, "\t", "    ")
		if width > 0 {
			l = truncate(l, width)
		}
		switch {
		case strings.HasPrefix(l, "diff --git"), strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"), strings.HasPrefix(l, "index "):
			lines[i] = t.FileHdr.Render(l)
		case strings.HasPrefix(l, "@@"):
			lines[i] = t.Hunk.Render(l)
		case strings.HasPrefix(l, "+"):
			lines[i] = t.Add.Render(l)
		case strings.HasPrefix(l, "-"):
			lines[i] = t.Del.Render(l)
		default:
			lines[i] = l
		}
	}
	return strings.Join(lines, "\n")
}

// Prompt renders the session's initial prompt, which the API does not
// return as an activity.
func (t *Theme) Prompt(s *api.Session, width int, full bool) string {
	if s == nil || strings.TrimSpace(s.Prompt) == "" {
		return ""
	}
	ts := t.Faint.Render(s.CreateTime.Local().Format("Jan 2 15:04"))
	body := t.Markdown(s.Prompt, max(20, width-2))
	const keep = 12
	if lines := strings.Split(body, "\n"); !full && len(lines) > keep+3 {
		body = strings.Join(lines[:keep], "\n") + "\n" + t.Faint.Render(fmt.Sprintf("… %d more lines", len(lines)-keep))
	}
	return t.User.Render("● You") + " " + ts + "\n" + Indent(body, "  ")
}

// Timeline renders the prompt and every activity, separated by blank lines.
func (t *Theme) Timeline(s *api.Session, acts []api.Activity, o Options) string {
	if o.Approved == nil {
		o.Approved = ApprovedPlans(acts)
	}
	if o.PrevPatch == nil {
		o.PrevPatch = new(string)
	}
	parts := make([]string, 0, len(acts)+1)
	if p := t.Prompt(s, o.Width, o.FullPrompt); p != "" {
		parts = append(parts, p)
	}
	for _, a := range acts {
		var s string
		if o.Cache != nil {
			k := cacheKey(a, o)
			if hit, ok := o.Cache[k]; ok {
				s = hit
				advancePatch(a, o)
			} else {
				s = t.Activity(a, o)
				o.Cache[k] = s
			}
		} else {
			s = t.Activity(a, o)
		}
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n")
}

func short(id string) string {
	if len(id) > 10 {
		return id[:10]
	}
	return id
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func prettyJSON(raw json.RawMessage, maxLines int) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	lines := strings.Split(string(b), "\n")
	if len(lines) > maxLines {
		lines = append(lines[:maxLines], "…")
	}
	return strings.Join(lines, "\n")
}
