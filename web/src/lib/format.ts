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
const relativeTimeFormatter = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });

const RELATIVE_TIME_DIVISIONS: { amount: number; unit: Intl.RelativeTimeFormatUnit }[] = [
  { amount: 60, unit: "second" },
  { amount: 60, unit: "minute" },
  { amount: 24, unit: "hour" },
  { amount: 7, unit: "day" },
  { amount: 4.34524, unit: "week" },
  { amount: 12, unit: "month" },
  { amount: Number.POSITIVE_INFINITY, unit: "year" },
];

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

export function formatRelativeTime(value: string | number | Date | undefined): string {
  if (value === undefined) return "";
  const time = toDate(value).getTime();
  if (Number.isNaN(time)) return "";
  let delta = (time - Date.now()) / 1000;
  for (const division of RELATIVE_TIME_DIVISIONS) {
    if (Math.abs(delta) < division.amount) {
      return relativeTimeFormatter.format(Math.round(delta), division.unit);
    }
    delta /= division.amount;
  }
  return "";
}
