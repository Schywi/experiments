// Command agent-runtime is the E7 harness: one HTTP surface that takes an
// objective, charges it against a user's quota, claims a browser session from the
// existing console, and runs an agent against it.
//
// This is the only package that imports both internal/user and internal/agent.
// A test beside this file fails the build if either package starts importing the
// other, which is the property the study exists to keep.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/agent-runtime/internal/agent"
	"github.com/lucasmirandoliveira/experiments/apps/agent-runtime/internal/agent/reasoners"
	"github.com/lucasmirandoliveira/experiments/apps/agent-runtime/internal/agent/tools"
	"github.com/lucasmirandoliveira/experiments/apps/agent-runtime/internal/user"
)

type config struct {
	addr       string
	consoleURL string
	routerURL  string
	qwenURL    string
	layaURL    string
	model      string
	userID     user.ID
	domains    []string
	denied     []string
	startURL   string
	maxOps     int
	// Per-run limits. The experiment varies MaxOpsPerCall: it is the cadence dial,
	// because the gate refuses the operations a plan sends beyond it.
	maxOpsPerCall int
	actions       []string
	agents        int
	sessions      int
}

func loadConfig() config {
	return config{
		addr:          env("ADDR", ":8080"),
		consoleURL:    env("CONSOLE_URL", ""),
		routerURL:     env("ROUTER_URL", ""),
		qwenURL:       env("QWEN_URL", ""),
		layaURL:       env("LAYA_URL", ""),
		model:         env("QWEN_MODEL", "qwen3-vl-2b-instruct"),
		userID:        user.ID(env("AGENT_USER", "e7")),
		domains:       split(env("AGENT_DOMAINS", "")),
		denied:        split(env("AGENT_DENIED", "delete")),
		startURL:      env("AGENT_START_URL", ""),
		maxOps:        envInt("AGENT_MAX_OPS", 50),
		maxOpsPerCall: envInt("AGENT_MAX_OPS_PER_CALL", 5),
		actions:       split(env("AGENT_ACTIONS", strings.Join(agent.Actions(), ","))),
		agents:        envInt("AGENT_CONCURRENT_AGENTS", 1),
		sessions:      envInt("AGENT_CONCURRENT_SESSIONS", 1),
	}
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func split(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

type runState struct {
	mu     sync.Mutex
	result agent.Result
	state  agent.State
}

func (r *runState) set(res agent.Result) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.result = res
	r.state = res.State
}

func (r *runState) snapshot() (agent.State, agent.Result) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state, r.result
}

type server struct {
	cfg    config
	users  *user.Registry
	defs   map[string]agent.Reasoner
	client *tools.Client

	mu      sync.Mutex
	runs    map[string]*runState
	cancels map[string]context.CancelFunc
	seq     int
}

func main() {
	cfg := loadConfig()
	users := user.NewRegistry()
	if err := users.Put(user.User{
		ID: cfg.userID,
		Limits: user.Limits{
			ConcurrentAgents:   cfg.agents,
			ConcurrentSessions: cfg.sessions,
			MaxOpsPerCall:      cfg.maxOpsPerCall,
		},
		Actions: cfg.actions,
		Domains: cfg.domains,
		Denied:  cfg.denied,
	}); err != nil {
		log.Fatalf("agent-runtime: register user: %v", err)
	}

	qwen := reasoners.NewQwen(cfg.qwenURL, cfg.model)
	laya := reasoners.NewLaya(cfg.layaURL)

	s := &server{
		cfg:    cfg,
		users:  users,
		client: tools.NewClient(cfg.consoleURL, cfg.routerURL),
		defs: map[string]agent.Reasoner{
			"qwen":      qwen,
			"laya+qwen": reasoners.NewPickerThenPlanner(laya, qwen),
		},
		runs:    map[string]*runState{},
		cancels: map[string]context.CancelFunc{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /agents", s.startRun)
	mux.HandleFunc("GET /agents/{id}", s.getRun)
	mux.HandleFunc("POST /agents/{id}/cancel", s.cancelRun)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	log.Printf("agent-runtime: listening on %s (user %s, agents %s)",
		cfg.addr, cfg.userID, strings.Join(defNames(s.defs), ", "))
	if err := http.ListenAndServe(cfg.addr, mux); err != nil {
		log.Fatalf("agent-runtime: %v", err)
	}
}

func defNames(defs map[string]agent.Reasoner) []string {
	out := make([]string, 0, len(defs))
	for name := range defs {
		out = append(out, name)
	}
	return out
}

type startRequest struct {
	Objective string `json:"objective"`
	AgentID   string `json:"agent_id"`
	Target    string `json:"target"`
	URL       string `json:"url"`
}

func (s *server) startRun(w http.ResponseWriter, r *http.Request) {
	var req startRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "body is not JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Objective) == "" {
		writeError(w, http.StatusBadRequest, "objective is required")
		return
	}
	reasoner, ok := s.defs[req.AgentID]
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown agent_id "+req.AgentID)
		return
	}

	grant, err := s.users.ReserveAgent(s.cfg.userID)
	if err != nil {
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	}
	if err := s.users.ReserveSession(s.cfg.userID); err != nil {
		s.users.ReleaseAgent(s.cfg.userID)
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	startURL := req.URL
	if startURL == "" {
		startURL = s.cfg.startURL
	}
	sandboxID, err := s.client.Claim(ctx, startURL)
	if err != nil {
		cancel()
		s.users.ReleaseSession(s.cfg.userID)
		s.users.ReleaseAgent(s.cfg.userID)
		writeError(w, http.StatusServiceUnavailable, "claim: "+err.Error())
		return
	}

	page := tools.NewPage()
	runner := agent.NewRunner(reasoner, map[string]agent.Tool{
		agent.ToolInspect: tools.NewInspect(s.client, sandboxID, page),
		agent.ToolAct:     tools.NewAct(s.client, sandboxID, page),
	}, agent.Policy{
		Actions:       grant.Actions,
		Domains:       grant.Domains,
		Denied:        grant.Denied,
		MaxOpsPerCall: grant.MaxOpsPerCall,
	}, agent.Budget{
		MaxInferences: grant.MaxInferences,
		MaxOps:        s.cfg.maxOps,
		Deadline:      time.Now().Add(grant.MaxRunDuration),
	})
	runner.Emit = agent.EventWriter(os.Stdout)
	if req.Target != "" {
		runner.Resolve = resolveTarget(req.Target)
	}

	runID := s.nextRunID()
	st := &runState{state: agent.StateRunning}
	st.result = agent.Result{State: agent.StateRunning, InferencesByReasoner: map[string]int{}}

	s.mu.Lock()
	s.runs[runID] = st
	s.cancels[runID] = cancel
	s.mu.Unlock()

	go func() {
		defer cancel()
		defer s.users.ReleaseAgent(s.cfg.userID)
		defer s.users.ReleaseSession(s.cfg.userID)
		defer func() {
			releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer releaseCancel()
			if err := s.client.Release(releaseCtx, sandboxID); err != nil {
				log.Printf("agent-runtime: release %s: %v", sandboxID, err)
			}
		}()

		res, err := runner.Run(ctx, agent.Task{RunID: runID, AgentID: req.AgentID, Objective: req.Objective})
		if err != nil {
			st.set(agent.Result{State: agent.StateFailed, Detail: err.Error()})
			return
		}
		st.set(res)
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"run_id":  runID,
		"sandbox": sandboxID,
		"state":   agent.StateRunning,
	})
}

func (s *server) getRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	st, ok := s.runs[id]
	s.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "unknown run "+id)
		return
	}
	state, res := st.snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"run_id": id,
		"state":  state,
		"result": res,
	})
}

func (s *server) cancelRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	cancel, ok := s.cancels[id]
	s.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "unknown run "+id)
		return
	}
	cancel()
	writeJSON(w, http.StatusOK, map[string]any{"run_id": id, "state": agent.StateCancelled})
}

func (s *server) nextRunID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return fmt.Sprintf("run-%04d", s.seq)
}

// resolveTarget answers a step from the accessibility tree alone. It is the path
// E6 measured resolving 7 of 8 tasks with no model call, kept available here as an
// arm rather than as an optimisation: a run that used it reports its inferences as
// coming from "dom".
func resolveTarget(target string) func(agent.AgentInput) (agent.Decision, bool) {
	return func(in agent.AgentInput) (agent.Decision, bool) {
		for _, a := range in.LastActions {
			if a.OK && a.Op.Action == agent.ActionClick {
				return agent.Decision{Kind: agent.Done, Source: "dom",
					Why: "clicked " + a.Op.Ref.String()}, true
			}
		}
		node, unique := agent.ExactlyOne(in.Snapshot, target)
		if !unique {
			return agent.Decision{}, false
		}
		return agent.Decision{Kind: agent.Plan, Source: "dom",
			Ops: []agent.Op{{Action: agent.ActionClick, Ref: node.Ref}},
			Why: "dom resolved " + node.Ref.String() + " " + node.Label()}, true
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}
