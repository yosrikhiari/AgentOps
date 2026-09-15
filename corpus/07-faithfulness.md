# Faithfulness scorer

The faithfulness scorer is hand-written Go code, not an imported eval library.
It takes a generated answer, splits it into individual claims, and checks each
claim against the retrieved source chunks with an LLM-judge call. The output is
a score plus the evidence behind it: which claims passed, which failed, the
judge model name, and the prompt version.

The judge prompt requires justification before the number. Research shows
requiring evidence first improves judge reliability by 15 to 25 percent compared
to asking for the score directly.

Judge calls run through a scheduler with retry and backoff from day one, because
parallel LLM-judge calls hitting timeouts and flaky errors is a known real-world
pitfall. Client errors (4xx) are never retried. Results are cached by prompt
hash so reruns are cheap.
