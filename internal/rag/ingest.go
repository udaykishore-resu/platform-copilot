package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/udaykishore-resu/platform-copilot/internal/embeddings"
	"github.com/udaykishore-resu/platform-copilot/internal/vectorstore"
)

// IngestStats summarises an ingestion run.
type IngestStats struct {
	Files    int
	Chunks   int
	Skipped  int // unchanged chunks (same content hash) not re-embedded
	Duration time.Duration
	Embedder string
	Dims     int
}

// Ingester walks a directory, chunks each supported file, embeds the chunks
// in batches and upserts them. IDs are content hashes, so re-running ingest
// on unchanged files is idempotent and free.
type Ingester struct {
	Embedder embeddings.Embedder
	Store    vectorstore.Store
	Chunker  Chunker
	// Extensions to ingest; default: md, txt, yaml, yml, tf, json, sh, go, py.
	Extensions map[string]bool
	Log        func(format string, args ...any)
}

var defaultExt = map[string]bool{".md": true, ".txt": true, ".yaml": true, ".yml": true, ".tf": true, ".json": true, ".sh": true, ".go": true, ".py": true, ".rst": true}

// Ingest processes every file under root.
func (in *Ingester) Ingest(ctx context.Context, root string) (*IngestStats, error) {
	start := time.Now()
	if in.Extensions == nil {
		in.Extensions = defaultExt
	}
	if in.Chunker.MaxTokens == 0 {
		in.Chunker = DefaultChunker()
	}
	if in.Log == nil {
		in.Log = func(string, ...any) {}
	}
	stats := &IngestStats{Embedder: in.Embedder.Model(), Dims: in.Embedder.Dimensions()}

	existing := map[string]bool{}
	if docs, err := in.Store.All(ctx); err == nil {
		for _, d := range docs {
			existing[d.ID] = true
		}
	}

	var pending []vectorstore.Document
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		texts := make([]string, len(pending))
		for i, d := range pending {
			texts[i] = d.Text
		}
		vecs, err := in.Embedder.Embed(ctx, texts)
		if err != nil {
			return fmt.Errorf("embed batch: %w", err)
		}
		for i := range pending {
			pending[i].Vector = vecs[i]
		}
		if err := in.Store.Upsert(ctx, pending); err != nil {
			return fmt.Errorf("upsert: %w", err)
		}
		pending = pending[:0]
		return nil
	}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !in.Extensions[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "" {
			rel = path
		}
		stats.Files++
		var chunks []Chunk
		if strings.HasSuffix(path, ".md") {
			chunks = in.Chunker.Markdown(string(data), rel)
		} else {
			chunks = in.Chunker.Recursive(string(data), rel)
		}
		for _, ch := range chunks {
			id := chunkID(rel, ch.Text)
			stats.Chunks++
			if existing[id] {
				stats.Skipped++
				continue
			}
			pending = append(pending, vectorstore.Document{
				ID:   id,
				Text: ch.Text,
				Metadata: map[string]string{
					"source":      rel,
					"source_type": sourceType(rel),
					"section":     ch.Section,
					"chunk":       fmt.Sprint(ch.Index),
				},
			})
			if len(pending) >= 64 {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		in.Log("ingested %s (%d chunks)", rel, len(chunks))
		return nil
	})
	if err != nil {
		return stats, err
	}
	if err := flush(); err != nil {
		return stats, err
	}
	stats.Duration = time.Since(start)
	return stats, nil
}

func chunkID(source, text string) string {
	h := sha256.Sum256([]byte(source + "\x00" + text))
	return hex.EncodeToString(h[:12])
}

// sourceType classifies a path for metadata filtering (`copilot ask --source runbook`).
func sourceType(rel string) string {
	name := strings.ToLower(filepath.Base(rel))
	switch {
	case strings.HasPrefix(name, "runbook"):
		return "runbook"
	case strings.HasPrefix(name, "postmortem"), strings.HasPrefix(name, "incident"):
		return "postmortem"
	case strings.HasSuffix(name, ".tf"):
		return "terraform"
	case strings.HasSuffix(name, ".yaml"), strings.HasSuffix(name, ".yml"):
		return "kubernetes"
	case strings.Contains(name, "policy"), strings.Contains(name, "oncall"):
		return "policy"
	}
	return "doc"
}
