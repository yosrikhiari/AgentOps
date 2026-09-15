# Tower console

The Tower console is the web interface served by the gateway at the root path. It is
three static files embedded in the binary, an HTML page, a stylesheet built on the Tower
design tokens, and a small JavaScript router, with no framework and no build step.

The overview page shows a status strip with the router state, the number of served models,
the latest faithfulness score and the p50 latency, then a row of metric cards for requests
in the last hour, p50 and p99 latency, faithfulness with its delta and the judge cost. Below
it a bar chart draws the last 30 requests with bar height equal to latency and amber bars
for the quality tier, a backend health table, and a table of recent requests with chips for
sensitive, fallback and error. The trace inspector renders every span of a trace as an
expandable step. The evals page charts every run of a golden version against the alert
threshold, lists the worst cases and runs, and offers a Run eval suite button. The
workflows page starts the Researcher, Drafter, Reviewer workflow, lists workflows with a
pill per step showing the attempt count, and offers Resume on unfinished ones.

The console reads the endpoints /v1/overview, /v1/requests, /v1/evals/runs and
/v1/workflows, all of which derive their data from spans and the eval and workflow tables.
Every page cancels its fetches when the user navigates away, retries once on a 5xx
response, shows an inline error with a Retry button when the store is unavailable, and
refreshes data in place without replacing a field the user is typing in.
