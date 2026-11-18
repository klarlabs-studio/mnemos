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
			Go: Has("Remember"), REST: Has("POST /v1/process"), GRPC: Has("ProcessText"),
			MCP: Has("process_text"), MCPLite: Has("remember"), HTTPLite: Has("POST /v1/remember"), Client: Has("Process"),
		},
		Notes: "REST, gRPC and MCP run the same pipeline (REST and gRPC through runProcessRequest, which calls process_text's mcpRunProcessText); Go and the lite adapters go through Memory.Remember.",
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
		ID: "episode.retract", Effect: Write,
		Summary: "Withdraw an episode as evidence: retract beliefs it alone supported, rescore the rest, report the impact (ADR 0037)",
		On: map[Transport]Binding{
			Go: Has("RetractEvent"), REST: Has("POST /v1/episodes/{id}/retract"), GRPC: Has("RetractEpisode"),
			MCP: Has("retract_episode"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("RetractEvent"),
		},
		Notes: "Every binding and `mnemos retract-event` go through internal/retraction.Run (id, existence and run-scope checks) and govwrite's one retraction: Writer.RetractEvent on the daemon surfaces, ApplyRetraction inside the library's governed kernel. REST, MCP and the CLI return the same retraction.Report. watch_file calls Writer.RetractEvent for text a revised or deleted file no longer contains.",
	},
	{
		ID: "episode.unretract", Effect: Write,
		Summary: "Clear an episode's retraction: restore the beliefs it deprecated that nothing changed since, relink the ones it rescored (ADR 0037 §8)",
		On: map[Transport]Binding{
			Go: Has("UnretractEvent"), REST: Has("POST /v1/episodes/{id}/unretract"), GRPC: Has("UnretractEpisode"),
			MCP: Has("unretract_episode"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("UnretractEvent"),
		},
		Notes: "Every binding and `mnemos unretract-event` go through internal/retraction.Unretract (id, existence, run-scope and was-it-ever-retracted checks) and govwrite's one un-retraction: Writer.UnretractEvent on the daemon surfaces, ApplyUnretraction inside the library's governed kernel. REST, MCP and the CLI return the same retraction.UnretractionReport. A never-retracted episode is REST 409 / gRPC FailedPrecondition.",
	},
	{
		ID: "episode.list", Effect: Read,
		Summary: "List stored episodes",
		On: map[Transport]Binding{
			Go: Has("ListEvents"), REST: Has("GET /v1/episodes"), GRPC: Has("ListEpisodes"),
			MCP: Has("list_episodes"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("Events"),
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
			MCP: Has("remember_belief"), MCPLite: Has("remember_claim"), HTTPLite: Has("POST /v1/claims"), Client: Has("Claims"),
		},
	},
	{
		ID: "belief.remember_fact", Effect: Write,
		Summary: "Store one fact as a belief, creating its evidence episode",
		On: map[Transport]Binding{
			Go: Has("RememberClaimWithEvidence"), REST: Has("POST /v1/facts"), GRPC: Has("RememberFact"),
			MCP: Has("remember"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("RememberClaimWithEvidence"),
		},
		Notes: "REST, gRPC and the client go through Memory.RememberClaimWithEvidence (one observation episode per evidence string, run-scoped tokens refused). MCP remember is a separate implementation that records the fact text itself as its one run-scoped episode.",
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
			Go: Has("Scan"), REST: Has("GET /v1/scan"), GRPC: Has("ScanBeliefs"),
			MCP: Has("scan_beliefs"), MCPLite: Has("scan"), HTTPLite: Has("POST /v1/scan"), Client: Has("Scan"),
		},
	},
	{
		ID: "belief.lifecycle", Effect: Write,
		Summary: "Set a belief's curation lifecycle (candidate, promoted, superseded)",
		On: map[Transport]Binding{
			Go: Has("SetClaimLifecycle"), REST: Has("POST /v1/beliefs/{id}/lifecycle"), GRPC: Has("SetBeliefLifecycle"),
			MCP: Has("set_belief_lifecycle"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("SetClaimLifecycle"),
		},
	},
	{
		ID: "belief.govern", Effect: Write,
		Summary: "Agent governance edits: deprecate, forget, update text, resolve or escalate a dissonance, promote",
		On: map[Transport]Binding{
			Go: Has("DeprecateClaim", "UpdateClaim", "ResolveContradiction", "EscalateClaim", "VerifyClaim"),
			REST: Has("POST /v1/beliefs/{id}/deprecate", "POST /v1/beliefs/{id}/update", "POST /v1/beliefs/{id}/resolve",
				"POST /v1/beliefs/{id}/escalate", "POST /v1/beliefs/{id}/verify"),
			GRPC:    Has("DeprecateBelief", "UpdateBelief", "ResolveDissonance", "EscalateBelief", "VerifyBelief"),
			MCP:     Has("memory_deprecate", "forget", "update", "memory_resolve_dissonance", "memory_escalate", "memory_promote"),
			MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("DeprecateClaim", "UpdateClaim", "ResolveContradiction", "EscalateClaim", "VerifyClaim"),
		},
		Notes: "Every binding runs internal/beliefops. MCP forget and memory_deprecate are one operation (Deprecate) recording different default reasons, so the other transports expose it once; memory_promote is Verify (last_verified, verify_count, last_confirmed) and takes promote:global on every transport. Escalate returns a verdict and writes nothing.",
	},
	{
		ID: "belief.purge", Effect: Write,
		Summary: "Hard-delete beliefs (and the run's episodes) by run",
		On: map[Transport]Binding{
			Go: No("destructive operator action; not part of the embedded API"), REST: Has("DELETE /v1/beliefs"), GRPC: No("destructive operator action; REST only by design"),
			MCP: No("destructive operator action; MCP forget deprecates instead"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("PurgeClaims"),
		},
	},
	{
		ID: "belief.provenance", Effect: Read,
		Summary: "Explain where a belief came from",
		On: map[Transport]Binding{
			Go: Has("ClaimProvenance"), REST: Has("GET /v1/beliefs/{id}/provenance"), GRPC: Has("BeliefProvenance"),
			MCP: Has("belief_provenance"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("BeliefProvenance"),
		},
	},
	{
		ID: "belief.history", Effect: Read,
		Summary: "A belief's version chain and status history",
		On: map[Transport]Binding{
			Go: Has("ClaimHistory"), REST: Has("GET /v1/beliefs/{id}/history"), GRPC: Has("BeliefHistory"),
			MCP: Has("belief_history"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("BeliefHistory"),
		},
	},
	{
		ID: "belief.export_markdown", Effect: Read,
		Summary: "Render a belief as human-editable Markdown",
		On: map[Transport]Binding{
			Go: No("a presentation format for HTTP consumers"), REST: Has("GET /v1/beliefs/{id}/export.md"), GRPC: No("a presentation format for HTTP consumers"),
			MCP: No("a presentation format for HTTP consumers"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("ExportClaimMarkdown"),
		},
	},
	{
		ID: "belief.feedback", Effect: Write,
		Summary: "Record helpful/unhelpful feedback on a belief",
		On: map[Transport]Binding{
			Go: Has("RecordFeedback"), REST: Has("POST /v1/beliefs/{id}/feedback"), GRPC: Has("RecordBeliefFeedback"),
			MCP: Has("record_feedback"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("RecordFeedback"),
		},
		Notes: "Every binding runs beliefops.RecordFeedback: an unhelpful vote decays confidence and contests the belief at MNEMOS_FEEDBACK_CONTEST_THRESHOLD.",
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
			MCP: Has("list_associations"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("Relationships"),
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
			Go: No(notLibraryRaw), REST: Has("GET /v1/entity-associations"), GRPC: Has("ListEntityAssociations"),
			MCP: Has("list_entity_associations"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("ListEntityRelationships"),
		},
	},
	{
		ID: "entity_association.append", Effect: Write,
		Summary: "Append entity-to-entity associations",
		On: map[Transport]Binding{
			Go: No(notLibraryRaw), REST: Has("POST /v1/entity-associations"), GRPC: Has("AppendEntityAssociations"),
			MCP: No("associations are derived, not written by agents"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("AppendEntityRelationships"),
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
			Go: No("the library's search is Recall"), REST: Has("GET /v1/search", "POST /v1/search"), GRPC: Has("Search"),
			MCP: Has("query_knowledge", "search_memory"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("Search"),
		},
		Notes: "REST /v1/search and gRPC Search share runSearch; every binding recalls through query.Engine.Recall, pinned by TestRecallParity_EverySurfaceAgrees.",
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
		ID: "recall.passages", Effect: Read,
		Summary: "Return the stored episode passages that best match a query (BM25)",
		On: map[Transport]Binding{
			Go: Has("Passages"), REST: Has("GET /v1/passages"), GRPC: Has("Passages"),
			MCP: Has("search_passages"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("Passages"),
		},
		Notes: "Measured on the answer-quality evaluation (#441, deviation 6) before it was exposed remotely. A run-scoped token must name one of its runs.",
	},
	{
		ID: "recall.context_block", Effect: Read,
		Summary: "Render the context block for a run",
		On: map[Transport]Binding{
			Go: Has("ContextBlock"), REST: Has("GET /v1/context", "POST /v1/context"), GRPC: Has("ContextBlock"),
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
			Go: Has("PredictiveError"), REST: Has("GET /v1/predictive-error"), GRPC: Has("PredictiveError"),
			MCP: Has("predictive_error"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("PredictiveError"),
		},
	},
	{
		ID: "brain.health", Effect: Read,
		Summary: "Brain health: vitals, integrity checks and one verdict (ADR 0019)",
		On: map[Transport]Binding{
			Go: Has("BrainHealth", "BrainHealthFull", "SnapshotHealth"), REST: Has("GET /v1/brain/health"), GRPC: Has("BrainHealth"),
			MCP: Has("brain_health"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("BrainHealth"),
		},
	},
	{
		ID: "brain.metrics", Effect: Read,
		Summary: "Knowledge-base counts and averages",
		On: map[Transport]Binding{
			Go: Has("Metrics"), REST: Has("GET /v1/metrics"), GRPC: Has("Metrics"),
			MCP: Has("knowledge_metrics"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("Metrics"),
		},
		Notes: "Every binding counts through internal/kbmetrics.Compute; a run-scoped token sees counts over its runs only.",
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
			Go: Has("Expect"), REST: Has("POST /v1/beliefs/{id}/expectation", "GET /v1/beliefs/{id}/expectation"), GRPC: Has("SetExpectation", "GetExpectation"),
			MCP: Has("record_expectation"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("SetExpectation", "Expectation"),
		},
		Notes: "Every binding sets through govwrite (CheckExpectation, then the set_expectation governed write; the library runs ApplySetExpectation in its own kernel) and returns the row as stored. The belief must exist; a run-scoped token may touch only beliefs whose evidence lies in its runs (runscope.ClaimAllowed), others read as not found.",
	},
	{
		ID: "expectation.observe", Effect: Write,
		Summary: "Record the observed value for a belief's prediction",
		On: map[Transport]Binding{
			Go: Has("RecordObservation"), REST: Has("POST /v1/beliefs/{id}/observation"), GRPC: Has("RecordObservation"),
			MCP: Has("record_observation"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("RecordObservation"),
		},
		Notes: "Every binding observes through govwrite (CheckObservation, then the observe_expectation governed write); no expectation is REST 404 / gRPC NotFound / mnemos.ErrExpectationNotFound. Run-scoped tokens are confined as for expectation.set.",
	},
	{
		ID: "expectation.reconcile", Effect: Write,
		Summary: "Close observed predictions into validates/refutes verdicts",
		On: map[Transport]Binding{
			Go: Has("ReconcileExpectations"), REST: Has("POST /v1/expectations/reconcile"), GRPC: Has("ReconcileExpectations"),
			MCP: Has("reconcile_expectations"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("ReconcileExpectations"),
		},
		Notes: "Every binding reconciles through govwrite (the reconcile_expectations governed write; the library runs ApplyReconcileExpectations in its own kernel). It spans every run, so a run-scoped token is refused.",
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
			Go: Has("ListActions"), REST: Has("GET /v1/actions"), GRPC: Has("ListActions"),
			MCP: Has("list_actions"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("ListActions"),
		},
		Notes: "Every binding reads through internal/skills.Actions; a run-scoped token sees only its runs and naming another run is refused.",
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
			Go: Has("ListOutcomes"), REST: Has("GET /v1/outcomes"), GRPC: Has("ListOutcomes"),
			MCP: Has("list_outcomes"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("ListOutcomes"),
		},
		Notes: "Every binding reads through internal/skills.Outcomes; an outcome belongs to its action's run for run-scoped tokens.",
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
			Go: Has("ListLessons"), REST: Has("GET /v1/tenant-schemas"), GRPC: Has("ListSchemas"),
			MCP: Has("query_schemas"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("ListLessons"),
		},
		Notes: "Every binding reads through internal/skills.Schemas. REST names the tenant tier (/v1/tenant-schemas) because /v1/schemas is schema.global_list.",
	},
	{
		ID: "schema.append", Effect: Write,
		Summary: "Append schemas (lessons) authored elsewhere",
		On: map[Transport]Binding{
			Go: No(notLibraryRaw), REST: Has("POST /v1/tenant-schemas"), GRPC: Has("AppendSchemas"),
			MCP: No("schemas are synthesized, not written by agents"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("AppendLessons"),
		},
		Notes: "REST and gRPC go through internal/skills.AppendSchemas (whole-batch validation, evidence must name stored actions) and govwrite's Writer.Lesson.",
	},
	{
		ID: "schema.global_list", Effect: Read,
		Summary: "List schemas promoted to the shared (global) brain",
		On: map[Transport]Binding{
			Go: Has("ListGlobalSchemas"), REST: Has("GET /v1/schemas"), GRPC: Has("ListGlobalSchemas"),
			MCP: Has("list_global_schemas"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("ListGlobalSchemas"),
		},
		Notes: "Same word, different entity from schema.list: gRPC ListSchemas returns tenant lessons, REST /v1/schemas returns promoted global schemas. Every binding reads through internal/skills.GlobalSchemas from the base (cross-tenant) connection.",
	},
	{
		ID: "reflex.list", Effect: Read,
		Summary: "List or match reflexes (playbooks)",
		On: map[Transport]Binding{
			Go: Has("ListPlaybooks"), REST: Has("GET /v1/reflexes"), GRPC: Has("ListReflexes"),
			MCP: Has("query_reflex"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("ListPlaybooks"),
		},
		Notes: "Every binding reads through internal/skills.Reflexes.",
	},
	{
		ID: "reflex.append", Effect: Write,
		Summary: "Append reflexes (playbooks) authored elsewhere",
		On: map[Transport]Binding{
			Go: No(notLibraryRaw), REST: Has("POST /v1/reflexes"), GRPC: Has("AppendReflexes"),
			MCP: No("reflexes are synthesized, not written by agents"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("AppendPlaybooks"),
		},
		Notes: "REST and gRPC go through internal/skills.AppendReflexes (whole-batch validation, derived-from ids must name stored schemas) and govwrite's Writer.Playbook.",
	},

	// --- Decisions -------------------------------------------------------
	{
		ID: "decision.record", Effect: Write,
		Summary: "Record an agent decision with its beliefs and alternatives",
		On: map[Transport]Binding{
			Go: Has("RecordDecision"), REST: Has("POST /v1/decisions"), GRPC: Has("AppendDecisions"),
			MCP: Has("record_decision"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("RecordDecision"),
		},
		Notes: "REST POST /v1/decisions and MCP record_decision share mcpRunRecordDecision (server-assigned id, validation, govwrite); gRPC AppendDecisions writes caller-supplied ids through govwrite; Go records through the library's governed kernel.",
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
			Go:   Has("OpenIncident", "ListIncidents", "GetIncident", "ResolveIncident"),
			REST: Has("POST /v1/incidents", "GET /v1/incidents", "GET /v1/incidents/{id}", "POST /v1/incidents/{id}/resolve"),
			GRPC: Has("OpenIncident", "ListIncidents", "GetIncident", "ResolveIncident"),
			MCP:  Has("open_incident", "list_incidents", "get_incident", "resolve_incident"), MCPLite: No(notLite), HTTPLite: No(notLite),
			Client: Has("OpenIncident", "ListIncidents", "GetIncident", "ResolveIncident"),
		},
		Notes: "Every binding goes through internal/incident (validation, not-found before any write, author from the caller) and govwrite's incident writes: Writer.Incident/ResolveIncident on the daemon surfaces, ApplyIncident/ApplyResolveIncident inside the library's governed kernel. Every backend stores incidents (memory, sqlite, libSQL via the sqlite repository, Postgres under RLS, MySQL per namespace). The REST incident document keeps its original keys (root_cause_claim_id, timeline_event_ids, playbook_id) deliberately: renaming them would break the wire; gRPC and MCP use the brain-native names.",
	},
	{
		ID: "incident.why_wrong", Effect: Read,
		Summary: "Post-mortem: which beliefs and decisions led to an incident",
		On: map[Transport]Binding{
			Go: Has("WhyWereWeWrong"), REST: Has("GET /v1/incidents/{id}/why-wrong"), GRPC: Has("WhyWereWeWrong"),
			MCP: Has("why_were_we_wrong"), MCPLite: No(notLite), HTTPLite: No(notLite), Client: Has("WhyWereWeWrong"),
		},
		Notes: "Every binding runs internal/incident.WhyWereWeWrong; an unknown incident is not-found on every transport.",
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
		Summary: "Landing page and registry web app shell, with their scripts and stylesheets",
		On: map[Transport]Binding{
			Go: No(notServer), REST: Has("GET /", "GET /app", "GET /assets/landing.css", "GET /assets/landing.js", "GET /assets/app.css", "GET /assets/app.js"), GRPC: No(notServer),
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
