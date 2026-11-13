package usecase

import (
	"encoding/hex"
	"fmt"
	"strings"

	"retrieval-service/internal/domain"
)

const insufficientDataText = "В доступных документах недостаточно данных для ответа."

// BuildGroundedAnswer accepts only verbatim facts supported by every referenced chunk.
// Link targets are built locally; model-provided Markdown is always escaped.
func BuildGroundedAnswer(draft domain.AnswerDraft, chunks []domain.SearchResult) domain.Answer {
	answer := domain.Answer{
		Status: domain.AnswerInsufficientData, Text: insufficientDataText,
		Facts: []domain.AnswerFact{}, Missing: []string{},
	}
	byID := make(map[string]domain.SearchResult, len(chunks))
	ambiguous := make(map[string]bool)
	for _, chunk := range chunks {
		if strings.TrimSpace(chunk.ID) == "" || strings.TrimSpace(chunk.Text) == "" {
			continue
		}
		if previous, exists := byID[chunk.ID]; exists && (previous.Text != chunk.Text || previous.DocumentID != chunk.DocumentID) {
			ambiguous[chunk.ID] = true
		}
		byID[chunk.ID] = chunk
	}
	rejected := false
	for _, fact := range draft.Facts {
		text := strings.TrimSpace(fact.Text)
		if text == "" || len(fact.ChunkIDs) == 0 {
			rejected = true
			continue
		}
		citations := make([]domain.Citation, 0, len(fact.ChunkIDs))
		seen := make(map[string]bool)
		valid := true
		for _, id := range fact.ChunkIDs {
			chunk, exists := byID[id]
			if !exists || ambiguous[id] || !strings.Contains(chunk.Text, text) {
				valid = false
				break
			}
			if !seen[id] {
				citations = append(citations, domain.Citation{
					ChunkID: id, DocumentID: chunk.DocumentID, Text: text,
					Href: "#chunk-" + hex.EncodeToString([]byte(id)),
				})
				seen[id] = true
			}
		}
		if !valid {
			rejected = true
			continue
		}
		answer.Facts = append(answer.Facts, domain.AnswerFact{Text: text, Citations: citations})
	}
	seenMissing := make(map[string]bool)
	for _, missing := range draft.Missing {
		missing = strings.TrimSpace(missing)
		if missing != "" && !seenMissing[missing] {
			answer.Missing = append(answer.Missing, missing)
			seenMissing[missing] = true
		}
	}
	if rejected {
		answer.Missing = append(answer.Missing, "Не удалось подтвердить часть ответа по доступным фрагментам.")
	}
	if len(answer.Facts) == 0 {
		return answer
	}
	answer.Status = domain.AnswerComplete
	if len(answer.Missing) > 0 {
		answer.Status = domain.AnswerPartial
	}
	paragraphs := make([]string, 0, len(answer.Facts)+len(answer.Missing))
	reference := 0
	for _, fact := range answer.Facts {
		paragraph := escapeMarkdown(fact.Text)
		for _, citation := range fact.Citations {
			reference++
			paragraph += fmt.Sprintf(" [%d](%s)", reference, citation.Href)
		}
		paragraphs = append(paragraphs, paragraph)
	}
	for _, missing := range answer.Missing {
		paragraphs = append(paragraphs, "Недостаточно данных: "+escapeMarkdown(missing))
	}
	answer.Text = strings.Join(paragraphs, "\n\n")
	return answer
}

func escapeMarkdown(text string) string {
	var escaped strings.Builder
	for _, r := range text {
		if r >= '!' && r <= '~' && strings.ContainsRune("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", r) {
			escaped.WriteByte('\\')
		}
		escaped.WriteRune(r)
	}
	return escaped.String()
}
