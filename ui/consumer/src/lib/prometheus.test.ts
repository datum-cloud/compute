import { afterEach, beforeEach, describe, expect, test } from 'bun:test';
import {
  fetchPrometheusLabelValues,
  metricErrorMessage,
  O11Y_METRICS_API_PATH,
  PrometheusError,
  queryInstant,
  queryRange,
  shouldRetryMetrics,
} from './prometheus';
import { formatForCard, formatForChart, formatValue, seriesColor, seriesName } from './prometheus-format';

const PROJECT_ID = 'proj-a';
const BASE = `/api/proxy/apis/resourcemanager.miloapis.com/v1alpha1/projects/${PROJECT_ID}/control-plane${O11Y_METRICS_API_PATH}`;

const realFetch = globalThis.fetch;
let calls: Array<{ url: string; init?: RequestInit }> = [];

function stubFetch(respond: (url: string, init?: RequestInit) => Response): void {
  globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
    const url = String(input);
    calls.push({ url, init });
    return respond(url, init);
  }) as typeof fetch;
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status });
}

const START = new Date('2024-01-01T00:00:00.000Z');
const END = new Date('2024-01-01T01:00:00.000Z');
const START_S = START.getTime() / 1000;
const END_S = END.getTime() / 1000;

const matrix = {
  status: 'success',
  data: {
    resultType: 'matrix',
    result: [
      {
        metric: { resource_name: 'web-0' },
        values: [
          [START_S, '0.5'],
          [START_S + 60, '0.75'],
        ],
      },
    ],
  },
};

const vector = {
  status: 'success',
  data: { resultType: 'vector', result: [{ metric: {}, value: [END_S, '1536'] }] },
};

beforeEach(() => {
  calls = [];
});

afterEach(() => {
  globalThis.fetch = realFetch;
});

describe('queryRange', () => {
  test('POSTs a form-encoded range query to the project-scoped queryapi path', async () => {
    stubFetch(() => json(matrix));

    const data = await queryRange(PROJECT_ID, 'up', { start: START, end: END }, '1m');

    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe(`${BASE}/query_range`);
    expect(calls[0].init?.method).toBe('POST');
    expect(new Headers(calls[0].init?.headers).get('Content-Type')).toBe(
      'application/x-www-form-urlencoded'
    );
    expect(calls[0].init?.body).toBe(`query=up&start=${START_S}&end=${END_S}&step=1m`);
    expect(data.series).toHaveLength(1);
    expect(data.series[0].name).toBe('web-0');
    expect(data.series[0].data.map((point) => point.value)).toEqual([0.5, 0.75]);
    expect(data.timeRange).toEqual({ start: START_S, end: END_S });
  });
});

describe('queryInstant', () => {
  test('POSTs an instant query at the given time and formats the card', async () => {
    stubFetch(() => json(vector));

    const card = await queryInstant(PROJECT_ID, 'sum(mem)', 'bytes', END);

    expect(calls[0].url).toBe(`${BASE}/query`);
    expect(calls[0].init?.body).toBe(`query=sum%28mem%29&time=${END_S}`);
    expect(card.value).toBe(1536);
    expect(card.formattedValue).toBe('1.5 KB');
  });

  test('omits time when none is given', async () => {
    stubFetch(() => json(vector));

    await queryInstant(PROJECT_ID, 'up', 'number');

    expect(calls[0].init?.body).toBe('query=up');
  });
});

describe('fetchPrometheusLabelValues', () => {
  test('GETs label values with a match[] selector', async () => {
    stubFetch(() => json({ status: 'success', data: ['web-0', 'web-1'] }));

    const values = await fetchPrometheusLabelValues(PROJECT_ID, 'resource_name', '{job="x"}');

    expect(calls[0].url).toBe(
      `${BASE}/label/resource_name/values?match%5B%5D=%7Bjob%3D%22x%22%7D`
    );
    expect(calls[0].init?.method).toBe('GET');
    expect(values).toEqual(['web-0', 'web-1']);
  });
});

describe('errors', () => {
  test('keeps the HTTP status and queryapi error text', async () => {
    stubFetch(() => json({ status: 'error', errorType: 'bad_data', error: 'parse error' }, 400));

    const error = await queryInstant(PROJECT_ID, 'sum(', 'number').catch((e: unknown) => e);

    expect(error).toBeInstanceOf(PrometheusError);
    expect((error as PrometheusError).status).toBe(400);
    expect((error as PrometheusError).message).toBe('parse error');
  });

  test('reads a Kubernetes Status message on 403', async () => {
    stubFetch(() => json({ kind: 'Status', message: 'forbidden' }, 403));

    const error = await queryInstant(PROJECT_ID, 'up', 'number').catch((e: unknown) => e);

    expect((error as PrometheusError).status).toBe(403);
    expect((error as PrometheusError).message).toBe('forbidden');
  });

  test('treats an error envelope on a 200 as a bad query', async () => {
    stubFetch(() => json({ status: 'error', error: 'boom' }));

    const error = await queryInstant(PROJECT_ID, 'up', 'number').catch((e: unknown) => e);

    expect((error as PrometheusError).status).toBe(400);
  });

  test.each([400, 401, 403, 422, 503, 504])('does not retry a %d', (status) => {
    expect(shouldRetryMetrics(0, new PrometheusError('x', status))).toBe(false);
  });

  test('retries other failures twice', () => {
    const error = new PrometheusError('bad gateway', 502);
    expect(shouldRetryMetrics(1, error)).toBe(true);
    expect(shouldRetryMetrics(2, error)).toBe(false);
  });

  test('explains 422 and 503 in the chart message', () => {
    expect(metricErrorMessage(new PrometheusError('x', 422))).toBe(
      'Too much data to show. Try a shorter time range'
    );
    expect(metricErrorMessage(new PrometheusError('x', 503))).toBe(
      'Took too long to load. Try a shorter time range'
    );
    expect(metricErrorMessage(new PrometheusError('x', 500))).toBe('Unable to load metrics');
  });
});

describe('formatter', () => {
  test('names series from label values, or unknown / Series', () => {
    expect(seriesName({ region: 'us-east', pod: 'web-0' })).toBe('us-east - web-0');
    expect(seriesName({ region: '' })).toBe('unknown');
    expect(seriesName({})).toBe('Series');
  });

  test('keeps the portal palette, then generates colors', () => {
    expect(seriesColor(0)).toBe('#8884d8');
    expect(seriesColor(12)).toMatch(/^hsl\(/);
  });

  test('formats an empty result as no series', () => {
    expect(formatForChart(undefined, { start: 1, end: 2 })).toEqual({
      series: [],
      timeRange: { start: 1, end: 2 },
    });
  });

  test('uses the latest matrix sample for a card', () => {
    const card = formatForCard(
      {
        resultType: 'matrix',
        result: [
          {
            metric: {},
            values: [
              [1, '1'],
              [2, '3'],
            ],
          },
        ],
      },
      'number'
    );
    expect(card.value).toBe(3);
    expect(card.timestamp).toBe(2000);
  });

  test('returns 0 for a card with no result', () => {
    expect(formatForCard({ resultType: 'vector', result: [] }, 'percent').formattedValue).toBe(
      '0.00%'
    );
  });

  test('formats values like the portal', () => {
    expect(formatValue(0.125, 'percent')).toBe('12.50%');
    expect(formatValue(2048, 'bytes')).toBe('2 KB');
    expect(formatValue(3.5, 'requestsPerSecond')).toBe('3.50 req/s');
    expect(formatValue(1500, 'milliseconds-auto')).toBe('1.50s');
    expect(formatValue(42, 'milliseconds-auto')).toBe('42ms');
  });
});
