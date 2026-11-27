// k6 mixed-workload gate for Tower.
// Exercises chat, workflow, metrics, and traces endpoints under load.
// Run: k6 run load-tests/mixed.js
// Gate: p95 latency < 500ms, error rate < 1%, all endpoints reachable.

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Trend } from 'k6/metrics';

const errorRate = new Rate('errors');
const chatLatency = new Trend('chat_latency');
const metricsLatency = new Trend('metrics_latency');

export const options = {
  stages: [
    { duration: '30s', target: 10 },   // ramp up
    { duration: '60s', target: 20 },   // sustained
    { duration: '30s', target: 0 },    // ramp down
  ],
  thresholds: {
    http_req_duration: ['p(95)<500'],
    errors: ['rate<0.01'],
    http_req_failed: ['rate<0.01'],
  },
};

const BASE = __ENV.BASE_URL || 'http://127.0.0.1:8080';

export default function () {
  // 1. Chat request (the hot path)
  const chatRes = http.post(
    BASE + '/v1/chat/completions',
    JSON.stringify({ prompt: 'What is the router?', model: 'auto' }),
    { headers: { 'Content-Type': 'application/json' } }
  );
  check(chatRes, {
    'chat: status 200': (r) => r.status === 200,
    'chat: has text': (r) => r.json('text') !== undefined || r.json('choices') !== undefined,
  });
  errorRate.add(chatRes.status !== 200);
  chatLatency.add(chatRes.timings.duration);

  sleep(0.5);

  // 2. Metrics endpoint
  const metricsRes = http.get(BASE + '/metrics');
  check(metricsRes, {
    'metrics: status 200': (r) => r.status === 200,
  });
  errorRate.add(metricsRes.status !== 200);
  metricsLatency.add(metricsRes.timings.duration);

  sleep(0.5);

  // 3. Overview endpoint
  const overviewRes = http.get(BASE + '/v1/overview');
  check(overviewRes, {
    'overview: status 200': (r) => r.status === 200,
  });
  errorRate.add(overviewRes.status !== 200);

  sleep(0.5);

  // 4. Traces list
  const tracesRes = http.get(BASE + '/v1/traces?limit=10');
  check(tracesRes, {
    'traces: status 200': (r) => r.status === 200,
  });
  errorRate.add(tracesRes.status !== 200);

  sleep(1);
}
