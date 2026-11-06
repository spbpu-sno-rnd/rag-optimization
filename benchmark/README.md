# Pilot retrieval benchmark

This folder contains a small, deterministic, fictional corpus and a human-readable gold set for comparing `vector`, `graph_local`, and `hybrid` retrieval. The corpus and questions are separate from `ingestion-service`; the ingestion service can index this directory using its existing rebuild command.

## Contents

- `corpus/`: documents to ingest; each source is deliberately short so one file remains one chunk with the current chunking setup.
- `corpus/aliases.json`: canonical names and known variants for this corpus.
- `queries.jsonl`: one labeled question per line.
- `validate_dataset.py`: offline structural validation of labels and corpus references.

The initial pilot has 13 questions: 4 single-hop, 4 multi-hop, 3 distractor/comparison, and 2 unanswerable. Cases are split into `dev` (7 cases) and `test` (6 cases), with all four categories represented in both. Use `dev` to tune retrieval settings and keep `test` untouched until reporting final results. The set is intended to expose retrieval behavior and integration issues, not to support statistically strong research claims. The current unanswerable cases are intended for later generation/abstention evaluation; retrieval systems may still return nearest-neighbor context for them.

Each query contains:

| Field | Meaning |
|---|---|
| `id` | Stable case identifier |
| `split` | `dev` for development or `test` for held-out checks |
| `query` | User-facing test question |
| `category` | `single_hop`, `multi_hop`, `distractor`, or `unanswerable` |
| `answerable` | Whether the corpus supports an answer |
| `expected_answer` | Reference answer for a future generation evaluation; `null` when unanswerable |
| `expected_entities` | Canonical query entities expected from `POST /v1/query-context` |
| `gold_evidence` | Source filenames containing the minimum evidence for the answer |
| `required_facts` | Gold directed relation triples and the source document supporting each triple |

Evidence uses source filenames, not chunk UUIDs: ingestion generates new UUIDs on every rebuild. At evaluation time, returned result metadata can be mapped to these stable source names. For an answer requiring multiple facts, every `required_facts` entry is expected evidence; source overlap alone is not sufficient to establish that the graph retrieved the complete reasoning path.

## Validate the dataset

From the repository root:

```powershell
py .\benchmark\validate_dataset.py
```

This checks JSONL syntax, schema, unique case IDs, category coverage, canonical entity labels, and that every gold evidence reference points to an existing corpus file.

## Index for a local integration run

**Warning:** ingestion's `rebuild` command replaces the Qdrant collection and clears the configured Neo4j `Entity`, `Document`, and `Chunk` nodes. Use a disposable local database or back up data first. Stop retrieval before rebuilding.

With Qdrant and Neo4j running and the E5 worker and Ollama available, from the repository root:

```powershell
cd ingestion-service
$env:NEO4J_PASSWORD = "local-dev-password"
$env:ALIASES_FILE = "..\benchmark\corpus\aliases.json"
go run . rebuild --input ..\benchmark\corpus ..\ingestion-service\out\benchmark-chunks.jsonl
```

The rebuild output reports the number of extracted relations. Inspect that count and the resulting graph before starting retrieval; the gold facts are labels, not a guarantee that the extraction model has populated every relation.

After indexing, start ingestion and retrieval as usual, then query `POST /v1/search` with each case using each strategy. The offline validator does not call either service and does not compute retrieval or answer-quality metrics; that belongs to the evaluation stage.
