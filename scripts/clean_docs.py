#!/usr/bin/env python3
"""Minimal corpus cleaner: markdown docs -> clean chunk jsonl.

Reads corpus/*.md, normalizes text, splits into overlapping chunks,
deduplicates by SHA hash, writes evals/corpus/clean/*.jsonl rows:
{doc_id, hash, text, source}. Reruns are idempotent.
"""

import hashlib
import json
import re
import sys
from pathlib import Path

CHUNK_SIZE = 900
CHUNK_OVERLAP = 120
MIN_CHUNK = 200

ROOT = Path(__file__).resolve().parent.parent
CORPUS = ROOT / "corpus"
OUT_DIR = ROOT / "evals" / "corpus" / "clean"


def normalize(text):
    text = text.replace("\r\n", "\n")
    text = re.sub(r"^#{1,6}\s+", "", text, flags=re.MULTILINE)
    text = re.sub(r"```.*?```", " ", text, flags=re.DOTALL)
    text = re.sub(r"^\s*[-*+]\s+", "", text, flags=re.MULTILINE)  # list bullets
    # keep `_` and `-` so identifiers like router_requests_total / nomic-embed-text survive
    text = re.sub(r"[`*>\[\]()#|]", " ", text)
    text = re.sub(r"[ \t]+", " ", text)
    text = re.sub(r"\n{3,}", "\n\n", text)
    return text.strip()


def chunk(text):
    paras = [p.strip() for p in text.split("\n\n") if p.strip()]
    chunks, buf = [], ""
    for p in paras:
        candidate = (buf + "\n\n" + p).strip() if buf else p
        if len(candidate) <= CHUNK_SIZE:
            buf = candidate
            continue
        if buf:
            chunks.append(buf)
        while len(p) > CHUNK_SIZE:
            chunks.append(p[:CHUNK_SIZE])
            p = p[CHUNK_SIZE - CHUNK_OVERLAP :]
        buf = p
    if buf:
        chunks.append(buf)
    return [c for c in chunks if len(c) >= MIN_CHUNK]


def main():
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    for stale in OUT_DIR.glob("*.jsonl"):  # full rebuild: a changed doc must not keep old chunks
        stale.unlink()
    docs = sorted(CORPUS.glob("*.md"))
    if not docs:
        print("no docs in corpus/", file=sys.stderr)
        return 1
    seen, written, dropped = set(), 0, 0
    for doc in docs:
        for c in chunk(normalize(doc.read_text(encoding="utf-8"))):
            h = hashlib.sha256(c.encode()).hexdigest()[:16]
            if h in seen:
                dropped += 1
                continue
            seen.add(h)
            row = {"doc_id": doc.stem, "hash": h, "text": c, "source": doc.name}
            with open(OUT_DIR / f"{doc.stem}.jsonl", "a", encoding="utf-8") as f:
                f.write(json.dumps(row, ensure_ascii=False) + "\n")
            written += 1
    for f in OUT_DIR.glob("*.jsonl"):
        lines = sorted(set(f.read_text(encoding="utf-8").splitlines()))
        f.write_text("\n".join(lines) + "\n", encoding="utf-8")
    print(f"docs={len(docs)} chunks={written} dedup_dropped={dropped}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
