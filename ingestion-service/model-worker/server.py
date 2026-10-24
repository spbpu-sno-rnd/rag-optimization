"""Local HTTP adapter for the pinned multilingual E5 model."""

import json
import os
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import torch
import torch.nn.functional as functional
from huggingface_hub import snapshot_download
from transformers import AutoModel, AutoTokenizer


MODEL_ID = "intfloat/multilingual-e5-small"
REVISION = "614241f622f53c4eeff9890bdc4f31cfecc418b3"
MAX_TOKENS = 512

model_path = snapshot_download(
    MODEL_ID,
    revision=REVISION,
    allow_patterns=[
        "config.json",
        "model.safetensors",
        "tokenizer.json",
        "tokenizer_config.json",
        "special_tokens_map.json",
        "sentencepiece.bpe.model",
    ],
    local_files_only=True,
)
tokenizer = AutoTokenizer.from_pretrained(model_path, local_files_only=True)
model = AutoModel.from_pretrained(model_path, local_files_only=True)
model.eval()
inference_lock = threading.Lock()


class Handler(BaseHTTPRequestHandler):
    def respond(self, status, payload):
        body = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        if self.path not in ("/count", "/embed"):
            self.respond(404, {"error": "not found"})
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if length < 1 or length > 1_048_576:
                raise ValueError("invalid body length")
            request = json.loads(self.rfile.read(length))
            text = request.get("text")
            if not isinstance(text, str) or not text.strip():
                raise ValueError("text must be nonempty")
            if self.path == "/embed":
                kind = request.get("kind")
                if kind not in ("query", "passage"):
                    raise ValueError("kind must be query or passage")
                text = kind + ": " + text
            tokens = tokenizer(text, return_tensors="pt", truncation=False)
            count = tokens["input_ids"].shape[1]
            if self.path == "/count":
                self.respond(200, {"tokens": count})
                return
            if count > MAX_TOKENS:
                raise ValueError("text exceeds 512 model tokens")
            with inference_lock, torch.inference_mode():
                output = model(**tokens).last_hidden_state
                mask = tokens["attention_mask"].unsqueeze(-1)
                embedding = (output * mask).sum(dim=1) / mask.sum(dim=1)
                embedding = functional.normalize(embedding, p=2, dim=1)
            vector = embedding[0].tolist()
            if len(vector) != 384:
                raise RuntimeError("model returned wrong embedding dimension")
            self.respond(200, {"embedding": vector})
        except (ValueError, KeyError, json.JSONDecodeError) as exc:
            self.respond(400, {"error": str(exc)})
        except Exception:
            self.respond(503, {"error": "model inference failed"})


if __name__ == "__main__":
    torch.set_num_threads(int(os.getenv("TORCH_NUM_THREADS", "2")))
    ThreadingHTTPServer(("127.0.0.1", int(os.getenv("MODEL_PORT", "8082"))), Handler).serve_forever()
