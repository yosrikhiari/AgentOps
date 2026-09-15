# Golden set

The golden set is the ground truth the fact-checker scores against. It lives in
`evals/golden/v1.jsonl` with one JSON object per line holding a question, the
expected answer, and the ids of the source documents that support it.

It is built the standard way used by DeepEval's Synthesizer and RAGAS testset
generation: an LLM drafts 20 to 30 question-answer pairs from the project's own
source documents, then a human reviews and corrects every single pair by hand
before it is frozen. The file hash is recorded so any later change is visible.

A bad golden set silently makes every later eval score meaningless, so the
manual review step is not optional. Any document change bumps the version to v2
and reruns the full suite.
