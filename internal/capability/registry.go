package capability

// Shared omission reasons. A reason is part of the contract, so the common ones
// are named once rather than paraphrased sixty times.
const (
	// The lite adapters are a deliberate core subset (see package mcp / http).
	notLite = "the lite adapters expose only the core Store port: remember, remember_claim/event, recall, get, scan, timeline"
	// Raw record I/O that the library performs through its own write path.
	notLibraryRaw = "raw record I/O for remote clients; the library writes and reads through its own Memory operations"
	// Operations over the server's process, not the knowledge.
	notServer = "server process plumbing; meaningless for this transport"
	// Agent-workflow tools built for an MCP client in a working directory.
	agentWorkflow = "agent-workflow tool that operates on the caller's working directory or session"
	// Library-only plumbing.
	libraryOnly = "library lifecycle; remote transports manage this per process or per request"
)

// Registry is the capability contract. Order is report order: grouped by area.
var Registry = []Capability{
	// --- Ingest ----------------------------------------------------------
	{
		ID: "ingest.text", Effect: Write,
		Summary: "Extract beliefs from free text (ingest → extract → relate → persist)",
		On: map[Transport]Binding{
			Go: Has("Remember"), REST: Has("POST /v1/process"), GRPC: Gap("no ingest RPC; gRPC clients append pre-extracted beliefs"),
			MCP: Has("process_text"), MCPLite: Has("remember"), HTTPLite: Has("POST /v1/remember"), Client: Has("Process"),
		},
		Notes: "REST and MCP run the pipeline directly; Go and the lite adapters go through Memory.Remember.",
	},
	{
		ID: "ingest.git", Effect: Write,
		Summary: "Ingest a repository's git log or pull requests as episodes",
		On: map[Transport]Binding{
			Go: No(agentWorkflow), REST: No(agentWorkflow), GRPC: No(agentWorkflow),
			MCP: Has("ingest_git_log", "ingest_git_prs"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: No(agentWorkflow),
		},
	},
	{
		ID: "ingest.watch", Effect: Write,
		Summary: "Keep a file's beliefs fresh as the file changes",
		On: map[Transport]Binding{
			Go: No(agentWorkflow), REST: No(agentWorkflow), GRPC: No(agentWorkflow),
			MCP: Has("watch_file"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: No(agentWorkflow),
		},
	},

	// --- Episodes and time -----------------------------------------------
	{
		ID: "episode.append", Effect: Write,
		Summary: "Append an episode (an immutable timestamped event)",
		On: map[Transport]Binding{
			Go: Has("RememberEvent"), REST: Has("POST /v1/episodes"), GRPC: Has("AppendEpisodes"),
			MCP: Has("remember_episode"), MCPLite: Has("remember_event"), HTTPLite: Has("POST /v1/events"), Client: Has("Events"),
		},
		Notes: "Go and the lite adapters write to the bundled Chronos timeline; REST, gRPC and MCP append to the episode store.",
	},
	{
		ID: "episode.list", Effect: Read,
		Summary: "List stored episodes",
		On: map[Transport]Binding{
			Go: Gap("no episode listing on Memory; Timeline covers the temporal view"), REST: Has("GET /v1/episodes"), GRPC: Has("ListEpisodes"),
			MCP: Gap("no episode listing tool"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("Events"),
		},
	},
	{
		ID: "timeline.query", Effect: Read,
		Summary: "Query episodes on the timeline by run, type and range",
		On: map[Transport]Binding{
			Go: Has("Timeline"), REST: Has("GET /v1/timeline"), GRPC: Has("Timeline"),
			MCP: Has("timeline_query"), MCPLite: No(notLite), HTTPLite: Has("POST /v1/timeline"), Client: Has("Timeline"),
		},
	},
	{
		ID: "timeline.signals", Effect: Read,
		Summary: "Detected temporal patterns over the timeline",
		On: map[Transport]Binding{
			Go: Has("Signals"), REST: Has("GET /v1/signals"), GRPC: Has("Signals"),
			MCP: Has("signals"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("Signals"),
		},
	},

	// --- Beliefs ---------------------------------------------------------
	{
		ID: "belief.write", Effect: Write,
		Summary: "Write a pre-built belief linked to existing evidence",
		On: map[Transport]Binding{
			Go: Has("RememberClaim"), REST: Has("POST /v1/beliefs"), GRPC: Has("AppendBeliefs"),
			MCP: Gap("no tool writes a pre-built belief; remember stores one fact with its own evidence"), MCPLite: Has("remember_claim"), HTTPLite: Has("POST /v1/claims"), Client: Has("Claims"),
		},
	},
	{
		ID: "belief.remember_fact", Effect: Write,
		Summary: "Store one fact as a belief, creating its evidence episode",
		On: map[Transport]Binding{
			Go: Has("RememberClaimWithEvidence"), REST: Gap("no single-fact write route"), GRPC: Gap("no single-fact write RPC"),
			MCP: Has("remember"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Gap("no single-fact write"),
		},
		Notes: "MCP remember and Go RememberClaimWithEvidence are separate implementations of the same idea.",
	},
	{
		ID: "belief.get", Effect: Read,
		Summary: "Read one belief by id",
		On: map[Transport]Binding{
			Go: Has("Get"), REST: Has("GET /v1/beliefs/{id}"), GRPC: Has("GetBelief"),
			MCP: Has("get_belief"), MCPLite: Has("get"), HTTPLite: Has("GET /v1/claims/{id}"), Client: Has("GetClaim"),
		},
	},
	{
		ID: "belief.list", Effect: Read,
		Summary: "List beliefs with filters (type, status, run, as-of)",
		On: map[Transport]Binding{
			Go: No("the library reads by query (Recall) or valid-time range (Scan), not by listing"), REST: Has("GET /v1/beliefs"), GRPC: Has("ListBeliefs"),
			MCP: Has("list_beliefs"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("Claims"),
		},
	},
	{
		ID: "belief.scan", Effect: Read,
		Summary: "Read beliefs valid within a time range",
		On: map[Transport]Binding{
			Go: Has("Scan"), REST: Gap("no valid-time range route; GET /v1/beliefs filters by a single as-of instant"), GRPC: Gap("no valid-time range RPC"),
			MCP: Gap("no valid-time range tool"), MCPLite: Has("scan"), HTTPLite: Has("POST /v1/scan"), Client: Gap("no valid-time range read"),
		},
	},
	{
		ID: "belief.lifecycle", Effect: Write,
		Summary: "Set a belief's curation lifecycle (candidate, promoted, superseded)",
		On: map[Transport]Binding{
			Go: Has("SetClaimLifecycle"), REST: Has("POST /v1/beliefs/{id}/lifecycle"), GRPC: Has("SetBeliefLifecycle"),
			MCP: Gap("no lifecycle tool; memory_promote verifies rather than setting lifecycle"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("SetClaimLifecycle"),
		},
	},
	{
		ID: "belief.govern", Effect: Write,
		Summary: "Agent governance edits: deprecate, forget, update text, resolve or escalate a dissonance, promote",
		On: map[Transport]Binding{
			Go: Gap("no governance edits on Memory"), REST: Gap("no governance edit routes"), GRPC: Gap("no governance edit RPCs"),
			MCP:     Has("memory_deprecate", "forget", "update", "memory_resolve_dissonance", "memory_escalate", "memory_promote"),
			MCPLite: No(notLite), HTTPLite: No(notLite), Client: Gap("no governance edits"),
		},
	},
	{
		ID: "belief.purge", Effect: Write,
		Summary: "Hard-delete beliefs (and the run's episodes) by run",
		On: map[Transport]Binding{
			Go: No("destructive operator action; not part of the embedded API"), REST: Has("DELETE /v1/beliefs"), GRPC: No("destructive operator action; REST only by design"),
			MCP: No("destructive operator action; MCP forget deprecates instead"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Gap("no purge call"),
		},
	},
	{
		ID: "belief.provenance", Effect: Read,
		Summary: "Explain where a belief came from",
		On: map[Transport]Binding{
			Go: Gap("no provenance read on Memory"), REST: Has("GET /v1/beliefs/{id}/provenance"), GRPC: Gap("no provenance RPC"),
			MCP: Gap("no provenance tool"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Gap("no provenance read"),
		},
	},
	{
		ID: "belief.history", Effect: Read,
		Summary: "A belief's version chain and status history",
		On: map[Transport]Binding{
			Go: Gap("no history read on Memory"), REST: Has("GET /v1/beliefs/{id}/history"), GRPC: Gap("no history RPC"),
			MCP: Gap("no history tool"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Gap("no history read"),
		},
	},
	{
		ID: "belief.export_markdown", Effect: Read,
		Summary: "Render a belief as human-editable Markdown",
		On: map[Transport]Binding{
			Go: No("a presentation format for HTTP consumers"), REST: Has("GET /v1/beliefs/{id}/export.md"), GRPC: No("a presentation format for HTTP consumers"),
			MCP: No("a presentation format for HTTP consumers"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Gap("no Markdown export"),
		},
	},
	{
		ID: "belief.feedback", Effect: Write,
		Summary: "Record helpful/unhelpful feedback on a belief",
		On: map[Transport]Binding{
			Go: Gap("no feedback write on Memory"), REST: Has("POST /v1/beliefs/{id}/feedback"), GRPC: Gap("no feedback RPC"),
			MCP: Has("record_feedback"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Gap("no feedback write"),
		},
	},
	{
		ID: "belief.classify", Effect: Read,
		Summary: "Classify a candidate statement as fitting existing knowledge or novel",
		On: map[Transport]Binding{
			Go: Has("ClassifyClaim"), REST: Has("GET /v1/classify"), GRPC: Has("Classify"),
			MCP: Has("classify"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("Classify"),
		},
	},
	{
		ID: "belief.analogous", Effect: Read,
		Summary: "Beliefs structurally analogous to a given belief",
		On: map[Transport]Binding{
			Go: Has("AnalogousClaims"), REST: Has("GET /v1/beliefs/{id}/analogous"), GRPC: Has("AnalogousBeliefs"),
			MCP: Has("analogous_beliefs"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("AnalogousClaims"),
		},
	},
	{
		ID: "belief.decision_claims", Effect: Read,
		Summary: "List beliefs of type decision",
		On: map[Transport]Binding{
			Go: No("Recall/Scan filter by type"), REST: No("GET /v1/beliefs?type=decision"), GRPC: No("ListBeliefs filters by type"),
			MCP: Has("list_decisions"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: No("Claims().Type(\"decision\")"),
		},
		Notes: "Not decision records: those are decision.list. The MCP name invites the confusion.",
	},
	{
		ID: "belief.which_test_to_trust", Effect: Read,
		Summary: "Pick the test result to trust among conflicting runs",
		On: map[Transport]Binding{
			Go: No(agentWorkflow), REST: No(agentWorkflow), GRPC: No(agentWorkflow),
			MCP: Has("which_test_to_trust"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: No(agentWorkflow),
		},
	},

	// --- Associations and vectors ----------------------------------------
	{
		ID: "association.list", Effect: Read,
		Summary: "List belief-to-belief associations",
		On: map[Transport]Binding{
			Go: No(notLibraryRaw), REST: Has("GET /v1/associations"), GRPC: Has("ListAssociations"),
			MCP: Gap("only contradictions are listable (dissonance.list)"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("Relationships"),
		},
	},
	{
		ID: "association.append", Effect: Write,
		Summary: "Append belief-to-belief associations",
		On: map[Transport]Binding{
			Go: No(notLibraryRaw), REST: Has("POST /v1/associations"), GRPC: Has("AppendAssociations"),
			MCP: No("associations are derived by relate, not written by agents"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("Relationships"),
		},
	},
	{
		ID: "dissonance.list", Effect: Read,
		Summary: "List contradictions between beliefs",
		On: map[Transport]Binding{
			Go: No("RecallWithConflicts and Hypercorrections surface contradictions in context"), REST: No("GET /v1/associations filters by type"), GRPC: No("ListAssociations filters by type"),
			MCP: Has("list_dissonances"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: No("Relationships() filters by type"),
		},
	},
	{
		ID: "entity_association.list", Effect: Read,
		Summary: "List entity-to-entity associations",
		On: map[Transport]Binding{
			Go: No(notLibraryRaw), REST: Gap("no entity association route"), GRPC: Has("ListEntityAssociations"),
			MCP: Gap("no entity association tool"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Gap("no entity association read"),
		},
	},
	{
		ID: "entity_association.append", Effect: Write,
		Summary: "Append entity-to-entity associations",
		On: map[Transport]Binding{
			Go: No(notLibraryRaw), REST: Gap("no entity association route"), GRPC: Has("AppendEntityAssociations"),
			MCP: No("associations are derived, not written by agents"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Gap("no entity association write"),
		},
	},
	{
		ID: "embedding.list", Effect: Read,
		Summary: "List stored embeddings",
		On: map[Transport]Binding{
			Go: No(notLibraryRaw), REST: Has("GET /v1/embeddings"), GRPC: Has("ListEmbeddings"),
			MCP: No("vectors are not agent-facing"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("Embeddings"),
		},
	},
	{
		ID: "embedding.append", Effect: Write,
		Summary: "Append embeddings computed elsewhere",
		On: map[Transport]Binding{
			Go: No(notLibraryRaw), REST: Has("POST /v1/embeddings"), GRPC: Has("AppendEmbeddings"),
			MCP: No("vectors are not agent-facing"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("Embeddings"),
		},
	},

	// --- Recall ----------------------------------------------------------
	{
		ID: "recall.basic", Effect: Read,
		Summary: "Recall beliefs for a query (with optional as-of and recorded-as-of)",
		On: map[Transport]Binding{
			Go: Has("Recall"), REST: No("served by recall.search and recall.advanced"), GRPC: No("served by recall.advanced (Recall RPC without a mode)"),
			MCP: No("served by recall.search and recall.at_time"), MCPLite: Has("recall"), HTTPLite: Has("POST /v1/recall"), Client: No("served by recall.search and recall.advanced"),
		},
		Notes: "The lite `recall` is plain Recall; the full MCP `recall` is recall.advanced. Same name, different capability.",
	},
	{
		ID: "recall.search", Effect: Read,
		Summary: "Hybrid search over beliefs (query engine ranking, filters)",
		On: map[Transport]Binding{
			Go: No("the library's search is Recall"), REST: Has("GET /v1/search", "POST /v1/search"), GRPC: Gap("no search RPC"),
			MCP: Has("query_knowledge", "search_memory"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("Search"),
		},
		Notes: "query_knowledge and /v1/search build their query engines separately; ranking parity is guarded in internal/query, not here.",
	},
	{
		ID: "recall.at_time", Effect: Read,
		Summary: "Recall what was believed at a past instant",
		On: map[Transport]Binding{
			Go: No("Recall with Query.AsOf / RecordedAsOf"), REST: No("GET /v1/search?as_of="), GRPC: No("ListBeliefs as_of"),
			MCP: Has("recall_at_time"), MCPLite: No("recall with as_of"), HTTPLite: No("POST /v1/recall with as_of"), Client: No("Search with AsOf"),
		},
	},
	{
		ID: "recall.advanced", Effect: Read,
		Summary: "Recall with sufficiency, effort, context, conflicts or iteration",
		On: map[Transport]Binding{
			Go:   Has("RecallWithSufficiency", "RecallWithEffort", "RecallWithContext", "RecallWithConflicts", "RecallIterative"),
			REST: Has("GET /v1/recall"), GRPC: Has("Recall"), MCP: Has("recall"),
			MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("Recall"),
		},
	},
	{
		ID: "recall.context_block", Effect: Read,
		Summary: "Render the context block for a run",
		On: map[Transport]Binding{
			Go: Gap("no context-block render on Memory"), REST: Has("GET /v1/context", "POST /v1/context"), GRPC: Gap("no context-block RPC"),
			MCP: Has("memory_context"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("Context"),
		},
	},

	// --- Metacognition ---------------------------------------------------
	{
		ID: "brain.who_knows", Effect: Read,
		Summary: "Who in the brain's provenance knows about a topic",
		On: map[Transport]Binding{
			Go: Has("WhoKnows"), REST: Has("GET /v1/who-knows"), GRPC: Has("WhoKnows"),
			MCP: Has("who_knows"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("WhoKnows"),
		},
	},
	{
		ID: "brain.knowledge_gaps", Effect: Read,
		Summary: "Where the brain's knowledge is thin, stale or uncertain",
		On: map[Transport]Binding{
			Go: Has("KnowledgeGaps"), REST: Has("GET /v1/knowledge-gaps"), GRPC: Has("KnowledgeGaps"),
			MCP: Has("knowledge_gaps"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("KnowledgeGaps"),
		},
	},
	{
		ID: "brain.calibration", Effect: Read,
		Summary: "Expected calibration error over adjudicated beliefs",
		On: map[Transport]Binding{
			Go: Has("Calibration"), REST: Has("GET /v1/calibration"), GRPC: Has("Calibration"),
			MCP: Has("calibration"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("Calibration"),
		},
	},
	{
		ID: "brain.hypercorrections", Effect: Read,
		Summary: "Contradictions of established beliefs, most-established first",
		On: map[Transport]Binding{
			Go: Has("Hypercorrections"), REST: Has("GET /v1/hypercorrections"), GRPC: Has("Hypercorrections"),
			MCP: Has("hypercorrections"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("Hypercorrections"),
		},
	},
	{
		ID: "brain.recombinations", Effect: Read,
		Summary: "Related but unlinked belief pairs worth connecting",
		On: map[Transport]Binding{
			Go: Has("Recombinations"), REST: Has("GET /v1/recombinations"), GRPC: Has("Recombinations"),
			MCP: Has("recombinations"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("Recombinations"),
		},
	},
	{
		ID: "brain.predictive_error", Effect: Read,
		Summary: "Hierarchical prediction error (outcome, schema, dissonance, calibration)",
		On: map[Transport]Binding{
			Go: Has("PredictiveError"), REST: Gap("no predictive-error route"), GRPC: Gap("no predictive-error RPC"),
			MCP: Has("predictive_error"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Gap("no predictive-error read"),
		},
	},
	{
		ID: "brain.health", Effect: Read,
		Summary: "Brain health: vitals, integrity checks and one verdict (ADR 0019)",
		On: map[Transport]Binding{
			Go: Has("BrainHealth", "SnapshotHealth"), REST: Gap("only Prometheus gauges at /internal/metrics; no health report route"), GRPC: Gap("no brain-health RPC"),
			MCP: Gap("no brain-health tool"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Gap("no brain-health read"),
		},
	},
	{
		ID: "brain.metrics", Effect: Read,
		Summary: "Knowledge-base counts and averages",
		On: map[Transport]Binding{
			Go: Gap("no metrics read on Memory"), REST: Has("GET /v1/metrics"), GRPC: Has("Metrics"),
			MCP: Has("knowledge_metrics"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("Metrics"),
		},
	},
	{
		ID: "brain.consolidate", Effect: Write,
		Summary: "Run the consolidation (sleep) pass",
		On: map[Transport]Binding{
			Go: Has("Consolidate"), REST: No("serve runs it on --consolidate-interval; not a request"), GRPC: No("serve runs it on a schedule; not a request"),
			MCP: No("the session hook runs sleep; not an agent tool"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: No("run by the server's schedule"),
		},
	},

	// --- Prediction ------------------------------------------------------
	{
		ID: "expectation.set", Effect: Write,
		Summary: "Attach a forward prediction to a belief (and read it back)",
		On: map[Transport]Binding{
			Go: Has("Expect"), REST: Has("POST /v1/beliefs/{id}/expectation", "GET /v1/beliefs/{id}/expectation"), GRPC: Gap("no expectation RPC"),
			MCP: Has("record_expectation"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("SetExpectation", "Expectation"),
		},
	},
	{
		ID: "expectation.observe", Effect: Write,
		Summary: "Record the observed value for a belief's prediction",
		On: map[Transport]Binding{
			Go: Has("RecordObservation"), REST: Has("POST /v1/beliefs/{id}/observation"), GRPC: Gap("no observation RPC"),
			MCP: Has("record_observation"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("RecordObservation"),
		},
	},
	{
		ID: "expectation.reconcile", Effect: Write,
		Summary: "Close observed predictions into validates/refutes verdicts",
		On: map[Transport]Binding{
			Go: Has("ReconcileExpectations"), REST: Gap("no reconcile route"), GRPC: Gap("no reconcile RPC"),
			MCP: Gap("no reconcile tool"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Gap("no reconcile call"),
		},
	},

	// --- Skill loop --------------------------------------------------------
	{
		ID: "action.record", Effect: Write,
		Summary: "Record an operational action",
		On: map[Transport]Binding{
			Go: Has("RecordAction"), REST: Has("POST /v1/actions"), GRPC: Has("AppendActions"),
			MCP: Has("record_action"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("RecordAction"),
		},
	},
	{
		ID: "action.list", Effect: Read,
		Summary: "List recorded actions",
		On: map[Transport]Binding{
			Go: Gap("no action listing on Memory"), REST: Gap("no action listing route"), GRPC: Has("ListActions"),
			MCP: Gap("no action listing tool"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Gap("no action listing"),
		},
	},
	{
		ID: "outcome.record", Effect: Write,
		Summary: "Record the observed outcome of an action",
		On: map[Transport]Binding{
			Go: Has("RecordActionOutcome"), REST: Has("POST /v1/actions/{id}/outcome"), GRPC: Has("AppendOutcomes"),
			MCP: Has("record_outcome"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("RecordActionOutcome"),
		},
	},
	{
		ID: "outcome.list", Effect: Read,
		Summary: "List recorded outcomes",
		On: map[Transport]Binding{
			Go: Gap("no outcome listing on Memory"), REST: Gap("no outcome listing route"), GRPC: Has("ListOutcomes"),
			MCP: Gap("no outcome listing tool"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Gap("no outcome listing"),
		},
	},
	{
		ID: "synthesis.run", Effect: Write,
		Summary: "Synthesize schemas (lessons) and reflexes (playbooks) from action→outcome chains",
		On: map[Transport]Binding{
			Go: Has("Synthesize"), REST: Has("POST /v1/synthesize"), GRPC: Has("Synthesize"),
			MCP: Has("synthesize_schemas", "synthesize_reflexes"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("Synthesize"),
		},
		Notes: "MCP splits the two stages into separate tools and does not go through Memory.Synthesize.",
	},
	{
		ID: "schema.list", Effect: Read,
		Summary: "List synthesized schemas (lessons)",
		On: map[Transport]Binding{
			Go: Gap("no schema listing on Memory"), REST: Gap("no lesson listing route; /v1/schemas lists PROMOTED global schemas"), GRPC: Has("ListSchemas"),
			MCP: Has("query_schemas"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Gap("no schema listing"),
		},
	},
	{
		ID: "schema.append", Effect: Write,
		Summary: "Append schemas (lessons) authored elsewhere",
		On: map[Transport]Binding{
			Go: No(notLibraryRaw), REST: Gap("no schema append route"), GRPC: Has("AppendSchemas"),
			MCP: No("schemas are synthesized, not written by agents"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Gap("no schema append"),
		},
	},
	{
		ID: "schema.global_list", Effect: Read,
		Summary: "List schemas promoted to the shared (global) brain",
		On: map[Transport]Binding{
			Go: Gap("no promoted-schema read on Memory"), REST: Has("GET /v1/schemas"), GRPC: Gap("no promoted-schema RPC (ListSchemas is lessons)"),
			MCP: Gap("no promoted-schema tool"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Gap("no promoted-schema read"),
		},
		Notes: "Same word, different entity from schema.list: gRPC ListSchemas returns tenant lessons, REST /v1/schemas returns promoted global schemas.",
	},
	{
		ID: "reflex.list", Effect: Read,
		Summary: "List or match reflexes (playbooks)",
		On: map[Transport]Binding{
			Go: Gap("no reflex read on Memory"), REST: Gap("no reflex route"), GRPC: Has("ListReflexes"),
			MCP: Has("query_reflex"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Gap("no reflex read"),
		},
	},
	{
		ID: "reflex.append", Effect: Write,
		Summary: "Append reflexes (playbooks) authored elsewhere",
		On: map[Transport]Binding{
			Go: No(notLibraryRaw), REST: Gap("no reflex append route"), GRPC: Has("AppendReflexes"),
			MCP: No("reflexes are synthesized, not written by agents"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Gap("no reflex append"),
		},
	},

	// --- Decisions -------------------------------------------------------
	{
		ID: "decision.record", Effect: Write,
		Summary: "Record an agent decision with its beliefs and alternatives",
		On: map[Transport]Binding{
			Go: Has("RecordDecision"), REST: Gap("no decision write route"), GRPC: Has("AppendDecisions"),
			MCP: Has("record_decision"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Gap("no decision write"),
		},
	},
	{
		ID: "decision.get", Effect: Read,
		Summary: "Read one decision record",
		On: map[Transport]Binding{
			Go: Has("GetDecision"), REST: Has("GET /v1/decisions/{id}"), GRPC: Has("GetDecision"),
			MCP: Has("get_decision"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("GetDecision"),
		},
	},
	{
		ID: "decision.list", Effect: Read,
		Summary: "List decision records",
		On: map[Transport]Binding{
			Go: Has("ListDecisions"), REST: Has("GET /v1/decisions"), GRPC: Has("ListDecisions"),
			MCP: Has("query_decisions"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("ListDecisions"),
		},
		Notes: "MCP list_decisions is belief.decision_claims, not this.",
	},

	// --- Working memory --------------------------------------------------
	{
		ID: "blocks.read", Effect: Read,
		Summary: "Read an owner's working-memory blocks",
		On: map[Transport]Binding{
			Go: Has("Blocks"), REST: Has("GET /v1/blocks"), GRPC: Has("GetBlocks"),
			MCP: Has("get_blocks"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("Blocks"),
		},
	},
	{
		ID: "blocks.write", Effect: Write,
		Summary: "Set or append to a working-memory block",
		On: map[Transport]Binding{
			Go: Has("SetBlock", "AppendBlock"), REST: Has("POST /v1/blocks"), GRPC: Has("SetBlock"),
			MCP: Has("set_block"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("SetBlock", "AppendBlock"),
		},
	},

	// --- Incidents and federation ----------------------------------------
	{
		ID: "incident.manage", Effect: Write,
		Summary: "Open, list, read and resolve incidents",
		On: map[Transport]Binding{
			Go:   Gap("no incident operations on Memory"),
			REST: Has("POST /v1/incidents", "GET /v1/incidents", "GET /v1/incidents/{id}", "POST /v1/incidents/{id}/resolve"),
			GRPC: Gap("no incident RPCs"), MCP: Gap("no incident tools"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Gap("no incident calls"),
		},
	},
	{
		ID: "incident.why_wrong", Effect: Read,
		Summary: "Post-mortem: which beliefs and decisions led to an incident",
		On: map[Transport]Binding{
			Go: Gap("no post-mortem read on Memory"), REST: Has("GET /v1/incidents/{id}/why-wrong"), GRPC: Gap("no post-mortem RPC"),
			MCP: Gap("no post-mortem tool"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Gap("no post-mortem read"),
		},
	},
	{
		ID: "federation.export", Effect: Read,
		Summary: "Export anonymized reflexes for federation (opt-in)",
		On: map[Transport]Binding{
			Go: No("server-to-server federation; opt-in via MNEMOS_FEDERATION_ENABLED"), REST: Has("GET /v1/federation/export"), GRPC: No("federation is HTTP by design"),
			MCP: No("server-to-server federation, not an agent tool"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: No("pulled by peer servers, not clients"),
		},
	},

	// --- Plumbing --------------------------------------------------------
	{
		ID: "infra.liveness", Effect: Infra,
		Summary: "Bare liveness probe",
		On: map[Transport]Binding{
			Go: No(notServer), REST: Has("GET /health", "GET /healthz"), GRPC: Has("Health"),
			MCP: No("MCP clients see liveness through the transport"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("Health"),
		},
	},
	{
		ID: "infra.readiness", Effect: Infra,
		Summary: "Readiness: version and a database write probe",
		On: map[Transport]Binding{
			Go: No(notServer), REST: Has("GET /internal/ready"), GRPC: No("readiness is an HTTP probe"),
			MCP: No(notServer), MCPLite: No(notLite), HTTPLite: No(notLite), Client: No("probed by orchestrators, not clients"),
		},
	},
	{
		ID: "infra.prometheus", Effect: Infra,
		Summary: "Prometheus metrics",
		On: map[Transport]Binding{
			Go: No(notServer), REST: Has("GET /internal/metrics"), GRPC: No("scraped over HTTP"),
			MCP: No(notServer), MCPLite: No(notLite), HTTPLite: No(notLite), Client: No("scraped by Prometheus, not clients"),
		},
	},
	{
		ID: "infra.web", Effect: Infra,
		Summary: "Landing page and registry web app shell",
		On: map[Transport]Binding{
			Go: No(notServer), REST: Has("GET /", "GET /app"), GRPC: No(notServer),
			MCP: No(notServer), MCPLite: No(notLite), HTTPLite: No(notLite), Client: No(notServer),
		},
	},
	{
		ID: "infra.leads", Effect: Infra,
		Summary: "Public, rate-limited lead-capture form",
		On: map[Transport]Binding{
			Go: No(notServer), REST: Has("POST /v1/leads"), GRPC: No(notServer),
			MCP: No(notServer), MCPLite: No(notLite), HTTPLite: No(notLite), Client: No(notServer),
		},
	},
	{
		ID: "infra.environment", Effect: Infra,
		Summary: "Configure the local agent environment (MCP registration, hooks, skills)",
		On: map[Transport]Binding{
			Go: No(agentWorkflow), REST: No(agentWorkflow), GRPC: No(agentWorkflow),
			MCP: Has("configure_environment"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: No(agentWorkflow),
		},
	},
	{
		ID: "infra.library", Effect: Infra,
		Summary: "Library lifecycle: info, close, last write session",
		On: map[Transport]Binding{
			Go: Has("Info", "Close", "LastWriteSession"), REST: No(libraryOnly), GRPC: No(libraryOnly),
			MCP: No(libraryOnly), MCPLite: No(libraryOnly), HTTPLite: No(libraryOnly), Client: No(libraryOnly),
		},
	},
	{
		ID: "infra.tenant", Effect: Infra,
		Summary: "Select the tenant a call is scoped to",
		On: map[Transport]Binding{
			Go: Has("Tenant"), REST: No("selected per request by the X-Mnemos-Tenant header and the token's tnt claim"),
			GRPC: No("selected per call by x-mnemos-tenant metadata and the token's tnt claim"),
			MCP:  No("selected per request by the token's tnt claim"), MCPLite: No(notLite), HTTPLite: No(notLite),
			Client: No("selected by the client's configured headers"),
		},
	},
	{
		ID: "infra.client", Effect: Infra,
		Summary: "Client plumbing: enablement check and run scoping",
		On: map[Transport]Binding{
			Go: No("client-side helpers"), REST: No("client-side helpers"), GRPC: No("client-side helpers"),
			MCP: No("client-side helpers"), MCPLite: No("client-side helpers"), HTTPLite: No("client-side helpers"),
			Client: Has("IsEnabled", "ForRun"),
		},
	},
}
