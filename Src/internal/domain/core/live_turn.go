package core

import "time"

// LiveTurnEvent is an ephemeral presentation hint. Durable Run events,
// messages, receipts and checkpoints remain the only replayable truth.
type LiveTurnEvent struct {
	RunID        string    `json:"run_id"`
	ModelCallID  string    `json:"model_call_id"`
	Kind         string    `json:"kind"`
	Delta        string    `json:"delta,omitempty"`
	Phase        string    `json:"phase,omitempty"`
	EphemeralSeq int64     `json:"ephemeral_seq"`
	EmittedAt    time.Time `json:"emitted_at"`
}

type LiveTurnPublisher interface {
	PublishLiveTurn(LiveTurnEvent)
}

type LiveTurnSubscriber interface {
	SubscribeLiveTurn(runID string) (<-chan LiveTurnEvent, func())
}
