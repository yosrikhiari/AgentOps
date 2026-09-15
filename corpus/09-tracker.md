# Task tracker

The task tracker keeps multi-step agent tasks alive through crashes. The toy agent
has three steps: Researcher retrieves the top three corpus chunks from pgvector,
Drafter writes a three-sentence brief with the fast model, and Reviewer approves
a non-empty draft.

It is built directly on Postgres with the standard library and the pgx driver,
not on a durable-execution framework. A `workflows` row holds the workflow id,
type, status, and original input; a `steps` row per step holds its sequence
number, name, status, output, and attempt count. Steps move from pending to
running to done, and the workflow row is marked done when the last step
finishes. The DBOS Go SDK was studied for its Workflow and Step shape and parked
as an alternative.

Resuming a workflow means re-running the same workflow id: steps already marked
done are skipped and their stored output is reused, the pending step runs again
with its attempt counter incremented, and the workflow's original input wins over
whatever input the resuming process was launched with. Killing the process with
`kill -9` mid-step therefore never repeats a completed step's side effect.
