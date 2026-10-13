// Due-date helpers for the todo dialog and cards. The dialog edits a local
// wall-clock value (<input type="datetime-local">); the API speaks RFC 3339 UTC.
const pad2 = (n) => String(n).padStart(2, "0");
/** RFC 3339 -> "YYYY-MM-DDTHH:mm" in the viewer's local time ("" when empty/invalid). */
export function isoToLocalInputValue(iso) {
    if (!iso)
        return "";
    const d = new Date(iso);
    if (Number.isNaN(d.getTime()))
        return "";
    return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}T${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}
/** Local "YYYY-MM-DDTHH:mm" -> RFC 3339 UTC (null when empty/invalid). */
export function localInputValueToIso(value) {
    const v = value.trim();
    if (!v)
        return null;
    const d = new Date(v);
    if (Number.isNaN(d.getTime()))
        return null;
    return d.toISOString();
}
/**
 * Decides what to send for dueAt. `undefined` means "leave it alone": the input
 * only has minute precision, so re-submitting an unchanged value would silently
 * truncate seconds set through the API.
 */
export function resolveDueAtForSubmit(mode, currentValue, baselineIso) {
    if (mode === "create") {
        return currentValue === "" ? undefined : (localInputValueToIso(currentValue) ?? undefined);
    }
    if (currentValue === isoToLocalInputValue(baselineIso))
        return undefined;
    return currentValue === "" ? null : (localInputValueToIso(currentValue) ?? undefined);
}
/** True when the due date has passed and the card is not in a done column. */
export function isOverdue(iso, inDoneColumn, nowMs = Date.now()) {
    if (!iso || inDoneColumn)
        return false;
    const ms = new Date(iso).getTime();
    return !Number.isNaN(ms) && ms < nowMs;
}
/** Short, locale-aware label for a card badge, e.g. "Nov 1". */
export function formatDueBadge(iso) {
    const d = new Date(iso);
    if (Number.isNaN(d.getTime()))
        return "";
    return d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}
/**
 * A half-typed datetime-local reports value "" with validity.badInput set. That must not be
 * read as "the user cleared the date", so callers block the submit instead.
 */
export function hasIncompleteDueInput(input) {
    return input?.validity?.badInput === true;
}
