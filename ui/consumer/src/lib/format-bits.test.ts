import { describe, expect, test } from 'bun:test';
import { formatBitsPerSecond } from './format-bits';

describe('formatBitsPerSecond', () => {
  test('formats SI units', () => {
    expect(formatBitsPerSecond(0)).toBe('0 b/s');
    expect(formatBitsPerSecond(950)).toBe('950 b/s');
    expect(formatBitsPerSecond(1500)).toBe('1.50 kb/s');
    expect(formatBitsPerSecond(12_500_000)).toBe('12.5 Mb/s');
    expect(formatBitsPerSecond(2_000_000_000)).toBe('2.00 Gb/s');
  });

  test('rounds bare values and scales at the 1000 boundary', () => {
    expect(formatBitsPerSecond(0.4)).toBe('0 b/s');
    expect(formatBitsPerSecond(1000)).toBe('1.00 kb/s');
    expect(formatBitsPerSecond(150_000)).toBe('150 kb/s');
    expect(formatBitsPerSecond(3e12)).toBe('3.00 Tb/s');
    expect(formatBitsPerSecond(5e15)).toBe('5000 Tb/s');
  });

  test('shows the chart placeholder for non-finite values', () => {
    expect(formatBitsPerSecond(NaN)).toBe('—');
    expect(formatBitsPerSecond(Infinity)).toBe('—');
    expect(formatBitsPerSecond(-Infinity)).toBe('—');
  });

  test('clamps negatives to zero', () => {
    expect(formatBitsPerSecond(-0.4)).toBe('0 b/s');
    expect(formatBitsPerSecond(-1500)).toBe('0 b/s');
  });
});
