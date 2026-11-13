package usecase

import (
	"strings"
	"testing"

	"retrieval-service/internal/domain"
)

func TestBuildGroundedAnswerCitesRetrievedEvidence(t *testing.T) {
	chunks := []domain.SearchResult{
		{ID: "a", DocumentID: "atlas", Text: "Atlas supplied processors for Orbit."},
		{ID: "b", DocumentID: "cedar", Text: "Cedar supplied protective casings for Aurora."},
	}
	answer := BuildGroundedAnswer("", domain.AnswerDraft{Facts: []domain.DraftFact{
		{Text: chunks[0].Text, ChunkIDs: []string{"a", "a"}},
		{Text: chunks[1].Text, ChunkIDs: []string{"b"}},
	}}, chunks)
	if answer.Status != domain.AnswerComplete || len(answer.Facts) != 2 || len(answer.Missing) != 0 {
		t.Fatalf("answer = %+v", answer)
	}
	if len(answer.Facts[0].Citations) != 1 || answer.Facts[0].Citations[0].DocumentID != "atlas" || answer.Facts[0].Citations[0].Text != chunks[0].Text {
		t.Fatalf("citation = %+v", answer.Facts[0])
	}
	if answer.Text != `Atlas supplied processors for Orbit\. [1](#chunk-61)`+"\n\n"+`Cedar supplied protective casings for Aurora\. [2](#chunk-62)` {
		t.Fatalf("rendered answer = %q", answer.Text)
	}
}

func TestBuildGroundedAnswerRejectsUnverifiedFacts(t *testing.T) {
	for _, test := range []struct {
		name string
		fact domain.DraftFact
	}{
		{"unknown ID", domain.DraftFact{Text: "Atlas supplied processors.", ChunkIDs: []string{"foreign"}}},
		{"one unknown citation", domain.DraftFact{Text: "Atlas supplied processors.", ChunkIDs: []string{"a", "foreign"}}},
		{"invented text", domain.DraftFact{Text: "Cedar supplied processors.", ChunkIDs: []string{"a"}}},
		{"missing citation", domain.DraftFact{Text: "Atlas supplied processors."}},
		{"empty text", domain.DraftFact{Text: " \n", ChunkIDs: []string{"a"}}},
		{"empty ID", domain.DraftFact{Text: "Atlas supplied processors.", ChunkIDs: []string{""}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			answer := BuildGroundedAnswer("", domain.AnswerDraft{Facts: []domain.DraftFact{test.fact}}, []domain.SearchResult{{ID: "a", Text: "Atlas supplied processors."}})
			if answer.Status != domain.AnswerInsufficientData || len(answer.Facts) != 0 || answer.Facts == nil || answer.Missing == nil || !strings.Contains(answer.Text, "недостаточно данных") {
				t.Fatalf("unsafe answer = %+v", answer)
			}
		})
	}
}

func TestBuildGroundedAnswerPreservesConfirmedPart(t *testing.T) {
	for _, test := range []struct {
		name    string
		draft   domain.AnswerDraft
		missing string
	}{
		{"missing data", domain.AnswerDraft{Facts: []domain.DraftFact{{Text: "Aurora launched in 2023.", ChunkIDs: []string{"a"}}}, Missing: []string{"Production volume in 2024", "Production volume in 2024", " "}}, "Production volume in 2024"},
		{"rejected fact", domain.AnswerDraft{Facts: []domain.DraftFact{{Text: "Aurora launched in 2023.", ChunkIDs: []string{"a"}}, {Text: "Invented", ChunkIDs: []string{"a"}}}}, "Не удалось подтвердить часть ответа"},
	} {
		t.Run(test.name, func(t *testing.T) {
			answer := BuildGroundedAnswer("When was Aurora launched? Production volume in 2024?", test.draft, []domain.SearchResult{{ID: "a", Text: "Aurora launched in 2023."}})
			if answer.Status != domain.AnswerPartial || len(answer.Facts) != 1 || len(answer.Missing) != 1 || !strings.Contains(answer.Text, test.missing) {
				t.Fatalf("partial answer = %+v", answer)
			}
		})
	}
}

func TestBuildGroundedAnswerRequiresTextInEveryCitedChunk(t *testing.T) {
	answer := BuildGroundedAnswer("", domain.AnswerDraft{Facts: []domain.DraftFact{{Text: "Atlas supplied processors.", ChunkIDs: []string{"a", "b"}}}}, []domain.SearchResult{
		{ID: "a", Text: "Atlas supplied processors."}, {ID: "b", Text: "Cedar supplied casings."},
	})
	if len(answer.Facts) != 0 {
		t.Fatalf("accepted unrelated citation: %+v", answer)
	}
}

func TestBuildGroundedAnswerRejectsAmbiguousChunkID(t *testing.T) {
	answer := BuildGroundedAnswer("", domain.AnswerDraft{Facts: []domain.DraftFact{{Text: "Atlas supplied processors.", ChunkIDs: []string{"a"}}}}, []domain.SearchResult{
		{ID: "a", Text: "Atlas supplied processors."}, {ID: "a", Text: "Other content"}, {ID: "a", Text: "Atlas supplied processors."},
	})
	if len(answer.Facts) != 0 {
		t.Fatalf("accepted ambiguous ID: %+v", answer)
	}
}

func TestBuildGroundedAnswerEscapesUntrustedMarkdown(t *testing.T) {
	text := "[click](https://example.com) <script> `code` *bold*"
	answer := BuildGroundedAnswer("What is [missing](https://bad)?", domain.AnswerDraft{Facts: []domain.DraftFact{{Text: text, ChunkIDs: []string{"x) [evil]"}}}, Missing: []string{"[missing](https://bad)"}}, []domain.SearchResult{{ID: "x) [evil]", Text: text}})
	if len(answer.Facts) != 1 || answer.Facts[0].Text != text || strings.Contains(answer.Text, "[click](https://") || strings.Contains(answer.Text, "<script>") || strings.Contains(answer.Text, "[missing](https://") {
		t.Fatalf("unsafe Markdown = %s", answer.Text)
	}
	if !strings.Contains(answer.Text, "[1](#chunk-7829205b6576696c5d)") {
		t.Fatalf("unsafe reference = %s", answer.Text)
	}
}

func TestBuildGroundedAnswerWithEmptyContext(t *testing.T) {
	answer := BuildGroundedAnswer("", domain.AnswerDraft{}, nil)
	if answer.Status != domain.AnswerInsufficientData || answer.Facts == nil || answer.Missing == nil {
		t.Fatalf("empty answer = %+v", answer)
	}
}

func TestBuildGroundedAnswerDoesNotPublishClaimsFromMissing(t *testing.T) {
	answer := BuildGroundedAnswer("", domain.AnswerDraft{
		Facts:   []domain.DraftFact{{Text: "Atlas supplied processors.", ChunkIDs: []string{"a"}}},
		Missing: []string{"Delivery date is unknown. Cedar owns Atlas."},
	}, []domain.SearchResult{{ID: "a", Text: "Atlas supplied processors."}})
	if strings.Contains(answer.Text, "Cedar") || strings.Contains(strings.Join(answer.Missing, " "), "Cedar") || answer.Status != domain.AnswerPartial || len(answer.Missing) == 0 {
		t.Fatalf("unverified missing assertion escaped validation: %+v", answer)
	}
}

func TestBuildGroundedAnswerKeepsMultilineCitationClickable(t *testing.T) {
	text := "Atlas\n\n    supplied processors."
	answer := BuildGroundedAnswer("", domain.AnswerDraft{Facts: []domain.DraftFact{{Text: text, ChunkIDs: []string{"a"}}}}, []domain.SearchResult{{ID: "a", Text: text}})
	if len(answer.Facts) != 1 || answer.Facts[0].Text != text || answer.Facts[0].Citations[0].Text != text || answer.Text != `Atlas supplied processors\. [1](#chunk-61)` {
		t.Fatalf("multiline rendering = %+v", answer)
	}
}

func TestBuildGroundedAnswerLabelsMissingAsQuestionExcerpt(t *testing.T) {
	answer := BuildGroundedAnswer("When did Atlas deliver? Who owns Cedar?", domain.AnswerDraft{
		Facts:   []domain.DraftFact{{Text: "Atlas supplied processors.", ChunkIDs: []string{"a"}}},
		Missing: []string{"Who owns Cedar?"},
	}, []domain.SearchResult{{ID: "a", Text: "Atlas supplied processors."}})
	if answer.Status != domain.AnswerPartial || len(answer.Missing) != 1 || answer.Missing[0] != "Who owns Cedar?" || !strings.Contains(answer.Text, `по части вопроса «Who owns Cedar\?»`) {
		t.Fatalf("missing lost query provenance: %+v", answer)
	}
}
