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
| `generate_answer` | boolean | no | `false` | Add an extractive answer with validated citations to the returned chunks |

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

`results` contains retrieved chunks. Graph search includes only evidence chunks referenced by `source_chunk_ids` on the traversed relationships; hybrid results fuse these with vector results by chunk ID. In hybrid responses, `graph_facts` only includes facts with at least one supporting chunk in the final `results`, and each fact's `source_chunk_ids` is limited to those returned chunks. `distance` is the shortest number of graph hops from a matched query entity to that relationship.

`score` is strategy-dependent: vector search returns the vector store score, graph search ranks evidence by relationship distance, and hybrid search returns the Reciprocal Rank Fusion score. Scores from different strategies are not directly comparable. `sources` identifies which retrieval paths contributed the result.

`vector_status` and `graph_status` are `ok`, `error`, or `skipped`. Hybrid search returns available results with `partial: true` and logs a warning if one retrieval source fails; if both sources fail, it returns `500`. A graph search is `skipped` when query processing finds no entities. For non-hybrid strategies, the unused source is `skipped`.

Errors are JSON objects of the form `{"error":"..."}`. Invalid requests return `400 Bad Request`; retrieval failures return `500 Internal Server Error`.

### Optional grounded answer

Set `"generate_answer": true` on the same request to add `answer`. Requests that omit the flag or set it to `false` keep the original response format and do not call the answer model. Query preparation in ingestion still uses its existing model dependencies.

```json
{
  "query": "Who supplied processors for Orbit?",
  "strategy": "vector",
  "limit": 3,
  "generate_answer": true
}
```

In addition to the retrieval fields above, the response can contain:

```json
{
  "answer": {
    "status": "complete",
    "text": "Atlas supplied processors for Orbit\\. [1](#chunk-61)",
    "facts": [
      {
        "text": "Atlas supplied processors for Orbit.",
        "citations": [
          {
            "chunk_id": "a",
            "document_id": "atlas",
            "text": "Atlas supplied processors for Orbit.",
            "href": "#chunk-61"
          }
        ]
      }
    ],
    "missing": []
  }
}
```

This example assumes `results` contains chunk `a` with the quoted text. `answer.text` is escaped Markdown with whitespace normalized for display; `facts[].text` and `citations[].text` retain the verbatim plain text. Every accepted fact is a verbatim excerpt of every chunk it cites, and every citation ID belongs to the final context sent to the model. Model-generated URLs and Markdown are not used. `href` is an internal fragment, not a new HTTP endpoint: a client rendering the results must assign each source the anchor `chunk-` followed by lowercase hex of the UTF-8 bytes of its `id`, and render source text as plain text. For example, ID `a` maps to `chunk-61`. The original full text remains available in `results`.

| `answer.status` | Meaning |
|---|---|
| `complete` | At least one validated fact; the model reported no missing information and no draft facts were rejected |
| `partial` | Confirmed facts remain, but the model reported missing information or some draft facts failed evidence validation |
| `insufficient_data` | No validated facts, including empty retrieval; text says the available documents contain insufficient data |
| `error` | No retrieval path could successfully check evidence, or the answer model failed, timed out, returned malformed output or did not finish normally; available retrieved results remain accessible |

All four statuses return `200` when retrieval returns a response. `facts` and `missing` are arrays, including when empty. A `missing` item is a verbatim unanswered excerpt of the question or a fixed server diagnostic; it never contains an unchecked model-written explanation. Errors raised by retrieval retain the existing `400`/`500` behavior. Answer generation does not change retrieval's `partial`, `vector_status`, `graph_status`, scores, or results. An answer can be `complete` while retrieval is `partial`; completeness reflects the accepted draft, not proof that every relevant document was found.

The MVP selects quotations rather than synthesizing or translating claims. Relevance, coverage of all question parts, and misleading quotations taken out of context still require real-model evaluation. See [setup, demo, architecture and limitations](grounded-answers.md).

## `GET /healthz`

Returns `200 OK` and `{"status":"ok"}` when the HTTP process is serving requests. This is a liveness check, not a dependency-readiness check for Qdrant or Neo4j.
