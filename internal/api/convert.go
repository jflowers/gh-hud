// Package api puts hub.Backend on the wire: Connect-RPC (proto in
// proto/ghhud/v1) served from a hub, and a client that is itself a
// hub.Backend, so the TUI works the same against a hub in another
// process.
package api

import (
	"errors"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/jflowers/gh-hud/internal/api/gen/ghhud/v1"
	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/fleet"
	"github.com/jflowers/gh-hud/internal/hub"
	"github.com/jflowers/gh-hud/internal/model"
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
	}
}

func planRowFromPB(p *pb.PlanRow) dispatch.PlanRow {
	r := dispatch.PlanRow{
		Task:    dispatch.Task(p.GetTask()),
		Queued:  int(p.GetQueued()),
		Note:    p.GetNote(),
		Missing: p.GetMissing(),
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
	return &pb.MenuChoice{Task: string(c.Task), Cmd: c.Cmd, Target: c.Target}
}

func menuChoiceFromPB(p *pb.MenuChoice) dispatch.MenuChoice {
	return dispatch.MenuChoice{Task: dispatch.Task(p.GetTask()), Cmd: p.GetCmd(), Target: p.GetTarget()}
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
