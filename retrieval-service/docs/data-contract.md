# Ingestion and storage contract

This is the first-version contract between the ingestion and retrieval services.

## Chunk JSONL

The ingestion service's chunk JSONL contains one chunk per line. It assigns each chunk a UUID used as both the Qdrant point ID and payload `chunk_id`, and as Neo4j `Chunk.id`. The ingestion service generates embeddings; the source documents do not need to contain chunk IDs or model vectors.

```json
{"chunk_id":"b3c2d56e-ae8e-44d3-bc44-a2d75a2ecb54","document_id":"report-2023","source":"report-2023.pdf","title":"Annual report 2023","text":"Company Acme announced ...","chunk_index":0,"metadata":{"author":"Research team","year":"2023","topic":"finance"}}
{"chunk_id":"26fb1d10-6c11-4f84-923d-838e8c143afa","document_id":"report-2023","source":"report-2023.pdf","title":"Annual report 2023","text":"The supplier relationship ...","chunk_index":1,"metadata":{"author":"Research team","year":"2023","topic":"finance"}}
```

Required fields: `chunk_id`, `document_id`, `text`, and `chunk_index`. `source` and `title` are optional. The Qdrant payload contains `chunk_id`, `document_id`, `text`, and `metadata`; metadata values should be strings and include source and chunk position when available.

## Qdrant

- Collection name defaults to `documents` and is configured with `QDRANT_COLLECTION`.
- The ingestion service creates the collection using cosine distance and 384-dimensional vectors from `intfloat/multilingual-e5-small`.
- Each point payload contains `chunk_id`, `document_id`, `text`, and `metadata`.
- The Qdrant point ID and payload `chunk_id` must match the corresponding Neo4j `Chunk.id`.
- Retrieval does not create or migrate the collection; it checks that the configured collection exists on startup.

## Neo4j

The first Graph Local adapter expects this minimal graph:

- `(:Entity {name: string})`
- `(:Chunk {id: string, text: string})`
- `(:Document {id: string})`
- `(entity:Entity)-[:RELATED_TO]-(other:Entity)`
- `(entity:Entity)-[:MENTIONED_IN]->(chunk:Chunk)`
- `(document:Document)-[:HAS_CHUNK]->(chunk:Chunk)`

`RELATED_TO` relationships have a `relation_type` property and a `source_chunk_ids` list containing IDs of chunks that support the relation. Entity names stored in `Entity.name` are canonical names; ingestion resolves known aliases to these names, and does not automatically merge ambiguous names.

The adapter matches extracted entity names case-insensitively and traverses `RELATED_TO` relationships up to the requested depth (1 or 2). It returns structured facts from traversed relationships and only the chunk texts whose IDs appear in those relationships' `source_chunk_ids`; it does not use all chunks that merely mention the neighboring entities as graph evidence. Fact source and target names follow the stored relationship direction. Add uniqueness constraints for `Chunk.id` and `Document.id` during ingestion setup.

For the initial version, document changes trigger a full rebuild of both indexes while search is stopped. Retrieval does not perform incremental index updates.

## Query processing API

The retrieval service calls `POST /v1/query-context` on `INGESTION_URL` with:

```json
{"query":"How did Acme's 2023 launch affect its supplier?"}
```

The ingestion service returns both outputs from the same query. `embedding` is an array of exactly 384 floats from `intfloat/multilingual-e5-small`, using the `query: ` prefix. Chunk vectors use the same model and version with the `passage: ` prefix. `entities` contains canonical entity names matching `Entity.name`; it may be empty when no entity is found. Invalid requests return `400`; embedding or entity extraction failures return `503` with a description. No authorization is required for the local first version.

This one call is performed for each retrieval request; in hybrid mode the resulting vector and graph searches then run concurrently.

## Answer generation boundary

When `generate_answer` is enabled, retrieval passes the original trimmed question and only the final, non-empty retrieved chunk IDs and texts to Ollama `POST /api/chat`. It does not pass unreturned graph evidence, filenames as IDs, metadata, or ingestion state. The existing query preparation and storage contracts above do not change.

The answer model uses `stream: false`, `truncate: false`, `shift: false`, a JSON schema, `temperature: 0`, `num_ctx: 16384`, and a bounded output of 2048 tokens. Its untrusted draft has this structure:

```json
{"facts":[{"text":"Verbatim excerpt from a retrieved chunk.","chunk_ids":["chunk-id"]}],"missing":["Verbatim unanswered part of the question"]}
```

Both arrays and the two fact fields are required. Retrieval rejects malformed/null/unknown draft fields, incomplete generation, tool output, and responses larger than 1 MiB. Each `missing` item must literally occur in the question. Invalid items are replaced with a fixed diagnostic, never displayed as model-written assertions. Valid items are explicitly labeled as quotations from the question, not facts from documents.

Serialized question/chunk context larger than 12 KiB fails generation rather than being truncated by this client. This conservative byte budget leaves room in the requested 16384-token Qwen window for the system prompt, chat template and 2048-token output. Overflow/truncation controls require a compatible Ollama daemon. A different model/tokenizer/template or an older daemon that ignores controls is outside this verified protocol: validate compatibility before changing the default model. The byte budget is not a general-purpose tokenizer.

For each fact, retrieval checks that every cited ID is in the context and that the full fact text is literally present in every cited chunk. A fact with any invalid reference is dropped as a whole. Conflicting duplicate IDs are not usable evidence. Citation destinations and document IDs are built from the retrieved data, not accepted from the model. The resulting [answer contract](search-api.md#optional-grounded-answer) distinguishes incomplete evidence from operational generation failures.

The server currently uses the same default model name as ingestion (`qwen2.5:3b`), with independent HTTP client timeout `ANSWER_TIMEOUT`. Ollama daemon version and model digest are not pinned by this repository. Its response envelope is decoded strictly; compatibility with other daemon/model versions must be checked before deployment.

## Local data services

Copy `.env.example` to `.env` and start the databases:

```powershell
Copy-Item .env.example .env
docker compose up -d
```

The Neo4j credentials in the example are for local development only. The `.env` file is used by Compose; Go does not load `.env` automatically. When running the Go server locally, set `NEO4J_PASSWORD` in the process environment as well. The retrieval server checks Qdrant and Neo4j at startup and logs a warning if either dependency is unavailable, but still starts so hybrid requests can return partial results from the available source. `GET /healthz` is a liveness check; inspect per-source statuses in search responses to determine dependency availability.
