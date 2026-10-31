package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Session is a flattened CDP session attached to one target. Commands sent through
// it may carry a sessionId, so one connection can drive many contexts and receive
// their events separately.
type Session struct {
	conn     *Conn
	ID       string
	TargetID TargetID
	events   chan Event
	closed   chan struct{}
	once     sync.Once
}

// deliver hands an event to this session's consumer without blocking the reader.
func (s *Session) deliver(event Event) {
	select {
	case s.events <- event:
	case <-s.closed:
	default:
	}
}

// shutdown closes the event stream once the connection is gone.
func (s *Session) shutdown() {
	s.once.Do(func() {
		close(s.closed)
		close(s.events)
	})
}

// Attach opens a flattened session on a target. The session is closed when ctx is
// cancelled or Close is called.
func (c *Conn) Attach(ctx context.Context, id TargetID) (*Session, error) {
	result, err := c.Do(ctx, "Target.attachToTarget", map[string]any{
		"targetId": string(id), "flatten": true,
	})
	if err != nil {
		return nil, err
	}
	var decoded struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		return nil, fmt.Errorf("attachToTarget: decode: %w", err)
	}
	if decoded.SessionID == "" {
		return nil, fmt.Errorf("attachToTarget: empty sessionId")
	}

	session := &Session{
		conn:     c,
		ID:       decoded.SessionID,
		TargetID: id,
		events:   make(chan Event, 128),
		closed:   make(chan struct{}),
	}
	c.mu.Lock()
	c.sessions[session.ID] = session
	c.mu.Unlock()
	return session, nil
}

// Do sends a command on this session.
func (s *Session) Do(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return s.conn.do(ctx, method, params, s.ID)
}

// Events is this session's event stream.
func (s *Session) Events() <-chan Event { return s.events }

// Close detaches the session and stops its event stream.
func (s *Session) Close(ctx context.Context) error {
	s.conn.mu.Lock()
	delete(s.conn.sessions, s.ID)
	s.conn.mu.Unlock()
	s.shutdown()
	_, err := s.Do(ctx, "Target.detachFromTarget", map[string]any{"sessionId": s.ID})
	if err != nil && isGone(err) {
		return nil
	}
	return err
}

// Usage is what Chromium reports about one target's cost since Performance.enable.
// These are main-thread quantities, not process CPU: they must not be added to or
// compared with cpu.stat's usage_usec.
type Usage struct {
	TaskDurationSeconds   float64
	ScriptDurationSeconds float64
	LayoutCount           float64
	JSHeapUsedBytes       float64
	NodesCount            float64
}

// EnableMetrics turns on the Performance domain for this session.
func (s *Session) EnableMetrics(ctx context.Context) error {
	_, err := s.Do(ctx, "Performance.enable", nil)
	return err
}

// EnablePage turns on the Page domain. Events such as Page.loadEventFired are only
// emitted for a session that has enabled their domain, which is why a navigation
// without this looks like a page that never loaded.
func (s *Session) EnablePage(ctx context.Context) error {
	_, err := s.Do(ctx, "Page.enable", nil)
	return err
}

// Metrics reads the current counters for this session's target.
func (s *Session) Metrics(ctx context.Context) (Usage, error) {
	result, err := s.Do(ctx, "Performance.getMetrics", nil)
	if err != nil {
		return Usage{}, err
	}
	var decoded struct {
		Metrics []struct {
			Name  string  `json:"name"`
			Value float64 `json:"value"`
		} `json:"metrics"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		return Usage{}, fmt.Errorf("getMetrics: decode: %w", err)
	}
	var usage Usage
	for _, metric := range decoded.Metrics {
		switch metric.Name {
		case "TaskDuration":
			usage.TaskDurationSeconds = metric.Value
		case "ScriptDuration":
			usage.ScriptDurationSeconds = metric.Value
		case "LayoutCount":
			usage.LayoutCount = metric.Value
		case "JSHeapUsedSize":
			usage.JSHeapUsedBytes = metric.Value
		case "Nodes":
			usage.NodesCount = metric.Value
		}
	}
	return usage, nil
}

// Delta is the difference between two readings of the same session. Counters are
// cumulative since Performance.enable, so a session's cost is end minus start.
func (u Usage) Delta(from Usage) Usage {
	return Usage{
		TaskDurationSeconds:   max0(u.TaskDurationSeconds - from.TaskDurationSeconds),
		ScriptDurationSeconds: max0(u.ScriptDurationSeconds - from.ScriptDurationSeconds),
		LayoutCount:           max0(u.LayoutCount - from.LayoutCount),
		JSHeapUsedBytes:       u.JSHeapUsedBytes,
		NodesCount:            u.NodesCount,
	}
}

func max0(value float64) float64 {
	if value < 0 {
		return 0
	}
	return value
}

// Navigate loads a URL in this session's page.
func (s *Session) Navigate(ctx context.Context, url string) (float64, error) {
	result, err := s.Do(ctx, "Page.navigate", map[string]any{"url": url})
	if err != nil {
		return 0, err
	}
	var decoded struct {
		LoaderID string  `json:"loaderId"`
		Error    string  `json:"errorText"`
		FrameID  string  `json:"frameId"`
		TS       float64 `json:"-"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		return 0, err
	}
	if decoded.Error != "" {
		return 0, fmt.Errorf("navigate %s: %s", url, decoded.Error)
	}
	return 0, nil
}

// WaitForLoad waits for Page.loadEventFired on this session, or for the context to
// end. An event-driven hold is the point of keeping a session at all.
func (s *Session) WaitForLoad(ctx context.Context) error {
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("wait for load: no loadEventFired")
		case event, ok := <-s.events:
			if !ok {
				return ErrClosed
			}
			if event.Method == "Page.loadEventFired" {
				return nil
			}
		}
	}
}

// Crash is a report that a renderer died. Chromium sends it at browser level with
// the target that failed.
type Crash struct {
	TargetID TargetID
	Status   string
	Code     int
}

// WatchCrashes forwards Target.targetCrashed reports to the returned channel until
// ctx ends or the connection closes.
func (c *Conn) WatchCrashes(ctx context.Context) <-chan Crash {
	out := make(chan Crash, 8)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-c.Events():
				if !ok {
					return
				}
				if event.Method != "Target.targetCrashed" {
					continue
				}
				var payload struct {
					TargetID string `json:"targetId"`
					Status   string `json:"status"`
					Code     int    `json:"errorCode"`
				}
				if err := json.Unmarshal(event.Params, &payload); err != nil {
					continue
				}
				select {
				case out <- Crash{TargetID: TargetID(payload.TargetID), Status: payload.Status, Code: payload.Code}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}
