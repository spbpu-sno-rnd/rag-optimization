# Search API

The retrieval service exposes a JSON REST API over `net/http`.

## `POST /v1/search`

Request:

```json
{
  "query": "How did Acme's 2023 launch affect its supplier?",
  "strategy": "hybrid",
  "limit": 5,
  "graph_depth": 2
}
```

Fields:

| Field | Type | Required | Default | Notes |
|---|---|---:|---:|---|
| `query` | string | yes | — | Non-empty search text |
| `strategy` | string | no | `hybrid` | One of `vector`, `graph_local`, `hybrid` |
| `limit` | integer | no | `5` | Result count from 1 to 100 |
| `graph_depth` | integer | no | `1` | Graph neighborhood depth, either 1 or 2; used by graph and hybrid search |

The request body must be one JSON object no larger than 1 MiB. Unknown fields are rejected.

Successful response (`200 OK`):

```json
{
  "results": [
    {
      "id": "chunk-42",
      "text": "Acme launched ...",
      "document_id": "annual-report-2023",
      "metadata": {
        "year": "2023",
        "source": "annual-report.pdf"
      },
      "score": 0.0325,
      "sources": ["vector", "graph"]
    }
  ],
  "graph_facts": [
    {
      "source_entity": "Acme",
      "relation_type": "partner_of",
      "target_entity": "Supplier Y",
      "distance": 1,
      "source_chunk_ids": ["chunk-42"]
    }
  ],
  "partial": false,
  "vector_status": "ok",
  "graph_status": "ok"
}
```

`results` contains retrieved chunks. Graph search includes only evidence chunks referenced by `source_chunk_ids` on the traversed relationships; hybrid results fuse these with vector results by chunk ID. `graph_facts` contains the traversed, evidence-backed relationships and their supporting chunk IDs. `distance` is the shortest number of graph hops from a matched query entity to that relationship.

`score` is strategy-dependent: vector search returns the vector store score, graph search ranks evidence by relationship distance, and hybrid search returns the Reciprocal Rank Fusion score. Scores from different strategies are not directly comparable. `sources` identifies which retrieval paths contributed the result.

`vector_status` and `graph_status` are `ok`, `error`, or `skipped`. Hybrid search returns available results with `partial: true` and logs a warning if one retrieval source fails; if both sources fail, it returns `500`. A graph search is `skipped` when query processing finds no entities. For non-hybrid strategies, the unused source is `skipped`.

Errors are JSON objects of the form `{"error":"..."}`. Invalid requests return `400 Bad Request`; retrieval failures return `500 Internal Server Error`.

## `GET /healthz`

Returns `200 OK` and `{"status":"ok"}` when the HTTP process is serving requests. This is a liveness check, not a dependency-readiness check for Qdrant or Neo4j.
