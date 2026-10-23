package graph

import (
	"context"
	"fmt"
	"strings"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"retrieval-service/internal/domain"
)

const localSearchQuery = `
MATCH (seed:Entity)
WHERE any(name IN $entities WHERE toLower(seed.name) = toLower(name))
MATCH path = (seed)-[:RELATED_TO*1..2]-(entity:Entity)
WHERE length(path) <= $depth
UNWIND relationships(path) AS relation
WITH relation, startNode(relation) AS source, endNode(relation) AS target, min(length(path)) AS distance
WITH source, target, relation, distance, coalesce(relation.source_chunk_ids, []) AS source_chunk_ids
WHERE relation.relation_type IS NOT NULL AND size(source_chunk_ids) > 0
UNWIND source_chunk_ids AS evidence_id
MATCH (chunk:Chunk {id: evidence_id})
OPTIONAL MATCH (document:Document)-[:HAS_CHUNK]->(chunk)
RETURN source.name AS source_entity, relation.relation_type AS relation_type,
       target.name AS target_entity, distance, chunk.id AS evidence_id,
       chunk.text AS evidence_text, document.id AS document_id
ORDER BY distance ASC, source_entity ASC, relation_type ASC, target_entity ASC, evidence_id ASC
LIMIT $limit
`

type Neo4j struct {
	driver   neo4j.DriverWithContext
	database string
}

func NewNeo4j(ctx context.Context, uri, username, password, database string) (*Neo4j, error) {
	if strings.TrimSpace(uri) == "" || strings.TrimSpace(database) == "" {
		return nil, fmt.Errorf("Neo4j URI and database must not be empty")
	}
	driver, err := neo4j.NewDriverWithContext(uri, neo4j.BasicAuth(username, password, ""))
	if err != nil {
		return nil, fmt.Errorf("create Neo4j driver: %w", err)
	}
	if err := driver.VerifyConnectivity(ctx); err != nil {
		closeErr := driver.Close(ctx)
		if closeErr != nil {
			return nil, fmt.Errorf("verify Neo4j connectivity: %w (close driver: %v)", err, closeErr)
		}
		return nil, fmt.Errorf("verify Neo4j connectivity: %w", err)
	}
	return &Neo4j{driver: driver, database: database}, nil
}

func (n *Neo4j) Search(ctx context.Context, entities []string, depth, limit int) (domain.GraphSearchResults, error) {
	session := n.driver.NewSession(ctx, neo4j.SessionConfig{
		AccessMode:   neo4j.AccessModeRead,
		DatabaseName: n.database,
	})
	value, queryErr := session.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		result, err := tx.Run(ctx, localSearchQuery, map[string]any{
			"entities": entities,
			"depth":    depth,
			"limit":    limit,
		})
		if err != nil {
			return nil, err
		}

		results := domain.GraphSearchResults{
			Chunks: make([]domain.SearchResult, 0),
			Facts:  make([]domain.GraphFact, 0),
		}
		chunkIndexes := make(map[string]int)
		type factKey struct {
			sourceEntity string
			relationType string
			targetEntity string
		}
		factIndexes := make(map[factKey]int)
		for result.Next(ctx) {
			record := result.Record()
			sourceEntity, err := requiredString(record, "source_entity")
			if err != nil {
				return nil, err
			}
			relationType, err := requiredString(record, "relation_type")
			if err != nil {
				return nil, err
			}
			targetEntity, err := requiredString(record, "target_entity")
			if err != nil {
				return nil, err
			}
			distance, err := requiredInt(record, "distance")
			if err != nil {
				return nil, err
			}
			if distance < 1 {
				return nil, fmt.Errorf("Neo4j returned a fact with invalid distance %d", distance)
			}

			evidenceID, err := requiredString(record, "evidence_id")
			if err != nil {
				return nil, err
			}
			evidenceText, err := requiredString(record, "evidence_text")
			if err != nil {
				return nil, err
			}

			key := factKey{sourceEntity: sourceEntity, relationType: relationType, targetEntity: targetEntity}
			factIndex, exists := factIndexes[key]
			if !exists {
				factIndex = len(results.Facts)
				factIndexes[key] = factIndex
				results.Facts = append(results.Facts, domain.GraphFact{
					SourceEntity: sourceEntity,
					RelationType: relationType,
					TargetEntity: targetEntity,
					Distance:     distance,
				})
			} else if distance < results.Facts[factIndex].Distance {
				results.Facts[factIndex].Distance = distance
			}
			if !containsChunkID(results.Facts[factIndex].EvidenceChunkIDs, evidenceID) {
				results.Facts[factIndex].EvidenceChunkIDs = append(results.Facts[factIndex].EvidenceChunkIDs, evidenceID)
			}

			if index, exists := chunkIndexes[evidenceID]; exists {
				if results.Chunks[index].Text != evidenceText {
					return nil, fmt.Errorf("Neo4j returned conflicting text for evidence chunk %q", evidenceID)
				}
				if results.Chunks[index].Score < 1/float64(distance) {
					results.Chunks[index].Score = 1 / float64(distance)
				}
				continue
			}
			item := domain.SearchResult{ID: evidenceID, Text: evidenceText, Score: 1 / float64(distance)}
			if rawDocumentID, found := record.Get("document_id"); found {
				if documentID, ok := rawDocumentID.(string); ok {
					item.DocumentID = documentID
				}
			}
			chunkIndexes[evidenceID] = len(results.Chunks)
			results.Chunks = append(results.Chunks, item)
		}
		if err := result.Err(); err != nil {
			return nil, err
		}
		return results, nil
	})
	closeErr := session.Close(ctx)
	if queryErr != nil {
		return domain.GraphSearchResults{}, fmt.Errorf("execute Neo4j local search: %w", queryErr)
	}
	if closeErr != nil {
		return domain.GraphSearchResults{}, fmt.Errorf("close Neo4j session: %w", closeErr)
	}
	results, ok := value.(domain.GraphSearchResults)
	if !ok {
		return domain.GraphSearchResults{}, fmt.Errorf("Neo4j local search returned unexpected result type %T", value)
	}
	return results, nil
}

func containsChunkID(ids []string, id string) bool {
	for _, existing := range ids {
		if existing == id {
			return true
		}
	}
	return false
}

func requiredString(record *neo4j.Record, key string) (string, error) {
	value, found := record.Get(key)
	text, ok := value.(string)
	if !found || !ok || strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("Neo4j returned a fact without a valid %s", key)
	}
	return text, nil
}

func requiredInt(record *neo4j.Record, key string) (int, error) {
	value, found := record.Get(key)
	if !found {
		return 0, fmt.Errorf("Neo4j returned a fact without %s", key)
	}
	switch number := value.(type) {
	case int64:
		return int(number), nil
	case int:
		return number, nil
	default:
		return 0, fmt.Errorf("Neo4j returned %s with unexpected type %T", key, value)
	}
}

func (n *Neo4j) Close(ctx context.Context) error {
	if err := n.driver.Close(ctx); err != nil {
		return fmt.Errorf("close Neo4j driver: %w", err)
	}
	return nil
}
