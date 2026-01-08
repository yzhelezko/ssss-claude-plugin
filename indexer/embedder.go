package indexer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sync"
	"time"

	"mcp-semantic-search/types"
)

// Embedder handles communication with Ollama for generating embeddings
type Embedder struct {
	baseURL    string
	model      string
	httpClient *http.Client
}

// EmbedRequest represents the request to Ollama's embed API
type EmbedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

// EmbedResponse represents the response from Ollama's embed API
type EmbedResponse struct {
	Model      string      `json:"model"`
	Embeddings [][]float32 `json:"embeddings"`
}

// NewEmbedder creates a new Embedder instance
func NewEmbedder(baseURL, model string) *Embedder {
	return &Embedder{
		baseURL: baseURL,
		model:   model,
		httpClient: &http.Client{
			Timeout: 60 * time.Second, // Embedding can take time for large texts
		},
	}
}

// Embed generates an embedding for the given text
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	reqBody := EmbedRequest{
		Model: e.model,
		Input: text,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal error: %w", err)
	}

	url := fmt.Sprintf("%s/api/embed", e.baseURL)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("request creation error: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama error (status %d): %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read error: %w", err)
	}

	var embedResp EmbedResponse
	if err := json.Unmarshal(body, &embedResp); err != nil {
		return nil, fmt.Errorf("unmarshal error: %w", err)
	}

	if len(embedResp.Embeddings) == 0 {
		return nil, fmt.Errorf("no embeddings returned")
	}

	// Normalize the embedding vector
	embedding := embedResp.Embeddings[0]
	normalized := normalizeVector(embedding)

	return normalized, nil
}

// EmbedBatch generates embeddings for multiple texts (sequential, for compatibility)
func (e *Embedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	return e.EmbedBatchParallel(ctx, texts, 1)
}

// EmbedBatchParallel generates embeddings with concurrent workers
func (e *Embedder) EmbedBatchParallel(ctx context.Context, texts []string, workers int) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}

	if workers < 1 {
		workers = 1
	}
	if workers > 8 {
		workers = 8 // Cap to avoid overwhelming Ollama
	}

	embeddings := make([][]float32, len(texts))
	errors := make([]error, len(texts))

	// Use semaphore pattern for worker pool
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup

	for i, text := range texts {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		wg.Add(1)
		go func(idx int, txt string) {
			defer wg.Done()

			// Acquire semaphore
			sem <- struct{}{}
			defer func() { <-sem }()

			// Check context again inside goroutine
			select {
			case <-ctx.Done():
				errors[idx] = ctx.Err()
				return
			default:
			}

			emb, err := e.EmbedWithRetry(ctx, txt, 3)
			if err != nil {
				errors[idx] = fmt.Errorf("embedding text %d: %w", idx, err)
				return
			}
			embeddings[idx] = emb
		}(i, text)
	}

	wg.Wait()

	// Check for errors
	for i, err := range errors {
		if err != nil {
			return nil, fmt.Errorf("batch embedding failed at index %d: %w", i, err)
		}
	}

	return embeddings, nil
}

// EmbedWithRetry attempts embedding with exponential backoff
func (e *Embedder) EmbedWithRetry(ctx context.Context, text string, maxRetries int) ([]float32, error) {
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		emb, err := e.Embed(ctx, text)
		if err == nil {
			return emb, nil
		}

		lastErr = err

		// Exponential backoff: 100ms, 200ms, 400ms...
		if attempt < maxRetries-1 {
			backoff := time.Duration(100*(1<<attempt)) * time.Millisecond
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}
	}

	return nil, fmt.Errorf("after %d retries: %w", maxRetries, lastErr)
}

// TestConnection tests the connection to Ollama
func (e *Embedder) TestConnection(ctx context.Context) error {
	// Try to embed a simple text
	_, err := e.Embed(ctx, "test")
	if err != nil {
		return fmt.Errorf("ollama connection failed: %w", err)
	}
	return nil
}

// GetModel returns the configured model name
func (e *Embedder) GetModel() string {
	return e.model
}

// EmbeddingFunc returns a function compatible with types.EmbeddingFunc for the store
func (e *Embedder) EmbeddingFunc() types.EmbeddingFunc {
	return func(ctx context.Context, text string) ([]float32, error) {
		return e.Embed(ctx, text)
	}
}

// normalizeVector normalizes a vector to unit length (L2 normalization)
func normalizeVector(v []float32) []float32 {
	var sum float64
	for _, val := range v {
		sum += float64(val) * float64(val)
	}
	norm := math.Sqrt(sum)

	if norm == 0 {
		return v
	}

	normalized := make([]float32, len(v))
	for i, val := range v {
		normalized[i] = float32(float64(val) / norm)
	}

	return normalized
}
