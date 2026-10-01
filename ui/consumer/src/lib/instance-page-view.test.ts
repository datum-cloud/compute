import { describe, expect, test } from 'bun:test';
import { instancePageView } from './instance-page-view';

describe('instancePageView', () => {
  test('is loading on the first fetch', () => {
    expect(instancePageView({ isLoading: true, hasInstance: false, errorStatus: undefined })).toBe(
      'loading'
    );
  });

  test('is an error when the first fetch fails', () => {
    expect(instancePageView({ isLoading: false, hasInstance: false, errorStatus: 500 })).toBe(
      'error'
    );
  });

  test('keeps the page when a refresh fails transiently', () => {
    expect(instancePageView({ isLoading: false, hasInstance: true, errorStatus: 503 })).toBe(
      'ready'
    );
    expect(instancePageView({ isLoading: false, hasInstance: true, errorStatus: 0 })).toBe('ready');
  });

  test('is an error once a refresh finds the instance deleted', () => {
    expect(instancePageView({ isLoading: false, hasInstance: true, errorStatus: 404 })).toBe(
      'error'
    );
  });
});
