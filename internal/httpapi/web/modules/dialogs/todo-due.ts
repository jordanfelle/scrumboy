// Due-date helpers for the todo dialog and cards. The dialog edits a local
// wall-clock value (<input type="datetime-local">); the API speaks RFC 3339 UTC.

const pad2 = (n: number): string => String(n).padStart(2, "0");

/** RFC 3339 -> "YYYY-MM-DDTHH:mm" in the viewer's local time ("" when empty/invalid). */
export function isoToLocalInputValue(iso: string | null | undefined): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}T${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

/** Local "YYYY-MM-DDTHH:mm" -> RFC 3339 UTC (null when empty/invalid). */
export function localInputValueToIso(value: string): string | null {
  const v = value.trim();
  if (!v) return null;
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) return null;
  return d.toISOString();
}

/**
 * Decides what to send for dueAt. `undefined` means "leave it alone": the input
 * only has minute precision, so re-submitting an unchanged value would silently
 * truncate seconds set through the API.
 */
export function resolveDueAtForSubmit(
  mode: "create" | "edit",
  currentValue: string,
  baselineIso: string | null | undefined,
): string | null | undefined {
  if (mode === "create") {
    return currentValue === "" ? undefined : (localInputValueToIso(currentValue) ?? undefined);
  }
  if (currentValue === isoToLocalInputValue(baselineIso)) return undefined;
  return currentValue === "" ? null : (localInputValueToIso(currentValue) ?? undefined);
}

/**
 * Resolves the due instant used by live overdue state. Keep the stored instant
 * while the minute-precision field is unchanged so seconds are not truncated.
 */
export function resolveDueAtForOverdueCheck(
  currentValue: string,
  baselineIso: string | null | undefined,
): string | null {
  if (currentValue === isoToLocalInputValue(baselineIso)) return baselineIso ?? null;
  return localInputValueToIso(currentValue);
}

/** True when the due date has passed and the card is not in a done column. */
export function isOverdue(iso: string | null | undefined, inDoneColumn: boolean, nowMs: number = Date.now()): boolean {
  if (!iso || inDoneColumn) return false;
  const ms = new Date(iso).getTime();
  return !Number.isNaN(ms) && ms < nowMs;
}

/** Short, locale-aware label for a card badge, e.g. "Nov 1". */
export function formatDueBadge(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

const WEEKDAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"] as const;
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"] as const;

function ordinalDay(day: number): string {
  const lastTwo = day % 100;
  if (lastTwo >= 11 && lastTwo <= 13) return `${day}th`;
  switch (day % 10) {
    case 1: return `${day}st`;
    case 2: return `${day}nd`;
    case 3: return `${day}rd`;
    default: return `${day}th`;
  }
}

/** Full English-style date for the overdue warning, using the viewer's local calendar date. */
export function formatDueDateTooltip(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return `${WEEKDAYS[d.getDay()]}, ${MONTHS[d.getMonth()]} ${ordinalDay(d.getDate())} ${d.getFullYear()}`;
}

export type TodoOverdueState = {
  mode: "create" | "edit";
  dueAt: string | null | undefined;
  archived: boolean;
  inDoneColumn: boolean;
  nowMs?: number;
};

export function shouldShowTodoOverdue(state: TodoOverdueState): boolean {
  return state.mode === "edit"
    && !state.archived
    && isOverdue(state.dueAt, state.inDoneColumn, state.nowMs);
}

/** Applies reusable dialog state without allowing a previous todo's warning to leak. */
export function setTodoOverdueIndicatorVisibility(
  indicator: HTMLElement | null | undefined,
  state: TodoOverdueState,
): void {
  if (indicator) indicator.hidden = !shouldShowTodoOverdue(state);
}

/**
 * A half-typed datetime-local reports value "" with validity.badInput set. That must not be
 * read as "the user cleared the date", so callers block the submit instead.
 */
export function hasIncompleteDueInput(input: { validity?: { badInput?: boolean } } | null | undefined): boolean {
  return input?.validity?.badInput === true;
}
