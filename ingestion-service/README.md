# Ingestion service

This Go service rebuilds the indexes consumed by `retrieval-service` and serves `POST /v1/query-context` on port 8081. It reads UTF-8 `.txt`, `.md`, and `.markdown` files. The output JSONL follows [the shared data contract](../retrieval-service/docs/data-contract.md). The first version runs a full rebuild while retrieval is stopped.

## Dependencies

- Go 1.26 or newer.
- Python 3.12, with packages from `model-worker/requirements.txt`.
- Hugging Face `hf` CLI, installed by `model-worker/requirements.txt`. The model download is pinned to `intfloat/multilingual-e5-small` commit `614241f622f53c4eeff9890bdc4f31cfecc418b3`.
- Ollama with `qwen2.5:3b` for entity and directed relation extraction. The extraction model can be changed with `OLLAMA_MODEL`. The model returns structured JSON; Go rejects unsupported relation endpoints and resolves only aliases declared in `corpus/aliases.json`. The small test corpus is English; extraction quality on other languages has not been established.
- Docker Desktop with Compose to run Qdrant 1.13.6 and Neo4j 5.26 from `retrieval-service/docker-compose.yml`.

From this directory, install the Python packages in a virtual environment, then download the exact E5 revision into the Hugging Face cache:

```powershell
py -3.12 -m venv .venv
.\.venv\Scripts\python -m pip install -r model-worker\requirements.txt
.\.venv\Scripts\hf download intfloat/multilingual-e5-small config.json model.safetensors tokenizer.json tokenizer_config.json special_tokens_map.json sentencepiece.bpe.model --revision 614241f622f53c4eeff9890bdc4f31cfecc418b3
```

Set `HF_HOME` before both `hf download` and model worker startup if you want a non-default cache. The worker loads this revision from the local cache only, with no runtime model download. Do not commit downloaded model files.

Start Qdrant and Neo4j from the retrieval directory:

```powershell
cd ..\retrieval-service
Copy-Item .env.example .env
docker compose up -d
```

Start Ollama in a separate terminal, or use its background app, then pull the extraction model:

```powershell
ollama serve
```

```powershell
ollama pull qwen2.5:3b
```

If Ollama already runs as a background service, skip `ollama serve`. Set the Neo4j password in the Go process environment. Secrets such as `NEO4J_PASSWORD` and `QDRANT_API_KEY` are read only from environment variables. The `.env` file is for Compose and is ignored by Git.

```powershell
cd ..\ingestion-service
$env:NEO4J_PASSWORD = 'local-dev-password'
.\.venv\Scripts\python model-worker\server.py
```

In another terminal, check the loaded E5 model with `.\.venv\Scripts\python scripts\check_model.py`.

In another terminal with the same `NEO4J_PASSWORD` value, rebuild while retrieval is **stopped**:

```powershell
cd ingestion-service
$env:NEO4J_PASSWORD = 'local-dev-password'
go run . rebuild --input corpus out/chunks.jsonl
go run . serve
```

The rebuild first prepares all chunks, embeddings and relations, validates evidence IDs, and writes the JSONL. It then checks that the retrieval TCP port is closed, recreates the 384-dimensional cosine Qdrant collection, clears the ingestion graph labels, and reloads both stores. A failed rebuild can leave partial indexes; keep retrieval stopped and run the rebuild again. The graph clear affects `Entity`, `Document` and `Chunk` nodes in the configured Neo4j database, so use a dedicated database.

Finally start retrieval in a separate terminal:

```powershell
cd retrieval-service
$env:NEO4J_PASSWORD = 'local-dev-password'
$env:INGESTION_TIMEOUT = '50s'
go run .\cmd\server
```

Run the corpus checks with both services running:

```powershell
cd ingestion-service
.\.venv\Scripts\python scripts\check_e2e.py
go test ./...
go test -tags integration ./...
```

The corpus cases exercise `vector`, `graph_local`, and `hybrid` searches. Results from the extraction model can vary, so inspect any failed expectation against the returned graph facts and source chunks before changing the corpus.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `INGESTION_ADDR` | `:8081` | Query-context listener |
| `MODEL_URL` | `http://127.0.0.1:8082` | Local E5 worker |
| `MODEL_PORT` | `8082` | E5 worker listen port |
| `HF_HOME` | Hugging Face default | Shared model cache |
| `TORCH_NUM_THREADS` | `2` | CPU inference threads |
| `OLLAMA_URL` | `http://127.0.0.1:11434` | Ollama endpoint |
| `OLLAMA_MODEL` | `qwen2.5:3b` | Extraction model |
| `CHUNK_MAX_TOKENS` | `384` | Maximum total tokenizer tokens including `passage: ` and special tokens; range 16–512 |
| `MAX_MODEL_REQUESTS` | `2` | Maximum simultaneous model HTTP requests; range 1–32 |
| `ALIASES_FILE` | `corpus/aliases.json` | Explicit canonical names and aliases |
| `QDRANT_URL` | `http://127.0.0.1:6333` | Qdrant REST endpoint |
| `QDRANT_COLLECTION` | `documents` | Rebuilt collection |
| `QDRANT_API_KEY` | unset | Optional Qdrant secret |
| `NEO4J_URI` | `bolt://127.0.0.1:7687` | Neo4j Bolt endpoint |
| `NEO4J_USERNAME` | `neo4j` | Neo4j user |
| `NEO4J_PASSWORD` | required | Neo4j secret |
| `NEO4J_DATABASE` | `neo4j` | Dedicated graph database |
| `RETRIEVAL_URL` | `http://127.0.0.1:8080` | Search stop check and end-to-end script |

`POST /v1/query-context` accepts one JSON object `{"query":"..."}` (up to 1 MiB) and returns `{"embedding":[384 numbers],"entities":[...]}`. Bad requests receive `400`. Embedding, tokenizer, or entity extraction failures receive `503`; successful extraction with no entities returns `[]`. `GET /healthz` is a process liveness check.

The model worker uses mean pooling over attention-masked token embeddings and L2 normalization. Both query and passage vectors come from the same pinned revision. Qdrant also normalizes vectors for cosine search.
