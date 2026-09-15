# Faithfulness scorer

The faithfulness scorer is hand-written Go code in the `evals` package, not an
imported eval library. It takes a golden answer, splits it into individual claims
at sentence boundaries, and checks each claim against the top five retrieved
chunks with one LLM-judge call per claim. Faithfulness is the fraction of claims
the judge marks as supported. The output records every claim, its verdict, the
judge's justification, the judge model name, and the prompt version
`faithfulness-v1`.

The judge prompt requires one or two sentences of evidence-based justification
before the verdict line, because asking for reasoning first makes the verdict more
reliable than asking for the score directly. The verdict parser checks the
negative forms REFUTED, NOT SUPPORTED, and UNSUPPORTED before the positive form so
a phrase like "not supported" is never read as supported.

Judge calls run through retry with exponential backoff and jitter from day one,
because parallel LLM-judge calls hitting timeouts and flaky errors is a known
real-world pitfall. Client errors (4xx) are never retried, except 429, which waits
for the server's Retry-After value. The scorer also reports retrieval precision
and recall at k=5 for every pair.
