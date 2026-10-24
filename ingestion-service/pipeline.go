package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

type chunk struct {
	ID         string            `json:"chunk_id"`
	DocumentID string            `json:"document_id"`
	Source     string            `json:"source"`
	Title      string            `json:"title"`
	Text       string            `json:"text"`
	Index      int               `json:"chunk_index"`
	Metadata   map[string]string `json:"metadata"`
	Vector     []float32         `json:"-"`
	Entities   []string          `json:"-"`
}

var relationName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func uuid() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func loadAliases(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read aliases: %w", err)
	}
	var entries map[string][]string
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("decode aliases: %w", err)
	}
	aliases := make(map[string]string)
	for canonical, names := range entries {
		canonical = strings.TrimSpace(canonical)
		if canonical == "" {
			return nil, fmt.Errorf("empty canonical name")
		}
		for _, name := range append(names, canonical) {
			key := strings.ToLower(strings.TrimSpace(name))
			if key == "" {
				return nil, fmt.Errorf("empty alias for %q", canonical)
			}
			if old, ok := aliases[key]; ok && old != canonical {
				return nil, fmt.Errorf("ambiguous alias %q", name)
			}
			aliases[key] = canonical
		}
	}
	return aliases, nil
}

func canonicalName(name string, aliases map[string]string) string {
	name = strings.TrimSpace(name)
	if canonical, ok := aliases[strings.ToLower(name)]; ok {
		return canonical
	}
	return name
}

func canonicalEntities(text string, proposed []string, aliases map[string]string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(proposed))
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" || !strings.Contains(strings.ToLower(text), strings.ToLower(raw)) {
			return
		}
		name := canonicalName(raw, aliases)
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, name)
	}
	for _, raw := range proposed {
		add(raw)
	}
	keys := make([]string, 0, len(aliases))
	for key := range aliases {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if strings.Contains(strings.ToLower(text), key) {
			add(key)
		}
	}
	return out
}

func splitChunks(ctx context.Context, text string, maxTokens int, count func(context.Context, string) (int, error)) ([]string, error) {
	text = strings.TrimSpace(text)
	var parts []string
	for text != "" {
		runes := []rune(text)
		window := len(runes)
		if window > 4096 {
			window = 4096
		}
		n, err := count(ctx, "passage: "+string(runes[:window]))
		if err != nil {
			return nil, err
		}
		if n <= maxTokens && window == len(runes) {
			parts = append(parts, text)
			break
		}
		lo, hi := 1, window
		best := window
		if n > maxTokens {
			best = 0
		}
		for lo <= hi {
			mid := lo + (hi-lo)/2
			counted, err := count(ctx, "passage: "+string(runes[:mid]))
			if err != nil {
				return nil, err
			}
			if counted <= maxTokens {
				best = mid
				lo = mid + 1
			} else {
				hi = mid - 1
			}
		}
		if best == 0 {
			return nil, fmt.Errorf("token budget cannot hold one character")
		}
		cut := best
		candidate := string(runes[:best])
		if boundary := strings.LastIndexAny(candidate, ".!?\n"); boundary > len(candidate)/2 {
			cut = utf8.RuneCountInString(candidate[:boundary+1])
		} else if boundary := strings.LastIndexAny(candidate, " \t"); boundary > 0 {
			cut = utf8.RuneCountInString(candidate[:boundary+1])
		}
		parts = append(parts, strings.TrimSpace(string(runes[:cut])))
		text = strings.TrimSpace(string(runes[cut:]))
	}
	return parts, nil
}

func readCorpus(ctx context.Context, root string, model *modelClient, aliases map[string]string, maxTokens int) ([]chunk, []relation, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".txt" || ext == ".md" || ext == ".markdown" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if len(paths) == 0 {
		return nil, nil, fmt.Errorf("no TXT or Markdown documents in %s", root)
	}
	sort.Strings(paths)
	chunks := make([]chunk, 0)
	relations := make([]relation, 0)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		if !utf8.Valid(data) {
			return nil, nil, fmt.Errorf("%s is not UTF-8", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil, nil, err
		}
		rel = filepath.ToSlash(rel)
		text := strings.TrimSpace(string(data))
		if text == "" {
			continue
		}
		title := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		if strings.HasPrefix(text, "# ") {
			title = strings.TrimSpace(strings.SplitN(text[2:], "\n", 2)[0])
		}
		pieces, err := splitChunks(ctx, text, maxTokens, model.count)
		if err != nil {
			return nil, nil, fmt.Errorf("chunk %s: %w", path, err)
		}
		for index, piece := range pieces {
			id, err := uuid()
			if err != nil {
				return nil, nil, err
			}
			vector, err := model.embed(ctx, piece, "passage")
			if err != nil {
				return nil, nil, fmt.Errorf("embed %s chunk %d: %w", path, index, err)
			}
			extracted, err := model.extract(ctx, piece, false)
			if err != nil {
				return nil, nil, fmt.Errorf("extract %s chunk %d: %w", path, index, err)
			}
			entities := canonicalEntities(piece, extracted.Entities, aliases)
			entitySet := make(map[string]struct{})
			for _, e := range entities {
				entitySet[strings.ToLower(e)] = struct{}{}
			}
			for _, fact := range extracted.Relations {
				originalSource, originalTarget := strings.TrimSpace(fact.Source), strings.TrimSpace(fact.Target)
				if !strings.Contains(strings.ToLower(piece), strings.ToLower(originalSource)) || !strings.Contains(strings.ToLower(piece), strings.ToLower(originalTarget)) || originalSource == "" || originalTarget == "" {
					continue
				}
				fact.Source, fact.Target = canonicalName(originalSource, aliases), canonicalName(originalTarget, aliases)
				fact.RelationType = strings.ToLower(strings.TrimSpace(fact.RelationType))
				if !relationName.MatchString(fact.RelationType) || strings.EqualFold(fact.Source, fact.Target) {
					continue
				}
				for _, name := range []string{fact.Source, fact.Target} {
					if _, ok := entitySet[strings.ToLower(name)]; !ok {
						entitySet[strings.ToLower(name)] = struct{}{}
						entities = append(entities, name)
					}
				}
				fact.SourceChunkIDs = []string{id}
				relations = append(relations, fact)
			}
			chunks = append(chunks, chunk{ID: id, DocumentID: strings.TrimSuffix(rel, filepath.Ext(rel)), Source: rel, Title: title, Text: piece, Index: index, Metadata: map[string]string{"source": rel, "title": title, "chunk_index": strconv.Itoa(index)}, Vector: vector, Entities: entities})
		}
	}
	if len(chunks) == 0 {
		return nil, nil, fmt.Errorf("all documents are empty")
	}
	return chunks, mergeRelations(relations), nil
}

func mergeRelations(input []relation) []relation {
	index := make(map[string]int)
	out := make([]relation, 0)
	for _, r := range input {
		key := r.Source + "\x00" + r.RelationType + "\x00" + r.Target
		if i, ok := index[key]; ok {
			for _, id := range r.SourceChunkIDs {
				found := false
				for _, existing := range out[i].SourceChunkIDs {
					if id == existing {
						found = true
						break
					}
				}
				if !found {
					out[i].SourceChunkIDs = append(out[i].SourceChunkIDs, id)
				}
			}
			continue
		}
		index[key] = len(out)
		out = append(out, r)
	}
	return out
}

func validateEvidence(chunks []chunk, relations []relation) error {
	ids := make(map[string]struct{}, len(chunks))
	for _, c := range chunks {
		if _, exists := ids[c.ID]; exists {
			return fmt.Errorf("duplicate chunk ID %s", c.ID)
		}
		ids[c.ID] = struct{}{}
	}
	for _, r := range relations {
		for _, id := range r.SourceChunkIDs {
			if _, ok := ids[id]; !ok {
				return fmt.Errorf("relation %s has missing evidence %s", r.RelationType, id)
			}
		}
	}
	return nil
}

func writeJSONL(path string, chunks []chunk) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	encoder := json.NewEncoder(w)
	for _, c := range chunks {
		if err := encoder.Encode(c); err != nil {
			return err
		}
	}
	return w.Flush()
}
