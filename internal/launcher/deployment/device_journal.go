package deployment

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const deviceJournalLimit = 4096

type journalAction struct {
	TaskID         string    `json:"task_id"`
	StepID         string    `json:"step_id"`
	ActionID       string    `json:"action_id"`
	AgentBuildID   string    `json:"agent_build_id,omitempty"`
	RunManifestID  string    `json:"run_manifest_id,omitempty"`
	Sequence       int64     `json:"sequence"`
	Revision       int64     `json:"revision"`
	IdempotencyKey string    `json:"idempotency_key"`
	StartedAt      time.Time `json:"started_at"`
}

type deviceActionJournal struct {
	Protocol  string                       `json:"protocol"`
	UpdatedAt time.Time                    `json:"updated_at"`
	Completed map[string]deviceObservation `json:"completed"`
	InFlight  map[string]journalAction     `json:"in_flight"`
	Sequences map[string]int64             `json:"sequences"`
}

func loadDeviceActionJournal(path string) (deviceActionJournal, error) {
	journal := deviceActionJournal{
		Protocol: deviceProtocol, Completed: make(map[string]deviceObservation),
		InFlight: make(map[string]journalAction), Sequences: make(map[string]int64),
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return journal, nil
	}
	if err != nil {
		return journal, fmt.Errorf("read device action journal: %w", err)
	}
	if err := json.Unmarshal(data, &journal); err != nil {
		return journal, fmt.Errorf("parse device action journal: %w", err)
	}
	if journal.Protocol != "" && journal.Protocol != deviceProtocol {
		return journal, fmt.Errorf("device action journal uses unsupported protocol %q", journal.Protocol)
	}
	journal.Protocol = deviceProtocol
	if journal.Completed == nil {
		journal.Completed = make(map[string]deviceObservation)
	}
	if journal.InFlight == nil {
		journal.InFlight = make(map[string]journalAction)
	}
	if journal.Sequences == nil {
		journal.Sequences = make(map[string]int64)
	}
	now := time.Now().UTC()
	for key, action := range journal.InFlight {
		observation := deviceObservation{
			Protocol: deviceProtocol, Type: "OBSERVATION", ObservationID: newDeviceProtocolID("observation"),
			TaskID: action.TaskID, StepID: action.StepID, ActionID: action.ActionID,
			AgentBuildID: action.AgentBuildID, RunManifestID: action.RunManifestID,
			Sequence: action.Sequence, Revision: action.Revision, Status: "FAILED",
			StartedAt: action.StartedAt, FinishedAt: now, ObservedAt: now,
			State: map[string]any{"outcome": "unknown", "verification_required": true},
			Error: "the desktop restarted before the action outcome was recorded",
		}
		journal.Completed[key] = observation
		if action.Sequence > journal.Sequences[action.TaskID] {
			journal.Sequences[action.TaskID] = action.Sequence
		}
		delete(journal.InFlight, key)
	}
	return journal, nil
}

func saveDeviceActionJournal(path string, journal deviceActionJournal) error {
	journal.Protocol = deviceProtocol
	journal.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return fmt.Errorf("encode device action journal: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create device journal directory: %w", err)
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return fmt.Errorf("write device action journal: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replace device action journal: %w", err)
	}
	return nil
}

func (d *deviceRuntime) beginDurableAction(action deviceAction) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.completed == nil {
		d.completed = make(map[string]deviceObservation)
	}
	if d.durable == nil {
		d.durable = make(map[string]journalAction)
	}
	if d.sequences == nil {
		d.sequences = make(map[string]int64)
	}
	if _, ok := d.completed[action.IdempotencyKey]; ok {
		return fmt.Errorf("action idempotency key %s is already completed", action.IdempotencyKey)
	}
	if _, ok := d.durable[action.IdempotencyKey]; ok {
		return fmt.Errorf("action idempotency key %s is already in progress", action.IdempotencyKey)
	}
	previous := d.sequences[action.TaskID]
	if action.Sequence != previous+1 {
		return fmt.Errorf("action sequence is out of order: got %d, want %d", action.Sequence, previous+1)
	}
	d.durable[action.IdempotencyKey] = journalAction{
		TaskID: action.TaskID, StepID: action.StepID, ActionID: action.ActionID,
		AgentBuildID: action.AgentBuildID, RunManifestID: action.RunManifestID,
		Sequence: action.Sequence, Revision: action.Revision, IdempotencyKey: action.IdempotencyKey,
		StartedAt: time.Now().UTC(),
	}
	if d.journal == "" {
		return nil
	}
	if err := saveDeviceActionJournal(d.journal, d.journalLocked()); err != nil {
		delete(d.durable, action.IdempotencyKey)
		return err
	}
	return nil
}

func (d *deviceRuntime) remember(action deviceAction, observation deviceObservation) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if observation.FinishedAt.IsZero() {
		observation.FinishedAt = time.Now().UTC()
	}
	if observation.ObservedAt.IsZero() || observation.ObservedAt.Before(observation.FinishedAt) {
		observation.ObservedAt = observation.FinishedAt
	}
	if d.completed == nil {
		d.completed = make(map[string]deviceObservation)
	}
	if d.durable == nil {
		d.durable = make(map[string]journalAction)
	}
	if d.sequences == nil {
		d.sequences = make(map[string]int64)
	}
	if len(d.completed) >= deviceJournalLimit {
		for key := range d.completed {
			delete(d.completed, key)
			break
		}
	}
	d.completed[action.IdempotencyKey] = observation.WithoutAttachmentData()
	delete(d.durable, action.IdempotencyKey)
	if action.Sequence > d.sequences[action.TaskID] {
		d.sequences[action.TaskID] = action.Sequence
	}
	if d.journal == "" {
		return nil
	}
	return saveDeviceActionJournal(d.journal, d.journalLocked())
}

// releaseDurableAction clears a deferred action without advancing its sequence
// or caching an outcome. The control plane may dispatch the same idempotency key
// again after an approval decision changes the policy to ALLOW.
func (d *deviceRuntime) releaseDurableAction(action deviceAction) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.durable == nil {
		return nil
	}
	pending, ok := d.durable[action.IdempotencyKey]
	if !ok {
		return nil
	}
	delete(d.durable, action.IdempotencyKey)
	if d.journal == "" {
		return nil
	}
	if err := saveDeviceActionJournal(d.journal, d.journalLocked()); err != nil {
		d.durable[action.IdempotencyKey] = pending
		return err
	}
	return nil
}

func (d *deviceRuntime) journalLocked() deviceActionJournal {
	return deviceActionJournal{
		Protocol: deviceProtocol, Completed: d.completed, InFlight: d.durable, Sequences: d.sequences,
	}
}
