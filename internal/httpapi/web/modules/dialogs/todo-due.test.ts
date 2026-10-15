import { describe, expect, it } from "vitest";
import {
  formatDueBadge,
  formatDueDateTooltip,
  hasIncompleteDueInput,
  isOverdue,
  isoToLocalInputValue,
  localInputValueToIso,
  resolveDueAtForOverdueCheck,
  resolveDueAtForSubmit,
  setTodoOverdueIndicatorVisibility,
  shouldShowTodoOverdue,
} from "./todo-due";

describe("todo due date helpers", () => {
  it("round-trips a local input value through RFC 3339", () => {
    const local = "2026-11-01T17:30";
    const iso = localInputValueToIso(local);
    expect(iso).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/);
    expect(isoToLocalInputValue(iso)).toBe(local);
  });

  it("treats empty and invalid input as no date", () => {
    expect(isoToLocalInputValue(null)).toBe("");
    expect(isoToLocalInputValue(undefined)).toBe("");
    expect(isoToLocalInputValue("not a date")).toBe("");
    expect(localInputValueToIso("")).toBeNull();
    expect(localInputValueToIso("   ")).toBeNull();
    expect(localInputValueToIso("garbage")).toBeNull();
  });

  describe("resolveDueAtForSubmit", () => {
    const baseline = "2026-11-01T17:30:45.000Z"; // has seconds the input cannot show

    it("omits dueAt on create when empty, sends it when set", () => {
      expect(resolveDueAtForSubmit("create", "", null)).toBeUndefined();
      expect(resolveDueAtForSubmit("create", "2026-11-01T17:30", null)).toBe(localInputValueToIso("2026-11-01T17:30"));
    });

    it("omits dueAt on edit when the field is unchanged (preserves seconds)", () => {
      expect(resolveDueAtForSubmit("edit", isoToLocalInputValue(baseline), baseline)).toBeUndefined();
      expect(resolveDueAtForSubmit("edit", "", null)).toBeUndefined();
    });

    it("sends null to clear and an ISO value to change", () => {
      expect(resolveDueAtForSubmit("edit", "", baseline)).toBeNull();
      expect(resolveDueAtForSubmit("edit", "2027-01-02T03:04", baseline)).toBe(localInputValueToIso("2027-01-02T03:04"));
      expect(resolveDueAtForSubmit("edit", "2027-01-02T03:04", null)).toBe(localInputValueToIso("2027-01-02T03:04"));
    });
  });

  it("preserves stored deadline precision until the local input is edited", () => {
    const baseline = "2026-11-01T12:00:45.500Z";
    const unchangedInput = isoToLocalInputValue(baseline);
    const nowMs = Date.parse("2026-11-01T12:00:15.000Z");

    const unchanged = resolveDueAtForOverdueCheck(unchangedInput, baseline);
    expect(unchanged).toBe(baseline);
    expect(isOverdue(unchanged, false, nowMs)).toBe(false);
    expect(isOverdue(localInputValueToIso(unchangedInput), false, nowMs)).toBe(true);

    const editedInput = isoToLocalInputValue("2026-11-01T11:59:45.500Z");
    expect(resolveDueAtForOverdueCheck(editedInput, baseline)).toBe(localInputValueToIso(editedInput));
    expect(resolveDueAtForOverdueCheck("", baseline)).toBeNull();
  });

  describe("isOverdue", () => {
    const now = Date.parse("2026-11-01T12:00:00Z");
    it("is overdue only when past and not in a done column", () => {
      expect(isOverdue("2026-11-01T11:00:00Z", false, now)).toBe(true);
      expect(isOverdue("2026-11-01T13:00:00Z", false, now)).toBe(false);
      expect(isOverdue("2026-11-01T11:00:00Z", true, now)).toBe(false);
      expect(isOverdue(null, false, now)).toBe(false);
      expect(isOverdue("nope", false, now)).toBe(false);
    });
  });

  it("formats a short badge label and tolerates bad input", () => {
    expect(formatDueBadge("2026-11-01T12:00:00Z")).not.toBe("");
    expect(formatDueBadge("nope")).toBe("");
  });

  it("formats the complete tooltip date in the viewer's local calendar", () => {
    const localNoon = new Date(2026, 2, 13, 12, 0, 0);
    expect(formatDueDateTooltip(localNoon.toISOString())).toBe("Fri, Mar 13th 2026");
    expect(formatDueDateTooltip("nope")).toBe("");
  });

  it.each([
    [1, "1st"], [2, "2nd"], [3, "3rd"], [4, "4th"],
    [11, "11th"], [12, "12th"], [13, "13th"],
    [21, "21st"], [22, "22nd"], [23, "23rd"],
  ])("formats ordinal day %i as %s", (day, suffix) => {
    const localNoon = new Date(2026, 0, day as number, 12, 0, 0);
    expect(formatDueDateTooltip(localNoon.toISOString())).toContain(suffix as string);
  });

  it("shows dialog overdue state only for active, open, overdue edits", () => {
    const nowMs = Date.parse("2026-11-01T12:00:00Z");
    const base = { mode: "edit" as const, dueAt: "2026-11-01T11:00:00Z", archived: false, inDoneColumn: false, nowMs };
    expect(shouldShowTodoOverdue(base)).toBe(true);
    expect(shouldShowTodoOverdue({ ...base, mode: "create" })).toBe(false);
    expect(shouldShowTodoOverdue({ ...base, archived: true })).toBe(false);
    expect(shouldShowTodoOverdue({ ...base, inDoneColumn: true })).toBe(false);
    expect(shouldShowTodoOverdue({ ...base, dueAt: "2026-11-01T13:00:00Z" })).toBe(false);
    expect(shouldShowTodoOverdue({ ...base, dueAt: null })).toBe(false);
  });

  it("resets a reused dialog indicator as todo state changes", () => {
    const indicator = { hidden: true } as HTMLElement;
    const nowMs = Date.parse("2026-11-01T12:00:00Z");
    setTodoOverdueIndicatorVisibility(indicator, {
      mode: "edit", dueAt: "2026-11-01T11:00:00Z", archived: false, inDoneColumn: false, nowMs,
    });
    expect(indicator.hidden).toBe(false);
    setTodoOverdueIndicatorVisibility(indicator, {
      mode: "edit", dueAt: "2026-11-01T11:00:00Z", archived: false, inDoneColumn: true, nowMs,
    });
    expect(indicator.hidden).toBe(true);
    setTodoOverdueIndicatorVisibility(indicator, {
      mode: "create", dueAt: null, archived: false, inDoneColumn: false, nowMs,
    });
    expect(indicator.hidden).toBe(true);
  });

  it("flags a half-edited date input so it is not mistaken for a cleared date", () => {
    expect(hasIncompleteDueInput({ validity: { badInput: true } })).toBe(true);
    expect(hasIncompleteDueInput({ validity: { badInput: false } })).toBe(false);
    expect(hasIncompleteDueInput({})).toBe(false);
    expect(hasIncompleteDueInput(null)).toBe(false);
  });
});
