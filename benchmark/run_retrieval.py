"""Run labeled retrieval cases against a live retrieval-service API."""

import argparse
import json
import math
import statistics
import sys
import time
import urllib.error
import urllib.request
from datetime import datetime, timezone
from pathlib import Path, PurePosixPath
from typing import Any


ROOT = Path(__file__).resolve().parent
DATASETS = ROOT / "datasets"
STRATEGIES = ("vector", "graph_local", "hybrid")


def normalize_source(source: str) -> str:
    return PurePosixPath(source.replace("\\", "/")).as_posix().casefold()


def load_source_map(dataset: str) -> dict[str, str]:
    dataset_root = DATASETS / dataset
    corpus = dataset_root / "corpus"
    source_map = {}
    for path in corpus.rglob("*"):
        if not path.is_file() or path.name == "aliases.json":
            continue
        source = path.relative_to(corpus).as_posix()
        source_map[normalize_source(source)] = source
        source_map[normalize_source(PurePosixPath(source).with_suffix("").as_posix())] = source

    manifest_path = ROOT.parent / "ingestion-service" / "out" / "benchmark" / dataset / "manifest.json"
    if not manifest_path.is_file():
        return source_map
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    if manifest.get("dataset") != dataset or not isinstance(manifest.get("files"), list):
        raise ValueError(f"invalid prepared-corpus manifest: {manifest_path}")
    for entry in manifest["files"]:
        prepared_file = entry["prepared_file"]
        source_file = entry["source_file"]
        source_map[normalize_source(prepared_file)] = source_file
        document_id = PurePosixPath(prepared_file).with_suffix("").as_posix()
        source_map[normalize_source(document_id)] = source_file
    return source_map


def result_source(result: dict[str, Any], source_map: dict[str, str]) -> str:
    metadata = result.get("metadata") or {}
    source = metadata.get("source", "")
    if source:
        normalized = normalize_source(source)
        if normalized in source_map:
            return source_map[normalized]
    document_id = result.get("document_id", "")
    if document_id:
        normalized = normalize_source(document_id)
        if normalized in source_map:
            return source_map[normalized]
    return source


def fact_matches(expected: dict[str, str], actual: dict[str, Any]) -> bool:
    return (
        expected["source_entity"].casefold() == str(actual.get("source_entity", "")).casefold()
        and expected["relation_type"].casefold() == str(actual.get("relation_type", "")).casefold()
        and expected["target_entity"].casefold() == str(actual.get("target_entity", "")).casefold()
    )


def score_response(
    case: dict[str, Any],
    response: dict[str, Any],
    source_map: dict[str, str],
    strategy: str,
) -> dict[str, Any]:
    results = response.get("results")
    graph_facts = response.get("graph_facts")
    if not isinstance(results, list) or not isinstance(graph_facts, list):
        raise ValueError("search response must contain results and graph_facts arrays")

    gold_sources = {normalize_source(source) for source in case["gold_evidence"]}
    retrieved_sources = [
        normalize_source(result_source(result, source_map))
        for result in results
        if result_source(result, source_map)
    ]
    relevant_results = sum(source in gold_sources for source in retrieved_sources)
    evidence_precision = None
    evidence_recall = None
    if case["answerable"]:
        evidence_precision = relevant_results / len(results) if results else 0.0
        evidence_recall = (
            len(gold_sources.intersection(retrieved_sources)) / len(gold_sources)
            if gold_sources
            else 0.0
        )

    fact_recall = None
    required_facts = case["required_facts"]
    if strategy != "vector" and required_facts:
        result_sources_by_id = {
            result.get("id"): normalize_source(result_source(result, source_map))
            for result in results
            if result.get("id") and result_source(result, source_map)
        }
        found_facts = 0
        for expected in required_facts:
            evidence_source = normalize_source(expected["evidence_source"])
            found = any(
                fact_matches(expected, actual)
                and any(
                    result_sources_by_id.get(chunk_id) == evidence_source
                    for chunk_id in actual.get("source_chunk_ids", [])
                )
                for actual in graph_facts
            )
            found_facts += bool(found)
        fact_recall = found_facts / len(required_facts)

    return {
        "retrieved_sources": [result_source(result, source_map) for result in results],
        "result_count": len(results),
        "evidence_precision_at_k": evidence_precision,
        "evidence_recall_at_k": evidence_recall,
        "fact_recall": fact_recall,
        "partial": bool(response.get("partial", False)),
        "vector_status": response.get("vector_status"),
        "graph_status": response.get("graph_status"),
    }


def post_search(base_url: str, payload: dict[str, Any], timeout: float) -> dict[str, Any]:
    request = urllib.request.Request(
        base_url.rstrip("/") + "/v1/search",
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json", "Accept": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            value = json.load(response)
    except urllib.error.HTTPError as error:
        detail = error.read(4096).decode("utf-8", errors="replace").strip()
        raise RuntimeError(f"retrieval returned HTTP {error.code}: {detail}") from error
    except urllib.error.URLError as error:
        raise RuntimeError(f"could not reach retrieval service: {error.reason}") from error
    if not isinstance(value, dict):
        raise RuntimeError("retrieval response must be a JSON object")
    return value


def percentile95(values: list[float]) -> float | None:
    if not values:
        return None
    ordered = sorted(values)
    return ordered[max(0, math.ceil(0.95 * len(ordered)) - 1)]


def summarize(records: list[dict[str, Any]]) -> dict[str, Any]:
    summary = {}
    for strategy in STRATEGIES:
        strategy_records = [
            record for record in records
            if record["strategy"] == strategy
        ]
        successful = [record for record in strategy_records if "error" not in record]
        category_names = sorted({record["category"] for record in strategy_records})
        summary[strategy] = summarize_records(strategy_records, successful)
        summary[strategy]["by_category"] = {
            category: summarize_records(
                [record for record in strategy_records if record["category"] == category],
                [record for record in successful if record["category"] == category],
            )
            for category in category_names
        }
    return summary


def summarize_records(records: list[dict[str, Any]], successful: list[dict[str, Any]]) -> dict[str, Any]:
    latencies = [record["latency_ms"] for record in successful]
    return {
        "runs": len(successful),
        "errors": len(records) - len(successful),
        "partial_runs": sum(record["partial"] for record in successful),
        "median_latency_ms": statistics.median(latencies) if latencies else None,
        "p95_latency_ms": percentile95(latencies),
        "mean_evidence_precision_at_k": mean_metric(successful, "evidence_precision_at_k"),
        "mean_evidence_recall_at_k": mean_metric(successful, "evidence_recall_at_k"),
        "mean_fact_recall": mean_metric(successful, "fact_recall"),
    }


def mean_metric(records: list[dict[str, Any]], field: str) -> float | None:
    values = [record[field] for record in records if record[field] is not None]
    return statistics.mean(values) if values else None


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("dataset", help="dataset directory under benchmark/datasets")
    parser.add_argument("--split", choices=("dev", "test", "all"), default="test")
    parser.add_argument("--url", default="http://127.0.0.1:8080")
    parser.add_argument("--limit", type=int, default=10)
    parser.add_argument("--graph-depth", type=int, choices=(1, 2), default=2)
    parser.add_argument("--repeat", type=int, default=1)
    parser.add_argument("--timeout", type=float, default=120.0)
    parser.add_argument("--output-dir", type=Path)
    args = parser.parse_args()

    if args.limit < 1 or args.repeat < 1 or args.timeout <= 0:
        parser.error("--limit, --repeat, and --timeout must be positive")
    dataset = (DATASETS / args.dataset).resolve()
    if dataset.parent != DATASETS.resolve() or not dataset.is_dir():
        parser.error(f"unknown dataset {args.dataset!r}")

    query_path = dataset / "queries.jsonl"
    if not query_path.is_file():
        parser.error(f"dataset has no queries.jsonl: {query_path}")
    cases = [
        json.loads(line)
        for line in query_path.read_text(encoding="utf-8").splitlines()
        if line.strip()
    ]
    if args.split != "all":
        cases = [case for case in cases if case["split"] == args.split]
    if not cases:
        parser.error(f"no cases found for split {args.split!r}")

    source_map = load_source_map(args.dataset)
    records = []
    for case in cases:
        for strategy in STRATEGIES:
            for iteration in range(1, args.repeat + 1):
                payload = {
                    "query": case["query"],
                    "strategy": strategy,
                    "limit": args.limit,
                    "graph_depth": args.graph_depth,
                }
                started = time.perf_counter()
                record = {
                    "case_id": case["id"],
                    "split": case["split"],
                    "category": case["category"],
                    "strategy": strategy,
                    "iteration": iteration,
                }
                try:
                    response = post_search(args.url, payload, args.timeout)
                    record.update(score_response(case, response, source_map, strategy))
                except RuntimeError as error:
                    record["error"] = str(error)
                record["latency_ms"] = round((time.perf_counter() - started) * 1000, 3)
                records.append(record)
                print(
                    f"{case['id']} {strategy} run={iteration} "
                    f"latency_ms={record['latency_ms']} "
                    f"error={record.get('error', '')}"
                )

    timestamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    output_dir = args.output_dir or (
        ROOT.parent / "ingestion-service" / "out" / "benchmark-results"
    )
    output_dir.mkdir(parents=True, exist_ok=True)
    stem = f"{args.dataset}-{args.split}-{timestamp}"
    results_path = output_dir / f"{stem}.jsonl"
    summary_path = output_dir / f"{stem}.summary.json"
    if results_path.exists() or summary_path.exists():
        raise FileExistsError(f"benchmark output already exists for timestamp {timestamp}")

    summary = {
        "dataset": args.dataset,
        "split": args.split,
        "query_count": len(cases),
        "strategies": list(STRATEGIES),
        "limit": args.limit,
        "graph_depth": args.graph_depth,
        "repeat": args.repeat,
        "metrics": summarize(records),
    }
    results_path.write_text(
        "".join(json.dumps(record, ensure_ascii=False) + "\n" for record in records),
        encoding="utf-8",
    )
    summary_path.write_text(
        json.dumps(summary, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )
    print(f"Results: {results_path}")
    print(f"Summary: {summary_path}")
    print(json.dumps(summary["metrics"], indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
