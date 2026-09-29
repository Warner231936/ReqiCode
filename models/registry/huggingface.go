package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/kilo/spiral-codemaker/models/routing"
)

type HFModelLister struct {
	apiToken string
	client   *http.Client
}

type hfSearchResult struct {
	Model struct {
		ID        string   `json:"id"`
		Name      string   `json:"name"`
		Downloads int      `json:"downloads"`
		Likes     int      `json:"likes"`
		Tags      []string `json:"tags"`
		Private   bool     `json:"private"`
	} `json:"model"`
}

type hfSearchResponse struct {
	Models []struct {
		ID        string   `json:"id"`
		Name      string   `json:"name"`
		Downloads int      `json:"downloads"`
		Likes     int      `json:"likes"`
		Tags      []string `json:"tags"`
		Private   bool     `json:"private"`
	} `json:"models"`
	Next bool `json:"next"`
}

func NewHFModelLister(apiToken string) *HFModelLister {
	return &HFModelLister{
		apiToken: apiToken,
		client:   &http.Client{Timeout: 30 * time.Second},
	}
}

func (l *HFModelLister) ListModels(ctx context.Context, query string, limit int) ([]ModelEntry, error) {
	if limit <= 0 {
		limit = 20
	}

	searchURL := fmt.Sprintf(
		"https://huggingface.co/api/models?search=%s&limit=%d&full=false",
		url.QueryEscape(query),
		limit,
	)

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return nil, err
	}
	if l.apiToken != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", l.apiToken))
	}

	resp, err := l.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("huggingface: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("huggingface: API returned %d", resp.StatusCode)
	}

	var hfResp hfSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&hfResp); err != nil {
		return nil, fmt.Errorf("failed to parse HF response: %w", err)
	}

	entries := make([]ModelEntry, 0, len(hfResp.Models))
	for _, m := range hfResp.Models {
		entries = append(entries, ModelEntry{
			ID:         fmt.Sprintf("hf-%s", m.ID),
			Name:       m.ID,
			Kind:       KindHuggingFace,
			Provider:   "huggingface",
			ModelID:    m.ID,
			Capability: detectHFCapability(m.Tags),
			Status:     ModelStatusMissing,
			SizeMB:     0,
			Description: fmt.Sprintf("HuggingFace model: %s (downloads: %d)", m.ID, m.Downloads),
			Tags:       m.Tags,
		})
	}

	return entries, nil
}

func (l *HFModelLister) CheckModelExists(ctx context.Context, modelID string) (bool, error) {
	checkURL := fmt.Sprintf("https://huggingface.co/api/models/%s", modelID)
	req, err := http.NewRequestWithContext(ctx, "GET", checkURL, nil)
	if err != nil {
		return false, err
	}
	if l.apiToken != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", l.apiToken))
	}

	resp, err := l.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK, nil
}

func detectHFCapability(tags []string) routing.ModelCapability {
	for _, tag := range tags {
		switch tag {
		case "text-classification":
			return routing.CapClassification
		case "feature-extraction", "embeddings":
			return routing.CapEmbed
		case "question-answering":
			return routing.CapReasoning
		}
	}
	for _, tag := range tags {
		if containsAny(tag, []string{"code", "coder", "starcoder"}) {
			return routing.CapSpecialize
		}
	}
	return routing.CapFast
}

func containsAny(s string, substrs []string) bool {
	lower := s
	for _, sub := range substrs {
		if len(sub) > 0 && (len(lower) >= len(sub)) {
			for i := 0; i <= len(lower)-len(sub); i++ {
				if lower[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
