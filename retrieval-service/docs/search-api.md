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
  ]
}
```

`score` is strategy-dependent: vector search returns the vector store score, graph search returns the graph adapter's score, and hybrid search returns the Reciprocal Rank Fusion score. Scores from different strategies are not directly comparable. `sources` identifies which retrieval paths contributed the result.

Errors are JSON objects of the form `{"error":"..."}`. Invalid requests return `400 Bad Request`; retrieval failures return `500 Internal Server Error`.

## `GET /healthz`

Returns `200 OK` and `{"status":"ok"}` when the HTTP process is serving requests. This is a liveness check, not a dependency-readiness check for Qdrant or Neo4j.
