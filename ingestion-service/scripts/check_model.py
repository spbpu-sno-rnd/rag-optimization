"""Smoke check the real pinned E5 worker before indexing."""

import json
import math
import os
import urllib.error
import urllib.request


BASE = os.getenv("MODEL_URL", "http://127.0.0.1:8082")


def post(path, payload):
    request = urllib.request.Request(
        BASE + path,
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=90) as response:
        return json.load(response)


text = "Acme partnered with Northstar Supplies."
count = post("/count", {"text": "passage: " + text})["tokens"]
assert 1 < count <= 512, count
passage = post("/embed", {"text": text, "kind": "passage"})["embedding"]
query = post("/embed", {"text": text, "kind": "query"})["embedding"]
assert len(passage) == len(query) == 384
assert math.isclose(math.sqrt(sum(x * x for x in passage)), 1, abs_tol=1e-5)
assert math.isclose(math.sqrt(sum(x * x for x in query)), 1, abs_tol=1e-5)
assert passage != query, "query and passage prefixes were not applied"
try:
    post("/embed", {"text": "word " * 600, "kind": "passage"})
except urllib.error.HTTPError as exc:
    assert exc.code == 400, exc.code
else:
    raise AssertionError("overlong input was accepted")
print("PASS pinned E5 model: token count, 384 dimensions, normalization, prefixes, length limit")
