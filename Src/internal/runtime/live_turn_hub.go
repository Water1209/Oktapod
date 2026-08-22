package runtime

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

const liveTurnSubscriberCapacity = 64

type liveTurnSubscription struct {
	id      uint64
	channel chan core.LiveTurnEvent
}

// LiveTurnHub fans out ephemeral safe assistant deltas inside one process. A
// slow subscriber is detached instead of applying backpressure to the Worker.
type LiveTurnHub struct {
	mu          sync.Mutex
	nextID      uint64
	sequences   map[string]int64
	subscribers map[string]map[uint64]*liveTurnSubscription
}

func NewLiveTurnHub() *LiveTurnHub {
	return &LiveTurnHub{sequences: make(map[string]int64), subscribers: make(map[string]map[uint64]*liveTurnSubscription)}
}

func (hub *LiveTurnHub) PublishLiveTurn(event core.LiveTurnEvent) {
	if hub == nil || strings.TrimSpace(event.RunID) == "" {
		return
	}
	hub.mu.Lock()
	if len(hub.subscribers[event.RunID]) == 0 {
		hub.mu.Unlock()
		return
	}
	hub.sequences[event.RunID]++
	event.EphemeralSeq = hub.sequences[event.RunID]
	if event.EmittedAt.IsZero() {
		event.EmittedAt = time.Now().UTC()
	}
	for id, subscription := range hub.subscribers[event.RunID] {
		select {
		case subscription.channel <- event:
		default:
			delete(hub.subscribers[event.RunID], id)
			close(subscription.channel)
		}
	}
	if len(hub.subscribers[event.RunID]) == 0 {
		delete(hub.subscribers, event.RunID)
		delete(hub.sequences, event.RunID)
	}
	hub.mu.Unlock()
}

func (hub *LiveTurnHub) SubscribeLiveTurn(runID string) (<-chan core.LiveTurnEvent, func()) {
	channel := make(chan core.LiveTurnEvent, liveTurnSubscriberCapacity)
	if hub == nil || strings.TrimSpace(runID) == "" {
		close(channel)
		return channel, func() {}
	}
	id := atomic.AddUint64(&hub.nextID, 1)
	hub.mu.Lock()
	if hub.subscribers[runID] == nil {
		hub.subscribers[runID] = make(map[uint64]*liveTurnSubscription)
	}
	hub.subscribers[runID][id] = &liveTurnSubscription{id: id, channel: channel}
	hub.mu.Unlock()
	var once sync.Once
	return channel, func() {
		once.Do(func() {
			hub.mu.Lock()
			if subscription := hub.subscribers[runID][id]; subscription != nil {
				delete(hub.subscribers[runID], id)
				close(subscription.channel)
			}
			if len(hub.subscribers[runID]) == 0 {
				delete(hub.subscribers, runID)
				delete(hub.sequences, runID)
			}
			hub.mu.Unlock()
		})
	}
}

type liveDeltaEmitter struct {
	mu          sync.Mutex
	publisher   core.LiveTurnPublisher
	runID       string
	modelCallID string
	buffer      strings.Builder
	timer       *time.Timer
	closed      bool
}

func newLiveDeltaEmitter(publisher core.LiveTurnPublisher, runID, modelCallID string) *liveDeltaEmitter {
	return &liveDeltaEmitter{publisher: publisher, runID: runID, modelCallID: modelCallID}
}

func (emitter *liveDeltaEmitter) Add(delta string) error {
	if emitter == nil || emitter.publisher == nil || delta == "" {
		return nil
	}
	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	if emitter.closed {
		return nil
	}
	emitter.buffer.WriteString(delta)
	if emitter.buffer.Len() >= 2*1024 {
		emitter.flushLocked()
		return nil
	}
	if emitter.timer == nil {
		emitter.timer = time.AfterFunc(50*time.Millisecond, emitter.flush)
	}
	return nil
}

func (emitter *liveDeltaEmitter) flush() {
	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	emitter.timer = nil
	emitter.flushLocked()
}

func (emitter *liveDeltaEmitter) flushLocked() {
	if emitter.buffer.Len() == 0 || emitter.publisher == nil {
		return
	}
	delta := emitter.buffer.String()
	emitter.buffer.Reset()
	emitter.publisher.PublishLiveTurn(core.LiveTurnEvent{RunID: emitter.runID, ModelCallID: emitter.modelCallID, Kind: "assistant_delta", Delta: delta, Phase: "responding"})
}

func (emitter *liveDeltaEmitter) Close() {
	if emitter == nil {
		return
	}
	emitter.mu.Lock()
	if emitter.timer != nil {
		emitter.timer.Stop()
		emitter.timer = nil
	}
	emitter.flushLocked()
	emitter.closed = true
	emitter.mu.Unlock()
}
