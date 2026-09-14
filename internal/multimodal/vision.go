// Package multimodal covers images, audio and speech: reading a Grafana
// screenshot, transcribing an incident bridge, generating a diagram,
// speaking a summary.
//
// Roadmap: Multimodal AI (Vision · DALL-E · Whisper · TTS · Implementing).
package multimodal

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/udaykishore-resu/platform-copilot/internal/llm"
)

// LoadImage reads an image file into an llm.ImagePart, inferring the MIME
// type from the extension. Providers cap images around 20 MB; downscale large
// screenshots first — a 1024×768 PNG is plenty for dashboard reading and
// costs far fewer image tokens than a 4k capture.
func LoadImage(path string) (llm.ImagePart, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return llm.ImagePart{}, err
	}
	if len(data) > 20<<20 {
		return llm.ImagePart{}, fmt.Errorf("image %s is %d MB; providers cap at ~20 MB — downscale it first", path, len(data)>>20)
	}
	mt := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if mt == "" {
		mt = "image/png"
	}
	return llm.ImagePart{MIME: mt, Data: data}, nil
}

// DashboardReading is the structured result of looking at a monitoring
// screenshot. Asking for JSON rather than prose turns "vision" into data the
// rest of the system can act on.
type DashboardReading struct {
	Summary    string   `json:"summary"`
	Severity   string   `json:"severity"` // ok | warning | critical
	Panels     []string `json:"panels"`
	Anomalies  []string `json:"anomalies"`
	NextChecks []string `json:"next_checks"`
}

// DashboardSchema is the JSON Schema for DashboardReading.
var DashboardSchema = json.RawMessage(`{
  "type":"object",
  "required":["summary","severity","panels","anomalies","next_checks"],
  "properties":{
    "summary":{"type":"string"},
    "severity":{"type":"string","enum":["ok","warning","critical"]},
    "panels":{"type":"array","items":{"type":"string"}},
    "anomalies":{"type":"array","items":{"type":"string"}},
    "next_checks":{"type":"array","items":{"type":"string"}}
  }
}`)

// VisionSystem frames the task for the model.
const VisionSystem = `You are an SRE reading a monitoring dashboard screenshot. Identify each panel, read the values and time ranges you can see, call out anomalies (spikes, drops, saturation, error bursts) with the approximate time they start, rate the overall severity, and list the next checks an on-call engineer should run. If text is unreadable, say so rather than guessing.`

// DescribeImage asks the model a free-form question about an image.
func DescribeImage(ctx context.Context, p llm.Provider, img llm.ImagePart, question, user string) (*llm.Response, error) {
	if question == "" {
		question = "Describe this screenshot for an on-call engineer."
	}
	return p.Complete(ctx, llm.Request{
		Messages: []llm.Message{
			llm.System(VisionSystem),
			{Role: llm.RoleUser, Content: question, Images: []llm.ImagePart{img}},
		},
		MaxTokens: 700,
		User:      user,
	})
}

// ReadDashboard asks for a structured reading.
func ReadDashboard(ctx context.Context, p llm.Provider, img llm.ImagePart, user string) (*DashboardReading, *llm.Response, error) {
	resp, err := p.Complete(ctx, llm.Request{
		Messages: []llm.Message{
			llm.System(VisionSystem),
			{Role: llm.RoleUser, Content: "Read this dashboard and return the JSON object.", Images: []llm.ImagePart{img}},
		},
		MaxTokens:  700,
		JSONSchema: DashboardSchema,
		User:       user,
	})
	if err != nil {
		return nil, nil, err
	}
	var r DashboardReading
	txt := strings.TrimSpace(resp.Content)
	txt = strings.TrimPrefix(strings.TrimPrefix(txt, "```json"), "```")
	txt = strings.TrimSuffix(txt, "```")
	if err := json.Unmarshal([]byte(strings.TrimSpace(txt)), &r); err != nil {
		return nil, resp, fmt.Errorf("model did not return valid JSON: %w\n%s", err, resp.Content)
	}
	return &r, resp, nil
}
