export function arrayFrom<T>(value: unknown): T[] {
  return Array.isArray(value) ? value as T[] : [];
}

export function objectFrom<T extends Record<string, unknown>>(value: unknown): Partial<T> {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Partial<T> : {};
}

export function booleanFrom(value: unknown, fallback = false): boolean {
  return typeof value === "boolean" ? value : fallback;
}
