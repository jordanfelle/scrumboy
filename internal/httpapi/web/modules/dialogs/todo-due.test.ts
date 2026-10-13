import { describe, expect, it } from "vitest";
import {
  formatDueBadge,
  hasIncompleteDueInput,
  isOverdue,
  isoToLocalInputValue,
  localInputValueToIso,
  resolveDueAtForSubmit,
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

  it("flags a half-edited date input so it is not mistaken for a cleared date", () => {
    expect(hasIncompleteDueInput({ validity: { badInput: true } })).toBe(true);
    expect(hasIncompleteDueInput({ validity: { badInput: false } })).toBe(false);
    expect(hasIncompleteDueInput({})).toBe(false);
    expect(hasIncompleteDueInput(null)).toBe(false);
  });
});
