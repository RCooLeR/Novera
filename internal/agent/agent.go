// Package agent implements assistant "Agent mode": a tool-calling loop over an
// OpenAI-compatible chat endpoint. The model can call a small, curated set of
// workspace tools; read tools run automatically, mutating tools are gated
// behind explicit user approval (permission control). Each step is streamed to
// the frontend as an "agent:event".
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"novera/internal/artifacts"
	"novera/internal/datatools"
	"novera/internal/db"
	"novera/internal/gitsvc"
	"novera/internal/jobs"
	"novera/internal/netsafe"
	"novera/internal/settings"
	"novera/internal/workspace"
)

const (
	EventName = "agent:event"
	// continueWait is how long a checkpoint waits for the user's keep-going/stop
	// decision before defaulting to stop (so an abandoned run can't live forever).
	continueWait    = 10 * time.Minute
	approvalTimeout = 5 * time.Minute
)

const systemPrompt = `You are Novera's coding agent, operating inside the user's open workspace.
You can call tools to inspect and modify the project. Prefer reading before writing.

Start by calling update_plan with your ordered steps (each "todo"), then work through them. Re-call update_plan to mark the current step "in_progress" and completed steps "done" so the user sees live progress.

Rules:
- Available tools for this run are supplied in the API tools field and summarized below. Call only those exact names.
- Never invent pseudo-tools such as thought, analysis, channel, or commentary.
- Each tool call returns a result. USE that result; never repeat an identical call — the answer will not change.
- Mutating or sensitive tools are approval-gated each time.
- If the current request is vague, use the provided recent conversation context only to resolve references such as "it", "that", or "do it".
- When you have enough information, STOP calling tools and reply with a normal text answer to finish the task.`

var allAgentToolNames = []string{
	"update_plan",
	"list_files", "list_dir", "read_file", "read_many_files", "search_workspace",
	"git_status", "git_diff", "read_diagnostics",
	"infer_csv_schema", "analyze_sql_dump",
	"db_list_connections", "db_list_tables", "db_query",
	"csv_to_sql", "extract_dump_table", "split_dump", "dump_table_to_csv",
	"clean_sql_dump", "csv_select_columns", "csv_add_column",
	"write_file", "apply_edit", "append_file", "apply_patch",
	"copy_file", "move_file", "delete_file",
	"list_rollbacks", "rollback_file_mutation",
	"create_artifact", "list_artifacts",
	"run_command",
}

var inspectToolNames = []string{
	"update_plan", "list_files", "list_dir", "read_file", "read_many_files", "search_workspace", "read_diagnostics",
}

var writeToolNames = []string{
	"write_file", "apply_edit", "append_file", "apply_patch", "copy_file", "move_file", "delete_file",
}

var gitToolNames = []string{"git_status", "git_diff"}

var dataToolNames = []string{
	"infer_csv_schema", "analyze_sql_dump",
	"csv_to_sql", "extract_dump_table", "split_dump", "dump_table_to_csv",
	"clean_sql_dump", "csv_select_columns", "csv_add_column",
}

var dbToolNames = []string{"db_list_connections", "db_list_tables", "db_query"}

var rollbackToolNames = []string{"list_rollbacks", "rollback_file_mutation"}

var artifactToolNames = []string{"create_artifact", "list_artifacts"}

// SecretReader resolves an API key ref.
type SecretReader interface {
	Get(ref string) (string, bool)
}

// Service is the bound Wails agent service.
type Service struct {
	settings  *settings.Service
	secrets   SecretReader
	ws        *workspace.Service
	git       *gitsvc.Service
	db        *db.Service
	http      *http.Client
	audit     *auditLog
	rollback  *rollbackJournal
	jobs      *jobs.Service
	artifacts *artifacts.Service

	mu        sync.Mutex
	cancels   map[string]context.CancelFunc
	approvals map[string]chan bool
	seq       int

	toolset       map[string]tool
	toolOrder     []string          // stable order for a reproducible, cache-friendly wire payload
	toolCanonical map[string]string // normalized name -> canonical tool name
}

// New constructs the agent service.
func New(set *settings.Service, sec SecretReader, ws *workspace.Service, git *gitsvc.Service, database *db.Service, jobsSvc *jobs.Service, artSvc *artifacts.Service) *Service {
	s := &Service{
		settings:  set,
		secrets:   sec,
		ws:        ws,
		git:       git,
		db:        database,
		jobs:      jobsSvc,
		artifacts: artSvc,
		http: &http.Client{
			// Refuse cross-host redirects: the completion body carries workspace
			// context / tool output and must not follow a redirect to another host.
			CheckRedirect: netsafe.RedirectPolicy(5),
		},
		audit:     newAuditLog(),
		rollback:  newRollbackJournal(),
		cancels:   map[string]context.CancelFunc{},
		approvals: map[string]chan bool{},
	}
	s.toolset, s.toolOrder = s.buildTools()
	s.toolCanonical = make(map[string]string, len(s.toolOrder))
	for _, name := range s.toolOrder {
		s.toolCanonical[normalizeToolName(name)] = name
	}
	return s
}

// normalizeToolName folds separators (-, ., :, /, space, _), case, and
// camelCase boundaries so a model calling "List Files", "list-files", or
// "listFiles" all resolve to the canonical "list_files". This is the fix for
// the exact-match dispatch that made the agent loop on unknown-tool errors.
func normalizeToolName(name string) string {
	var b strings.Builder
	needsSep := false
	prevAlnum := false
	for _, ch := range strings.TrimSpace(name) {
		switch {
		case ch >= 'A' && ch <= 'Z':
			if (needsSep || prevAlnum) && b.Len() > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(ch + ('a' - 'A'))
			needsSep = false
			prevAlnum = false // a run of capitals shouldn't insert separators between each
		case (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9'):
			if needsSep && b.Len() > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(ch)
			needsSep = false
			prevAlnum = true
		default:
			if b.Len() > 0 {
				needsSep = true
			}
			prevAlnum = false
		}
	}
	return b.String()
}

func selectToolNamesForPrompt(prompt string) []string {
	text := strings.ToLower(prompt)
	chosen := map[string]bool{}
	add := func(names ...string) {
		for _, name := range names {
			chosen[name] = true
		}
	}
	add(inspectToolNames...)

	wantsWrite := containsAny(text,
		"write", "save", "create", "edit", "modify", "fix", "implement", "change",
		"patch", "append", "delete", "remove", "move", "rename", "copy", "refactor",
		"generate", "add ", "update ", "replace", "overwrite", "persist",
		"write these findings", "write the result", "write the review", "to file", "into file",
	)
	wantsData := containsAny(text,
		"csv", "tsv", "sql dump", ".dump", "dump file", "pg_dump", "schema", "column",
		"convert csv", "clean dump", "split dump", "extract table", "select columns",
	)
	wantsDB := containsAny(text,
		"database", "db ", "connection", "connections", "table", "tables", "query",
		"select ", "sqlite", "postgres", "mysql", "mariadb",
	)
	wantsGit := containsAny(text,
		"git", "diff", "status", "branch", "commit", "push", "staged", "unstaged",
		"working tree", "pull request", " pr ", "review changes",
	)
	wantsRun := wantsWrite || containsAny(text,
		"run ", "execute", "command", "shell", "terminal", "powershell", "cmd ",
		"test", "tests", "typecheck", "lint", "format", "build", "rebuild", "re-build",
		"npm ", "pnpm ", "yarn ", "go test", "task ",
	)
	wantsRollback := containsAny(text, "rollback", "undo", "revert", "restore previous")
	wantsArtifact := containsAny(text, "artifact", "artifacts", "register artifact", "lineage", "freshness")

	if wantsWrite {
		add(writeToolNames...)
	}
	if wantsGit {
		add(gitToolNames...)
	}
	if wantsData {
		add(dataToolNames...)
	}
	if wantsDB {
		add(dbToolNames...)
	}
	if wantsRollback {
		add(rollbackToolNames...)
	}
	if wantsArtifact {
		add(artifactToolNames...)
	}
	if wantsRun {
		add("run_command")
	}
	return orderedToolSubset(chosen)
}

func containsAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func orderedToolSubset(chosen map[string]bool) []string {
	out := make([]string, 0, len(chosen))
	for _, name := range allAgentToolNames {
		if chosen[name] {
			out = append(out, name)
		}
	}
	return out
}

func toolNameSet(names []string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, name := range names {
		out[name] = true
	}
	return out
}

// Start kicks off an agent run for the prompt and returns the run id.
func (s *Service) Start(prompt string) (string, error) {
	cfg := s.settings.Load().LLM
	if strings.TrimSpace(cfg.BaseURL) == "" || strings.TrimSpace(cfg.Model) == "" {
		return "", errors.New("Configure an LLM provider and model in the Assistant settings first.")
	}
	if err := netsafe.ValidateEndpoint(cfg.BaseURL); err != nil {
		return "", err
	}
	// Cancel-only context: the run isn't bounded by a fixed wall clock — the
	// user gates it at each step checkpoint and via Cancel. Each LLM call is
	// individually time-bounded inside run().
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	// Only one run at a time: approvals are keyed by callID, so concurrent runs
	// could collide on a provider-reused tool-call id. A single active run also
	// makes the tool loop's sequential approval handling unambiguous.
	if len(s.cancels) > 0 {
		s.mu.Unlock()
		cancel()
		return "", errors.New("An agent run is already in progress. Stop it before starting another.")
	}
	s.seq++
	id := fmt.Sprintf("run-%d", s.seq)
	s.cancels[id] = cancel
	s.mu.Unlock()
	// Mirror the run into the jobs ledger so it appears in the Jobs panel and can
	// be cancelled there too (the cancel hook is this run's context cancel).
	jobID := s.jobs.Start("agent", clip(prompt, 80), cancel)
	go s.run(ctx, id, jobID, prompt)
	return id, nil
}

// Approve resolves a pending mutating-tool approval or step checkpoint. The
// delivery and de-registration happen atomically under the lock so a waiter
// that is simultaneously timing out can't miss a decision: whoever takes the
// lock first wins — either Approve delivers into the (cap-1, send-once) buffer
// and removes the entry, or the waiter has already removed it and Approve sees
// nothing to do. The buffered send never blocks because each channel is sent to
// at most once before its entry is deleted.
func (s *Service) Approve(callID string, approved bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := s.approvals[callID]
	if ch == nil {
		return
	}
	delete(s.approvals, callID)
	select {
	case ch <- approved:
	default:
	}
}

// AuditLog returns recent agent tool-action records (newest first) — the durable
// approval/audit surface. A limit <= 0 returns all retained entries.
func (s *Service) AuditLog(limit int) []AuditEntry {
	return s.audit.list(limit)
}

// Cancel aborts a run.
func (s *Service) Cancel(runID string) {
	s.mu.Lock()
	cancel := s.cancels[runID]
	delete(s.cancels, runID)
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Service) run(ctx context.Context, runID, jobID, prompt string) {
	// Job outcome: defaults to failed so an unexpected return path still closes
	// the ledger entry; terminal points below set success, and the finalizer
	// promotes to canceled when the context was cancelled. Declared first so it
	// runs LAST (after the recover below has had a chance to record a crash).
	jobStatus := jobs.StatusFailed
	jobErr := ""
	defer func() {
		if ctx.Err() != nil && jobStatus != jobs.StatusSuccess {
			jobStatus = jobs.StatusCanceled
		}
		s.jobs.Finish(jobID, jobStatus, jobErr)
	}()
	defer func() {
		s.mu.Lock()
		delete(s.cancels, runID)
		s.mu.Unlock()
	}()
	// A panic in a provider call / JSON decode must not take down the whole app —
	// surface it as a run error instead.
	defer func() {
		if r := recover(); r != nil {
			jobErr = fmt.Sprintf("crashed: %v", r)
			s.emit(agentEvent{RunID: runID, Type: "error", Text: fmt.Sprintf("Agent run crashed: %v", r)})
		}
	}()

	appSettings := s.settings.Load()
	cfg := appSettings.LLM
	agentCfg := appSettings.Agent.Normalized()
	key := ""
	if cfg.APIKeyRef != "" && s.secrets != nil {
		key, _ = s.secrets.Get(cfg.APIKeyRef)
	}

	activeToolNames := selectToolNamesForPrompt(prompt)
	messages := []wireMsg{
		{Role: "system", Content: s.systemPromptForTools(activeToolNames)},
		{Role: "user", Content: prompt},
	}
	toolDefs := s.toolDefsFor(activeToolNames)
	activeToolSet := toolNameSet(activeToolNames)
	seen := map[string]int{} // signature -> times called, to break repeat-loops
	var currentPlan []planStep
	retriedNoProgressCompletion := false
	s.jobs.Append(jobID, fmt.Sprintf("active tools: %s", strings.Join(activeToolNames, ", ")))
	s.jobs.Append(jobID, fmt.Sprintf("limits: requestTimeout=%s stepBatch=%d maxSteps=%d historyWindow=%d maxToolOutput=%d commandTimeout=%s",
		cfg.RequestTimeout(), agentCfg.StepBatch, agentCfg.MaxTotalSteps, agentCfg.HistoryWindowGroups, agentCfg.MaxToolOutputChars, agentCfg.CommandTimeout()))

	totalSteps := 0
	for {
		batchEnd := totalSteps + agentCfg.StepBatch
		for totalSteps < batchEnd {
			if ctx.Err() != nil {
				s.emit(agentEvent{RunID: runID, Type: "error", Text: "Run cancelled."})
				return
			}
			// Bound each completion so a wedged provider can't hang a step. The
			// completion is non-streaming, so this must cover the model's whole
			// generation time (plus first-call model load) — hence the generous,
			// user-configurable budget rather than a fixed wall clock. The closure
			// scopes ccancel to this iteration — released even if complete panics,
			// with no per-iteration timer accumulation.
			comp, err := func() (completion, error) {
				cctx, ccancel := context.WithTimeout(ctx, cfg.RequestTimeout())
				defer ccancel()
				// Send a recency-windowed view; the full transcript is still kept
				// locally (messages) for correct tool-call/result pairing.
				return s.complete(cctx, cfg.BaseURL, cfg.Model, key, windowMessages(messages, agentCfg.HistoryWindowGroups), toolDefs)
			}()
			if err != nil {
				if ctx.Err() != nil {
					s.emit(agentEvent{RunID: runID, Type: "error", Text: "Run cancelled."})
					return
				}
				// The per-completion deadline (not a user cancel — ctx is still
				// live) is the common failure with a slow local model. Translate
				// the opaque "context deadline exceeded" into an actionable hint.
				text := err.Error()
				if errors.Is(err, context.DeadlineExceeded) {
					text = fmt.Sprintf("The model didn't respond within %s. If your model is slow (a large model, or the first call after load), raise \"Request timeout\" in the Assistant provider settings.", cfg.RequestTimeout())
				}
				s.emit(agentEvent{RunID: runID, Type: "error", Text: text})
				jobErr = text
				return
			}
			visibleContent := visibleAssistantContent(comp.Content)
			if len(comp.ToolCalls) == 0 {
				if hasOpenPlanStep(currentPlan) {
					if !retriedNoProgressCompletion {
						retriedNoProgressCompletion = true
						messages = append(messages,
							wireMsg{Role: "assistant", Content: comp.Content},
							wireMsg{
								Role:    "user",
								Content: "You stopped without completing the visible plan. Continue now by emitting real tool_calls, not by describing them in text. If the remaining step is to create or update a file, call write_file/apply_edit/apply_patch with valid JSON arguments. After the tool succeeds, call update_plan to mark the step done.",
							},
						)
						continue
					}
					text := "The model stopped without completing the plan or emitting a real tool call. This provider may not support OpenAI-compatible tool calls reliably; it wrote text instead of calling the file-writing tool."
					s.emit(agentEvent{RunID: runID, Type: "error", Text: text})
					jobErr = text
					return
				}
				if visibleContent != "" {
					s.emit(agentEvent{RunID: runID, Type: "assistant_text", Text: visibleContent})
					s.jobs.Append(jobID, "assistant: "+clip(visibleContent, 160))
				}
				jobStatus = jobs.StatusSuccess
				s.emit(agentEvent{RunID: runID, Type: "done"})
				return
			}
			knownCalls := 0
			// Some providers omit tool-call ids; synthesize a stable, unique one so
			// the echoed assistant message, the follow-up tool message, and the
			// frontend tool cards / approvals all correlate.
			for idx := range comp.ToolCalls {
				if strings.TrimSpace(comp.ToolCalls[idx].ID) == "" {
					comp.ToolCalls[idx].ID = fmt.Sprintf("%s-%d-%d", runID, totalSteps, idx)
				}
			}
			messages = append(messages, wireMsg{Role: "assistant", Content: comp.Content, ToolCalls: comp.ToolCalls})
			for _, tc := range comp.ToolCalls {
				canon, known := s.toolCanonical[normalizeToolName(tc.Function.Name)]
				if known && activeToolSet[canon] {
					knownCalls++
				}
				sigName := normalizeToolName(tc.Function.Name)
				if known {
					sigName = canon
				}
				sig := sigName + "|" + strings.TrimSpace(tc.Function.Arguments)
				seen[sig]++
				var result dispatchResult
				if seen[sig] > 2 {
					result.output = "You already made this exact tool call twice; the result will not change. Stop calling tools and use what you already have to write your final answer."
					s.emit(agentEvent{RunID: runID, Type: "tool_result", CallID: tc.ID, Tool: tc.Function.Name, Result: result.output})
				} else {
					result = s.dispatch(ctx, runID, tc, activeToolSet, activeToolNames, agentCfg)
				}
				if result.denied {
					text := fmt.Sprintf("Action denied: %s was not run. The agent stopped and made no fallback changes.", result.tool)
					s.jobs.Append(jobID, text)
					jobErr = text
					jobStatus = jobs.StatusCanceled
					s.emit(agentEvent{RunID: runID, Type: "error", Text: text})
					return
				}
				if canon == "update_plan" && activeToolSet[canon] {
					currentPlan = parsePlan(parseArgs(tc.Function.Arguments))
				}
				messages = append(messages, wireMsg{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: result.output})
				s.jobs.Append(jobID, fmt.Sprintf("%s: %s", tc.Function.Name, clip(result.output, 160)))
			}
			if knownCalls == 0 {
				if !retriedNoProgressCompletion {
					retriedNoProgressCompletion = true
					messages = append(messages, wireMsg{
						Role:    "user",
						Content: fmt.Sprintf("The tool calls you just emitted were not valid active Novera tools. Do not use pseudo-tools such as thought, analysis, or channel. Continue by calling one of these active function tools exactly as named, with valid JSON arguments: %s.", strings.Join(activeToolNames, ", ")),
					})
					continue
				}
				text := "The model repeatedly emitted invalid pseudo-tool calls instead of Novera tools. This provider may not support OpenAI-compatible tool calls reliably."
				s.emit(agentEvent{RunID: runID, Type: "error", Text: text})
				jobErr = text
				return
			}
			retriedNoProgressCompletion = false
			totalSteps++
		}
		// Reached a checkpoint without finishing. Absolute backstop first…
		if totalSteps >= agentCfg.MaxTotalSteps {
			jobErr = fmt.Sprintf("reached the absolute step ceiling (%d)", agentCfg.MaxTotalSteps)
			s.emit(agentEvent{RunID: runID, Type: "error", Text: fmt.Sprintf("Reached the absolute step ceiling (%d). Send another message to continue.", agentCfg.MaxTotalSteps)})
			return
		}
		// …then ask the user whether to keep going.
		if !s.awaitContinue(ctx, runID, totalSteps) {
			jobStatus = jobs.StatusSuccess
			s.emit(agentEvent{RunID: runID, Type: "done"})
			return
		}
		messages = append(messages, wireMsg{
			Role:    "user",
			Content: fmt.Sprintf("Continue. You have used %d steps so far; keep working toward the goal and stop calling tools once it's complete.", totalSteps),
		})
	}
}

// awaitContinue pauses at a step checkpoint and asks the user whether to keep
// going. It reuses the approval channel plumbing: the frontend answers with
// Agent.Approve(callID, true) to continue or false to stop. Returns false on
// stop, cancel, or no response within continueWait.
func (s *Service) awaitContinue(ctx context.Context, runID string, steps int) bool {
	callID := fmt.Sprintf("%s-continue-%d", runID, steps)
	return s.awaitGate(ctx, callID, continueWait, func() {
		s.emit(agentEvent{RunID: runID, Type: "continue_request", CallID: callID, Text: fmt.Sprintf("%d", steps)})
	})
}

type dispatchResult struct {
	output string
	denied bool
	tool   string
}

func (s *Service) dispatch(ctx context.Context, runID string, tc wireToolCall, active map[string]bool, activeNames []string, cfg settings.Agent) dispatchResult {
	name := tc.Function.Name
	args := parseArgs(tc.Function.Arguments)

	canon, known := s.toolCanonical[normalizeToolName(name)]
	if !known {
		out := fmt.Sprintf("Unknown tool %q. Active tools: %s. Call one of these exactly.", name, strings.Join(activeNames, ", "))
		return dispatchResult{output: out, tool: name}
	}
	name = canon
	if !active[name] {
		out := fmt.Sprintf("Tool %q is not active for this run. Active tools: %s. Continue with one of those tools, or explain why the task cannot be completed with them.", name, strings.Join(activeNames, ", "))
		return dispatchResult{output: out, tool: name}
	}
	s.emit(agentEvent{RunID: runID, Type: "tool_call", CallID: tc.ID, Tool: name, Args: tc.Function.Arguments})
	t := s.toolset[name]
	if name == "update_plan" {
		s.emit(agentEvent{RunID: runID, Type: "plan", Plan: parsePlan(args)})
	}
	decision := "auto"
	if t.mutating || t.gated {
		if !s.awaitApproval(ctx, runID, tc.ID, name, tc.Function.Arguments) {
			out := "The user denied this action."
			s.audit.record(AuditEntry{RunID: runID, CallID: tc.ID, Tool: name, Summary: auditSummary(args), Decision: "denied", Status: "denied", Detail: out})
			s.emit(agentEvent{RunID: runID, Type: "tool_result", CallID: tc.ID, Tool: name, Result: out})
			return dispatchResult{output: out, denied: true, tool: name}
		}
		decision = "approved"
	}
	// run_command is the one tool that must honour the run's context (cancel /
	// run-timeout), so it is dispatched with ctx rather than the ctx-less closure.
	var out string
	var err error
	if name == "run_command" {
		out, err = s.runCommand(ctx, getStr(args, "command"), cfg.CommandTimeout())
	} else {
		out, err = t.run(args)
	}
	if err != nil {
		out = "error: " + err.Error()
	}
	out = clip(out, cfg.Normalized().MaxToolOutputChars)
	status := "ok"
	if err != nil {
		status = "error"
	}
	s.audit.record(AuditEntry{RunID: runID, CallID: tc.ID, Tool: name, Summary: auditSummary(args), Decision: decision, Status: status, Detail: clip(out, 300)})
	s.emit(agentEvent{RunID: runID, Type: "tool_result", CallID: tc.ID, Tool: name, Result: out})
	return dispatchResult{output: out, tool: name}
}

func (s *Service) awaitApproval(ctx context.Context, runID, callID, tool, args string) bool {
	return s.awaitGate(ctx, callID, approvalTimeout, func() {
		s.emit(agentEvent{RunID: runID, Type: "approval_request", CallID: callID, Tool: tool, Args: args})
	})
}

// awaitGate registers a one-shot decision channel under callID, emits the
// request, then blocks until the user answers (via Approve), the run is
// cancelled, or timeout elapses. It is race-free with Approve: de-registration
// happens under the same lock Approve uses, so on timeout we either drain a
// decision Approve already buffered or guarantee Approve will find no entry and
// not send. Returns false on cancel/timeout/no-response (fail-closed).
func (s *Service) awaitGate(ctx context.Context, callID string, timeout time.Duration, emitReq func()) bool {
	ch := make(chan bool, 1)
	s.mu.Lock()
	s.approvals[callID] = ch
	s.mu.Unlock()
	// deregister removes our entry under the lock and reports whether Approve
	// had already removed it (i.e. has delivered, or is delivering, a decision).
	deregister := func() (deliveredByApprove bool) {
		s.mu.Lock()
		_, present := s.approvals[callID]
		delete(s.approvals, callID)
		s.mu.Unlock()
		return !present
	}
	emitReq()
	select {
	case ok := <-ch:
		deregister()
		return ok
	case <-ctx.Done():
		deregister()
		return false
	case <-time.After(timeout):
		if deregister() {
			// Approve beat the timer — its decision is buffered; read it.
			select {
			case ok := <-ch:
				return ok
			default:
			}
		}
		return false
	}
}

func (s *Service) emit(ev agentEvent) {
	if app := application.Get(); app != nil {
		app.Event.Emit(EventName, ev)
	}
}

// --- tools ---

type tool struct {
	description string
	parameters  map[string]any
	mutating    bool // modifies the workspace; gated behind approval
	gated       bool // non-mutating but sensitive (e.g. reads DB rows); gated behind approval
	run         func(args map[string]any) (string, error)
}

// buildTools is called once in New; closures capture s. Returns the tool map
// plus a stable name order so the wire payload is deterministic (cache-friendly).
func (s *Service) buildTools() (map[string]tool, []string) {
	strSchema := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required}
	}
	prop := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

	m := map[string]tool{
		"update_plan": {
			description: `Record or update your ordered task plan and keep it CURRENT. 'steps' is an array of objects, each {"title": short step text, "status": "todo" | "in_progress" | "done"}. Call this first with every step "todo", then call it again as you work to flip the step you're on to "in_progress" and finished steps to "done". Keep exactly one step "in_progress" at a time.`,
			parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"steps": map[string]any{
						"type":        "array",
						"description": "Ordered plan steps, each with its status",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"title":  map[string]any{"type": "string", "description": "Short step description"},
								"status": map[string]any{"type": "string", "enum": []string{"todo", "in_progress", "done"}, "description": "Step status"},
							},
							"required": []string{"title", "status"},
						},
					},
				},
				"required": []string{"steps"},
			},
			run: func(map[string]any) (string, error) { return "Plan updated.", nil },
		},
		"list_files": {
			description: "List source file paths in the workspace. Generated noise (lockfiles, minified bundles, source maps, *.pb.go/_pb2.py, *.generated.*) is omitted to keep the listing focused; reach those with list_dir or read_file by exact path.",
			parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			run: func(map[string]any) (string, error) {
				files, err := s.ws.ListAllFiles()
				if err != nil {
					return "", err
				}
				// Drop generated noise from the bulk listing so it doesn't waste
				// the model's token budget (the UI quick-open still shows them).
				kept := files[:0]
				for _, f := range files {
					if !isAgentNoiseFile(f) {
						kept = append(kept, f)
					}
				}
				if len(kept) == 0 {
					return "(no source files)", nil
				}
				return strings.Join(kept, "\n"), nil
			},
		},
		"read_file": {
			description: "Read a UTF-8 text file from the workspace by its relative path.",
			parameters:  strSchema(map[string]any{"path": prop("Workspace-relative file path")}, "path"),
			run: func(args map[string]any) (string, error) {
				fc, err := s.ws.ReadFile(getStr(args, "path"))
				if err != nil {
					return "", err
				}
				if fc.Binary {
					return "(binary file)", nil
				}
				if fc.TooLarge {
					return "(file too large)", nil
				}
				return fc.Content, nil
			},
		},
		"search_workspace": {
			description: "Search the workspace for a literal string. Returns matching path:line: text.",
			parameters:  strSchema(map[string]any{"query": prop("Text to search for")}, "query"),
			run: func(args map[string]any) (string, error) {
				r, err := s.ws.Search(getStr(args, "query"), false)
				if err != nil {
					return "", err
				}
				var b strings.Builder
				for _, m := range r.Matches {
					fmt.Fprintf(&b, "%s:%d: %s\n", m.Path, m.Line, strings.TrimSpace(m.Text))
				}
				if b.Len() == 0 {
					return "(no matches)", nil
				}
				return b.String(), nil
			},
		},
		"list_dir": {
			description: "List the immediate entries of a directory (use \"\" or \".\" for the workspace root).",
			parameters:  strSchema(map[string]any{"path": prop("Workspace-relative directory path, empty for root")}),
			run: func(args map[string]any) (string, error) {
				entries, err := s.ws.ListDir(getStr(args, "path"))
				if err != nil {
					return "", err
				}
				if len(entries) == 0 {
					return "(empty)", nil
				}
				var b strings.Builder
				for _, e := range entries {
					kind := "file"
					if e.IsDir {
						kind = "dir "
					}
					fmt.Fprintf(&b, "%s  %s\n", kind, e.Path)
				}
				return b.String(), nil
			},
		},
		"read_many_files": {
			description: "Read several text files at once. Provide 'paths' as an array of workspace-relative paths.",
			parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Workspace-relative file paths"},
				},
				"required": []string{"paths"},
			},
			run: func(args map[string]any) (string, error) {
				raw, _ := args["paths"].([]any)
				if len(raw) == 0 {
					return "", errors.New("paths is required")
				}
				limit := s.settings.Load().Agent.Normalized().MaxToolOutputChars * 2
				var b strings.Builder
				for _, p := range raw {
					path, ok := p.(string)
					if !ok || strings.TrimSpace(path) == "" {
						continue
					}
					fmt.Fprintf(&b, "===== %s =====\n", path)
					fc, err := s.ws.ReadFile(path)
					switch {
					case err != nil:
						fmt.Fprintf(&b, "(error: %s)\n\n", err.Error())
					case fc.Binary || fc.TooLarge:
						b.WriteString("(binary or too large)\n\n")
					default:
						b.WriteString(fc.Content)
						b.WriteString("\n\n")
					}
					if b.Len() > limit {
						break
					}
				}
				return clip(b.String(), limit), nil
			},
		},
		"git_status": {
			description: "Show the current git branch and changed files.",
			parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			run: func(map[string]any) (string, error) {
				st, err := s.git.Status()
				if err != nil {
					return "", err
				}
				if !st.Available {
					return st.Message, nil
				}
				var b strings.Builder
				fmt.Fprintf(&b, "branch: %s\n", st.Branch)
				for _, c := range st.Staged {
					fmt.Fprintf(&b, "staged   %s %s\n", c.Summary, c.Path)
				}
				for _, c := range st.Unstaged {
					fmt.Fprintf(&b, "unstaged %s %s\n", c.Summary, c.Path)
				}
				return b.String(), nil
			},
		},
		"read_diagnostics": {
			description: "List static issues across the workspace (TODO/FIXME/HACK/XXX markers and merge-conflict markers) with file:line.",
			parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			run: func(map[string]any) (string, error) {
				d, err := s.ws.Diagnostics()
				if err != nil {
					return "", err
				}
				if len(d.Items) == 0 {
					return "(no issues found)", nil
				}
				var b strings.Builder
				for _, it := range d.Items {
					fmt.Fprintf(&b, "%s:%d [%s] %s\n", it.Path, it.Line, it.Kind, it.Message)
				}
				return b.String(), nil
			},
		},
		"infer_csv_schema": {
			description: "Infer the column schema (SQL types, null counts, sample values) of a CSV/TSV file from a sample of its rows.",
			parameters:  strSchema(map[string]any{"path": prop("Workspace-relative CSV/TSV path")}, "path"),
			run: func(args map[string]any) (string, error) {
				res, err := s.ws.InferTableSchema(getStr(args, "path"), "")
				if err != nil {
					return "", err
				}
				var b strings.Builder
				fmt.Fprintf(&b, "%d columns (sampled %d rows", len(res.Columns), res.RowsScanned)
				if res.Truncated {
					b.WriteString("+")
				}
				b.WriteString("):\n")
				for _, c := range res.Columns {
					fmt.Fprintf(&b, "- %s\t%s\t(nulls: %d", c.Name, c.Type, c.Null)
					if len(c.Samples) > 0 {
						fmt.Fprintf(&b, "; e.g. %s", strings.Join(c.Samples, ", "))
					}
					b.WriteString(")\n")
				}
				return b.String(), nil
			},
		},
		"analyze_sql_dump": {
			description: "Inventory a SQL dump (.sql/.dump): its tables and CREATE/INSERT statement counts, without loading the whole file.",
			parameters:  strSchema(map[string]any{"path": prop("Workspace-relative .sql/.dump path")}, "path"),
			run: func(args map[string]any) (string, error) {
				res, err := s.ws.AnalyzeSQLDump(getStr(args, "path"))
				if err != nil {
					return "", err
				}
				var b strings.Builder
				fmt.Fprintf(&b, "%d tables; %d CREATE, %d INSERT statements", len(res.Tables), res.CreateTables, res.InsertTables)
				if res.Mysqldump {
					b.WriteString("; mysqldump")
				}
				if res.Truncated {
					b.WriteString("; (scan capped)")
				}
				b.WriteByte('\n')
				const maxListed = 200
				for i, t := range res.Tables {
					if i >= maxListed {
						fmt.Fprintf(&b, "(+%d more tables)\n", len(res.Tables)-maxListed)
						break
					}
					fmt.Fprintf(&b, "- %s\n", t.Name)
				}
				return b.String(), nil
			},
		},
		"csv_to_sql": {
			description: "Convert a CSV/TSV file to a .sql file (CREATE TABLE + batched INSERTs) at outPath. Requires user approval (writes a file).",
			parameters:  strSchema(map[string]any{"path": prop("Workspace-relative CSV/TSV path"), "outPath": prop("Workspace-relative output .sql path"), "tableName": prop("SQL table name")}, "path", "outPath", "tableName"),
			mutating:    true,
			run: func(args map[string]any) (string, error) {
				sum, err := s.ws.ConvertCsvToSql(getStr(args, "path"), getStr(args, "outPath"), getStr(args, "tableName"), true)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("wrote %s (%d rows, %d bytes)", getStr(args, "outPath"), sum.Rows, sum.Bytes), nil
			},
		},
		"extract_dump_table": {
			description: "Copy one table's statements out of a SQL dump (.sql/.dump) into a new file at outPath. Requires user approval.",
			parameters:  strSchema(map[string]any{"path": prop("Workspace-relative dump path"), "table": prop("Table name (from analyze_sql_dump)"), "outPath": prop("Workspace-relative output .sql path")}, "path", "table", "outPath"),
			mutating:    true,
			run: func(args map[string]any) (string, error) {
				res, err := s.ws.ExtractDumpTable(getStr(args, "path"), getStr(args, "table"), getStr(args, "outPath"))
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("extracted %q to %s (%d bytes)", getStr(args, "table"), getStr(args, "outPath"), res.Bytes), nil
			},
		},
		"split_dump": {
			description: "Split a SQL dump into one .sql file per table under outDir. Requires user approval (writes files).",
			parameters:  strSchema(map[string]any{"path": prop("Workspace-relative dump path"), "outDir": prop("Workspace-relative output directory")}, "path", "outDir"),
			mutating:    true,
			run: func(args map[string]any) (string, error) {
				res, err := s.ws.SplitDump(getStr(args, "path"), getStr(args, "outDir"))
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("wrote %d table files (%d bytes) under %s", res.Tables, res.Bytes, getStr(args, "outDir")), nil
			},
		},
		"dump_table_to_csv": {
			description: "Extract a table's data from a PostgreSQL dump's COPY block into a CSV file. Requires user approval.",
			parameters:  strSchema(map[string]any{"path": prop("Workspace-relative .sql/.dump path"), "table": prop("Table name (from analyze_sql_dump)"), "outPath": prop("Workspace-relative output .csv path")}, "path", "table", "outPath"),
			mutating:    true,
			run: func(args map[string]any) (string, error) {
				n, err := s.ws.DumpTableToCsv(getStr(args, "path"), getStr(args, "table"), getStr(args, "outPath"))
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("wrote %s (%d rows)", getStr(args, "outPath"), n), nil
			},
		},
		"clean_sql_dump": {
			description: "Clean a SQL dump into a new file: remove DEFINER clauses, rewrite ENGINE/CHARSET/COLLATE, drop AUTO_INCREMENT table options, and/or rename a database. Requires user approval.",
			parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":              prop("Workspace-relative .sql/.dump path"),
					"outPath":           prop("Workspace-relative output path"),
					"removeDefiner":     map[string]any{"type": "boolean", "description": "Strip DEFINER=user@host clauses"},
					"dropAutoIncrement": map[string]any{"type": "boolean", "description": "Strip table-option AUTO_INCREMENT=N"},
					"engine":            prop("Rewrite ENGINE= to this (e.g. InnoDB); empty leaves it"),
					"charset":           prop("Rewrite CHARSET/CHARACTER SET to this; empty leaves it"),
					"collation":         prop("Rewrite COLLATE to this; empty leaves it"),
					"fromDatabase":      prop("Database to rename (requires toDatabase)"),
					"toDatabase":        prop("New database name"),
				},
				"required": []string{"path", "outPath"},
			},
			mutating: true,
			run: func(args map[string]any) (string, error) {
				t := datatools.DumpTransform{
					RemoveDefiner:     getBool(args, "removeDefiner"),
					DropAutoIncrement: getBool(args, "dropAutoIncrement"),
					Engine:            getStr(args, "engine"),
					Charset:           getStr(args, "charset"),
					Collation:         getStr(args, "collation"),
					FromDatabase:      getStr(args, "fromDatabase"),
					ToDatabase:        getStr(args, "toDatabase"),
				}
				sum, err := s.ws.TransformDump(getStr(args, "path"), getStr(args, "outPath"), t)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("wrote %s (%d replacements, %d bytes)", getStr(args, "outPath"), sum.Replacements, sum.BytesOut), nil
			},
		},
		"csv_select_columns": {
			description: "Write a new CSV/TSV keeping only the named columns, in the given order. Requires user approval.",
			parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    prop("Workspace-relative CSV/TSV path"),
					"outPath": prop("Workspace-relative output path"),
					"columns": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Column names to keep, in order"},
				},
				"required": []string{"path", "outPath", "columns"},
			},
			mutating: true,
			run: func(args map[string]any) (string, error) {
				n, err := s.ws.ProjectCsv(getStr(args, "path"), getStr(args, "outPath"), getStrSlice(args, "columns"))
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("wrote %s (%d rows)", getStr(args, "outPath"), n), nil
			},
		},
		"csv_add_column": {
			description: "Write a new CSV/TSV with a constant-valued column appended to every row. Requires user approval.",
			parameters:  strSchema(map[string]any{"path": prop("Workspace-relative CSV/TSV path"), "outPath": prop("Workspace-relative output path"), "name": prop("New column name"), "value": prop("Constant value for every row")}, "path", "outPath", "name", "value"),
			mutating:    true,
			run: func(args map[string]any) (string, error) {
				n, err := s.ws.AddCsvColumn(getStr(args, "path"), getStr(args, "outPath"), getStr(args, "name"), getStr(args, "value"))
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("wrote %s (%d rows)", getStr(args, "outPath"), n), nil
			},
		},
		"db_list_connections": {
			description: "List configured database connections (returns each connection's id, name, and kind).",
			parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			run: func(map[string]any) (string, error) {
				ps := s.db.ListProfiles()
				if len(ps) == 0 {
					return "(no database connections are configured)", nil
				}
				var b strings.Builder
				for _, p := range ps {
					fmt.Fprintf(&b, "%s  %s (%s)\n", p.ID, p.Name, p.Kind)
				}
				return b.String(), nil
			},
		},
		"db_list_tables": {
			description: "List tables and views for a database connection. Use the id from db_list_connections.",
			parameters:  strSchema(map[string]any{"connectionId": prop("Connection id from db_list_connections")}, "connectionId"),
			run: func(args map[string]any) (string, error) {
				ts, err := s.db.ListTables(getStr(args, "connectionId"))
				if err != nil {
					return "", err
				}
				if len(ts) == 0 {
					return "(no tables)", nil
				}
				var b strings.Builder
				const maxListed = 500 // cap so a huge schema can't flood the model
				for i, t := range ts {
					if i >= maxListed {
						fmt.Fprintf(&b, "(+%d more tables not shown)\n", len(ts)-maxListed)
						break
					}
					fmt.Fprintf(&b, "%s\t%s\n", t.Type, t.Name)
				}
				return b.String(), nil
			},
		},
		"db_query": {
			description: "Run a read-only SELECT against a database connection (id from db_list_connections). Returns columns and rows.",
			parameters:  strSchema(map[string]any{"connectionId": prop("Connection id"), "sql": prop("A single read-only SELECT/WITH query")}, "connectionId", "sql"),
			// Read-only, but it surfaces DB rows into the transcript (and onward via
			// the LLM channel), so a prompt-injection payload shouldn't be able to
			// exfiltrate data unattended. Gate each query behind explicit approval.
			gated: true,
			run: func(args map[string]any) (string, error) {
				r, err := s.db.Query(getStr(args, "connectionId"), getStr(args, "sql"), 200)
				if err != nil {
					return "", err
				}
				var b strings.Builder
				b.WriteString(strings.Join(r.Columns, " | "))
				b.WriteByte('\n')
				for _, row := range r.Rows {
					b.WriteString(strings.Join(row, " | "))
					b.WriteByte('\n')
				}
				fmt.Fprintf(&b, "(%d rows", r.RowCount)
				if r.Truncated {
					b.WriteString(", truncated")
				}
				b.WriteString(")")
				return b.String(), nil
			},
		},
		"git_diff": {
			description: "Show a unified diff of unsaved/committed changes vs HEAD. Optionally pass a path to limit it to one file.",
			parameters:  strSchema(map[string]any{"path": prop("Optional workspace-relative file path")}),
			run: func(args map[string]any) (string, error) {
				out, err := s.git.UnifiedDiff(getStr(args, "path"))
				if err != nil {
					return "", err
				}
				if strings.TrimSpace(out) == "" {
					return "(no changes vs HEAD)", nil
				}
				return out, nil
			},
		},
		"write_file": {
			description: "Create or overwrite a workspace file with the given content. Requires user approval.",
			parameters: strSchema(map[string]any{
				"path":    prop("Workspace-relative file path"),
				"content": prop("Full new file content"),
			}, "path", "content"),
			mutating: true,
			run: func(args map[string]any) (string, error) {
				path := getStr(args, "path")
				rb := s.rollback.snapshot(s.ws, "write_file", "write_file "+path, path)
				res, err := s.ws.WriteFile(path, getStr(args, "content"), "", "")
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("wrote %s (undo: %s)", res.Path, rb.ID), nil
			},
		},
		"apply_edit": {
			description: "Replace an exact, unique snippet in a file (surgical edit). oldText must occur exactly once. Requires user approval.",
			parameters: strSchema(map[string]any{
				"path":    prop("Workspace-relative file path"),
				"oldText": prop("Exact existing text to replace (must be unique in the file)"),
				"newText": prop("Replacement text"),
			}, "path", "oldText", "newText"),
			mutating: true,
			run: func(args map[string]any) (string, error) {
				path := getStr(args, "path")
				oldText := getStr(args, "oldText")
				fc, err := s.ws.ReadFile(path)
				if err != nil {
					return "", err
				}
				if fc.Binary || fc.TooLarge {
					return "", errors.New("cannot edit a binary or oversized file")
				}
				n := strings.Count(fc.Content, oldText)
				if oldText == "" || n == 0 {
					return "", errors.New("oldText was not found in the file")
				}
				if n > 1 {
					return "", fmt.Errorf("oldText is not unique (%d matches) — include more surrounding context", n)
				}
				updated := strings.Replace(fc.Content, oldText, getStr(args, "newText"), 1)
				rb := s.rollback.snapshot(s.ws, "apply_edit", "apply_edit "+path, path)
				// Round-trip the file's original encoding so a surgical edit to a
				// non-UTF-8 file doesn't silently rewrite it as UTF-8.
				res, err := s.ws.WriteFile(path, updated, fc.Revision, fc.Encoding)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("edited %s (undo: %s)", res.Path, rb.ID), nil
			},
		},
		"append_file": {
			description: "Append text to the end of a workspace file (creating it if missing). Requires user approval. Prefer apply_edit for changing existing content.",
			parameters: strSchema(map[string]any{
				"path":    prop("Workspace-relative file path"),
				"content": prop("Text to append at the end of the file"),
			}, "path", "content"),
			mutating: true,
			run: func(args map[string]any) (string, error) {
				path := getStr(args, "path")
				prior, _, err := s.ws.ReadRaw(path)
				if err != nil {
					return "", err
				}
				rb := s.rollback.snapshot(s.ws, "append_file", "append_file "+path, path)
				if err := s.ws.WriteRaw(path, append(prior, []byte(getStr(args, "content"))...)); err != nil {
					return "", err
				}
				return fmt.Sprintf("appended to %s (undo: %s)", path, rb.ID), nil
			},
		},
		"apply_patch": {
			description: "Apply a unified-diff patch (one or more hunks) to a single file. Provide 'path' and 'patch' (the unified diff body with @@ headers and space/-/+ lines). @@ line numbers may be approximate — each hunk is located by its context/removed lines, which must match the file exactly once. Use this for several edits to one file in a single call; requires user approval.",
			parameters: strSchema(map[string]any{
				"path":  prop("Workspace-relative file path to patch"),
				"patch": prop("Unified diff hunks (@@ ... @@ with ' ', '-', '+' lines)"),
			}, "path", "patch"),
			mutating: true,
			run: func(args map[string]any) (string, error) {
				path := getStr(args, "path")
				fc, err := s.ws.ReadFile(path)
				if err != nil {
					return "", err
				}
				if fc.Binary || fc.TooLarge {
					return "", errors.New("cannot patch a binary or oversized file")
				}
				hunks, err := parseUnifiedHunks(getStr(args, "patch"))
				if err != nil {
					return "", err
				}
				updated, err := applyHunks(fc.Content, hunks)
				if err != nil {
					return "", err
				}
				rb := s.rollback.snapshot(s.ws, "apply_patch", "apply_patch "+path, path)
				res, err := s.ws.WriteFile(path, updated, fc.Revision, fc.Encoding)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("patched %s with %d hunk(s) (undo: %s)", res.Path, len(hunks), rb.ID), nil
			},
		},
		"copy_file": {
			description: "Copy a workspace file from one path to another (overwrites the destination). Requires user approval.",
			parameters: strSchema(map[string]any{
				"from": prop("Source workspace-relative file path"),
				"to":   prop("Destination workspace-relative file path"),
			}, "from", "to"),
			mutating: true,
			run: func(args map[string]any) (string, error) {
				from, to := getStr(args, "from"), getStr(args, "to")
				data, existed, err := s.ws.ReadRaw(from)
				if err != nil {
					return "", err
				}
				if !existed {
					return "", fmt.Errorf("source %q does not exist", from)
				}
				rb := s.rollback.snapshot(s.ws, "copy_file", "copy_file "+from+" -> "+to, to)
				if err := s.ws.WriteRaw(to, data); err != nil {
					return "", err
				}
				return fmt.Sprintf("copied %s -> %s (undo: %s)", from, to, rb.ID), nil
			},
		},
		"move_file": {
			description: "Move or rename a workspace file. Requires user approval.",
			parameters: strSchema(map[string]any{
				"from": prop("Source workspace-relative file path"),
				"to":   prop("Destination workspace-relative file path"),
			}, "from", "to"),
			mutating: true,
			run: func(args map[string]any) (string, error) {
				from, to := getStr(args, "from"), getStr(args, "to")
				// Snapshot both endpoints so undo restores the source and any
				// destination the move overwrote.
				rb := s.rollback.snapshot(s.ws, "move_file", "move_file "+from+" -> "+to, from, to)
				if err := s.ws.Rename(from, to); err != nil {
					return "", err
				}
				return fmt.Sprintf("moved %s -> %s (undo: %s)", from, to, rb.ID), nil
			},
		},
		"delete_file": {
			description: "Delete a workspace file or directory (recursively). Requires user approval. The deletion can be undone with rollback_file_mutation.",
			parameters:  strSchema(map[string]any{"path": prop("Workspace-relative path to delete")}, "path"),
			mutating:    true,
			run: func(args map[string]any) (string, error) {
				path := getStr(args, "path")
				rb := s.rollback.snapshot(s.ws, "delete_file", "delete_file "+path, path)
				if err := s.ws.Delete(path); err != nil {
					return "", err
				}
				note := ""
				if rb.incomplete {
					note = " (note: too large to snapshot — not undoable)"
				}
				return fmt.Sprintf("deleted %s (undo: %s)%s", path, rb.ID, note), nil
			},
		},
		"list_rollbacks": {
			description: "List recent undoable file mutations (newest first) with their ids, so you can undo one with rollback_file_mutation.",
			parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			run: func(map[string]any) (string, error) {
				entries := s.rollback.list()
				if len(entries) == 0 {
					return "(no undoable mutations recorded)", nil
				}
				var b strings.Builder
				for _, e := range entries {
					fmt.Fprintf(&b, "%s\t%s\t%s\n", e.ID, e.Tool, strings.Join(e.Files, ", "))
				}
				return strings.TrimRight(b.String(), "\n"), nil
			},
		},
		"rollback_file_mutation": {
			description: "Undo a previous file mutation by its rollback id (from list_rollbacks or a write tool's result), restoring the affected files to their prior state. Requires user approval.",
			parameters:  strSchema(map[string]any{"id": prop("Rollback id, e.g. rb-3")}, "id"),
			mutating:    true,
			run: func(args map[string]any) (string, error) {
				e, err := s.rollback.rollback(s.ws, getStr(args, "id"))
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("rolled back %s (%s): restored %s", e.ID, e.Tool, strings.Join(e.Files, ", ")), nil
			},
		},
		"create_artifact": {
			description: "Register a workspace file you produced (a report, chart, SQL, dataset, answer, …) as a tracked artifact, recording its lineage (the source files it derived from) so the user can find and re-generate it later. Write the file first, then register it.",
			parameters: strSchema(map[string]any{
				"path":    prop("Workspace-relative path of the content file"),
				"kind":    prop("Artifact kind: report | chart | sql | dataset | answer | comparison | notebook | file"),
				"title":   prop("Short human title"),
				"sources": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Workspace-relative source files this derived from (lineage)"},
				"note":    prop("Optional note"),
			}, "path", "kind"),
			run: func(args map[string]any) (string, error) {
				a, err := s.artifacts.CreateArtifact(artifacts.Artifact{
					Kind:    getStr(args, "kind"),
					Title:   getStr(args, "title"),
					Path:    getStr(args, "path"),
					Sources: getStrSlice(args, "sources"),
					Note:    getStr(args, "note"),
					Tool:    "create_artifact",
				})
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("registered artifact %s (%s) %q", a.ID, a.Kind, a.Title), nil
			},
		},
		"list_artifacts": {
			description: "List registered artifacts with their kind, title, path, and freshness (ok / stale / missing / archived).",
			parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			run: func(map[string]any) (string, error) {
				list, err := s.artifacts.ListArtifacts()
				if err != nil {
					return "", err
				}
				if len(list) == 0 {
					return "(no artifacts registered)", nil
				}
				var b strings.Builder
				for _, a := range list {
					status := "ok"
					switch {
					case a.Missing:
						status = "missing"
					case a.Stale:
						status = "stale"
					}
					if a.Archived {
						status += ",archived"
					}
					fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t[%s]\n", a.ID, a.Kind, a.Title, a.Path, status)
				}
				return strings.TrimRight(b.String(), "\n"), nil
			},
		},
		"run_command": {
			description: "Run a shell command in the workspace root and return its combined output. Requires user approval.",
			parameters:  strSchema(map[string]any{"command": prop("Shell command line to run")}, "command"),
			mutating:    true,
			run: func(args map[string]any) (string, error) {
				// dispatch special-cases run_command with the run ctx; this closure
				// is a ctx-less fallback only.
				return s.runCommand(context.Background(), getStr(args, "command"), s.settings.Load().Agent.CommandTimeout())
			},
		},
	}
	return m, append([]string(nil), allAgentToolNames...)
}

func (s *Service) systemPromptForTools(names []string) string {
	var b strings.Builder
	b.WriteString(systemPrompt)
	b.WriteString("\n\nActive tools for this run:\n")
	for _, name := range names {
		t, ok := s.toolset[name]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "- %s: %s\n", name, t.description)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (s *Service) toolDefsFor(names []string) []wireToolDef {
	defs := make([]wireToolDef, 0, len(names))
	for _, name := range names {
		t := s.toolset[name]
		defs = append(defs, wireToolDef{
			Type:     "function",
			Function: wireToolFunc{Name: name, Description: t.description, Parameters: t.parameters},
		})
	}
	return defs
}

// --- completion (non-streaming, tools) ---

func (s *Service) complete(ctx context.Context, base, model, key string, msgs []wireMsg, tools []wireToolDef) (completion, error) {
	body := completionRequest{Model: model, Messages: msgs, Tools: tools, Temperature: 0.2, Stream: false, ToolChoice: "auto"}
	raw, err := json.Marshal(body)
	if err != nil {
		return completion{}, err
	}
	endpoint := strings.TrimRight(base, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return completion{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return completion{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return completion{}, fmt.Errorf("provider HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var parsed completionResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return completion{}, err
	}
	if len(parsed.Choices) == 0 {
		return completion{}, errors.New("provider returned no choices")
	}
	return completion{Content: parsed.Choices[0].Message.Content, ToolCalls: parsed.Choices[0].Message.ToolCalls}, nil
}

// windowMessages returns a recency-trimmed copy of the transcript to send to
// the provider: the leading system message(s) and the original user prompt are
// always kept, then only the last keepGroups assistant+tool "rounds". The cut
// always lands on an assistant message so a tool message is never orphaned from
// the assistant tool_calls it answers (which OpenAI-compatible APIs reject).
// The caller keeps the full slice for local bookkeeping; this only shapes the
// wire payload.
func windowMessages(msgs []wireMsg, keepGroups int) []wireMsg {
	if keepGroups <= 0 || len(msgs) <= 2 {
		return msgs
	}
	// Preserve leading system message(s) and the first user prompt.
	head := 0
	for head < len(msgs) && msgs[head].Role == "system" {
		head++
	}
	if head < len(msgs) && msgs[head].Role == "user" {
		head++
	}
	rest := msgs[head:]
	// A group starts at each assistant message; tool/user-nudge messages belong
	// to the group that precedes them.
	var starts []int
	for i, m := range rest {
		if m.Role == "assistant" {
			starts = append(starts, i)
		}
	}
	if len(starts) <= keepGroups {
		return msgs // nothing old enough to trim
	}
	cut := starts[len(starts)-keepGroups] // index in rest of the first kept assistant
	out := make([]wireMsg, 0, head+1+len(rest)-cut)
	out = append(out, msgs[:head]...)
	out = append(out, wireMsg{Role: "system", Content: "(Earlier steps omitted to stay within the context limit; continue from the recent results below.)"})
	out = append(out, rest[cut:]...)
	return out
}

func parseArgs(raw string) map[string]any {
	out := map[string]any{}
	_ = json.Unmarshal([]byte(strings.TrimSpace(raw)), &out)
	return out
}

func (s *Service) runCommand(parent context.Context, command string, timeout time.Duration) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", errors.New("command is required")
	}
	// Never run a command with no workspace open — cmd.Dir would default to the
	// app's own process directory, an unexpected and unsafe target.
	root := strings.TrimSpace(s.ws.Root())
	if root == "" {
		return "", errors.New("open a folder first — refusing to run a command with no workspace")
	}
	// Derive from the run's context so cancelling/timing-out the run also kills
	// the child process; cap any single command by the Agent runtime setting.
	if timeout <= 0 {
		timeout = time.Duration(settings.DefaultAgentCommandTimeoutSec) * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/c", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Dir = root
	hideCmd(cmd)
	out, runErr := cmd.CombinedOutput()
	text := clip(string(out), s.settings.Load().Agent.Normalized().MaxToolOutputChars)
	if ctx.Err() == context.DeadlineExceeded {
		return text + "\n(command timed out)", nil
	}
	if parent.Err() != nil {
		return text + "\n(run cancelled)", nil
	}
	if runErr != nil {
		// Surface a non-zero exit / failure-to-start so the model (and user) can
		// tell a failed command from a silent, successful one.
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return strings.TrimRight(text, "\n") + fmt.Sprintf("\n(command exited with status %d)", exitErr.ExitCode()), nil
		}
		return strings.TrimRight(text, "\n") + fmt.Sprintf("\n(command failed to run: %v)", runErr), nil
	}
	if strings.TrimSpace(text) == "" {
		return "(no output)", nil
	}
	return text, nil
}

// parsePlan reads update_plan args tolerantly: each step may be a plain string
// (status defaults to "todo") or an object {title,status}. Status is normalised
// to todo|in_progress|done so a model's loose wording still renders correctly.
func parsePlan(args map[string]any) []planStep {
	raw, ok := args["steps"].([]any)
	if !ok {
		return nil
	}
	out := make([]planStep, 0, len(raw))
	for _, item := range raw {
		switch v := item.(type) {
		case string:
			if title := strings.TrimSpace(v); title != "" {
				out = append(out, planStep{Title: title, Status: "todo"})
			}
		case map[string]any:
			title := strings.TrimSpace(firstStr(v, "title", "step", "text", "name", "description"))
			if title == "" {
				continue
			}
			out = append(out, planStep{Title: title, Status: normalizePlanStatus(getStr(v, "status"))})
		}
	}
	return out
}

func hasOpenPlanStep(steps []planStep) bool {
	for _, step := range steps {
		if step.Status != "done" {
			return true
		}
	}
	return false
}

func visibleAssistantContent(content string) string {
	text := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n"))
	if text == "" {
		return ""
	}
	if idx := hiddenAssistantTailIndex(text); idx >= 0 {
		text = text[:idx]
	}
	return strings.TrimSpace(text)
}

func hiddenAssistantTailIndex(text string) int {
	best := -1
	for _, marker := range []string{"<channel|>", "<|channel", "<|thought", "\nthought\n", "\nanalysis\n"} {
		if idx := strings.Index(text, marker); idx >= 0 && (best < 0 || idx < best) {
			best = idx
		}
	}
	offset := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		if isInternalAssistantLine(strings.TrimSpace(line)) && (best < 0 || offset < best) {
			best = offset
			break
		}
		offset += len(line)
	}
	return best
}

func isInternalAssistantLine(line string) bool {
	lower := strings.ToLower(line)
	switch {
	case lower == "thought" || lower == "analysis":
		return true
	case strings.HasPrefix(lower, "the user denied the write_file request"):
		return true
	case strings.HasPrefix(lower, "the user denied the append_file request"):
		return true
	case strings.HasPrefix(lower, "the write_file was rejected"):
		return true
	case strings.HasPrefix(lower, "since write_file was denied"):
		return true
	case strings.HasPrefix(lower, "wait, looking at the previous turn"):
		return true
	case strings.HasPrefix(lower, "wait, i see what happened"):
		return true
	case strings.HasPrefix(lower, "wait, i see the instruction"):
		return true
	case strings.HasPrefix(lower, "actually, looking at the error"):
		return true
	case strings.HasPrefix(lower, "actually, let me try"):
		return true
	case strings.HasPrefix(lower, "let me try append_file"):
		return true
	case strings.Contains(lower, "continue now by emitting real tool_calls"):
		return true
	case strings.HasPrefix(lower, "the prompt says") && strings.Contains(lower, "tool"):
		return true
	}
	return false
}

func firstStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func normalizePlanStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "in_progress", "in-progress", "inprogress", "doing", "active", "current", "started", "wip", "running":
		return "in_progress"
	case "done", "complete", "completed", "finished", "closed", "resolved":
		return "done"
	default:
		return "todo"
	}
}

// agentNoiseNames / agentNoiseSuffixes classify generated files that are pure
// token-waste in the agent's bulk file listing. This trims ONLY list_files —
// the workspace search/diagnostics and read_file by explicit path still reach
// these files (lockfiles are legitimate quick-open targets, just not useful in
// a flat dump to the model).
var agentNoiseNames = map[string]bool{
	"package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true,
	"bun.lockb": true, "go.sum": true, "cargo.lock": true, "poetry.lock": true,
	"pipfile.lock": true, "composer.lock": true, "gemfile.lock": true, "pubspec.lock": true,
}

var agentNoiseSuffixes = []string{".min.js", ".min.css", ".map", ".bundle.js", ".lock", ".snap", ".pb.go", "_pb2.py"}

func isAgentNoiseFile(rel string) bool {
	base := rel
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		base = rel[i+1:] // ListAllFiles returns slash-separated paths
	}
	base = strings.ToLower(base)
	if agentNoiseNames[base] {
		return true
	}
	if strings.Contains(base, ".generated.") {
		return true
	}
	for _, suf := range agentNoiseSuffixes {
		if strings.HasSuffix(base, suf) {
			return true
		}
	}
	return false
}

func getStr(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getBool(args map[string]any, key string) bool {
	if v, ok := args[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func getStrSlice(args map[string]any, key string) []string {
	raw, _ := args[key].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func clip(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "\n…(truncated)"
	}
	return s
}

// --- wire types ---

type agentEvent struct {
	RunID  string     `json:"runId"`
	Type   string     `json:"type"`
	Text   string     `json:"text,omitempty"`
	CallID string     `json:"callId,omitempty"`
	Tool   string     `json:"tool,omitempty"`
	Args   string     `json:"args,omitempty"`
	Result string     `json:"result,omitempty"`
	Plan   []planStep `json:"plan,omitempty"`
}

// planStep is one entry of the agent's task plan, carried on "plan" events so
// the UI can show per-step status (todo / in_progress / done) instead of a bare
// numbered list.
type planStep struct {
	Title  string `json:"title"`
	Status string `json:"status"` // todo | in_progress | done
}

type completion struct {
	Content   string
	ToolCalls []wireToolCall
}

type wireMsg struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
}

type wireToolCall struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function wireFunc `json:"function"`
}

type wireFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireToolDef struct {
	Type     string       `json:"type"`
	Function wireToolFunc `json:"function"`
}

type wireToolFunc struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type completionRequest struct {
	Model       string        `json:"model"`
	Messages    []wireMsg     `json:"messages"`
	Tools       []wireToolDef `json:"tools,omitempty"`
	Temperature float64       `json:"temperature"`
	Stream      bool          `json:"stream"`
	ToolChoice  string        `json:"tool_choice,omitempty"`
}

type completionResponse struct {
	Choices []struct {
		Message struct {
			Content   string         `json:"content"`
			ToolCalls []wireToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
}
