package qlog

import (
	"context"

	"github.com/quic-go/quic-go"
	quicqlog "github.com/quic-go/quic-go/qlog"
	"github.com/quic-go/quic-go/qlogwriter"
)

// EventSchema is the qlog event schema for RTP over QUIC.
const EventSchema = "urn:ietf:params:qlog:events:roq-01"

// DefaultConnectionTracer creates a qlog trace that accepts both QUIC and RoQ
// events. Set it as quic.Config.Tracer to have a roq Session log its events into
// the same file as the QUIC transport events. Like quic-go's tracer, it writes to
// the directory named by the QLOGDIR environment variable and returns nil if
// QLOGDIR is not set.
func DefaultConnectionTracer(ctx context.Context, isClient bool, connID quic.ConnectionID) qlogwriter.Trace {
	return quicqlog.DefaultConnectionTracerWithSchemas(ctx, isClient, connID, []string{quicqlog.EventSchema, EventSchema})
}
