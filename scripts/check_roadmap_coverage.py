#!/usr/bin/env python3
"""Fail if any roadmap.sh AI Engineer node lacks a concept card.

Reads modules/ROADMAP-NODES.md (the node → module map) and checks each module
README for a `### <node>` heading. Used by `make docs-check` and CI.
"""
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
nodes_md = (ROOT / "modules" / "ROADMAP-NODES.md").read_text(encoding="utf-8")

missing, total = [], 0
for section in re.split(r"^## ", nodes_md, flags=re.M)[1:]:
    header, _, body = section.partition("\n")
    m = re.search(r"\(`(modules/[^`]+)/`\)", header)
    if not m:
        continue
    readme = ROOT / m.group(1) / "README.md"
    text = readme.read_text(encoding="utf-8") if readme.exists() else ""
    headings = {h.strip() for h in re.findall(r"^###\s+(.+?)\s*$", text, flags=re.M)}
    for node in [n.strip() for n in body.replace("\n", " ").split("·") if n.strip()]:
        if node.startswith("(plus:"):
            continue
        total += 1
        if node not in headings:
            missing.append(f"{readme.relative_to(ROOT)}: {node}")

print(f"roadmap nodes: {total}, missing cards: {len(missing)}")
for x in missing:
    print("  MISSING", x)
sys.exit(1 if missing else 0)
