import http from 'k6/http';
import { check } from 'k6';

export const options = {
  scenarios: {
    overhead: {
      executor: 'constant-arrival-rate',
      rate: 50,
      timeUnit: '1s',
      duration: '1m',
      preAllocatedVUs: 10,
      exec: 'health',
    },
    chat: {
      executor: 'per-vu-iterations',
      vus: 1,
      iterations: 2,
      exec: 'chat',
    },
  },
  thresholds: {
    'http_req_failed{scenario:overhead}': ['rate<0.01'],
    'http_req_duration{scenario:overhead}': ['p(99)<100'],
    'checks{scenario:chat}': ['rate>0.99'],
  },
};

const BASE = __ENV.BASE_URL || 'http://localhost:8080';

export function health() {
  http.get(`${BASE}/health`);
}

export function chat() {
  const res = http.post(
    `${BASE}/v1/chat/completions`,
    JSON.stringify({ prompt: 'k6 smoke test, reply in one word' }),
    { headers: { 'Content-Type': 'application/json' }, timeout: '120s' }
  );
  check(res, { 'chat 200': (r) => r.status === 200 });
}
