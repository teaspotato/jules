// Package api is a thin, hand-mirrored client for the Jules REST API
// (v1alpha). The schema lives in api/discovery-v1alpha.json at the repo root.
//
// The API is alpha, so every type keeps the raw JSON it was decoded from:
// fields we do not model yet are never lost and can be shown or re-emitted.
package api

import (
	"encoding/json"
	"strings"
	"time"
)

// Session states.
const (
	StateUnspecified          = "STATE_UNSPECIFIED"
	StateQueued               = "QUEUED"
	StatePlanning             = "PLANNING"
	StateAwaitingPlanApproval = "AWAITING_PLAN_APPROVAL"
	StateAwaitingUserFeedback = "AWAITING_USER_FEEDBACK"
	StateInProgress           = "IN_PROGRESS"
	StatePaused               = "PAUSED"
	StateFailed               = "FAILED"
	StateCompleted            = "COMPLETED"
)

// AutomationAutoCreatePR asks Jules to open a pull request when done.
const AutomationAutoCreatePR = "AUTO_CREATE_PR"

// IsTerminal reports whether a session in this state will not change again
// on its own.
func IsTerminal(state string) bool {
	return state == StateCompleted || state == StateFailed
}

// NeedsAttention reports whether the session is blocked on the user.
func NeedsAttention(state string) bool {
	return state == StateAwaitingPlanApproval || state == StateAwaitingUserFeedback
}

type GitHubBranch struct {
	DisplayName string `json:"displayName,omitempty"`
}

type GitHubRepo struct {
	Owner         string         `json:"owner,omitempty"`
	Repo          string         `json:"repo,omitempty"`
	IsPrivate     bool           `json:"isPrivate,omitempty"`
	DefaultBranch *GitHubBranch  `json:"defaultBranch,omitempty"`
	Branches      []GitHubBranch `json:"branches,omitempty"`
}

type Source struct {
	Name       string      `json:"name,omitempty"`
	ID         string      `json:"id,omitempty"`
	GitHubRepo *GitHubRepo `json:"githubRepo,omitempty"`
}

// Slug is "owner/repo" for GitHub sources, otherwise the resource id.
func (s Source) Slug() string {
	if s.GitHubRepo != nil && s.GitHubRepo.Owner != "" {
		return s.GitHubRepo.Owner + "/" + s.GitHubRepo.Repo
	}
	return strings.TrimPrefix(s.Name, "sources/")
}

type GitHubRepoContext struct {
	StartingBranch string `json:"startingBranch,omitempty"`
}

type SourceContext struct {
	Source                      string             `json:"source,omitempty"`
	GitHubRepoContext           *GitHubRepoContext `json:"githubRepoContext,omitempty"`
	WorkingBranch               string             `json:"workingBranch,omitempty"`
	EnvironmentVariablesEnabled bool               `json:"environmentVariablesEnabled,omitempty"`
}

type PullRequest struct {
	URL         string `json:"url,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	BaseRef     string `json:"baseRef,omitempty"`
	HeadRef     string `json:"headRef,omitempty"`
}

type SessionOutput struct {
	PullRequest *PullRequest `json:"pullRequest,omitempty"`
	ChangeSet   *ChangeSet   `json:"changeSet,omitempty"`
}

type Session struct {
	Name                string          `json:"name,omitempty"`
	ID                  string          `json:"id,omitempty"`
	Title               string          `json:"title,omitempty"`
	Prompt              string          `json:"prompt,omitempty"`
	SourceContext       *SourceContext  `json:"sourceContext,omitempty"`
	RequirePlanApproval bool            `json:"requirePlanApproval,omitempty"`
	AutomationMode      string          `json:"automationMode,omitempty"`
	State               string          `json:"state,omitempty"`
	URL                 string          `json:"url,omitempty"`
	CreateTime          time.Time       `json:"createTime,omitzero"`
	UpdateTime          time.Time       `json:"updateTime,omitzero"`
	Outputs             []SessionOutput `json:"outputs,omitempty"`
	Archived            bool            `json:"archived,omitempty"`
	// Raw is the session as received; MarshalJSON re-emits it.
	Raw json.RawMessage `json:"-"`
}

func (s *Session) UnmarshalJSON(b []byte) error {
	type plain Session
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*s = Session(p)
	s.Raw = append(json.RawMessage(nil), b...)
	return nil
}

func (s Session) MarshalJSON() ([]byte, error) {
	if len(s.Raw) > 0 {
		return s.Raw, nil
	}
	type plain Session
	return json.Marshal(plain(s))
}

// Repo returns "owner/repo" derived from the session's source name
// ("sources/github/owner/repo"), or "" when the session has no source.
func (s Session) Repo() string {
	if s.SourceContext == nil {
		return ""
	}
	name := strings.TrimPrefix(s.SourceContext.Source, "sources/")
	name = strings.TrimPrefix(name, "github/")
	return name
}

// Branch is the starting branch, if any.
func (s Session) Branch() string {
	if s.SourceContext == nil || s.SourceContext.GitHubRepoContext == nil {
		return ""
	}
	return s.SourceContext.GitHubRepoContext.StartingBranch
}

// PullRequests lists the PRs the session produced.
func (s Session) PullRequests() []PullRequest {
	var prs []PullRequest
	for _, o := range s.Outputs {
		if o.PullRequest != nil && o.PullRequest.URL != "" {
			prs = append(prs, *o.PullRequest)
		}
	}
	return prs
}

// DisplayTitle falls back to the first line of the prompt.
func (s Session) DisplayTitle() string {
	if s.Title != "" {
		return s.Title
	}
	line, _, _ := strings.Cut(strings.TrimSpace(s.Prompt), "\n")
	if line == "" {
		return s.ID
	}
	return line
}

type PlanStep struct {
	ID          string `json:"id,omitempty"`
	Index       int    `json:"index,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

type Plan struct {
	ID         string     `json:"id,omitempty"`
	Steps      []PlanStep `json:"steps,omitempty"`
	CreateTime time.Time  `json:"createTime,omitzero"`
}

type GitPatch struct {
	UnidiffPatch           string `json:"unidiffPatch,omitempty"`
	BaseCommitID           string `json:"baseCommitId,omitempty"`
	SuggestedCommitMessage string `json:"suggestedCommitMessage,omitempty"`
}

type ChangeSet struct {
	Source   string    `json:"source,omitempty"`
	GitPatch *GitPatch `json:"gitPatch,omitempty"`
}

type BashOutput struct {
	Command  string `json:"command,omitempty"`
	Output   string `json:"output,omitempty"`
	ExitCode int    `json:"exitCode,omitempty"`
}

type Media struct {
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

type Artifact struct {
	ChangeSet  *ChangeSet  `json:"changeSet,omitempty"`
	BashOutput *BashOutput `json:"bashOutput,omitempty"`
	Media      *Media      `json:"media,omitempty"`
	// Raw is the artifact as received, for kinds we don't model.
	Raw json.RawMessage `json:"-"`
}

func (a *Artifact) UnmarshalJSON(b []byte) error {
	type plain Artifact
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*a = Artifact(p)
	a.Raw = append(json.RawMessage(nil), b...)
	return nil
}

type AgentMessaged struct {
	AgentMessage string `json:"agentMessage,omitempty"`
}

type UserMessaged struct {
	UserMessage string `json:"userMessage,omitempty"`
}

type PlanGenerated struct {
	Plan *Plan `json:"plan,omitempty"`
}

type PlanApproved struct {
	PlanID string `json:"planId,omitempty"`
}

type ProgressUpdated struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

type SessionCompleted struct{}

type SessionFailed struct {
	Reason string `json:"reason,omitempty"`
}

type Activity struct {
	Name             string            `json:"name,omitempty"`
	ID               string            `json:"id,omitempty"`
	Description      string            `json:"description,omitempty"`
	Originator       string            `json:"originator,omitempty"`
	CreateTime       time.Time         `json:"createTime,omitzero"`
	Artifacts        []Artifact        `json:"artifacts,omitempty"`
	AgentMessaged    *AgentMessaged    `json:"agentMessaged,omitempty"`
	UserMessaged     *UserMessaged     `json:"userMessaged,omitempty"`
	PlanGenerated    *PlanGenerated    `json:"planGenerated,omitempty"`
	PlanApproved     *PlanApproved     `json:"planApproved,omitempty"`
	ProgressUpdated  *ProgressUpdated  `json:"progressUpdated,omitempty"`
	SessionCompleted *SessionCompleted `json:"sessionCompleted,omitempty"`
	SessionFailed    *SessionFailed    `json:"sessionFailed,omitempty"`
	// Raw is the activity as received. Unknown activity kinds still decode
	// (all known fields nil) and are rendered from Raw.
	Raw json.RawMessage `json:"-"`
}

func (a *Activity) UnmarshalJSON(b []byte) error {
	type plain Activity
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*a = Activity(p)
	a.Raw = append(json.RawMessage(nil), b...)
	return nil
}

// MarshalJSON re-emits the activity exactly as the API sent it, so --json
// output never drops fields this client doesn't know about.
func (a Activity) MarshalJSON() ([]byte, error) {
	if len(a.Raw) > 0 {
		return a.Raw, nil
	}
	type plain Activity
	return json.Marshal(plain(a))
}

// Activity kinds, as returned by Kind.
const (
	KindAgentMessaged    = "agentMessaged"
	KindUserMessaged     = "userMessaged"
	KindPlanGenerated    = "planGenerated"
	KindPlanApproved     = "planApproved"
	KindProgressUpdated  = "progressUpdated"
	KindSessionCompleted = "sessionCompleted"
	KindSessionFailed    = "sessionFailed"
	KindUnknown          = "unknown"
)

// Kind names the union member that is set.
func (a Activity) Kind() string {
	switch {
	case a.AgentMessaged != nil:
		return KindAgentMessaged
	case a.UserMessaged != nil:
		return KindUserMessaged
	case a.PlanGenerated != nil:
		return KindPlanGenerated
	case a.PlanApproved != nil:
		return KindPlanApproved
	case a.ProgressUpdated != nil:
		return KindProgressUpdated
	case a.SessionCompleted != nil:
		return KindSessionCompleted
	case a.SessionFailed != nil:
		return KindSessionFailed
	}
	return KindUnknown
}

// LatestChangeSet returns the newest change set in a timeline, or nil.
func LatestChangeSet(acts []Activity) *ChangeSet {
	for i := len(acts) - 1; i >= 0; i-- {
		for j := len(acts[i].Artifacts) - 1; j >= 0; j-- {
			if cs := acts[i].Artifacts[j].ChangeSet; cs != nil && cs.GitPatch != nil {
				return cs
			}
		}
	}
	return nil
}

// LatestPlan returns the newest plan and whether it has been approved.
func LatestPlan(acts []Activity) (*Plan, bool) {
	var plan *Plan
	approved := map[string]bool{}
	for _, a := range acts {
		if a.PlanGenerated != nil && a.PlanGenerated.Plan != nil {
			plan = a.PlanGenerated.Plan
		}
		if a.PlanApproved != nil {
			approved[a.PlanApproved.PlanID] = true
		}
	}
	if plan == nil {
		return nil, false
	}
	return plan, approved[plan.ID]
}
