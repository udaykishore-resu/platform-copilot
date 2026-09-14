// Package agent gives the model hands: a loop that lets it call tools,
// observe results and decide the next step. Two protocols are implemented
// over the same tool registry — the text ReAct protocol that works with any
// model, and native tool calling for models that support it.
//
// Roadmap: AI Agents (Agents Usecases · ReAct Prompting · Manual
// Implementation · OpenAI Functions / Tools).
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/udaykishore-resu/platform-copilot/internal/llm"
)

// Tool is something the agent may do. Every tool in this repo is read-only
// by construction (ADR-0007): an agent that can read the cluster but not
// change it can be wrong without being dangerous.
type Tool struct {
	Name        string
	Description string
	// Schema is a JSON Schema for the arguments object.
	Schema json.RawMessage
	// Run executes with the parsed arguments and returns an observation.
	Run func(ctx context.Context, args map[string]any) (string, error)
	// Dangerous marks tools that require explicit operator approval.
	Dangerous bool
}

// Registry holds tools by name.
type Registry struct{ tools map[string]Tool }

// NewRegistry creates an empty registry.
func NewRegistry() *Registry { return &Registry{tools: map[string]Tool{}} }

// Register adds a tool (panics on duplicate names — a programming error).
func (r *Registry) Register(t Tool) *Registry {
	if _, dup := r.tools[t.Name]; dup {
		panic("agent: duplicate tool " + t.Name)
	}
	r.tools[t.Name] = t
	return r
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Names lists tools in stable order.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.tools))
	for n := range r.tools {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// LLMTools converts the registry to provider tool definitions.
func (r *Registry) LLMTools() []llm.Tool {
	var out []llm.Tool
	for _, n := range r.Names() {
		t := r.tools[n]
		out = append(out, llm.Tool{Name: t.Name, Description: t.Description, Parameters: t.Schema})
	}
	return out
}

// Describe renders the registry for a text (ReAct) prompt.
func (r *Registry) Describe() string {
	var sb strings.Builder
	for _, n := range r.Names() {
		t := r.tools[n]
		sb.WriteString(fmt.Sprintf("- %s: %s\n  args schema: %s\n", t.Name, t.Description, compactJSON(t.Schema)))
	}
	return sb.String()
}

// Call runs a tool with raw JSON arguments, returning the observation. Errors
// are returned as observations too, so the model can recover ("namespace not
// found" is information, not a crash).
func (r *Registry) Call(ctx context.Context, name string, raw json.RawMessage) string {
	t, ok := r.tools[name]
	if !ok {
		return fmt.Sprintf("error: unknown tool %q; available: %s", name, strings.Join(r.Names(), ", "))
	}
	args := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			// ReAct models often pass a bare string; wrap it as the first schema property
			args = map[string]any{firstProperty(t.Schema): strings.Trim(string(raw), "\" \n")}
		}
	}
	out, err := t.Run(ctx, args)
	if err != nil {
		return "error: " + err.Error()
	}
	if len(out) > 6000 {
		out = out[:6000] + "\n…[truncated]"
	}
	return out
}

func compactJSON(raw json.RawMessage) string {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return string(raw)
	}
	return b.String()
}

func firstProperty(schema json.RawMessage) string {
	var s struct {
		Required   []string       `json:"required"`
		Properties map[string]any `json:"properties"`
	}
	_ = json.Unmarshal(schema, &s)
	if len(s.Required) > 0 {
		return s.Required[0]
	}
	for k := range s.Properties {
		return k
	}
	return "input"
}

// Schema is a small helper to build a JSON Schema for an object.
func Schema(required []string, props map[string]string) json.RawMessage {
	p := map[string]any{}
	for name, desc := range props {
		typ := "string"
		if strings.HasPrefix(desc, "int:") {
			typ, desc = "integer", strings.TrimPrefix(desc, "int:")
		}
		p[name] = map[string]any{"type": typ, "description": strings.TrimSpace(desc)}
	}
	obj := map[string]any{"type": "object", "properties": p}
	if len(required) > 0 {
		obj["required"] = required
	}
	b, _ := json.Marshal(obj)
	return b
}
