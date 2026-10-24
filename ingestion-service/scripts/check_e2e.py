"""Run after starting ingestion and retrieval against rebuilt local databases."""

import json
import os
import pathlib
import urllib.request


ROOT = pathlib.Path(__file__).resolve().parents[1]
CASES = json.loads((ROOT / "corpus" / "expected-queries.json").read_text(encoding="utf-8"))
INGESTION = os.getenv("INGESTION_URL", "http://127.0.0.1:8081")
RETRIEVAL = os.getenv("RETRIEVAL_URL", "http://127.0.0.1:8080")


def post(url, body):
    request = urllib.request.Request(
        url,
        data=json.dumps(body).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=90) as response:
        return json.load(response)


for case in CASES:
    context = post(INGESTION + "/v1/query-context", {"query": case["query"]})
    assert len(context["embedding"]) == 384, case
    assert set(case["expected_entities"]).issubset(context["entities"]), case
    search = post(RETRIEVAL + "/v1/search", {
        "query": case["query"],
        "strategy": case["strategy"],
        "limit": 10,
        "graph_depth": 2,
    })
    assert not search["partial"], (case, search)
    sources = {
        result.get("metadata", {}).get("source", "")
        for result in search["results"]
    }
    document_ids = {result.get("document_id", "") for result in search["results"]}
    expected = case["expected_source"]
    assert expected in sources or pathlib.Path(expected).stem in document_ids, (case, search)
    if "expected_relation" in case:
        expected_fact = case["expected_relation"]
        result_ids = {result["id"] for result in search["results"]}
        assert any(
            fact["source_entity"] == expected_fact["source_entity"]
            and fact["relation_type"] == expected_fact["relation_type"]
            and fact["target_entity"] == expected_fact["target_entity"]
            and fact["source_chunk_ids"]
            and set(fact["source_chunk_ids"]).issubset(result_ids)
            for fact in search["graph_facts"]
        ), (case, search)
    print("PASS", case["strategy"], case["query"])
