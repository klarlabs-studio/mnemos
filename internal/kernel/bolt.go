package kernel

import (
	"time"

	"go.klarlabs.de/axi/domain"
	"go.klarlabs.de/bolt"
)

// boltLogger adapts *bolt.Logger to axi-go's domain.Logger interface so
// the kernel's internal logging lands in the same JSON stream as the
// rest of the host process.
type boltLogger struct{ logger *bolt.Logger }

// Debug logs at debug level through the wrapped bolt logger.
func (l boltLogger) Debug(msg string, fields ...domain.Field) { l.emit(l.logger.Debug(), msg, fields) }

// Info logs at info level through the wrapped bolt logger.
func (l boltLogger) Info(msg string, fields ...domain.Field) { l.emit(l.logger.Info(), msg, fields) }

// Warn logs at warn level through the wrapped bolt logger.
func (l boltLogger) Warn(msg string, fields ...domain.Field) { l.emit(l.logger.Warn(), msg, fields) }

// Error logs at error level through the wrapped bolt logger.
func (l boltLogger) Error(msg string, fields ...domain.Field) { l.emit(l.logger.Error(), msg, fields) }

func (l boltLogger) emit(ev *bolt.Event, msg string, fields []domain.Field) {
	for _, f := range fields {
		ev = ev.Any(f.Key, f.Value)
	}
	ev.Msg(msg)
}

// boltPublisher fans every kernel domain event into bolt as a structured
// "axi_event" log line. Cheap, synchronous, never blocks the execution
// path beyond a single JSON encode.
type boltPublisher struct{ logger *bolt.Logger }

// Publish writes the domain event to bolt as a structured "axi_event" line.
func (p boltPublisher) Publish(e domain.DomainEvent) {
	p.logger.Info().
		Str("event_type", e.EventType()).
		Str("occurred_at", e.OccurredAt().UTC().Format(time.RFC3339Nano)).
		Msg("axi_event")
}

// multiPublisher fans every kernel event into every wrapped publisher in
// declaration order. Used to compose the bolt logger with the optional
// JSONL evidence sink without forcing consumers who don't enable the
// sink to pay for an extra hop.
type multiPublisher []domain.DomainEventPublisher

// Publish satisfies domain.DomainEventPublisher.
func (m multiPublisher) Publish(e domain.DomainEvent) {
	for _, p := range m {
		if p == nil {
			continue
		}
		p.Publish(e)
	}
}
