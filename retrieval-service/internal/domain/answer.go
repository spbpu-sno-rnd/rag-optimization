package domain

type AnswerStatus string

const (
	AnswerComplete         AnswerStatus = "complete"
	AnswerPartial          AnswerStatus = "partial"
	AnswerInsufficientData AnswerStatus = "insufficient_data"
	AnswerError            AnswerStatus = "error"
)

type Answer struct {
	Status  AnswerStatus `json:"status"`
	Text    string       `json:"text"`
	Facts   []AnswerFact `json:"facts"`
	Missing []string     `json:"missing"`
}

type AnswerFact struct {
	Text      string     `json:"text"`
	Citations []Citation `json:"citations"`
}

type Citation struct {
	ChunkID    string `json:"chunk_id"`
	DocumentID string `json:"document_id,omitempty"`
	Text       string `json:"text"`
	Href       string `json:"href"`
}

// AnswerDraft is untrusted model output; it must be checked against the request's chunks.
type AnswerDraft struct {
	Facts   []DraftFact `json:"facts"`
	Missing []string    `json:"missing"`
}

type DraftFact struct {
	Text     string   `json:"text"`
	ChunkIDs []string `json:"chunk_ids"`
}
