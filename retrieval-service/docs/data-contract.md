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

## Local data services

Copy `.env.example` to `.env` and start the databases:

```powershell
Copy-Item .env.example .env
docker compose up -d
```

The Neo4j credentials in the example are for local development only. The `.env` file is used by Compose; Go does not load `.env` automatically. When running the Go server locally, set `NEO4J_PASSWORD` in the process environment as well. The retrieval server starts after the configured Qdrant collection and Neo4j are reachable.
