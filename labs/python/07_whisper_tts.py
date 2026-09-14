#!/usr/bin/env python3
"""
Module 07 lab — speech-to-text (Whisper) and text-to-speech on an incident recording.

Python counterpart of `internal/multimodal/whisper.go → Transcribe` and
`internal/multimodal/tts.go → Speak`, and of `copilot ingest bridge.m4a` /
`copilot ask --speak`.

The lab demonstrates the three settings that matter in production:

1. **Vocabulary prompt.** Whisper's `prompt` biases recognition toward your service
   names and acronyms — the dominant source of errors in engineering audio. Here it
   is built from a list you would derive from the knowledge index in the capstone.
2. **Chunking under the 25 MB upload limit at silence boundaries**, so sentences are
   not cut. Uses ffmpeg if available; otherwise explains what it would do.
3. **Timestamps** (`verbose_json`) so the transcript can be aligned to a postmortem
   timeline and stitched across chunks with offsets.

Then it synthesises a short spoken summary with the TTS endpoint, applying a small
pronunciation table for jargon (there is no SSML on OpenAI TTS).

Fallback: with no OPENAI_API_KEY but `faster-whisper` installed, transcription runs
locally with the same interface, to show the self-hosting path from the module.

Running
-------
    pip install openai>=1.40            # plus: ffmpeg on PATH for chunking; optional: faster-whisper
    export OPENAI_API_KEY=sk-...
    python 07_whisper_tts.py path/to/incident-bridge.m4a [--speak]

Without a key and without faster-whisper the script explains the flow and exits 0.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from dataclasses import dataclass
from pathlib import Path

UPLOAD_LIMIT_BYTES = 25 * 1024 * 1024

# In the capstone this list is built automatically from resource names found in the
# index (Deployments, Services, alert names). Hard-coded here for the lab.
SERVICE_VOCABULARY = [
    "payments-api", "payments-worker", "checkout", "notifications", "kube-system", "CrashLoopBackOff",
    "PromQL", "Grafana", "etcd", "kubectl", "ArgoCD", "Terraform", "p99", "SLO", "error budget", "rollout undo",
]

# Whisper's prompt works best as natural text containing the words, not a bare list.
VOCAB_PROMPT = ("Incident bridge for the platform team. Services discussed include " + ", ".join(SERVICE_VOCABULARY) +
                ". Speakers use Kubernetes and SRE terminology.")

# TTS has no SSML; substitute jargon before synthesis.
PRONUNCIATION = {
    r"\bkubectl\b": "kube control",
    r"\betcd\b": "et-see-dee",
    r"\bPromQL\b": "prom Q L",
    r"\bp99\b": "p ninety-nine",
    r"\b5xx\b": "five-x-x",
    r"\bSLO\b": "S-L-O",
}


@dataclass
class Segment:
    start: float
    end: float
    text: str


# --------------------------------------------------------------------------- #
# Chunking
# --------------------------------------------------------------------------- #

def split_audio(path: Path, workdir: Path, target_seconds: int = 600) -> list[tuple[Path, float]]:
    """Split `path` into chunks of roughly `target_seconds`, cutting at detected silences.

    Returns [(chunk_path, start_offset_seconds)]. Requires ffmpeg/ffprobe. If the file is
    already under the upload limit, returns it unchanged with offset 0.
    """
    if path.stat().st_size <= UPLOAD_LIMIT_BYTES:
        return [(path, 0.0)]
    if not shutil.which("ffmpeg") or not shutil.which("ffprobe"):
        raise RuntimeError("file exceeds 25 MB and ffmpeg is not on PATH; install ffmpeg to chunk it")

    duration = float(subprocess.check_output(
        ["ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", str(path)], text=True
    ).strip())

    # Find silences (>= 0.6 s below -35 dB); pick the one nearest each target boundary.
    out = subprocess.run(
        ["ffmpeg", "-i", str(path), "-af", "silencedetect=noise=-35dB:d=0.6", "-f", "null", "-"],
        capture_output=True, text=True,
    ).stderr
    silences = [float(m) for m in re.findall(r"silence_start: ([0-9.]+)", out)]

    cuts: list[float] = []
    t = target_seconds
    while t < duration:
        nearest = min(silences, key=lambda s: abs(s - t), default=t)
        cuts.append(nearest if abs(nearest - t) < 60 else t)
        t = cuts[-1] + target_seconds
    bounds = [0.0, *cuts, duration]

    chunks = []
    for i in range(len(bounds) - 1):
        start, end = bounds[i], bounds[i + 1]
        chunk = workdir / f"chunk-{i:03d}.m4a"
        subprocess.run(["ffmpeg", "-v", "error", "-y", "-i", str(path), "-ss", f"{start:.2f}", "-to", f"{end:.2f}",
                        "-c:a", "aac", "-b:a", "64k", str(chunk)], check=True)
        chunks.append((chunk, start))
    return chunks


# --------------------------------------------------------------------------- #
# Transcription backends: OpenAI API, or faster-whisper locally.
# --------------------------------------------------------------------------- #

def transcribe_openai(chunk: Path, model: str) -> list[Segment]:
    from openai import OpenAI

    client = OpenAI()
    with chunk.open("rb") as fh:
        resp = client.audio.transcriptions.create(
            model=model,
            file=fh,
            prompt=VOCAB_PROMPT,
            response_format="verbose_json",
            timestamp_granularities=["segment"],
        )
    segs = getattr(resp, "segments", None) or []
    if not segs:  # some models return only text
        return [Segment(0.0, 0.0, resp.text)]
    return [Segment(float(s.start), float(s.end), s.text.strip()) for s in segs]


def transcribe_local(chunk: Path) -> list[Segment]:
    from faster_whisper import WhisperModel  # type: ignore

    model = WhisperModel(os.environ.get("WHISPER_LOCAL_MODEL", "base"), compute_type="int8")
    segments, _info = model.transcribe(str(chunk), initial_prompt=VOCAB_PROMPT)
    return [Segment(s.start, s.end, s.text.strip()) for s in segments]


def transcribe(path: Path, backend: str, model: str) -> list[Segment]:
    with tempfile.TemporaryDirectory() as tmp:
        chunks = split_audio(path, Path(tmp))
        print(f"chunks: {len(chunks)}  (upload limit {UPLOAD_LIMIT_BYTES // (1024*1024)} MB)")
        all_segments: list[Segment] = []
        for chunk, offset in chunks:
            segs = transcribe_openai(chunk, model) if backend == "openai" else transcribe_local(chunk)
            # Offset-correct so timestamps are relative to the original recording.
            all_segments.extend(Segment(s.start + offset, s.end + offset, s.text) for s in segs)
        return all_segments


def fmt_ts(seconds: float) -> str:
    m, s = divmod(int(seconds), 60)
    h, m = divmod(m, 60)
    return f"{h:02d}:{m:02d}:{s:02d}"


# --------------------------------------------------------------------------- #
# TTS
# --------------------------------------------------------------------------- #

def speak(text: str, out: Path, voice: str = "alloy", model: str = "tts-1") -> None:
    from openai import OpenAI

    for pattern, replacement in PRONUNCIATION.items():
        text = re.sub(pattern, replacement, text)
    client = OpenAI()
    # Streaming response: playback could start before synthesis finishes; here we just write the file.
    with client.audio.speech.with_streaming_response.create(model=model, voice=voice, input=text,
                                                            response_format="mp3") as resp:
        resp.stream_to_file(out)


def summarise_for_speech(segments: list[Segment], max_chars: int = 600) -> str:
    """A cheap, model-free summary: first and last lines plus any line mentioning a service.
    The capstone uses the chat model for this; the lab keeps TTS cost separate from chat cost."""
    hits = [s.text for s in segments if any(v.lower() in s.text.lower() for v in SERVICE_VOCABULARY)]
    parts = [segments[0].text] + hits[:4] + ([segments[-1].text] if len(segments) > 1 else [])
    text = " ".join(dict.fromkeys(parts))  # de-dup, keep order
    return text[:max_chars]


# --------------------------------------------------------------------------- #
# Entry point
# --------------------------------------------------------------------------- #

def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser(description="Transcribe an incident recording and optionally speak a summary.")
    ap.add_argument("audio", nargs="?", help="path to m4a/mp3/wav/mp4")
    ap.add_argument("--speak", action="store_true", help="also synthesise a spoken summary to summary.mp3")
    ap.add_argument("--stt-model", default=os.environ.get("COPILOT_STT_MODEL", "whisper-1"))
    ap.add_argument("--tts-model", default=os.environ.get("COPILOT_TTS_MODEL", "tts-1"))
    ap.add_argument("--out", default="transcript.json")
    args = ap.parse_args(argv[1:])

    have_key = bool(os.environ.get("OPENAI_API_KEY"))
    try:
        import faster_whisper  # type: ignore # noqa: F401
        have_local = True
    except ImportError:
        have_local = False

    backend = "openai" if have_key else ("local" if have_local else None)
    print(f"vocabulary prompt ({len(VOCAB_PROMPT)} chars): {VOCAB_PROMPT[:90]}…")

    if not args.audio or not Path(args.audio).exists():
        print("no audio file given or file not found.")
        print("What this lab does with one: chunk at silences under 25 MB → transcribe each chunk with the vocabulary "
              "prompt and segment timestamps → offset-correct and stitch → write transcript.json → "
              "(--speak) synthesise a summary with jargon pronunciation fixes.")
        print(f"backend available: {backend or 'none (set OPENAI_API_KEY or pip install faster-whisper)'}")
        return 0
    if backend is None:
        print("OPENAI_API_KEY is not set and faster-whisper is not installed — skipping transcription.")
        return 0
    if backend == "openai":
        try:
            import openai  # noqa: F401
        except ImportError:
            print("The `openai` package is not installed: pip install openai>=1.40")
            return 0

    path = Path(args.audio)
    print(f"backend: {backend}  file: {path} ({path.stat().st_size / 1024 / 1024:.1f} MB)")
    segments = transcribe(path, backend, args.stt_model)

    print("\n--- transcript ---")
    for s in segments[:40]:
        print(f"[{fmt_ts(s.start)}] {s.text}")
    if len(segments) > 40:
        print(f"… {len(segments) - 40} more segments")

    Path(args.out).write_text(json.dumps([s.__dict__ for s in segments], indent=2), encoding="utf-8")
    print(f"\nwrote {args.out} ({len(segments)} segments). In the capstone this is chunked and indexed by "
          "internal/rag.Ingest with timestamps as metadata.")

    if args.speak:
        if not have_key:
            print("--speak needs OPENAI_API_KEY (no local TTS fallback in this lab).")
            return 0
        summary = summarise_for_speech(segments)
        speak(summary, Path("summary.mp3"), model=args.tts_model)
        print(f"\nspoke {len(summary)} chars → summary.mp3")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
