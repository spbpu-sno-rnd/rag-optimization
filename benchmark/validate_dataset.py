"""Audit benchmark annotations and ingestion compatibility without opening documents."""

import argparse
import json
import sys
from collections import Counter
from pathlib import Path


ROOT = Path(__file__).resolve().parent
DATASETS = ROOT / "datasets"
PREPARED_ROOT = ROOT.parent / "ingestion-service" / "out" / "benchmark"
SUPPORTED_EXTENSIONS = {".txt", ".md", ".markdown"}
CATEGORIES = {"single_hop", "multi_hop", "distractor", "unanswerable"}
SPLITS = {"dev", "test"}
FACT_FIELDS = {"source_entity", "relation_type", "target_entity", "evidence_source"}
QUERY_FIELDS = {
    "id",
    "split",
    "query",
    "category",
    "answerable",
    "expected_answer",
    "expected_entities",
    "gold_evidence",
    "required_facts",
}


class DatasetError(ValueError):
    pass


def fail(dataset: Path, line_number: int, message: str) -> None:
    raise DatasetError(f"{dataset.name}/queries.jsonl:{line_number}: {message}")


def load_aliases(dataset: Path) -> tuple[set[str], dict[str, str]]:
    aliases_path = dataset / "corpus" / "aliases.json"
    try:
        aliases = json.loads(aliases_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise DatasetError(f"{dataset.name}: cannot read corpus/aliases.json: {error}") from error
    if not isinstance(aliases, dict):
        raise DatasetError(f"{dataset.name}: aliases.json must contain a JSON object")

    canonical_entities = set()
    alias_to_canonical = {}
    for canonical, variants in aliases.items():
        if not isinstance(canonical, str) or not canonical.strip():
            raise DatasetError(f"{dataset.name}: alias map canonical names must be non-empty strings")
        if not isinstance(variants, list) or not all(
            isinstance(item, str) and item.strip() for item in variants
        ):
            raise DatasetError(f"{dataset.name}: aliases for {canonical!r} must be non-empty strings")
        canonical_entities.add(canonical)
        for variant in [canonical, *variants]:
            normalized = variant.casefold().strip()
            if normalized in alias_to_canonical and alias_to_canonical[normalized] != canonical:
                raise DatasetError(f"{dataset.name}: ambiguous alias {variant!r}")
            alias_to_canonical[normalized] = canonical
    return canonical_entities, alias_to_canonical


def validate_queries(
    dataset: Path,
    source_files: set[str],
    canonical_entities: set[str],
    alias_to_canonical: dict[str, str],
) -> tuple[int, Counter, Counter]:
    queries_path = dataset / "queries.jsonl"
    if not queries_path.is_file():
        raise DatasetError(f"{dataset.name}: queries.jsonl is missing")

    seen_ids = set()
    categories = Counter()
    split_categories = Counter()
    with queries_path.open(encoding="utf-8") as queries:
        for line_number, line in enumerate(queries, start=1):
            if not line.strip():
                fail(dataset, line_number, "blank lines are not allowed")
            try:
                case = json.loads(line)
            except json.JSONDecodeError as error:
                fail(dataset, line_number, f"invalid JSON: {error.msg}")
            if not isinstance(case, dict) or set(case) != QUERY_FIELDS:
                fail(dataset, line_number, f"fields must be exactly {sorted(QUERY_FIELDS)}")

            case_id = case["id"]
            if not isinstance(case_id, str) or not case_id.strip():
                fail(dataset, line_number, "id must be a non-empty string")
            if case_id in seen_ids:
                fail(dataset, line_number, f"duplicate id {case_id!r}")
            seen_ids.add(case_id)

            split = case["split"]
            if not isinstance(split, str) or split not in SPLITS:
                fail(dataset, line_number, f"unsupported split {split!r}")
            query_text = case["query"]
            if not isinstance(query_text, str) or not query_text.strip():
                fail(dataset, line_number, "query must be a non-empty string")
            category = case["category"]
            if not isinstance(category, str) or category not in CATEGORIES:
                fail(dataset, line_number, f"unsupported category {category!r}")
            categories[category] += 1
            split_categories[(split, category)] += 1

            if not isinstance(case["answerable"], bool):
                fail(dataset, line_number, "answerable must be a boolean")
            evidence = case["gold_evidence"]
            facts = case["required_facts"]
            entities = case["expected_entities"]
            if not isinstance(evidence, list) or not all(
                isinstance(item, str) and item for item in evidence
            ):
                fail(dataset, line_number, "gold_evidence must be a list of source filenames")
            if len(evidence) != len(set(evidence)):
                fail(dataset, line_number, "gold_evidence must not contain duplicates")
            missing_sources = set(evidence) - source_files
            if missing_sources:
                fail(dataset, line_number, f"gold evidence files not in corpus: {sorted(missing_sources)}")

            if not isinstance(entities, list) or not all(
                isinstance(item, str) and item for item in entities
            ):
                fail(dataset, line_number, "expected_entities must be a list of non-empty canonical names")
            missing_entities = set(entities) - canonical_entities
            if missing_entities:
                fail(dataset, line_number, f"entities missing from aliases: {sorted(missing_entities)}")
            query_folded = query_text.casefold()
            unmentioned = [
                entity
                for entity in entities
                if not any(
                    query_folded.find(alias) >= 0
                    for alias, canonical in alias_to_canonical.items()
                    if canonical == entity
                )
            ]
            if unmentioned:
                fail(dataset, line_number, f"expected entities not mentioned in query: {unmentioned}")

            if not isinstance(facts, list):
                fail(dataset, line_number, "required_facts must be a list")
            for fact in facts:
                if not isinstance(fact, dict) or set(fact) != FACT_FIELDS:
                    fail(dataset, line_number, f"each fact must have fields {sorted(FACT_FIELDS)}")
                if any(not isinstance(fact[field], str) or not fact[field].strip() for field in FACT_FIELDS):
                    fail(dataset, line_number, "required fact fields must be non-empty strings")
                if fact["evidence_source"] not in evidence:
                    fail(dataset, line_number, "fact evidence_source must also appear in gold_evidence")
                if fact["source_entity"] not in canonical_entities or fact["target_entity"] not in canonical_entities:
                    fail(dataset, line_number, "fact endpoints must use canonical names from aliases.json")

            if case["answerable"]:
                if not isinstance(case["expected_answer"], str) or not case["expected_answer"].strip():
                    fail(dataset, line_number, "answerable case must have expected_answer")
                if not evidence:
                    fail(dataset, line_number, "answerable case must have gold_evidence")
            elif case["expected_answer"] is not None or evidence or facts:
                fail(dataset, line_number, "unanswerable case must have null answer and no gold evidence/facts")

    if not seen_ids:
        raise DatasetError(f"{dataset.name}: queries.jsonl is empty")
    return len(seen_ids), categories, split_categories


def validate_prepared_manifest(dataset: Path, source_paths: list[Path]) -> int | None:
    prepared = PREPARED_ROOT / dataset.name
    manifest_path = prepared / "manifest.json"
    if not manifest_path.is_file():
        return None
    try:
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise DatasetError(f"{dataset.name}: cannot read prepared manifest: {error}") from error
    if not isinstance(manifest, dict) or manifest.get("dataset") != dataset.name:
        raise DatasetError(f"{dataset.name}: prepared manifest has an invalid dataset id")
    entries = manifest.get("files")
    if not isinstance(entries, list):
        raise DatasetError(f"{dataset.name}: prepared manifest files must be a list")

    expected_sources = {
        path.relative_to(dataset / "corpus").as_posix() for path in source_paths
    }
    mapped_sources = set()
    prepared_files = set()
    output_corpus = prepared / "corpus"
    prepared_aliases = output_corpus / "aliases.json"
    if not prepared_aliases.is_file():
        raise DatasetError(f"{dataset.name}: prepared corpus is missing aliases.json")
    try:
        source_aliases = json.loads((dataset / "corpus" / "aliases.json").read_text(encoding="utf-8"))
        output_aliases = json.loads(prepared_aliases.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise DatasetError(f"{dataset.name}: cannot validate prepared aliases: {error}") from error
    if output_aliases != source_aliases:
        raise DatasetError(f"{dataset.name}: prepared aliases.json is stale")
    for entry in entries:
        if not isinstance(entry, dict):
            raise DatasetError(f"{dataset.name}: malformed prepared manifest entry")
        source_file = entry.get("source_file")
        prepared_file = entry.get("prepared_file")
        if not isinstance(source_file, str) or not isinstance(prepared_file, str):
            raise DatasetError(f"{dataset.name}: manifest entries need source_file and prepared_file")
        if source_file in mapped_sources:
            raise DatasetError(f"{dataset.name}: duplicate source mapping for {source_file!r}")
        mapped_sources.add(source_file)
        relative_output = Path(prepared_file)
        if relative_output.is_absolute() or ".." in relative_output.parts:
            raise DatasetError(f"{dataset.name}: unsafe prepared_file path {prepared_file!r}")
        output_path = output_corpus / relative_output
        if not output_path.is_file() or output_path.suffix.lower() not in SUPPORTED_EXTENSIONS:
            raise DatasetError(f"{dataset.name}: missing or unsupported prepared file {prepared_file!r}")
        prepared_files.add(relative_output.as_posix())

    if mapped_sources != expected_sources:
        missing = sorted(expected_sources - mapped_sources)
        unknown = sorted(mapped_sources - expected_sources)
        raise DatasetError(f"{dataset.name}: manifest source mismatch; missing={missing}, unknown={unknown}")
    if len(prepared_files) != len(entries):
        raise DatasetError(f"{dataset.name}: prepared files must have unique manifest paths")
    return len(entries)


def audit_dataset(dataset: Path) -> bool:
    corpus = dataset / "corpus"
    if not corpus.is_dir():
        raise DatasetError(f"{dataset.name}: corpus directory is missing")

    files = [
        path for path in corpus.rglob("*")
        if path.is_file() and path.name != "aliases.json"
    ]
    source_files = {path.name for path in files}
    if not source_files:
        raise DatasetError(f"{dataset.name}: corpus has no source documents")

    extensions = Counter(path.suffix.lower() or "[no extension]" for path in files)
    unsupported = sorted(
        extension for extension in extensions if extension not in SUPPORTED_EXTENSIONS
    )
    print(f"{dataset.name}: corpus files={len(files)}, formats={dict(sorted(extensions.items()))}")
    prepared_count = validate_prepared_manifest(dataset, files)
    if prepared_count is not None:
        print(f"  prepared corpus READY ({prepared_count} source mappings)")
    elif unsupported:
        print(
            "  ingestion BLOCKED: unsupported formats "
            f"{ {extension: extensions[extension] for extension in unsupported} }"
        )
    else:
        print("  ingestion READY: every source file is a supported text format")

    canonical_entities, alias_to_canonical = load_aliases(dataset)
    query_count, categories, split_categories = validate_queries(
        dataset, source_files, canonical_entities, alias_to_canonical
    )
    split_counts = Counter(split for split, _ in split_categories.elements())

    print(
        f"  annotations PASS ({query_count} queries, "
        f"categories={dict(sorted(categories.items()))}, "
        f"splits={dict(sorted(split_counts.items()))})"
    )
    return not unsupported or prepared_count is not None


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dataset", help="audit one directory under benchmark/datasets")
    args = parser.parse_args()

    if args.dataset:
        datasets = [DATASETS / args.dataset]
        if not datasets[0].is_dir():
            parser.error(f"unknown dataset {args.dataset!r}")
    else:
        datasets = sorted(path for path in DATASETS.iterdir() if path.is_dir())
    if not datasets:
        parser.error(f"no dataset directories found under {DATASETS}")

    failures = []
    blocked = []
    for dataset in datasets:
        try:
            ingestion_ready = audit_dataset(dataset)
            if not ingestion_ready:
                blocked.append(dataset.name)
        except DatasetError as error:
            print(f"FAIL {error}", file=sys.stderr)
            failures.append(dataset.name)

    if failures or blocked:
        if blocked:
            print(f"Needs preprocessing: {', '.join(blocked)}")
        if failures:
            print(f"Needs annotation fixes: {', '.join(failures)}")
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
