package safety

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// ModerationResult is provider-neutral.
type ModerationResult struct {
	Flagged    bool
	Categories map[string]float64 // category → score (0–1)
	Provider   string
}

// Moderator classifies content for policy violations.
type Moderator interface {
	Moderate(ctx context.Context, text string) (*ModerationResult, error)
}

// OpenAIModerator calls the free /moderations endpoint (omni-moderation-latest
// handles text and images). It is free, fast (~100 ms) and the obvious first
// filter for any product that accepts user text.
type OpenAIModerator struct {
	apiKey, baseURL string
	http            *http.Client
}

// NewOpenAIModerator constructs from OPENAI_API_KEY.
func NewOpenAIModerator() *OpenAIModerator {
	base := os.Getenv("OPENAI_BASE_URL")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	return &OpenAIModerator{apiKey: os.Getenv("OPENAI_API_KEY"), baseURL: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 20 * time.Second}}
}

// Moderate implements Moderator.
func (m *OpenAIModerator) Moderate(ctx context.Context, text string) (*ModerationResult, error) {
	if m.apiKey == "" {
		return nil, fmt.Errorf("moderation: OPENAI_API_KEY not set")
	}
	b, _ := json.Marshal(map[string]any{"model": "omni-moderation-latest", "input": text})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/moderations", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.apiKey)
	resp, err := m.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("moderation: HTTP %d: %s", resp.StatusCode, string(data))
	}
	var out struct {
		Results []struct {
			Flagged        bool               `json:"flagged"`
			CategoryScores map[string]float64 `json:"category_scores"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &out); err != nil || len(out.Results) == 0 {
		return nil, fmt.Errorf("moderation: bad response")
	}
	return &ModerationResult{Flagged: out.Results[0].Flagged, Categories: out.Results[0].CategoryScores, Provider: "openai"}, nil
}

// LocalModerator is the offline fallback: a small keyword/regex classifier for
// the categories that matter in an internal engineering tool (harassment,
// self-harm, violence, sexual content). It exists so the safety path is
// always exercised, never silently skipped when a key is missing.
type LocalModerator struct{}

var localCategories = map[string]*regexp.Regexp{
	"harassment": regexp.MustCompile(`(?i)\b(idiot|moron|stupid|worthless|kill yourself|kys)\b`),
	"violence":   regexp.MustCompile(`(?i)\b(kill|murder|shoot|stab|bomb)\s+(him|her|them|people|everyone|the team)\b`),
	"self-harm":  regexp.MustCompile(`(?i)\b(suicide|self[- ]harm|end my life|hurt myself)\b`),
	"sexual":     regexp.MustCompile(`(?i)\b(porn|explicit sexual|nsfw)\b`),
	"hate":       regexp.MustCompile(`(?i)\b(ethnic cleansing|racial slur|subhuman)\b`),
}

// Moderate implements Moderator.
func (LocalModerator) Moderate(_ context.Context, text string) (*ModerationResult, error) {
	res := &ModerationResult{Categories: map[string]float64{}, Provider: "local"}
	for cat, re := range localCategories {
		if re.MatchString(text) {
			res.Categories[cat] = 0.9
			res.Flagged = true
		} else {
			res.Categories[cat] = 0.0
		}
	}
	return res, nil
}

// NewModerator returns the OpenAI moderator when a key is present, otherwise
// the local fallback — and says which, so nobody mistakes one for the other.
func NewModerator() Moderator {
	if os.Getenv("OPENAI_API_KEY") != "" && os.Getenv("COPILOT_MODERATION") != "local" {
		return NewOpenAIModerator()
	}
	return LocalModerator{}
}
