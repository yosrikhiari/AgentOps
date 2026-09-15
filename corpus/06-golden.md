# Golden set

The golden set is the ground truth the fact-checker scores against. It lives in
`evals/golden/` as one JSON object per line holding a question, the expected
answer, and the ids of the source documents that support it. Each frozen version
has its SHA-256 hash stored next to it, for example `v2.jsonl` and `v2.sha256`.
Version 2 holds 48 pairs and scored a faithfulness of 1.000.

It is built the standard way used by DeepEval's Synthesizer and RAGAS testset
generation: a local LLM drafts two question-answer pairs per corpus chunk, then a
human reviews and corrects every single pair by hand before it is frozen with
`--freeze-golden`. Freezing rejects empty fields, unknown document ids, duplicate
questions, any answer that yields no scorable claim, and any answer that opens with a
subordinating word such as because or since.

Every golden answer must be a full declarative sentence with its own main clause,
because the scorer splits answers into claims of at least ten characters and the judge
never sees the question. A bad golden set silently makes every later eval score
meaningless, so the manual review step is not optional. Any document change bumps the
version and reruns the full suite.
