# Task tracker

The task tracker keeps multi-step agent tasks alive through crashes. The toy
agent has three steps: Researcher retrieves context, Drafter writes with the
fast model, Reviewer checks with the local judge.

It is built directly on the DBOS Go SDK (`dbos-transact-golang`), a small
Postgres-native durable-execution library. Agent steps are decorated as DBOS
steps inside DBOS workflows, and state persists in Postgres tables, so killing
the process with `kill -9` mid-task and restarting resumes from the last
checkpoint instead of restarting from scratch.

Workflow code must stay deterministic: no direct I/O, clock reads, or randomness
inside the workflow body. All side effects go through steps. Every step carries
an idempotency key so a retried step never applies its effect twice.
