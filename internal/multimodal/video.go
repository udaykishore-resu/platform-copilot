package multimodal

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/udaykishore-resu/platform-copilot/internal/llm"
)

// Video understanding, the pragmatic way: most chat models accept images, not
// video, so a recording is sampled into frames (one every N seconds) and sent
// as a multi-image prompt — optionally alongside a Whisper transcript of its
// audio track. Gemini accepts video natively; for everyone else this is the
// standard pattern. Sampling needs ffmpeg on PATH.

// SampleFrames extracts one JPEG every `every` seconds from a video into a
// temp dir and returns them as image parts, capped at maxFrames (each frame
// costs image tokens; 8–12 frames is plenty for a dashboard recording).
func SampleFrames(ctx context.Context, path string, every time.Duration, maxFrames int) ([]llm.ImagePart, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, fmt.Errorf("ffmpeg not found on PATH (brew install ffmpeg / apt install ffmpeg)")
	}
	if every <= 0 {
		every = 5 * time.Second
	}
	if maxFrames <= 0 {
		maxFrames = 10
	}
	dir, err := os.MkdirTemp("", "copilot-frames-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	fps := fmt.Sprintf("fps=1/%d", int(every.Seconds()))
	cmd := exec.CommandContext(ctx, "ffmpeg", "-loglevel", "error", "-i", path, "-vf", fps+",scale=1024:-1", "-frames:v", fmt.Sprint(maxFrames), filepath.Join(dir, "frame-%03d.jpg"))
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %v: %s", err, out)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "frame-*.jpg"))
	sort.Strings(files)
	var parts []llm.ImagePart
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		parts = append(parts, llm.ImagePart{MIME: "image/jpeg", Data: data})
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("no frames extracted from %s", path)
	}
	return parts, nil
}

// DescribeVideo sends sampled frames (and an optional transcript) as one
// multi-image prompt.
func DescribeVideo(ctx context.Context, p llm.Provider, frames []llm.ImagePart, transcript, question, user string) (*llm.Response, error) {
	if question == "" {
		question = "These are frames sampled in order from a screen recording. Describe what happens over time and flag anything an on-call engineer should act on."
	}
	if transcript != "" {
		question += "\n\nAudio transcript:\n" + transcript
	}
	return p.Complete(ctx, llm.Request{
		Messages:  []llm.Message{llm.System(VisionSystem), {Role: llm.RoleUser, Content: question, Images: frames}},
		MaxTokens: 900,
		User:      user,
	})
}
