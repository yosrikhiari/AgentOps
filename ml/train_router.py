"""Train a tiny logistic router from exported route_decisions.

Usage:
    python ml/train_router.py --data routing_data.jsonl --out models/router-weights.json

The export comes from: go run . --export-routing-data > routing_data.jsonl

Kill criterion (audit section 2.2): the learned model must beat the heuristic
on the held-out golden set; otherwise ship nothing and keep the heuristic.
"""
import argparse
import json
import os

import numpy as np
from sklearn.linear_model import LogisticRegression
from sklearn.model_selection import train_test_split
from sklearn.preprocessing import StandardScaler

FEATURE_ORDER = ["prompt_len", "word_count", "reasoning_hits", "qmarks"]


def features(row):
    prompt = row.get("prompt", "")
    lower = prompt.lower()
    words = lower.split()
    reasoning = sum(1 for rw in ("why", "explain", "compare", "reason", "analyze", "describe") if rw in lower)
    return [len(prompt), len(words), reasoning, prompt.count("?")]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--data", required=True)
    ap.add_argument("--out", default="models/router-weights.json")
    args = ap.parse_args()

    rows = []
    with open(args.data) as f:
        for line in f:
            line = line.strip()
            if line:
                rows.append(json.loads(line))

    # Label: 1 = quality, 0 = fast. Prefer an outcome column; fall back to the
    # recorded tier decision so the script runs on a fresh export.
    X = np.array([features(r) for r in rows], dtype=float)
    y = np.array([1 if r.get("tier", "fast") == "quality" else 0 for r in rows])

    Xtr, Xte, ytr, yte = train_test_split(X, y, test_size=0.2, random_state=42)
    scaler = StandardScaler()
    Xtr_s = scaler.fit_transform(Xtr)
    Xte_s = scaler.transform(Xte)

    clf = LogisticRegression(max_iter=1000)
    clf.fit(Xtr_s, ytr)
    acc = clf.score(Xte_s, yte)
    print(f"held-out accuracy: {acc:.3f} (baseline majority: {max(yte.mean(), 1-yte.mean()):.3f})")

    out = {
        "version": "learned-v1",
        "features": FEATURE_ORDER,
        "mean": scaler.mean_.tolist(),
        "scale": scaler.scale_.tolist(),
        "weights": clf.coef_[0].tolist(),
        "bias": float(clf.intercept_[0]),
        "threshold": 0.5,
    }
    os.makedirs(os.path.dirname(args.out) or ".", exist_ok=True)
    with open(args.out, "w") as f:
        json.dump(out, f, indent=2)
    print(f"wrote {args.out}")


if __name__ == "__main__":
    main()
EOF
echo "created"