// Package store persists a run in SQLite.
//
// The writing discipline is the point: every lifecycle transition is its own
// committed statement, issued before the next action. A run that is killed — an
// OOM kill of the runner, node pressure, a lost API connection — leaves every
// row that was reached, so the boundary is still visible afterwards.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/model"
)

const schema = `
PRAGMA journal_mode=WAL;
PRAGMA synchronous=FULL;

CREATE TABLE IF NOT EXISTS runs (
    run_id      TEXT PRIMARY KEY,
    config      TEXT NOT NULL,
    started_ns  INTEGER NOT NULL,
    ended_ns    INTEGER,
    replicas    TEXT NOT NULL DEFAULT '[]',
    limits      TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS sessions (
    id             INTEGER PRIMARY KEY,
    bucket         TEXT NOT NULL,
    policy         TEXT NOT NULL,
    namespace      TEXT NOT NULL,
    template       TEXT NOT NULL,
    pool           TEXT NOT NULL,
    instance       TEXT NOT NULL DEFAULT '',
    outcome        TEXT NOT NULL DEFAULT 'pending',
    error          TEXT NOT NULL DEFAULT '',
    arrival_ns     INTEGER NOT NULL,
    claim_created_ns INTEGER NOT NULL DEFAULT 0,
    ready_ns       INTEGER NOT NULL DEFAULT 0,
    hold_start_ns  INTEGER NOT NULL DEFAULT 0,
    hold_end_ns    INTEGER NOT NULL DEFAULT 0,
    delete_called_ns INTEGER NOT NULL DEFAULT 0,
    gone_ns        INTEGER NOT NULL DEFAULT 0,
    reset_start_ns INTEGER NOT NULL DEFAULT 0,
    reset_end_ns   INTEGER NOT NULL DEFAULT 0,
    rebuild_start_ns INTEGER NOT NULL DEFAULT 0,
    rebuild_end_ns INTEGER NOT NULL DEFAULT 0,
    launch_type    TEXT NOT NULL DEFAULT '',
    node           TEXT NOT NULL DEFAULT '',
    pod_uid        TEXT NOT NULL DEFAULT '',
    cpu_usec       INTEGER NOT NULL DEFAULT 0,
    cpu_pressure_some_usec INTEGER NOT NULL DEFAULT 0,
    cpu_pressure_full_usec INTEGER NOT NULL DEFAULT 0,
    throttled_usec INTEGER NOT NULL DEFAULT 0,
    nr_throttled   INTEGER NOT NULL DEFAULT 0,
    mem_peak_bytes INTEGER NOT NULL DEFAULT 0,
    mem_working_set_peak_bytes INTEGER NOT NULL DEFAULT 0,
    mem_inactive_file_bytes INTEGER NOT NULL DEFAULT 0,
    shmem_peak_bytes INTEGER NOT NULL DEFAULT 0,
    anon_peak_bytes INTEGER NOT NULL DEFAULT 0,
    workingset_refault_anon INTEGER NOT NULL DEFAULT 0,
    workingset_refault_file INTEGER NOT NULL DEFAULT 0,
    swap_peak_bytes INTEGER NOT NULL DEFAULT 0,
    oom_kill       INTEGER NOT NULL DEFAULT 0,
    mem_pressure_some_usec INTEGER NOT NULL DEFAULT 0,
    samples        INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS sessions_outcome ON sessions(outcome);
CREATE INDEX IF NOT EXISTS sessions_instance ON sessions(instance);

CREATE TABLE IF NOT EXISTS instances (
    id         TEXT PRIMARY KEY,
    kind       TEXT NOT NULL,
    namespace  TEXT NOT NULL DEFAULT '',
    node       TEXT NOT NULL DEFAULT '',
    pod_uid    TEXT NOT NULL DEFAULT '',
    created_ns INTEGER NOT NULL DEFAULT 0,
    ready_ns   INTEGER NOT NULL DEFAULT 0,
    deleted_ns INTEGER NOT NULL DEFAULT 0,
    suspends   INTEGER NOT NULL DEFAULT 0,
    resumes    INTEGER NOT NULL DEFAULT 0,
    resets     INTEGER NOT NULL DEFAULT 0,
    sessions   INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS events (
    seq        INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id INTEGER NOT NULL DEFAULT 0,
    at_ns      INTEGER NOT NULL,
    kind       TEXT NOT NULL,
    detail     TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS host_samples (
    seq           INTEGER PRIMARY KEY AUTOINCREMENT,
    at_ns         INTEGER NOT NULL,
    mem_total     INTEGER NOT NULL DEFAULT 0,
    mem_available INTEGER NOT NULL DEFAULT 0,
    swap_total    INTEGER NOT NULL DEFAULT 0,
    swap_free     INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS events_session ON events(session_id);
`

// Store is one open run database.
type Store struct {
	db *sql.DB
}

// Open creates or extends the database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// One connection: the runner serialises writes through it, which keeps the
	// sessions table and the events log in the same order.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// StartRun records the configuration a run was made with, so the report can be
// read without the terminal that launched it.
func (s *Store) StartRun(cfg model.RunConfig, at time.Time) error {
	payload, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO runs (run_id, config, started_ns) VALUES (?, ?, ?)
		 ON CONFLICT(run_id) DO UPDATE SET config = excluded.config, started_ns = excluded.started_ns`,
		cfg.RunID, string(payload), at.UnixNano())
	return err
}

// EndRun closes a run and stores the pool replica trajectory the runner saw.
func (s *Store) EndRun(runID string, at time.Time, replicas []ReplicaSample) error {
	payload, err := json.Marshal(replicas)
	if err != nil {
		return fmt.Errorf("marshal replicas: %w", err)
	}
	_, err = s.db.Exec(`UPDATE runs SET ended_ns = ?, replicas = ? WHERE run_id = ?`, at.UnixNano(), string(payload), runID)
	return err
}

// SaveTemplateLimits records the requests and limits the sandbox template
// applies, which is what makes a throttling count readable.
func (s *Store) SaveTemplateLimits(runID, limitsJSON string) error {
	_, err := s.db.Exec(`UPDATE runs SET limits = ? WHERE run_id = ?`, limitsJSON, runID)
	return err
}

// TemplateLimits returns the recorded template limits for a run, as JSON.
func (s *Store) TemplateLimits(runID string) (string, error) {
	var limits string
	err := s.db.QueryRow(`SELECT limits FROM runs WHERE run_id = ?`, runID).Scan(&limits)
	if err != nil {
		return "", err
	}
	return limits, nil
}

// ReplicaSample is one reading of the warm pool's desired and ready counts.
type ReplicaSample struct {
	AtNS    int64 `json:"at_ns"`
	Desired int32 `json:"desired"`
	Ready   int32 `json:"ready"`
	Note    string `json:"note,omitempty"`
}

// AddSession inserts the row for a session that has just arrived.
func (s *Store) AddSession(session model.Session) error {
	_, err := s.db.Exec(
		`INSERT INTO sessions (id, bucket, policy, namespace, template, pool, outcome, arrival_ns)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		session.ID, session.Bucket, string(session.Policy), session.Namespace, session.Template,
		session.Pool, string(session.Outcome), session.ArrivalNS)
	if err != nil {
		return fmt.Errorf("add session %d: %w", session.ID, err)
	}
	return nil
}

// UpdateSession writes back the mutable columns of one session row. It is called
// on every transition, so the row is never ahead of what was observed.
func (s *Store) UpdateSession(session model.Session) error {
	_, err := s.db.Exec(
		`UPDATE sessions SET instance = ?, outcome = ?, error = ?,
		    claim_created_ns = ?, ready_ns = ?, hold_start_ns = ?, hold_end_ns = ?,
		    delete_called_ns = ?, gone_ns = ?, reset_start_ns = ?, reset_end_ns = ?,
		    rebuild_start_ns = ?, rebuild_end_ns = ?, launch_type = ?, node = ?, pod_uid = ?,
		    cpu_usec = ?, cpu_pressure_some_usec = ?, cpu_pressure_full_usec = ?,
		    throttled_usec = ?, nr_throttled = ?, mem_peak_bytes = ?,
		    mem_working_set_peak_bytes = ?, mem_inactive_file_bytes = ?, shmem_peak_bytes = ?,
		    anon_peak_bytes = ?, workingset_refault_anon = ?, workingset_refault_file = ?,
		    swap_peak_bytes = ?, oom_kill = ?, mem_pressure_some_usec = ?, samples = ?
		 WHERE id = ?`,
		session.Instance, string(session.Outcome), session.Error,
		session.ClaimCreatedNS, session.ReadyNS, session.HoldStartNS, session.HoldEndNS,
		session.DeleteCalledNS, session.GoneNS, session.ResetStartNS, session.ResetEndNS,
		session.RebuildStartNS, session.RebuildEndNS, session.LaunchType, session.Node, session.PodUID,
		session.CPUUsec, session.CPUPressureSomeUsec, session.CPUPressureFullUsec,
		session.ThrottledUsec, session.NRThrottled, session.MemPeakBytes,
		session.MemWorkingSetPeak, session.MemInactiveFile, session.ShmemPeak,
		session.AnonPeak, session.RefaultAnon, session.RefaultFile,
		session.SwapPeakBytes, session.OOMKill, session.MemPressureSomeUsec, session.Samples,
		session.ID)
	if err != nil {
		return fmt.Errorf("update session %d: %w", session.ID, err)
	}
	return nil
}

// SaveInstance upserts one instance row.
func (s *Store) SaveInstance(instance model.Instance) error {
	_, err := s.db.Exec(
		`INSERT INTO instances (id, kind, namespace, node, pod_uid, created_ns, ready_ns, deleted_ns,
		     suspends, resumes, resets, sessions)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		     kind = excluded.kind, namespace = excluded.namespace, node = excluded.node,
		     pod_uid = excluded.pod_uid, created_ns = excluded.created_ns, ready_ns = excluded.ready_ns,
		     deleted_ns = excluded.deleted_ns, suspends = excluded.suspends, resumes = excluded.resumes,
		     resets = excluded.resets, sessions = excluded.sessions`,
		instance.ID, instance.Kind, instance.Namespace, instance.Node, instance.PodUID,
		instance.CreatedNS, instance.ReadyNS, instance.DeletedNS,
		instance.Suspends, instance.Resumes, instance.Resets, instance.Sessions)
	return err
}

// AddEvent appends one lifecycle transition.
func (s *Store) AddEvent(event model.Event) error {
	_, err := s.db.Exec(`INSERT INTO events (session_id, at_ns, kind, detail) VALUES (?, ?, ?, ?)`,
		event.SessionID, event.AtNS, event.Kind, event.Detail)
	return err
}

// AddHostSample appends one node-memory reading.
func (s *Store) AddHostSample(sample model.HostSample) error {
	_, err := s.db.Exec(
		`INSERT INTO host_samples (at_ns, mem_total, mem_available, swap_total, swap_free) VALUES (?, ?, ?, ?, ?)`,
		sample.AtNS, sample.MemTotal, sample.MemAvailable, sample.SwapTotal, sample.SwapFree)
	return err
}

// HostSamples returns the node-memory series in time order.
func (s *Store) HostSamples() ([]model.HostSample, error) {
	rows, err := s.db.Query(`SELECT at_ns, mem_total, mem_available, swap_total, swap_free FROM host_samples ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.HostSample, 0, 128)
	for rows.Next() {
		var sample model.HostSample
		if err := rows.Scan(&sample.AtNS, &sample.MemTotal, &sample.MemAvailable, &sample.SwapTotal, &sample.SwapFree); err != nil {
			return nil, err
		}
		out = append(out, sample)
	}
	return out, rows.Err()
}

// Sessions returns every session row in id order.
func (s *Store) Sessions() ([]model.Session, error) {
	rows, err := s.db.Query(
		`SELECT id, bucket, policy, namespace, template, pool, instance, outcome, error,
		        arrival_ns, claim_created_ns, ready_ns, hold_start_ns, hold_end_ns,
		        delete_called_ns, gone_ns, reset_start_ns, reset_end_ns, rebuild_start_ns, rebuild_end_ns,
		        launch_type, node, pod_uid, cpu_usec, cpu_pressure_some_usec, cpu_pressure_full_usec,
		        throttled_usec, nr_throttled, mem_peak_bytes, mem_working_set_peak_bytes,
		        mem_inactive_file_bytes, shmem_peak_bytes, anon_peak_bytes,
		        workingset_refault_anon, workingset_refault_file, swap_peak_bytes, oom_kill,
		        mem_pressure_some_usec, samples
		 FROM sessions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]model.Session, 0, 256)
	for rows.Next() {
		var s model.Session
		var policy, outcome string
		if err := rows.Scan(
			&s.ID, &s.Bucket, &policy, &s.Namespace, &s.Template, &s.Pool, &s.Instance, &outcome, &s.Error,
			&s.ArrivalNS, &s.ClaimCreatedNS, &s.ReadyNS, &s.HoldStartNS, &s.HoldEndNS,
			&s.DeleteCalledNS, &s.GoneNS, &s.ResetStartNS, &s.ResetEndNS, &s.RebuildStartNS, &s.RebuildEndNS,
			&s.LaunchType, &s.Node, &s.PodUID, &s.CPUUsec, &s.CPUPressureSomeUsec, &s.CPUPressureFullUsec,
			&s.ThrottledUsec, &s.NRThrottled, &s.MemPeakBytes, &s.MemWorkingSetPeak,
			&s.MemInactiveFile, &s.ShmemPeak, &s.AnonPeak,
			&s.RefaultAnon, &s.RefaultFile, &s.SwapPeakBytes, &s.OOMKill,
			&s.MemPressureSomeUsec, &s.Samples); err != nil {
			return nil, err
		}
		s.Policy = model.Policy(policy)
		s.Outcome = model.Outcome(outcome)
		out = append(out, s)
	}
	return out, rows.Err()
}

// Instances returns every instance row.
func (s *Store) Instances() ([]model.Instance, error) {
	rows, err := s.db.Query(`SELECT id, kind, namespace, node, pod_uid, created_ns, ready_ns,
	    deleted_ns, suspends, resumes, resets, sessions FROM instances ORDER BY created_ns`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.Instance, 0, 64)
	for rows.Next() {
		var i model.Instance
		if err := rows.Scan(&i.ID, &i.Kind, &i.Namespace, &i.Node, &i.PodUID, &i.CreatedNS,
			&i.ReadyNS, &i.DeletedNS, &i.Suspends, &i.Resumes, &i.Resets, &i.Sessions); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// Count returns how many sessions the store holds, which the report compares
// against the number the run planned.
func (s *Store) Count() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n)
	return n, err
}
