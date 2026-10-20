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
MATCH path = (seed)-[:RELATED_TO*0..2]-(entity:Entity)
WHERE length(path) <= $depth
MATCH (entity)-[:MENTIONED_IN]->(chunk:Chunk)
OPTIONAL MATCH (document:Document)-[:HAS_CHUNK]->(chunk)
WITH chunk, document, min(length(path)) AS distance
RETURN chunk.id AS id, chunk.text AS text, document.id AS document_id, distance
ORDER BY distance ASC, id ASC
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

func (n *Neo4j) Search(ctx context.Context, entities []string, depth, limit int) ([]domain.SearchResult, error) {
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

		results := make([]domain.SearchResult, 0)
		for result.Next(ctx) {
			record := result.Record()
			rawID, _ := record.Get("id")
			id, ok := rawID.(string)
			if !ok || strings.TrimSpace(id) == "" {
				return nil, fmt.Errorf("Neo4j returned a chunk without a string ID")
			}
			rawText, _ := record.Get("text")
			text, ok := rawText.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return nil, fmt.Errorf("Neo4j returned a chunk without string text")
			}

			item := domain.SearchResult{ID: id, Text: text, Score: 1}
			if rawDocumentID, found := record.Get("document_id"); found {
				if documentID, ok := rawDocumentID.(string); ok {
					item.DocumentID = documentID
				}
			}
			if rawDistance, found := record.Get("distance"); found {
				switch distance := rawDistance.(type) {
				case int64:
					item.Score = 1 / float64(distance+1)
				case int:
					item.Score = 1 / float64(distance+1)
				}
			}
			results = append(results, item)
		}
		if err := result.Err(); err != nil {
			return nil, err
		}
		return results, nil
	})
	closeErr := session.Close(ctx)
	if queryErr != nil {
		return nil, fmt.Errorf("execute Neo4j local search: %w", queryErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close Neo4j session: %w", closeErr)
	}
	results, ok := value.([]domain.SearchResult)
	if !ok {
		return nil, fmt.Errorf("Neo4j local search returned unexpected result type %T", value)
	}
	return results, nil
}

func (n *Neo4j) Close(ctx context.Context) error {
	if err := n.driver.Close(ctx); err != nil {
		return fmt.Errorf("close Neo4j driver: %w", err)
	}
	return nil
}
