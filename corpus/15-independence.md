# Independence rule

No competing whole product is wrapped: no TensorZero, no LiteLLM, no DeepEval
as a dependency, no Helicone. Existing tools are studied for architecture ideas
and reimplemented where the CV story lives: routing decisions, faithfulness
scoring, durable tracking, and the trace layer.

Small focused infrastructure primitives are fine to use, the same way using a
Postgres driver is not cheating. The DBOS Go SDK for durable execution, the
pgx driver for Postgres, and Prometheus and Grafana as observability
infrastructure all fall on the allowed side.

The test is simple: does this library do the thing the CV story is about? If
yes, write it yourself. If no, use the well-built free thing. Prometheus and
Grafana may be used as-is because they are infrastructure, not the product.
