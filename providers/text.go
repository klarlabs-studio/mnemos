// Package providers exposes framework-neutral interfaces for language
// model and embedding providers. Mnemos consumes these interfaces; agent
// runtimes (Claude Code, Codex, Hermes, Nomi, OpenClaw, NanoClaw, ...)
// implement them as adapters around their own provider clients.
//
// The interfaces are deliberately small. They carry only the fields
// Mnemos needs and intentionally do NOT expose provider-specific
// configuration. If a consumer wants Mnemos to use its model, it wraps
// its client in a small adapter that satisfies one of these interfaces.
//
// Example adapter for an Anthropic-style client:
//
//	type myTextGenerator struct{ client *anthropic.Client }
//
//	func (g *myTextGenerator) GenerateText(
//	    ctx context.Context,
//	    in providers.GenerateTextInput,
//	) (providers.GenerateTextOutput, error) {
//	    resp, err := g.client.Messages.Create(ctx, ...)
//	    if err != nil { return providers.GenerateTextOutput{}, err }
//	    return providers.GenerateTextOutput{
//	        Content:      resp.Content,
//	        Model:        resp.Model,
//	        InputTokens:  resp.Usage.InputTokens,
//	        OutputTokens: resp.Usage.OutputTokens,
//	    }, nil
//	}
package providers

import "context"

// Role names the speaker of a message in a chat-style exchange. Mnemos
// uses the standard system / user / assistant taxonomy.
type Role string

// Recognised Role values. Implementations should not invent new roles
// outside this set without updating Mnemos's prompt assembly.
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one turn of a chat-style exchange. Content is plain text;
// Mnemos does not currently use tool-call structures.
type Message struct {
	Role    Role
	Content string
}

// GenerateTextInput is the input to a [TextGenerator].
type GenerateTextInput struct {
	// Messages is the chat history to complete. The last message is
	// usually a [RoleUser] message; the implementation produces the
	// next [RoleAssistant] message.
	Messages []Message
}

// GenerateTextOutput is the output of a [TextGenerator]. Token counts
// are optional (zero is acceptable when the provider does not report
// them).
type GenerateTextOutput struct {
	Content      string
	Model        string
	InputTokens  int
	OutputTokens int
}

// TextGenerator is the framework-neutral abstraction Mnemos uses for any
// language model call (claim extraction, grounded answer generation,
// playbook synthesis, etc.). Consumers implement this interface around
// their own provider clients.
//
// Implementations MUST be safe for concurrent use.
type TextGenerator interface {
	GenerateText(ctx context.Context, in GenerateTextInput) (GenerateTextOutput, error)
}
