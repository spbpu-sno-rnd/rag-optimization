import unittest

from benchmark.run_retrieval import percentile95, score_response, summarize


class RetrievalScoringTests(unittest.TestCase):
    def test_scores_evidence_and_fact_coverage_after_source_mapping(self):
        case = {
            "answerable": True,
            "gold_evidence": ["contract.pdf"],
            "required_facts": [{
                "source_entity": "A",
                "relation_type": "signed",
                "target_entity": "B",
                "evidence_source": "contract.pdf",
            }],
        }
        response = {
            "results": [
                {"id": "chunk-1", "metadata": {"source": "contract.pdf.md"}},
                {"id": "chunk-2", "metadata": {"source": "irrelevant.md"}},
            ],
            "graph_facts": [{
                "source_entity": "A",
                "relation_type": "signed",
                "target_entity": "B",
                "source_chunk_ids": ["chunk-1"],
            }],
            "partial": False,
            "vector_status": "ok",
            "graph_status": "ok",
        }

        got = score_response(
            case,
            response,
            {"contract.pdf.md": "contract.pdf"},
            "hybrid",
        )

        self.assertEqual(got["evidence_precision_at_k"], 0.5)
        self.assertEqual(got["evidence_recall_at_k"], 1.0)
        self.assertEqual(got["fact_recall"], 1.0)

    def test_vector_does_not_claim_graph_fact_recall(self):
        case = {
            "answerable": True,
            "gold_evidence": ["source.md"],
            "required_facts": [{
                "source_entity": "A",
                "relation_type": "related_to",
                "target_entity": "B",
                "evidence_source": "source.md",
            }],
        }
        response = {
            "results": [{"id": "chunk-1", "metadata": {"source": "source.md"}}],
            "graph_facts": [],
        }

        got = score_response(case, response, {}, "vector")

        self.assertIsNone(got["fact_recall"])
        self.assertEqual(got["evidence_recall_at_k"], 1.0)

    def test_graph_results_map_document_id_when_metadata_is_absent(self):
        case = {
            "answerable": True,
            "gold_evidence": ["contract.pdf"],
            "required_facts": [{
                "source_entity": "A",
                "relation_type": "signed",
                "target_entity": "B",
                "evidence_source": "contract.pdf",
            }],
        }
        response = {
            "results": [{"id": "chunk-1", "document_id": "contract.pdf"}],
            "graph_facts": [{
                "source_entity": "A",
                "relation_type": "signed",
                "target_entity": "B",
                "source_chunk_ids": ["chunk-1"],
            }],
        }

        got = score_response(
            case,
            response,
            {"contract.pdf": "contract.pdf"},
            "graph_local",
        )

        self.assertEqual(got["evidence_recall_at_k"], 1.0)
        self.assertEqual(got["fact_recall"], 1.0)

    def test_percentile95_uses_nearest_rank(self):
        self.assertEqual(percentile95([1, 2, 3, 4, 5]), 5)
        self.assertIsNone(percentile95([]))

    def test_summary_excludes_failed_requests_from_latency_metrics(self):
        records = [
            {"strategy": "vector", "category": "single_hop", "latency_ms": 100, "partial": False,
             "evidence_precision_at_k": 1.0, "evidence_recall_at_k": 1.0, "fact_recall": None},
            {"strategy": "vector", "category": "single_hop", "error": "unavailable"},
        ]

        got = summarize(records)

        self.assertEqual(got["vector"]["runs"], 1)
        self.assertEqual(got["vector"]["errors"], 1)
        self.assertEqual(got["vector"]["median_latency_ms"], 100)
        self.assertEqual(got["vector"]["by_category"]["single_hop"]["runs"], 1)


if __name__ == "__main__":
    unittest.main()
