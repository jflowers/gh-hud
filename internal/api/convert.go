// Package api puts hub.Backend on the wire: Connect-RPC (proto in
// proto/ghhud/v1) served from a hub, and a client that is itself a
// hub.Backend, so the TUI works the same against a hub in another
// process.
package api

import (
	"errors"
	"math"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/jflowers/gh-hud/internal/api/gen/ghhud/v1"
	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/epic"
	"github.com/jflowers/gh-hud/internal/fleet"
	"github.com/jflowers/gh-hud/internal/hub"
	"github.com/jflowers/gh-hud/internal/model"
	"github.com/jflowers/gh-hud/internal/toolkit"
)

// Conversions between the hub's Go types and the wire types, both ways.
// TestRoundTrip fills every field of the Go types and checks they survive
// Go → wire → Go, so a field added in Go without a wire mapping fails it.
//
// Zero times and nil slices and maps stay zero and nil. Enums whose Go
// zero value means something (model.Kind, model.CIState, hub.EventKind,
// hub.AlertType) go on the wire as Go value + 1, keeping 0 for the proto
// UNSPECIFIED value.

func ts(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func fromTS(t *timestamppb.Timestamp) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.AsTime()
}

func tsPtr(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

func fromTSPtr(t *timestamppb.Timestamp) *time.Time {
	if t == nil {
		return nil
	}
	v := t.AsTime()
	return &v
}

func dur(d time.Duration) *durationpb.Duration {
	if d == 0 {
		return nil
	}
	return durationpb.New(d)
}

func fromDur(d *durationpb.Duration) time.Duration {
	if d == nil {
		return 0
	}
	return d.AsDuration()
}

// mapSlice converts a slice element-wise, keeping nil (and empty) as nil.
func mapSlice[T, U any](in []T, f func(T) U) []U {
	if len(in) == 0 {
		return nil
	}
	out := make([]U, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}

func strs(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return append([]string(nil), in...)
}

// ---------------------------------------------------------------------------
// snapshot

// SnapshotToPB converts a hub snapshot for the wire.
func SnapshotToPB(s hub.Snapshot) *pb.Snapshot {
	out := &pb.Snapshot{
		Seq:               s.Seq,
		FetchedAt:         ts(s.FetchedAt),
		Fetching:          s.Fetching,
		FetchError:        s.FetchErr,
		Dispatch:          s.Dispatch,
		Follower:          s.Follower,
		Sessions:          mapSlice(s.Sessions, SessionToPB),
		Runs:              mapSlice(s.Runs, RunToPB),
		Usage:             mapSlice(s.Usage, usageToPB),
		AgentsAt:          ts(s.AgentsAt),
		Plan:              mapSlice(s.Plan, planRowToPB),
		Queue:             mapSlice(s.Queue, queueEntryToPB),
		Cases:             mapSlice(s.Cases, caseToPB),
		Epics:             mapSlice(s.Epics, epicToPB),
		ConfigWarnings:    s.ConfigWarnings,
		Workspaces:        mapSlice(s.Workspaces, checkoutToPB),
		WorkspacesAt:      ts(s.WorkspacesAt),
		WorkspacesLoading: s.WorkspacesLoading,
	}
	if s.Res != nil {
		out.Result = fetchResultToPB(s.Res)
	}
	if len(s.Stages) > 0 {
		out.Stages = make(map[string]*pb.Stage, len(s.Stages))
		for url, st := range s.Stages {
			out.Stages[url] = stageToPB(st)
		}
	}
	if s.Toolkit != nil {
		out.Toolkit = toolkitToPB(*s.Toolkit)
	}
	if s.Fleet != nil {
		out.Fleet = fleetToPB(*s.Fleet)
	}
	return out
}

// SnapshotFromPB converts a wire snapshot back.
func SnapshotFromPB(p *pb.Snapshot) hub.Snapshot {
	s := hub.Snapshot{
		Seq:               p.GetSeq(),
		FetchedAt:         fromTS(p.GetFetchedAt()),
		Fetching:          p.GetFetching(),
		FetchErr:          p.GetFetchError(),
		Dispatch:          p.GetDispatch(),
		Follower:          p.GetFollower(),
		Sessions:          mapSlice(p.GetSessions(), SessionFromPB),
		Runs:              mapSlice(p.GetRuns(), RunFromPB),
		Usage:             mapSlice(p.GetUsage(), usageFromPB),
		AgentsAt:          fromTS(p.GetAgentsAt()),
		Plan:              mapSlice(p.GetPlan(), planRowFromPB),
		Queue:             mapSlice(p.GetQueue(), queueEntryFromPB),
		Cases:             mapSlice(p.GetCases(), caseFromPB),
		Epics:             mapSlice(p.GetEpics(), epicFromPB),
		ConfigWarnings:    p.GetConfigWarnings(),
		Workspaces:        mapSlice(p.GetWorkspaces(), checkoutFromPB),
		WorkspacesAt:      fromTS(p.GetWorkspacesAt()),
		WorkspacesLoading: p.GetWorkspacesLoading(),
	}
	if p.GetResult() != nil {
		s.Res = fetchResultFromPB(p.GetResult())
	}
	if len(p.GetStages()) > 0 {
		s.Stages = make(map[string]dispatch.Stage, len(p.GetStages()))
		for url, st := range p.GetStages() {
			s.Stages[url] = stageFromPB(st)
		}
	}
	if p.GetToolkit() != nil {
		t := toolkitFromPB(p.GetToolkit())
		s.Toolkit = &t
	}
	if p.GetFleet() != nil {
		f := fleetFromPB(p.GetFleet())
		s.Fleet = &f
	}
	return s
}

func fetchResultToPB(r *model.FetchResult) *pb.FetchResult {
	return &pb.FetchResult{
		Items: mapSlice(r.Items, ItemToPB),
		Errors: mapSlice(r.Errors, func(e model.SectionError) *pb.SectionError {
			msg := ""
			if e.Err != nil {
				msg = e.Err.Error()
			}
			return &pb.SectionError{Section: e.Section, Message: msg}
		}),
		Took:      dur(r.Took),
		FetchedAt: ts(r.FetchedAt),
		User:      r.User,
	}
}

func fetchResultFromPB(p *pb.FetchResult) *model.FetchResult {
	return &model.FetchResult{
		Items: mapSlice(p.GetItems(), ItemFromPB),
		Errors: mapSlice(p.GetErrors(), func(e *pb.SectionError) model.SectionError {
			return model.SectionError{Section: e.GetSection(), Err: errors.New(e.GetMessage())}
		}),
		Took:      fromDur(p.GetTook()),
		FetchedAt: fromTS(p.GetFetchedAt()),
		User:      p.GetUser(),
	}
}

// ---------------------------------------------------------------------------
// items

// ItemToPB converts a dashboard item for the wire.
func ItemToPB(it model.Item) *pb.Item {
	return &pb.Item{
		Kind:             pb.ItemKind(it.Kind) + 1,
		Categories:       uint32(it.Cats),
		Url:              it.URL,
		Repo:             it.Repo,
		Num:              int64(it.Num),
		Title:            it.Title,
		Author:           it.Author,
		Draft:            it.Draft,
		CreatedAt:        ts(it.CreatedAt),
		UpdatedAt:        ts(it.UpdatedAt),
		Ci:               pb.CIState(it.CI) + 1,
		Codecov:          pb.CIState(it.CC) + 1,
		Review:           it.Review,
		BotReview:        it.Bot,
		Merge:            it.Merge,
		Threads:          int64(it.Thread),
		BotThreads:       int64(it.BotThread),
		ThreadsMore:      it.ThreadsMore,
		ThreadsOpen:      int64(it.ThreadOpen),
		LastActor:        it.LastActor,
		WaitingOn:        it.WaitingOn,
		Tagged:           it.Tagged,
		OtherApproved:    it.OtherAppr,
		MyApproval:       it.MyAppr,
		BotReviewDue:     it.BotReviewDue,
		Reasons:          strs(it.Reasons),
		NodeId:           it.NodeID,
		HeadOid:          it.HeadOID,
		MergeState:       it.MergeState,
		ReviewDecision:   it.ReviewDecision,
		RequiredReviews:  int64(it.RequiredReviews),
		AutoMergeOn:      it.AutoMergeOn,
		AutoMergeAllowed: it.AutoMergeAllowed,
		Labels:           strs(it.Labels),
		FailingChecks:    strs(it.FailingChecks),
		ChecksRequired:   it.ChecksRequired,
		RequiredFails:    strs(it.RequiredFails),
		BaseFailing:      strs(it.BaseFailing),
		BasePassing:      strs(it.BasePassing),
		BaseOid:          it.BaseOID,
		Size:             int64(it.Size),
		Files:            strs(it.Files),
		HeadBranch:       it.HeadBranch,
		RunBranch:        it.RunBranch,
		RunEvent:         it.RunEvent,
	}
}

// enumFromPB undoes the +1 offset (an unspecified value reads as zero).
func enumFromPB[T ~uint8, P ~int32](p P) T {
	if p <= 0 {
		return 0
	}
	return T(p - 1)
}

// ItemFromPB converts a wire item back.
func ItemFromPB(p *pb.Item) model.Item {
	return model.Item{
		Kind:             enumFromPB[model.Kind](p.GetKind()),
		Cats:             model.Cats(p.GetCategories() & 0xff), //nolint:gosec // an 8-bit mask, masked
		URL:              p.GetUrl(),
		Repo:             p.GetRepo(),
		Num:              int(p.GetNum()),
		Title:            p.GetTitle(),
		Author:           p.GetAuthor(),
		Draft:            p.GetDraft(),
		CreatedAt:        fromTS(p.GetCreatedAt()),
		UpdatedAt:        fromTS(p.GetUpdatedAt()),
		CI:               enumFromPB[model.CIState](p.GetCi()),
		CC:               enumFromPB[model.CIState](p.GetCodecov()),
		Review:           p.GetReview(),
		Bot:              p.GetBotReview(),
		Merge:            p.GetMerge(),
		Thread:           int(p.GetThreads()),
		BotThread:        int(p.GetBotThreads()),
		ThreadOpen:       int(p.GetThreadsOpen()),
		ThreadsMore:      p.GetThreadsMore(),
		LastActor:        p.GetLastActor(),
		WaitingOn:        p.GetWaitingOn(),
		Tagged:           p.GetTagged(),
		OtherAppr:        p.GetOtherApproved(),
		MyAppr:           p.GetMyApproval(),
		BotReviewDue:     p.GetBotReviewDue(),
		Reasons:          strs(p.GetReasons()),
		NodeID:           p.GetNodeId(),
		HeadOID:          p.GetHeadOid(),
		MergeState:       p.GetMergeState(),
		ReviewDecision:   p.GetReviewDecision(),
		RequiredReviews:  int(p.GetRequiredReviews()),
		AutoMergeOn:      p.GetAutoMergeOn(),
		AutoMergeAllowed: p.GetAutoMergeAllowed(),
		Labels:           strs(p.GetLabels()),
		FailingChecks:    strs(p.GetFailingChecks()),
		ChecksRequired:   p.GetChecksRequired(),
		RequiredFails:    strs(p.GetRequiredFails()),
		BaseFailing:      strs(p.GetBaseFailing()),
		BasePassing:      strs(p.GetBasePassing()),
		BaseOID:          p.GetBaseOid(),
		Size:             int(p.GetSize()),
		Files:            strs(p.GetFiles()),
		HeadBranch:       p.GetHeadBranch(),
		RunBranch:        p.GetRunBranch(),
		RunEvent:         p.GetRunEvent(),
	}
}

// ---------------------------------------------------------------------------
// agents

// SessionToPB converts an agent session for the wire.
func SessionToPB(s dispatch.Session) *pb.Session {
	return &pb.Session{
		Id:           s.ID,
		Title:        s.Title,
		Directory:    s.Directory,
		UpdatedAt:    ts(s.UpdatedAt),
		Running:      s.Running,
		Failed:       s.Failed,
		Unknown:      s.Unknown,
		Note:         s.Note,
		Task:         string(s.Task),
		Log:          s.Log,
		Workspace:    s.Workspace,
		Ref:          s.Ref,
		Machine:      s.Machine,
		State:        s.State,
		PaneId:       s.PaneID,
		WsId:         s.WSID,
		Blocked:      s.Blocked,
		BlockedSince: ts(s.BlockedSince),
		RunId:        s.RunID,
	}
}

// SessionFromPB converts a wire session back.
func SessionFromPB(p *pb.Session) dispatch.Session {
	return dispatch.Session{
		ID:           p.GetId(),
		Title:        p.GetTitle(),
		Directory:    p.GetDirectory(),
		UpdatedAt:    fromTS(p.GetUpdatedAt()),
		Running:      p.GetRunning(),
		Failed:       p.GetFailed(),
		Unknown:      p.GetUnknown(),
		Note:         p.GetNote(),
		Task:         dispatch.Task(p.GetTask()),
		Log:          p.GetLog(),
		Workspace:    p.GetWorkspace(),
		Ref:          p.GetRef(),
		Machine:      p.GetMachine(),
		State:        p.GetState(),
		PaneID:       p.GetPaneId(),
		WSID:         p.GetWsId(),
		Blocked:      p.GetBlocked(),
		BlockedSince: fromTS(p.GetBlockedSince()),
		RunID:        p.GetRunId(),
	}
}

// RunToPB converts a dispatch run for the wire.
func RunToPB(r dispatch.Run) *pb.Run {
	return &pb.Run{
		Id:          r.ID,
		Task:        string(r.Task),
		Ref:         r.Ref,
		Repo:        r.Repo,
		Num:         int64(r.Num),
		Machine:     r.Machine,
		Workspace:   r.Workspace,
		Dir:         r.Dir,
		Status:      string(r.Status),
		Auto:        r.Auto,
		StartedAt:   ts(r.StartedAt),
		FinishedAt:  tsPtr(r.FinishedAt),
		SessionId:   r.SessionID,
		PaneId:      r.PaneID,
		WsId:        r.WSID,
		Log:         r.Log,
		Reason:      r.Reason,
		Summary:     r.Summary,
		Left:        strs(r.Left),
		Pushed:      strs(r.Pushed),
		Branch:      r.Branch,
		Cleaned:     r.Cleaned,
		Fingerprint: r.Fingerprint,
		HumanNeeded: r.HumanNeeded,
		IndexHealth: r.IndexHealth,
	}
}

// RunFromPB converts a wire run back.
func RunFromPB(p *pb.Run) dispatch.Run {
	return dispatch.Run{
		ID:          p.GetId(),
		Task:        dispatch.Task(p.GetTask()),
		Ref:         p.GetRef(),
		Repo:        p.GetRepo(),
		Num:         int(p.GetNum()),
		Machine:     p.GetMachine(),
		Workspace:   p.GetWorkspace(),
		Dir:         p.GetDir(),
		Status:      dispatch.RunStatus(p.GetStatus()),
		Auto:        p.GetAuto(),
		StartedAt:   fromTS(p.GetStartedAt()),
		FinishedAt:  fromTSPtr(p.GetFinishedAt()),
		SessionID:   p.GetSessionId(),
		PaneID:      p.GetPaneId(),
		WSID:        p.GetWsId(),
		Log:         p.GetLog(),
		Reason:      p.GetReason(),
		Summary:     p.GetSummary(),
		Left:        strs(p.GetLeft()),
		Pushed:      strs(p.GetPushed()),
		Branch:      p.GetBranch(),
		Cleaned:     p.GetCleaned(),
		Fingerprint: p.GetFingerprint(),
		HumanNeeded: p.GetHumanNeeded(),
		IndexHealth: p.GetIndexHealth(),
	}
}

func usageToPB(u dispatch.MachineUsage) *pb.MachineUsage {
	return &pb.MachineUsage{Name: u.Name, Used: int64(u.Used), Max: int64(u.Max), Ok: u.OK}
}

func usageFromPB(p *pb.MachineUsage) dispatch.MachineUsage {
	return dispatch.MachineUsage{Name: p.GetName(), Used: int(p.GetUsed()), Max: int(p.GetMax()), OK: p.GetOk()}
}

// ---------------------------------------------------------------------------
// pipeline and queue

func stageToPB(s dispatch.Stage) *pb.Stage {
	return &pb.Stage{Kind: string(s.Kind), Task: string(s.Task), Reason: s.Reason}
}

func stageFromPB(p *pb.Stage) dispatch.Stage {
	return dispatch.Stage{Kind: dispatch.StageKind(p.GetKind()), Task: dispatch.Task(p.GetTask()), Reason: p.GetReason()}
}

func planRowToPB(r dispatch.PlanRow) *pb.PlanRow {
	return &pb.PlanRow{
		Item:    ItemToPB(r.Item),
		Task:    string(r.Task),
		Stage:   stageToPB(r.Stage),
		Queued:  int64(r.Queued),
		Note:    r.Note,
		Missing: r.Missing,
		Epic:    r.Epic,
		Rule:    r.Rule,
	}
}

func planRowFromPB(p *pb.PlanRow) dispatch.PlanRow {
	r := dispatch.PlanRow{
		Task:    dispatch.Task(p.GetTask()),
		Queued:  int(p.GetQueued()),
		Note:    p.GetNote(),
		Missing: p.GetMissing(),
		Epic:    p.GetEpic(),
		Rule:    p.GetRule(),
	}
	if p.GetItem() != nil {
		r.Item = ItemFromPB(p.GetItem())
	}
	if p.GetStage() != nil {
		r.Stage = stageFromPB(p.GetStage())
	}
	return r
}

func caseToPB(c dispatch.Case) *pb.Case {
	out := &pb.Case{Id: c.ID, Kind: string(c.Kind), Repo: c.Repo, Check: c.Check,
		Facts: factsToPB(c.CaseFacts), Supervising: c.Supervising}
	if c.Decision != nil {
		out.Decision = decisionToPB(*c.Decision)
	}
	if c.Proposal != nil {
		out.Proposal = proposalToPB(*c.Proposal)
	}
	return out
}

func caseFromPB(p *pb.Case) dispatch.Case {
	c := dispatch.Case{ID: p.GetId(), Kind: dispatch.CaseKind(p.GetKind()), Repo: p.GetRepo(), Check: p.GetCheck(),
		CaseFacts: factsFromPB(p.GetFacts()), Supervising: p.GetSupervising()}
	if p.GetDecision() != nil {
		d := decisionFromPB(p.GetDecision())
		c.Decision = &d
	}
	if p.GetProposal() != nil {
		pr := proposalFromPB(p.GetProposal())
		c.Proposal = &pr
	}
	return c
}

func factsToPB(f dispatch.CaseFacts) *pb.CaseFacts {
	return &pb.CaseFacts{Prs: mapSlice(f.PRs, func(n int) int64 { return int64(n) }), OnBase: f.OnBase, BaseOid: f.BaseOID}
}

func factsFromPB(p *pb.CaseFacts) dispatch.CaseFacts {
	return dispatch.CaseFacts{PRs: mapSlice(p.GetPrs(), func(n int64) int { return int(n) }), OnBase: p.GetOnBase(), BaseOID: p.GetBaseOid()}
}

func decisionToPB(d dispatch.Decision) *pb.Decision {
	return &pb.Decision{CaseId: d.Case, Kind: string(d.Kind), FixPr: int64(d.FixPR), Summary: d.Summary,
		Options: d.Options, By: d.By, Facts: factsToPB(d.Facts), Decided: ts(d.Decided)}
}

func decisionFromPB(p *pb.Decision) dispatch.Decision {
	return dispatch.Decision{Case: p.GetCaseId(), Kind: dispatch.DecisionKind(p.GetKind()), FixPR: int(p.GetFixPr()),
		Summary: p.GetSummary(), Options: p.GetOptions(), By: p.GetBy(), Facts: factsFromPB(p.GetFacts()), Decided: fromTS(p.GetDecided())}
}

func proposalToPB(p dispatch.Proposal) *pb.Proposal {
	return &pb.Proposal{Id: p.ID, Decision: decisionToPB(p.Decision), Status: string(p.Status), Created: ts(p.Created),
		NoAnswer: p.NoAnswer, Note: p.Note, Log: p.Log}
}

func proposalFromPB(p *pb.Proposal) dispatch.Proposal {
	return dispatch.Proposal{ID: p.GetId(), Decision: decisionFromPB(p.GetDecision()), Status: dispatch.ProposalStatus(p.GetStatus()),
		Created: fromTS(p.GetCreated()), NoAnswer: p.GetNoAnswer(), Note: p.GetNote(), Log: p.GetLog()}
}

func epicPlanToPB(p *epic.Plan) *pb.EpicPlan {
	if p == nil {
		return nil
	}
	return &pb.EpicPlan{Id: p.ID, Sources: p.Sources, Nodes: mapSlice(p.Nodes, epicNodeToPB), Rules: p.Rules,
		Skipped: p.Skipped, Created: ts(p.Created), BodyHash: p.BodyHash, Stale: p.Stale}
}

func epicPlanFromPB(pp *pb.EpicPlan) *epic.Plan {
	if pp == nil {
		return nil
	}
	return &epic.Plan{ID: pp.GetId(), Sources: pp.GetSources(), Nodes: mapSlice(pp.GetNodes(), epicNodeFromPB),
		Rules: pp.GetRules(), Skipped: pp.GetSkipped(), Created: fromTS(pp.GetCreated()), BodyHash: pp.GetBodyHash(), Stale: pp.GetStale()}
}

func epicToPB(e epic.Epic) *pb.Epic {
	out := &pb.Epic{Ref: e.Ref, Url: e.URL, Title: e.Title, Enrolled: ts(e.Enrolled), State: string(e.State),
		Error: e.Error, Approved: tsPtr(e.Approved), ApprovedPlan: e.ApprovedPlan, CheckedAt: ts(e.CheckedAt), Log: e.Log,
		Plan: epicPlanToPB(e.Plan), ApprovedSnapshot: epicPlanToPB(e.ApprovedSnapshot), Changes: e.Changes,
		StatusComment: e.StatusComment, StatusHash: e.StatusHash}
	out.Reports = mapSlice(e.Reports, func(r epic.Report) *pb.EpicReport {
		return &pb.EpicReport{Piece: r.Piece, RunId: r.RunID, On: r.On, Status: r.Status, Finished: ts(r.Finished),
			Summary: r.Summary, Reason: r.Reason, Verdict: r.Verdict, Why: r.Why, Hint: r.Hint, Log: r.Log}
	})
	return out
}

func epicFromPB(p *pb.Epic) epic.Epic {
	e := epic.Epic{Ref: p.GetRef(), URL: p.GetUrl(), Title: p.GetTitle(), Enrolled: fromTS(p.GetEnrolled()),
		State: epic.State(p.GetState()), Error: p.GetError(), Approved: fromTSPtr(p.GetApproved()),
		ApprovedPlan: p.GetApprovedPlan(), CheckedAt: fromTS(p.GetCheckedAt()), Log: p.GetLog(),
		Plan: epicPlanFromPB(p.GetPlan()), ApprovedSnapshot: epicPlanFromPB(p.GetApprovedSnapshot()), Changes: p.GetChanges(),
		StatusComment: p.GetStatusComment(), StatusHash: p.GetStatusHash()}
	e.Reports = mapSlice(p.GetReports(), func(r *pb.EpicReport) epic.Report {
		return epic.Report{Piece: r.GetPiece(), RunID: r.GetRunId(), On: r.GetOn(), Status: r.GetStatus(), Finished: fromTS(r.GetFinished()),
			Summary: r.GetSummary(), Reason: r.GetReason(), Verdict: r.GetVerdict(), Why: r.GetWhy(), Hint: r.GetHint(), Log: r.GetLog()}
	})
	return e
}

func epicNodeToPB(n epic.Node) *pb.EpicNode {
	return &pb.EpicNode{Ref: n.Ref, Title: n.Title, Phase: n.Phase, After: n.After, Gate: n.Gate, Source: n.Source,
		State: string(n.State), Note: n.Note, Pr: int64(n.PR), Cleared: n.Cleared}
}

func epicNodeFromPB(p *pb.EpicNode) epic.Node {
	return epic.Node{Ref: p.GetRef(), Title: p.GetTitle(), Phase: p.GetPhase(), After: p.GetAfter(), Gate: p.GetGate(),
		Source: p.GetSource(), State: epic.NodeState(p.GetState()), Note: p.GetNote(), PR: int(p.GetPr()), Cleared: p.GetCleared()}
}

func queueEntryToPB(e dispatch.QueueEntry) *pb.QueueEntry {
	return &pb.QueueEntry{Ref: e.Ref, Task: string(e.Task), Title: e.Title, Added: ts(e.Added), LastError: e.LastErr}
}

func queueEntryFromPB(p *pb.QueueEntry) dispatch.QueueEntry {
	return dispatch.QueueEntry{
		Ref: p.GetRef(), Task: dispatch.Task(p.GetTask()), Title: p.GetTitle(),
		Added: fromTS(p.GetAdded()), LastErr: p.GetLastError(),
	}
}

// ---------------------------------------------------------------------------
// workspaces

func checkoutToPB(h dispatch.CheckoutHealth) *pb.CheckoutHealth {
	return &pb.CheckoutHealth{
		Machine:       h.Machine,
		Workspace:     h.Workspace,
		Repo:          h.Repo,
		Dir:           h.Dir,
		Branch:        h.Branch,
		DefaultBranch: h.Default,
		Ahead:         int64(h.Ahead),
		Behind:        int64(h.Behind),
		Dirty:         strs(h.Dirty),
		Learnings:     int64(h.Learnings),
		LocalOnly:     strs(h.LocalOnly),
		Stashes:       int64(h.Stashes),
		Busy:          h.Busy,
		NoUpstream:    h.NoUpstream,
		Error:         h.Err,
	}
}

func checkoutFromPB(p *pb.CheckoutHealth) dispatch.CheckoutHealth {
	return dispatch.CheckoutHealth{
		Machine:    p.GetMachine(),
		Workspace:  p.GetWorkspace(),
		Repo:       p.GetRepo(),
		Dir:        p.GetDir(),
		Branch:     p.GetBranch(),
		Default:    p.GetDefaultBranch(),
		Ahead:      int(p.GetAhead()),
		Behind:     int(p.GetBehind()),
		Dirty:      strs(p.GetDirty()),
		Learnings:  int(p.GetLearnings()),
		LocalOnly:  strs(p.GetLocalOnly()),
		Stashes:    int(p.GetStashes()),
		Busy:       p.GetBusy(),
		NoUpstream: p.GetNoUpstream(),
		Err:        p.GetError(),
	}
}

// ---------------------------------------------------------------------------
// fleet

func fleetToPB(f fleet.Snapshot) *pb.FleetSnapshot {
	return &pb.FleetSnapshot{
		Target:      f.Target,
		TargetShort: f.TargetShort,
		Ci:          string(f.CI),
		CiDetail:    f.CIDetail,
		Blocked:     f.Blocked,
		Paused:      f.Paused,
		Active:      f.Active,
		Failed:      f.Failed,
		Banner:      f.Banner,
		LastError:   f.LastError,
		Machines: mapSlice(f.Machines, func(m fleet.MachineStatus) *pb.FleetMachine {
			return &pb.FleetMachine{
				Name: m.Name, State: string(m.State), Reachable: m.Reachable,
				Applied: m.Applied, AppliedShort: m.AppliedShort,
				Waited: dur(m.Waited), LastError: m.LastErr,
			}
		}),
	}
}

func fleetFromPB(p *pb.FleetSnapshot) fleet.Snapshot {
	return fleet.Snapshot{
		Target:      p.GetTarget(),
		TargetShort: p.GetTargetShort(),
		CI:          fleet.CIStatus(p.GetCi()),
		CIDetail:    p.GetCiDetail(),
		Blocked:     p.GetBlocked(),
		Paused:      p.GetPaused(),
		Active:      p.GetActive(),
		Failed:      p.GetFailed(),
		Banner:      p.GetBanner(),
		LastError:   p.GetLastError(),
		Machines: mapSlice(p.GetMachines(), func(m *pb.FleetMachine) fleet.MachineStatus {
			return fleet.MachineStatus{
				Name: m.GetName(), State: fleet.State(m.GetState()), Reachable: m.GetReachable(),
				Applied: m.GetApplied(), AppliedShort: m.GetAppliedShort(),
				Waited: fromDur(m.GetWaited()), LastErr: m.GetLastError(),
			}
		}),
	}
}

// ---------------------------------------------------------------------------
// toolkit

func toolkitToPB(t toolkit.Snapshot) *pb.ToolkitSnapshot {
	return &pb.ToolkitSnapshot{
		CheckedAt: ts(t.CheckedAt),
		Checking:  t.Checking,
		Error:     t.Err,
		Tools: mapSlice(t.Tools, func(x toolkit.ToolStatus) *pb.ToolkitTool {
			return &pb.ToolkitTool{
				Name: x.Name, Release: x.Release, Pin: x.Pin, Applied: x.Applied, Latest: x.Latest,
				Scaffolds: x.Scaffolds, Error: x.Err,
				Repos: mapSlice(x.Repos, func(r toolkit.RepoVersion) *pb.ToolkitRepo {
					return &pb.ToolkitRepo{Target: r.Target, Version: r.Version, Error: r.Err}
				}),
			}
		}),
		Rollouts: mapSlice(t.Rollouts, func(r toolkit.Rollout) *pb.ToolkitRolloutState {
			return &pb.ToolkitRolloutState{
				Id: r.ID, Tool: r.Tool, Version: r.Version, From: r.From, Stage: string(r.Stage),
				Started: ts(r.Started), Updated: ts(r.Updated), PinPr: r.PinPR, PinState: r.PinState,
				Applied: r.Applied, Error: r.Error, Summary: r.Summary(), Notes: r.Notes, NotesUrl: r.NotesURL,
				Targets: mapSlice(r.Targets, func(x toolkit.TargetRun) *pb.ToolkitTarget {
					return &pb.ToolkitTarget{
						Target: x.Target, Status: string(x.Status), Detail: x.Detail, Url: x.URL,
						Branch: x.Branch, Base: x.Base, Held: x.Held, PrState: x.PRState, Ci: x.CI,
						Files: int32(min(x.Files, 1<<30)), NeedsYou: x.NeedsYou(), //nolint:gosec // clamped
					}
				}),
			}
		}),
	}
}

func toolkitFromPB(p *pb.ToolkitSnapshot) toolkit.Snapshot {
	return toolkit.Snapshot{
		CheckedAt: fromTS(p.GetCheckedAt()),
		Checking:  p.GetChecking(),
		Err:       p.GetError(),
		Tools: mapSlice(p.GetTools(), func(x *pb.ToolkitTool) toolkit.ToolStatus {
			return toolkit.ToolStatus{
				Name: x.GetName(), Release: x.GetRelease(), Pin: x.GetPin(), Applied: x.GetApplied(),
				Latest: x.GetLatest(), Scaffolds: x.GetScaffolds(), Err: x.GetError(),
				Repos: mapSlice(x.GetRepos(), func(r *pb.ToolkitRepo) toolkit.RepoVersion {
					return toolkit.RepoVersion{Target: r.GetTarget(), Version: r.GetVersion(), Err: r.GetError()}
				}),
			}
		}),
		Rollouts: mapSlice(p.GetRollouts(), func(r *pb.ToolkitRolloutState) toolkit.Rollout {
			return toolkit.Rollout{
				ID: r.GetId(), Tool: r.GetTool(), Version: r.GetVersion(), From: r.GetFrom(),
				Stage: toolkit.Stage(r.GetStage()), Started: fromTS(r.GetStarted()), Updated: fromTS(r.GetUpdated()),
				PinPR: r.GetPinPr(), PinState: r.GetPinState(), Applied: r.GetApplied(), Error: r.GetError(), Notes: r.GetNotes(), NotesURL: r.GetNotesUrl(),
				Targets: mapSlice(r.GetTargets(), func(x *pb.ToolkitTarget) toolkit.TargetRun {
					return toolkit.TargetRun{
						Target: x.GetTarget(), Status: toolkit.Status(x.GetStatus()), Detail: x.GetDetail(),
						URL: x.GetUrl(), Branch: x.GetBranch(), Base: x.GetBase(), Held: x.GetHeld(),
						PRState: x.GetPrState(), CI: x.GetCi(), Files: int(x.GetFiles()),
					}
				}),
			}
		}),
	}
}

// ---------------------------------------------------------------------------
// events

// EventToPB converts a hub event for the wire.
func EventToPB(e hub.Event) *pb.Event {
	out := &pb.Event{Kind: pb.EventKind(e.Kind) + 1, Text: e.Text} //nolint:gosec // a small enum
	if a := e.Alert; a != nil {
		out.Alert = &pb.Alert{Type: pb.AlertType(a.Type) + 1, Ref: a.Ref, Text: a.Text} //nolint:gosec // a small enum
		if a.Run != nil {
			out.Alert.Run = RunToPB(*a.Run)
		}
		if a.Session != nil {
			out.Alert.Session = SessionToPB(*a.Session)
		}
	}
	return out
}

// EventFromPB converts a wire event back.
func EventFromPB(p *pb.Event) hub.Event {
	e := hub.Event{Kind: hub.EventKind(max(p.GetKind()-1, 0)), Text: p.GetText()}
	if a := p.GetAlert(); a != nil {
		e.Alert = &hub.Alert{Type: hub.AlertType(max(a.GetType()-1, 0)), Ref: a.GetRef(), Text: a.GetText()}
		if a.GetRun() != nil {
			r := RunFromPB(a.GetRun())
			e.Alert.Run = &r
		}
		if a.GetSession() != nil {
			s := SessionFromPB(a.GetSession())
			e.Alert.Session = &s
		}
	}
	return e
}

// ---------------------------------------------------------------------------
// commands

func menuChoiceToPB(c dispatch.MenuChoice) *pb.MenuChoice {
	return &pb.MenuChoice{Task: string(c.Task), Cmd: c.Cmd, Target: c.Target, Suggested: c.Suggested}
}

func menuChoiceFromPB(p *pb.MenuChoice) dispatch.MenuChoice {
	return dispatch.MenuChoice{Task: dispatch.Task(p.GetTask()), Cmd: p.GetCmd(), Target: p.GetTarget(), Suggested: p.GetSuggested()}
}

// recordToPB keeps the part of a dispatch record a client shows: where
// the run started.
func recordToPB(r dispatch.Record) *pb.DispatchResponse {
	return &pb.DispatchResponse{
		Task: string(r.Task), Ref: r.Ref, Workspace: r.Workspace,
		Machine: r.Machine, Runner: r.Runner, Dir: r.Dir,
	}
}

func recordFromPB(p *pb.DispatchResponse) dispatch.Record {
	return dispatch.Record{
		Task: dispatch.Task(p.GetTask()), Ref: p.GetRef(), Workspace: p.GetWorkspace(),
		Machine: p.GetMachine(), Runner: p.GetRunner(), Dir: p.GetDir(),
	}
}

func sweepToPB(s dispatch.BranchSweep) *pb.PruneBranchesResponse {
	return &pb.PruneBranchesResponse{
		Local:     mapSlice(s.Local, branchPruneToPB),
		Remote:    mapSlice(s.Remote, branchPruneToPB),
		Kept:      int32(s.Kept), //nolint:gosec // a branch count
		Errors:    s.Errors,
		Forks:     mapSlice(s.Forks, forkSyncToPB),
		Worktrees: s.Worktrees,
	}
}

func sweepFromPB(p *pb.PruneBranchesResponse) dispatch.BranchSweep {
	return dispatch.BranchSweep{
		Local:     mapSlice(p.GetLocal(), branchPruneFromPB),
		Remote:    mapSlice(p.GetRemote(), branchPruneFromPB),
		Kept:      int(p.GetKept()),
		Errors:    p.GetErrors(),
		Forks:     mapSlice(p.GetForks(), forkSyncFromPB),
		Worktrees: p.GetWorktrees(),
	}
}

func branchPruneToPB(b dispatch.BranchPrune) *pb.BranchPrune {
	return &pb.BranchPrune{
		Machine: b.Machine, Workspace: b.Workspace, Repo: b.Repo, Branch: b.Branch,
		Pr: b.PR, State: b.State, Action: b.Action,
	}
}

func branchPruneFromPB(p *pb.BranchPrune) dispatch.BranchPrune {
	return dispatch.BranchPrune{
		Machine: p.GetMachine(), Workspace: p.GetWorkspace(), Repo: p.GetRepo(), Branch: p.GetBranch(),
		PR: p.GetPr(), State: p.GetState(), Action: p.GetAction(),
	}
}

func forkSyncToPB(f dispatch.ForkSync) *pb.ForkSync {
	return &pb.ForkSync{
		Repo: f.Repo, Branch: f.Branch, Action: f.Action,
		Behind: int32(f.Behind), Ahead: int32(f.Ahead), //nolint:gosec // commit counts
	}
}

func forkSyncFromPB(p *pb.ForkSync) dispatch.ForkSync {
	return dispatch.ForkSync{
		Repo: p.GetRepo(), Branch: p.GetBranch(), Action: p.GetAction(),
		Behind: int(p.GetBehind()), Ahead: int(p.GetAhead()),
	}
}

func transcriptToPB(t hub.Transcript) *pb.TranscriptResponse {
	out := &pb.TranscriptResponse{Omitted: clamp32(t.Omitted)}
	for _, m := range t.Messages {
		pm := &pb.TranscriptMessage{Role: m.Role, Created: ts(m.Created)}
		for _, p := range m.Parts {
			pm.Parts = append(pm.Parts, &pb.TranscriptPart{Text: p.Text, Tool: p.Tool, Failed: p.Failed})
		}
		out.Messages = append(out.Messages, pm)
	}
	return out
}

func transcriptFromPB(r *pb.TranscriptResponse) hub.Transcript {
	t := hub.Transcript{Omitted: int(r.GetOmitted())}
	for _, pm := range r.GetMessages() {
		m := hub.TranscriptMessage{Role: pm.GetRole(), Created: fromTS(pm.GetCreated())}
		for _, p := range pm.GetParts() {
			m.Parts = append(m.Parts, hub.TranscriptPart{Text: p.GetText(), Tool: p.GetTool(), Failed: p.GetFailed()})
		}
		t.Messages = append(t.Messages, m)
	}
	return t
}

// clamp32 is n as an int32, held to its range.
func clamp32(n int) int32 {
	return int32(max(min(n, math.MaxInt32), math.MinInt32)) //nolint:gosec // clamped on this line
}
