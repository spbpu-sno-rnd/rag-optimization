"""Validate benchmark corpus references and gold labels without external packages."""

import json
from pathlib import Path


ROOT = Path(__file__).resolve().parent
CORPUS = ROOT / "corpus"
DATASET = ROOT / "queries.jsonl"
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


def fail(line_number: int, message: str) -> None:
    raise ValueError(f"{DATASET.name}:{line_number}: {message}")


def main() -> None:
    corpus_sources = {
        path.name
        for path in CORPUS.iterdir()
        if path.is_file() and path.suffix.lower() in {".md", ".txt", ".markdown"}
    }
    if not corpus_sources:
        raise ValueError(f"no Markdown or text documents found in {CORPUS}")

    aliases = json.loads((CORPUS / "aliases.json").read_text(encoding="utf-8"))
    if not isinstance(aliases, dict):
        raise ValueError(f"{CORPUS / 'aliases.json'} must contain a JSON object")
    canonical_entities = set(aliases)
    alias_to_canonical = {}
    for canonical, variants in aliases.items():
        if not isinstance(canonical, str) or not canonical.strip():
            raise ValueError("alias map canonical names must be non-empty strings")
        if not isinstance(variants, list) or not all(isinstance(item, str) and item.strip() for item in variants):
            raise ValueError(f"aliases for {canonical!r} must be a list of non-empty strings")
        for variant in [canonical, *variants]:
            normalized = variant.casefold().strip()
            if normalized in alias_to_canonical and alias_to_canonical[normalized] != canonical:
                raise ValueError(f"ambiguous alias {variant!r}")
            alias_to_canonical[normalized] = canonical
    seen_ids: set[str] = set()
    category_counts = {category: 0 for category in sorted(CATEGORIES)}
    split_category_counts = {
        split: {category: 0 for category in sorted(CATEGORIES)} for split in SPLITS
    }
    answerable_count = 0

    with DATASET.open(encoding="utf-8") as dataset:
        for line_number, line in enumerate(dataset, start=1):
            if not line.strip():
                fail(line_number, "blank lines are not allowed")
            try:
                case = json.loads(line)
            except json.JSONDecodeError as error:
                fail(line_number, f"invalid JSON: {error.msg}")
            if not isinstance(case, dict):
                fail(line_number, "each line must contain a JSON object")
            if set(case) != QUERY_FIELDS:
                fail(line_number, f"fields must be exactly {sorted(QUERY_FIELDS)}")

            case_id = case["id"]
            if not isinstance(case_id, str) or not case_id.strip():
                fail(line_number, "id must be a non-empty string")
            if case_id in seen_ids:
                fail(line_number, f"duplicate id {case_id!r}")
            seen_ids.add(case_id)

            split = case["split"]
            if not isinstance(split, str) or split not in SPLITS:
                fail(line_number, f"unsupported split {split!r}")
            if not isinstance(case["query"], str) or not case["query"].strip():
                fail(line_number, "query must be a non-empty string")
            category = case["category"]
            if not isinstance(category, str) or category not in CATEGORIES:
                fail(line_number, f"unsupported category {category!r}")
            category_counts[category] += 1
            split_category_counts[split][category] += 1

            if not isinstance(case["answerable"], bool):
                fail(line_number, "answerable must be a boolean")
            evidence = case["gold_evidence"]
            facts = case["required_facts"]
            entities = case["expected_entities"]
            if not isinstance(evidence, list) or not all(isinstance(item, str) for item in evidence):
                fail(line_number, "gold_evidence must be a list of source filenames")
            if len(evidence) != len(set(evidence)):
                fail(line_number, "gold_evidence must not contain duplicates")
            missing_sources = set(evidence) - corpus_sources
            if missing_sources:
                fail(line_number, f"gold evidence sources not in corpus: {sorted(missing_sources)}")
            if not isinstance(entities, list) or not all(isinstance(item, str) and item for item in entities):
                fail(line_number, "expected_entities must be a list of non-empty canonical names")
            if set(entities) - canonical_entities:
                fail(line_number, f"expected_entities missing from aliases: {sorted(set(entities) - canonical_entities)}")
            query_text = case["query"].casefold()
            missing_query_entities = [
                entity
                for entity in entities
                if not any(
                    query_text.find(alias) >= 0
                    for alias, canonical in alias_to_canonical.items()
                    if canonical == entity
                )
            ]
            if missing_query_entities:
                fail(
                    line_number,
                    f"expected_entities must be mentioned in query via a known alias: {missing_query_entities}",
                )
            if not isinstance(facts, list):
                fail(line_number, "required_facts must be a list")
            for fact in facts:
                if not isinstance(fact, dict) or set(fact) != FACT_FIELDS:
                    fail(line_number, f"each required fact must have fields {sorted(FACT_FIELDS)}")
                if any(not isinstance(fact[field], str) or not fact[field].strip() for field in FACT_FIELDS):
                    fail(line_number, "required fact fields must be non-empty strings")
                if fact["evidence_source"] not in evidence:
                    fail(line_number, f"fact evidence {fact['evidence_source']!r} must be listed in gold_evidence")
                if fact["evidence_source"] not in corpus_sources:
                    fail(line_number, f"fact evidence source not in corpus: {fact['evidence_source']!r}")
                if fact["source_entity"] not in canonical_entities or fact["target_entity"] not in canonical_entities:
                    fail(line_number, "required fact entities must use canonical names from aliases.json")

            if case["answerable"]:
                answerable_count += 1
                if not isinstance(case["expected_answer"], str) or not case["expected_answer"].strip():
                    fail(line_number, "answerable case must have expected_answer")
                if not evidence:
                    fail(line_number, "answerable case must have at least one gold evidence source")
            elif case["expected_answer"] is not None or evidence or facts:
                fail(line_number, "unanswerable case must have null answer and no gold evidence or facts")

    if not seen_ids:
        raise ValueError(f"no benchmark cases found in {DATASET}")
    missing_categories = [category for category, count in category_counts.items() if not count]
    if missing_categories:
        raise ValueError(f"dataset is missing benchmark categories: {missing_categories}")
    for split, counts in split_category_counts.items():
        missing_categories = [category for category, count in counts.items() if not count]
        if missing_categories:
            raise ValueError(f"{split} split is missing benchmark categories: {missing_categories}")

    print(
        f"PASS {len(seen_ids)} cases ({answerable_count} answerable), "
        f"{len(corpus_sources)} corpus documents; categories={category_counts}; "
        f"splits={ {split: sum(counts.values()) for split, counts in split_category_counts.items()} }"
    )


if __name__ == "__main__":
    main()
