package domain

import "time"

// WorkingMemoryBlock is a bounded, labeled, mutable text block owned by an agent
// — the persisted unit of "working memory" (core memory): a small, always-loaded
// scratchpad the agent maintains about itself and its current focus, distinct
// from the queried claim archive. Bounded size is the attention budget; the cap
// is enforced at the public API boundary, not here (the repository is a dumb
// side-table store).
type WorkingMemoryBlock struct {
	Owner     string    // the agent this block belongs to
	Label     string    // block name, e.g. "persona", "open_threads", "working_context"
	Content   string    // the block's text
	UpdatedAt time.Time // last-write timestamp
}
