package query

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.klarlabs.de/mnemos/internal/domain"
)

// MaxHops bounds graph expansion on every recall surface.
const MaxHops = 5

// ErrInvalidRecall marks a recall request the caller got wrong. Surfaces map
// it to their user-error form (CLI exit code, MCP user input error, HTTP 400).
var ErrInvalidRecall = errors.New("invalid recall request")

// RecallRequest is the one shape every recall surface builds: the CLI `query`
// command, the query_knowledge and recall_at_time MCP tools, REST /v1/search,
// gRPC and the mnemos library. Surfaces differ in how a caller spells a
// request, never in what the request means.
type RecallRequest struct {
	Question string
	// RunID scopes the recall to one ingestion run; empty searches the brain.
	RunID   string
	Options AnswerOptions
}

// Recall is THE recall entry point. Every surface used to repeat the same four
// steps — validate, apply the cognitive defaults, bound hops, pick the run or
// whole-brain path — and each repeated them a little differently: the CLI
// rejected hops above five while MCP silently clamped them, MCP accepted no
// time bounds at all, and a surface that forgot WithCognitiveDefaults recalled
// without the cognitive behaviours. One function makes "the same question gets
// the same answer on every surface" a property of the code rather than of four
// hand-maintained copies.
func (e Engine) Recall(ctx context.Context, req RecallRequest) (domain.Answer, error) {
	question := strings.TrimSpace(req.Question)
	if question == "" {
		return domain.Answer{}, fmt.Errorf("%w: question is required", ErrInvalidRecall)
	}
	opts := req.Options
	if opts.Hops < 0 || opts.Hops > MaxHops {
		return domain.Answer{}, fmt.Errorf("%w: hops must be in [0, %d], got %d", ErrInvalidRecall, MaxHops, opts.Hops)
	}
	if opts.MinTrust < 0 || opts.MinTrust > 1 {
		return domain.Answer{}, fmt.Errorf("%w: min trust must be in [0, 1], got %g", ErrInvalidRecall, opts.MinTrust)
	}
	opts = opts.WithCognitiveDefaults()
	if run := strings.TrimSpace(req.RunID); run != "" {
		return e.AnswerForRunWithOptions(ctx, question, run, opts)
	}
	return e.AnswerWithOptions(ctx, question, opts)
}
