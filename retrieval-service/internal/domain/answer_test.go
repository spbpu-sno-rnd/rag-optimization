package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSearchResponseAnswerIsOptional(t *testing.T) {
	legacy, err := json.Marshal(SearchResponse{})
	if err != nil || strings.Contains(string(legacy), `"answer"`) {
		t.Fatalf("legacy response = %s, %v", legacy, err)
	}
	response := SearchResponse{Answer: &Answer{
		Status:  AnswerComplete,
		Text:    "Atlas supplied processors. [1](#chunk-63)",
		Facts:   []AnswerFact{{Text: "Atlas supplied processors.", Citations: []Citation{{ChunkID: "c", Text: "Atlas supplied processors.", Href: "#chunk-63"}}}},
		Missing: []string{},
	}}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	var answer Answer
	if err := json.Unmarshal(decoded["answer"], &answer); err != nil {
		t.Fatal(err)
	}
	if answer.Status != AnswerComplete || len(answer.Facts) != 1 || answer.Facts[0].Citations[0].ChunkID != "c" || answer.Missing == nil {
		t.Fatalf("answer contract = %s", decoded["answer"])
	}
}
