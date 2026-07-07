const dateTimeFormatter = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "short",
});

const compactDateTimeFormatter = new Intl.DateTimeFormat(undefined, {
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
});

const clockTimeFormatter = new Intl.DateTimeFormat(undefined, {
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
});

function toDate(value: string | number | Date): Date {
  return value instanceof Date ? value : new Date(value);
}

export function formatDateTime(value: string | number | Date): string {
  return dateTimeFormatter.format(toDate(value));
}

export function formatCompactDateTime(value: string | number | Date): string {
  return compactDateTimeFormatter.format(toDate(value));
}

export function formatClockTime(value: string | number | Date): string {
  return clockTimeFormatter.format(toDate(value));
}
