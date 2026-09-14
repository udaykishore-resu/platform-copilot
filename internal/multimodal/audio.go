package multimodal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// OpenAIAudio wraps the audio endpoints: Whisper transcription (speech-to-
// text), TTS (text-to-speech) and the Images API (generation). They share an
// API key and base URL with the chat provider. The same file also documents
// the open alternatives: faster-whisper / whisper.cpp for local STT, Piper /
// Coqui for local TTS, Stable Diffusion / FLUX via Replicate or Hugging Face
// for images.
type OpenAIAudio struct {
	APIKey, BaseURL string
	HTTP            *http.Client
}

// NewOpenAIAudio constructs from environment.
func NewOpenAIAudio() *OpenAIAudio {
	base := os.Getenv("OPENAI_BASE_URL")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	return &OpenAIAudio{APIKey: os.Getenv("OPENAI_API_KEY"), BaseURL: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 5 * time.Minute}}
}

// Transcription is the STT result.
type Transcription struct {
	Text     string  `json:"text"`
	Language string  `json:"language,omitempty"`
	Duration float64 `json:"duration,omitempty"`
	Segments []struct {
		Start float64 `json:"start"`
		End   float64 `json:"end"`
		Text  string  `json:"text"`
	} `json:"segments,omitempty"`
}

// Transcribe sends an audio file (≤ 25 MB: mp3, mp4, m4a, wav, webm) to
// Whisper. prompt is an optional vocabulary hint — pass service names
// ("payments-api, ledger-worker, EKS") so they are spelled correctly.
func (a *OpenAIAudio) Transcribe(ctx context.Context, path, prompt string) (*Transcription, error) {
	if a.APIKey == "" {
		return nil, fmt.Errorf("transcribe: OPENAI_API_KEY not set")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("file", filepath.Base(path))
	if _, err := io.Copy(part, f); err != nil {
		return nil, err
	}
	_ = w.WriteField("model", "whisper-1")
	_ = w.WriteField("response_format", "verbose_json")
	if prompt != "" {
		_ = w.WriteField("prompt", prompt)
	}
	w.Close()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+"/audio/transcriptions", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+a.APIKey)
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("transcribe: HTTP %d: %s", resp.StatusCode, string(data))
	}
	var t Transcription
	return &t, json.Unmarshal(data, &t)
}

// Speak converts text to speech and writes an MP3. Voices: alloy, echo, fable,
// onyx, nova, shimmer. tts-1 is fast; tts-1-hd is higher quality.
func (a *OpenAIAudio) Speak(ctx context.Context, text, voice, outPath string) error {
	if a.APIKey == "" {
		return fmt.Errorf("speak: OPENAI_API_KEY not set")
	}
	if voice == "" {
		voice = "alloy"
	}
	if len(text) > 4096 {
		text = text[:4096] // API limit per request; chunk longer texts
	}
	b, _ := json.Marshal(map[string]any{"model": "tts-1", "voice": voice, "input": text, "response_format": "mp3"})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+"/audio/speech", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.APIKey)
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("speak: HTTP %d: %s", resp.StatusCode, string(data))
	}
	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	return err
}

// GenerateImage calls the Images API and returns the image URL (or b64). For
// architecture diagrams prefer text-to-Mermaid (see `copilot diagram`):
// pixel models cannot reliably render exact labels and arrows.
func (a *OpenAIAudio) GenerateImage(ctx context.Context, prompt, model, size string) (string, error) {
	if a.APIKey == "" {
		return "", fmt.Errorf("images: OPENAI_API_KEY not set")
	}
	if model == "" {
		model = "dall-e-3"
	}
	if size == "" {
		size = "1024x1024"
	}
	b, _ := json.Marshal(map[string]any{"model": model, "prompt": prompt, "n": 1, "size": size})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+"/images/generations", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.APIKey)
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("images: HTTP %d: %s", resp.StatusCode, string(data))
	}
	var out struct {
		Data []struct {
			URL     string `json:"url"`
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &out); err != nil || len(out.Data) == 0 {
		return "", fmt.Errorf("images: bad response")
	}
	if out.Data[0].URL != "" {
		return out.Data[0].URL, nil
	}
	return "data:image/png;base64," + out.Data[0].B64JSON, nil
}
