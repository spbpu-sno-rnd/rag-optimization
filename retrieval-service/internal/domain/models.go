package domain

type Source string

const (
	SourceVector Source = "vector"
	SourceGraph  Source = "graph"
)

type SearchResult struct {
	ID         string            `json:"id"`
	Text       string            `json:"text"`
	DocumentID string            `json:"document_id,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	Score      float64           `json:"score"`
	Sources    []Source          `json:"sources"`
}

type GraphFact struct {
	SourceEntity     string   `json:"source_entity"`
	RelationType     string   `json:"relation_type"`
	TargetEntity     string   `json:"target_entity"`
	Distance         int      `json:"distance"`
	EvidenceChunkIDs []string `json:"source_chunk_ids"`
}

type GraphSearchResults struct {
	Chunks []SearchResult
	Facts  []GraphFact
}

type SearchResponse struct {
	Results      []SearchResult `json:"results"`
	GraphFacts   []GraphFact    `json:"graph_facts"`
	Partial      bool           `json:"partial"`
	VectorStatus string         `json:"vector_status"`
	GraphStatus  string         `json:"graph_status"`
}
