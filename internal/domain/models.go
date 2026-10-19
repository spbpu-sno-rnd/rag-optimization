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
