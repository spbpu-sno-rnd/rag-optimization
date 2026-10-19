# Ingestion and storage contract

This is the proposed first-version contract between the ingestion and retrieval services. Confirm the field names and the embedding model/dimension with the ingestion service before loading the shared dataset.

## Chunk JSONL

The JSONL file contains one chunk per line. `chunk_id` is globally stable and identical in Qdrant and Neo4j. The ingestion service generates embeddings; the source JSONL does not need to contain model vectors.

```json
{"chunk_id":"report-2023:chunk:0","document_id":"report-2023","source":"report-2023.pdf","title":"Annual report 2023","text":"Company Acme announced ...","chunk_index":0,"metadata":{"author":"Research team","year":"2023","topic":"finance"}}
{"chunk_id":"report-2023:chunk:1","document_id":"report-2023","source":"report-2023.pdf","title":"Annual report 2023","text":"The supplier relationship ...","chunk_index":1,"metadata":{"author":"Research team","year":"2023","topic":"finance"}}
```

Required fields: `chunk_id`, `document_id`, `text`, and `chunk_index`. `source`, `title`, and `metadata` are optional; metadata values should be strings for consistent filtering and response serialization.

## Qdrant

- Collection name defaults to `documents` and is configured with `QDRANT_COLLECTION`.
- The ingestion service creates the collection using cosine distance and its embedding model's output dimension.
- Each point payload contains `chunk_id`, `document_id`, `text`, and optionally `metadata`.
- `chunk_id` must be unique and shared with the corresponding Neo4j `Chunk.id`.
- Retrieval does not create or migrate the collection; it checks that the configured collection exists on startup.

## Neo4j

The first Graph Local adapter expects this minimal graph:

- `(:Entity {name: string})`
- `(:Chunk {id: string, text: string})`
- `(:Document {id: string})`
- `(entity:Entity)-[:RELATED_TO]-(other:Entity)`
- `(entity:Entity)-[:MENTIONED_IN]->(chunk:Chunk)`
- `(document:Document)-[:HAS_CHUNK]->(chunk:Chunk)`

The adapter matches extracted entity names case-insensitively, traverses `RELATED_TO` relationships up to the requested depth (1 or 2), then returns the associated chunk text. Add uniqueness constraints for `Chunk.id` and `Document.id` during ingestion setup.

## Query processing API

The retrieval service calls `POST /v1/query-context` on `INGESTION_URL` with:

```json
{"query":"How did Acme's 2023 launch affect its supplier?"}
```

The ingestion service returns both outputs from the same query:

```json
{"embedding":[0.012,-0.034],"entities":["Acme","supplier"]}
```

`embedding` must use exactly the model and dimension used to embed the stored chunks. `entities` may be empty when no entity is found. This one call is performed for each retrieval request; in hybrid mode the resulting vector and graph searches then run concurrently.

## Local data services

Copy `.env.example` to `.env` and start the databases:

```powershell
Copy-Item .env.example .env
docker compose up -d
```

The Neo4j credentials in the example are for local development only. The `.env` file is used by Compose; Go does not load `.env` automatically. When running the Go server locally, set `NEO4J_PASSWORD` in the process environment as well. The retrieval server starts after the configured Qdrant collection and Neo4j are reachable.
