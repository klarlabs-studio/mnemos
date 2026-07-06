# Mnemos Integration Guide

Practical recipes for using Mnemos with LangChain, LlamaIndex, and MCP-compatible AI agents.

**Time to integrate: ~30 minutes.**

---

## Table of Contents

1. [Mnemos as a Pre-Processing Layer for RAG](#mnemos-as-a-pre-processing-layer-for-rag)
2. [LangChain Integration](#langchain-integration)
3. [LlamaIndex Integration](#llamaindex-integration)
4. [MCP Integration for AI Agents](#mcp-integration-for-ai-agents)

---

## Mnemos as a Pre-Processing Layer for RAG

Traditional RAG chunks documents into fixed-size text blocks and embeds them. The problem: chunks are semantically arbitrary, contradictions across chunks are invisible, and retrieved context lacks structure.

Mnemos replaces blind chunking with **claim extraction**. Each claim has a type (`fact`, `hypothesis`, `decision`), a confidence score (0-1), a status (`active`, `contested`, `deprecated`), and traced evidence back to source events. Contradictions between claims are detected and surfaced at query time.

### The workflow

```
Documents  -->  mnemos process  -->  Structured claims  -->  Vector store
                                     with evidence           (your existing one)
```

### Process documents into claims

```bash
# Single file
mnemos process meeting-notes.md

# With LLM extraction for higher quality
export MNEMOS_LLM_PROVIDER=openai
export MNEMOS_LLM_API_KEY=sk-...
mnemos process --llm architecture-decision.md

# Raw text
mnemos process --text "We decided to migrate from MongoDB to PostgreSQL. The DBA team recommends staying on MongoDB for operational simplicity."
```

### Query returns structured JSON by default

```bash
mnemos query "What database should we use?"
```

```json
{
  "answer": "Tech stack decisions show contradiction: PostgreSQL vs MongoDB",
  "claims": [
    {
      "ID": "clm-a1b2c3",
      "Text": "We decided to migrate from MongoDB to PostgreSQL",
      "Type": "decision",
      "Confidence": 0.88,
      "Status": "contested",
      "CreatedAt": "2026-04-13T10:00:00Z"
    },
    {
      "ID": "clm-d4e5f6",
      "Text": "The DBA team recommends staying on MongoDB for operational simplicity",
      "Type": "fact",
      "Confidence": 0.75,
      "Status": "contested",
      "CreatedAt": "2026-04-13T10:00:00Z"
    }
  ],
  "contradictions": [
    {
      "ID": "rel-x7y8z9",
      "Type": "contradicts",
      "FromClaimID": "clm-a1b2c3",
      "ToClaimID": "clm-d4e5f6",
      "CreatedAt": "2026-04-13T10:00:00Z"
    }
  ],
  "timeline": ["evt-001", "evt-002"]
}
```

### Why claims beat raw chunks

| Dimension | Raw chunks | Mnemos claims |
|-----------|-----------|---------------|
| Retrieval unit | Arbitrary 512-token window | Single atomic assertion |
| Metadata | Page number, maybe section header | Type, confidence, status, evidence links |
| Contradictions | Invisible -- both chunks retrieved independently | Explicitly detected and surfaced |
| Ranking signal | Cosine similarity only | Confidence + recency + contradiction status |
| Downstream prompt | "Here are some passages..." | "Here are verified claims, and these two contradict each other..." |

---

## LangChain Integration

### Subprocess wrapper

```python
import json
import subprocess
from typing import Optional


def mnemos_process(text: str, use_llm: bool = False) -> dict:
    """Ingest text into Mnemos and return processing summary."""
    cmd = ["mnemos", "process", "--text", text]
    if use_llm:
        cmd.append("--llm")
    result = subprocess.run(cmd, capture_output=True, text=True, check=True)
    # process command outputs status lines to stderr; stdout has structured output
    return {"status": "ok", "stderr": result.stderr.strip()}


def mnemos_query(question: str, run_id: Optional[str] = None) -> dict:
    """Query the Mnemos knowledge base and return structured JSON."""
    cmd = ["mnemos", "query", question]
    if run_id:
        cmd.extend(["--run", run_id])
    result = subprocess.run(cmd, capture_output=True, text=True, check=True)
    return json.loads(result.stdout)
```

### Parse claims into LangChain Documents

```python
from langchain_core.documents import Document


def mnemos_claims_to_documents(query_result: dict) -> list[Document]:
    """Convert Mnemos query output into LangChain Documents with claim metadata."""
    documents = []
    contradiction_map = _build_contradiction_map(query_result.get("contradictions", []))

    for claim in query_result.get("claims", []):
        claim_id = claim["ID"]
        is_contested = claim_id in contradiction_map

        doc = Document(
            page_content=claim["Text"],
            metadata={
                "source": "mnemos",
                "claim_id": claim_id,
                "claim_type": claim["Type"],
                "confidence": claim["Confidence"],
                "status": claim["Status"],
                "created_at": claim["CreatedAt"],
                "is_contested": is_contested,
                "contradicts": contradiction_map.get(claim_id, []),
            },
        )
        documents.append(doc)

    return documents


def _build_contradiction_map(contradictions: list[dict]) -> dict[str, list[str]]:
    """Map each claim ID to the IDs of claims that contradict it."""
    result: dict[str, list[str]] = {}
    for rel in contradictions:
        from_id = rel["FromClaimID"]
        to_id = rel["ToClaimID"]
        result.setdefault(from_id, []).append(to_id)
        result.setdefault(to_id, []).append(from_id)
    return result
```

### Custom retriever

```python
from langchain_core.callbacks import CallbackManagerForRetrieverRun
from langchain_core.retrievers import BaseRetriever


class MnemosRetriever(BaseRetriever):
    """LangChain retriever backed by Mnemos query engine."""

    use_llm: bool = False

    def _get_relevant_documents(
        self, query: str, *, run_manager: CallbackManagerForRetrieverRun
    ) -> list[Document]:
        result = mnemos_query(query)
        docs = mnemos_claims_to_documents(result)

        # Sort by confidence descending, contested claims last
        docs.sort(
            key=lambda d: (
                not d.metadata["is_contested"],
                d.metadata["confidence"],
            ),
            reverse=True,
        )
        return docs
```

### Grounded generation with contradiction awareness

```python
from langchain_core.output_parsers import StrOutputParser
from langchain_core.prompts import ChatPromptTemplate
from langchain_openai import ChatOpenAI


def build_grounded_chain(llm: ChatOpenAI | None = None):
    """Build a LangChain chain that uses Mnemos claims for grounded generation."""
    if llm is None:
        llm = ChatOpenAI(model="gpt-4o")

    prompt = ChatPromptTemplate.from_messages(
        [
            (
                "system",
                """You are a precise assistant. Answer based ONLY on the provided claims.

Rules:
- If claims contradict each other, explicitly state the contradiction.
- Include confidence scores when claims are below 0.8.
- Never fabricate information beyond what the claims state.
- If no claims are relevant, say "No evidence found in the knowledge base."

Claims:
{claims}

Contradictions:
{contradictions}""",
            ),
            ("human", "{question}"),
        ]
    )

    return prompt | llm | StrOutputParser()


def ask_with_evidence(question: str) -> str:
    """Query Mnemos, format context, and generate a grounded answer."""
    result = mnemos_query(question)

    claims_text = "\n".join(
        f"- [{c['Type']}] (confidence: {c['Confidence']:.2f}, status: {c['Status']}) {c['Text']}"
        for c in result.get("claims", [])
    )

    contradictions_text = "None"
    if result.get("contradictions"):
        lines = []
        claim_lookup = {c["ID"]: c["Text"] for c in result.get("claims", [])}
        for rel in result["contradictions"]:
            from_text = claim_lookup.get(rel["FromClaimID"], rel["FromClaimID"])
            to_text = claim_lookup.get(rel["ToClaimID"], rel["ToClaimID"])
            lines.append(f"- CONTRADICTION: \"{from_text}\" vs \"{to_text}\"")
        contradictions_text = "\n".join(lines)

    chain = build_grounded_chain()
    return chain.invoke(
        {
            "question": question,
            "claims": claims_text or "No claims found.",
            "contradictions": contradictions_text,
        }
    )
```

### Full pipeline example

```python
# 1. Ingest project documents
mnemos_process("We decided to use gRPC for service communication.", use_llm=True)
mnemos_process("REST was chosen as the API protocol for all services.", use_llm=True)

# 2. Query with grounded generation
answer = ask_with_evidence("What protocol do we use for service communication?")
print(answer)
# Output will surface the gRPC vs REST contradiction with confidence scores

# 3. Or use the retriever in a larger chain
retriever = MnemosRetriever()
docs = retriever.invoke("What protocol decisions were made?")
for doc in docs:
    print(f"[{doc.metadata['claim_type']}] {doc.page_content} "
          f"(confidence={doc.metadata['confidence']:.2f}, contested={doc.metadata['is_contested']})")
```

---

## LlamaIndex Integration

### Custom reader returning NodeWithScore

```python
import json
import subprocess
from typing import Optional

from llama_index.core.schema import NodeWithScore, TextNode


class MnemosReader:
    """Reads claims from Mnemos as LlamaIndex nodes."""

    def query(
        self, question: str, run_id: Optional[str] = None
    ) -> list[NodeWithScore]:
        """Query Mnemos and return results as NodeWithScore objects."""
        cmd = ["mnemos", "query", question]
        if run_id:
            cmd.extend(["--run", run_id])

        result = subprocess.run(cmd, capture_output=True, text=True, check=True)
        data = json.loads(result.stdout)

        contradiction_ids = set()
        for rel in data.get("contradictions", []):
            contradiction_ids.add(rel["FromClaimID"])
            contradiction_ids.add(rel["ToClaimID"])

        nodes = []
        for claim in data.get("claims", []):
            claim_id = claim["ID"]
            node = TextNode(
                text=claim["Text"],
                id_=claim_id,
                metadata={
                    "source": "mnemos",
                    "claim_type": claim["Type"],
                    "confidence": claim["Confidence"],
                    "status": claim["Status"],
                    "created_at": claim["CreatedAt"],
                    "is_contested": claim_id in contradiction_ids,
                },
            )
            # Use Mnemos confidence directly as the retrieval score
            score = claim["Confidence"]
            # Penalize contested claims so the synthesizer treats them carefully
            if claim_id in contradiction_ids:
                score *= 0.7
            nodes.append(NodeWithScore(node=node, score=score))

        # Sort by score descending
        nodes.sort(key=lambda n: n.score or 0, reverse=True)
        return nodes

    def get_answer_text(self, question: str) -> str:
        """Return Mnemos' own synthesized answer string."""
        cmd = ["mnemos", "query", question]
        result = subprocess.run(cmd, capture_output=True, text=True, check=True)
        data = json.loads(result.stdout)
        return data.get("answer", "")
```

### Custom query engine alongside LlamaIndex retrieval

```python
from llama_index.core.query_engine import CustomQueryEngine
from llama_index.core.response_synthesizers import get_response_synthesizer
from llama_index.core.retrievers import BaseRetriever as LlamaBaseRetriever
from llama_index.core.schema import QueryBundle


class MnemosQueryEngine(CustomQueryEngine):
    """Query engine that combines Mnemos claims with a standard LlamaIndex retriever."""

    retriever: LlamaBaseRetriever
    mnemos_reader: MnemosReader

    class Config:
        arbitrary_types_allowed = True

    def custom_query(self, query_str: str) -> str:
        # Get claims from Mnemos
        mnemos_nodes = self.mnemos_reader.query(query_str)

        # Get nodes from your existing vector index
        index_nodes = self.retriever.retrieve(query_str)

        # Merge: Mnemos claims first (structured), then vector results (context)
        all_nodes = mnemos_nodes + index_nodes

        # Synthesize with contradiction awareness
        synthesizer = get_response_synthesizer(response_mode="compact")
        query_bundle = QueryBundle(query_str=query_str)
        response = synthesizer.synthesize(query_bundle, all_nodes)
        return str(response)
```

### Usage with an existing VectorStoreIndex

```python
from llama_index.core import VectorStoreIndex, SimpleDirectoryReader

# Your existing index
documents = SimpleDirectoryReader("./project-docs").load_data()
index = VectorStoreIndex.from_documents(documents)
retriever = index.as_retriever(similarity_top_k=5)

# Add Mnemos as a parallel knowledge source
mnemos = MnemosReader()
engine = MnemosQueryEngine(retriever=retriever, mnemos_reader=mnemos)

response = engine.query("What database should we use for the new service?")
print(response)
```

### Feeding Mnemos claims into a LlamaIndex prompt template

```python
from llama_index.core.prompts import PromptTemplate

GROUNDED_QA_PROMPT = PromptTemplate(
    """\
Context from knowledge base (verified claims):
{mnemos_claims}

Known contradictions:
{mnemos_contradictions}

Additional context from document index:
{context_str}

Given the above, answer the question. If claims contradict, state both positions.

Question: {query_str}
Answer: """
)


def query_with_mnemos_context(question: str, retriever: LlamaBaseRetriever) -> str:
    """Combine Mnemos structured claims with vector retrieval."""
    mnemos = MnemosReader()
    claim_nodes = mnemos.query(question)

    claims_text = "\n".join(
        f"- [{n.node.metadata['claim_type']}] (conf: {n.score:.2f}) {n.node.text}"
        for n in claim_nodes
    )

    # Get Mnemos answer which includes contradiction summary
    mnemos_answer = mnemos.get_answer_text(question)
    contradictions_text = mnemos_answer if "contradiction" in mnemos_answer.lower() else "None detected"

    # Standard vector retrieval
    vector_nodes = retriever.retrieve(question)
    context_str = "\n".join(n.node.text for n in vector_nodes)

    synthesizer = get_response_synthesizer()
    from llama_index.core.schema import QueryBundle

    query_bundle = QueryBundle(query_str=question)

    # Use the combined prompt
    response = synthesizer.synthesize(
        query_bundle,
        vector_nodes,
        text_qa_template=GROUNDED_QA_PROMPT.partial_format(
            mnemos_claims=claims_text or "No claims found.",
            mnemos_contradictions=contradictions_text,
        ),
    )
    return str(response)
```

---

## MCP Integration for AI Agents

Mnemos includes an MCP server (`mnemos mcp`) that exposes three tools over stdio:

| Tool | Description | Input |
|------|-------------|-------|
| `query_knowledge` | Query the knowledge base with evidence-backed results | `question` (required), `runId` (optional) |
| `process_text` | Ingest raw text, extract claims, detect relationships | `text` (required), `useLlm` (optional), `useEmbeddings` (optional) |
| `knowledge_metrics` | Return counts and statistics about the knowledge base | (none) |

### Claude Desktop

Add to `~/Library/Application Support/Claude/claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "mnemos": {
      "command": "mnemos",
      "args": ["mcp"],
      "env": {
        "MNEMOS_LLM_PROVIDER": "anthropic",
        "MNEMOS_LLM_API_KEY": "sk-ant-..."
      }
    }
  }
}
```

If `mnemos` is not on your `PATH`, use the absolute path:

```json
{
  "mcpServers": {
    "mnemos": {
      "command": "/usr/local/bin/mnemos",
      "args": ["mcp"],
      "env": {
        "MNEMOS_LLM_PROVIDER": "anthropic",
        "MNEMOS_LLM_API_KEY": "sk-ant-..."
      }
    }
  }
}
```

### Claude Code

Add to `~/.claude/mcp.json` (global) or `.claude/mcp.json` (per-project):

```json
{
  "mcpServers": {
    "mnemos": {
      "command": "mnemos",
      "args": ["mcp"],
      "env": {
        "MNEMOS_DB_URL": "sqlite:///absolute/path/to/project/.mnemos/mnemos.db"
      }
    }
  }
}
```

Setting `MNEMOS_DB_URL` per-project scopes the knowledge base to that project. Without it, Mnemos walks up looking for `.mnemos/mnemos.db` and falls back to `~/.local/share/mnemos/mnemos.db`.

### Cursor

Add to `.cursor/mcp.json` in your project root:

```json
{
  "mcpServers": {
    "mnemos": {
      "command": "mnemos",
      "args": ["mcp"],
      "env": {
        "MNEMOS_DB_URL": "sqlite://.mnemos/mnemos.db"
      }
    }
  }
}
```

### What the agent gets

Once connected, the AI agent can:

1. **Ingest context** -- process meeting notes, design docs, or ADRs into the knowledge base during a conversation.
2. **Query with evidence** -- ask questions and get back claims with confidence scores and contradiction flags instead of raw text.
3. **Check knowledge health** -- call `knowledge_metrics` to see how many claims exist, how many are contested, and whether the knowledge base needs updating.
4. **Reason over the connected brain** -- `who_knows` (which agent is the expert on a topic), `knowledge_gaps`, `calibration`, `hypercorrections`, `recombinations`, and `analogous_claims` (transfer from a solved problem to a new one).
5. **Recall with epistemic honesty** -- `recall` with `mode` = sufficiency (is this enough to answer?), effort (scale the budget to the stakes), context (bias by situation), conflicts (surface contradictions inline), or iterative (multi-round expansion).
6. **Carry working memory + learn** -- `get_blocks`/`set_block` for an agent's bounded "core memory," `record_action`/`record_outcome` + `synthesize_lessons`/`synthesize_playbooks` for the skill loop, and `signals` for temporal patterns over a run's events.

The MCP tool surface is at full parity with the HTTP and gRPC transports (same cognitive layer, tiers 0–4), so the choice of transport is a deployment detail, not a capability trade-off.

Example agent interaction:

```
User: "Process our latest architecture decision and tell me if it conflicts with anything."

Agent calls process_text:
  text: "We decided to adopt event sourcing for the order service."
  useLlm: true

Agent calls query_knowledge:
  question: "What architecture patterns are we using for the order service?"

Agent response:
  "I processed the new decision. There is a contradiction in the knowledge base:
   - Decision (conf: 0.88): 'Adopt event sourcing for the order service'
   - Decision (conf: 0.82): 'Use simple CRUD for all services to reduce complexity'
   These two claims are flagged as contradicting. You may want to deprecate the CRUD decision
   or scope it to exclude the order service."
```

### Per-project knowledge isolation

For multi-project setups, use separate databases:

```bash
# Project A
MNEMOS_DB_URL=sqlite://~/projects/project-a/.mnemos/mnemos.db mnemos process docs/*.md

# Project B
MNEMOS_DB_URL=sqlite://~/projects/project-b/.mnemos/mnemos.db mnemos process specs/*.md
```

Each MCP config can point to a different `MNEMOS_DB_URL`, so your agent's knowledge stays scoped to the project it is working on.
