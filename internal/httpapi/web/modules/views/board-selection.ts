import { initBulkEditDialog, openBulkEditDialog } from '../dialogs/bulk-edit.js';
import { archiveTodos } from '../api.js';
import { apiErrorMessage, I18N_LOCALE_CHANGED, t } from '../i18n/index.js';
import { invalidateBoard } from '../orchestration/board-refresh.js';
import { recordLocalMutation, setBulkUpdating } from '../realtime/guard.js';
import { getAssigneeFromUrl, getBoard, getBoardLaneMeta, getPriorityFromUrl, getSearch, getSlug, getSortFromUrl, getSprintIdFromUrl, getTagsFromUrl } from '../state/selectors.js';
import { isTemporaryBoard, showToast } from '../utils.js';

let selectedTodoIds = new Set<number>();
let selectMode = false;
let anchorTodoId: number | null = null;
let bulkEditUiInitialized = false;
let getCurrentRole: (() => string | null) | null = null;
const ARCHIVE_BATCH_MAX = 500;

function selectionLabel(count: number): string {
  return count === 1
    ? t("board.selection.single")
    : t("board.selection.multiple", { count });
}

export function __selectionLabelForTest(count: number): string {
  return selectionLabel(count);
}

export function getSelectedTodoIds(): Set<number> {
  return selectedTodoIds;
}

export function clearTodoMultiSelection(): void {
  selectedTodoIds.clear();
  anchorTodoId = null;
  setSelectMode(false);
  document.querySelectorAll(".board .card--selected").forEach((el) => el.classList.remove("card--selected"));
}

export function isSelectMode(): boolean {
  return selectMode;
}

export function setSelectMode(on: boolean): void {
  selectMode = on;
  document.querySelector(".board")?.classList.toggle("board--select-mode", on);
  const toggle = document.getElementById("boardSelectToggle");
  if (toggle) {
    toggle.textContent = on ? t("board.select.done") : t("board.select.toggle");
    toggle.setAttribute("aria-pressed", on ? "true" : "false");
  }
  updateBulkEditBar();
}

/** Todo ids of the cards currently shown on the board, in DOM order (lane by lane). The board payload is already
 *  scoped by the active filters/search/sprint, and cards in lanes hidden by the mobile tab strip are skipped, so
 *  "select all" never reaches past what the user can see. */
export function visibleTodoIdsInDomOrder(root: ParentNode = document): number[] {
  const seen = new Set<number>();
  const out: number[] = [];
  root.querySelectorAll<HTMLElement>(".board [data-todo-id]").forEach((el) => {
    const id = Number(el.getAttribute("data-todo-id"));
    if (!Number.isFinite(id) || seen.has(id)) return;
    const lane = el.closest<HTMLElement>(".col");
    if (lane && getComputedStyle(lane).display === "none") return;
    seen.add(id);
    out.push(id);
  });
  return out;
}

/** How many matching todos exist beyond the cards loaded so far (lanes still have "load more"). */
export function notLoadedTodoCount(): number {
  let missing = 0;
  const meta = getBoardLaneMeta();
  for (const [key, m] of Object.entries(meta || {})) {
    if (!m?.hasMore) continue;
    const loaded = getBoard()?.columns?.[key]?.length ?? 0;
    missing += typeof m.totalCount === "number" ? Math.max(m.totalCount - loaded, 1) : 1;
  }
  return missing;
}

function syncCardClasses(): void {
  document.querySelectorAll<HTMLElement>(".board [data-todo-id]").forEach((el) => {
    el.classList.toggle("card--selected", selectedTodoIds.has(Number(el.getAttribute("data-todo-id"))));
  });
}

/** Shift+click: select every card between the anchor and `toId` (inclusive) in `orderedIds`, additively. */
export function selectRangeTo(toId: number, orderedIds: number[] = visibleTodoIdsInDomOrder()): void {
  const a = anchorTodoId == null ? -1 : orderedIds.indexOf(anchorTodoId);
  const b = orderedIds.indexOf(toId);
  if (a < 0 || b < 0) {
    toggleTodoSelection(toId);
    return;
  }
  const [lo, hi] = a <= b ? [a, b] : [b, a];
  for (let i = lo; i <= hi; i++) selectedTodoIds.add(orderedIds[i]);
  syncCardClasses();
  updateBulkEditBar();
}

/** Select every card currently shown (respects the active filters; see visibleTodoIdsInDomOrder). */
export function selectAllVisible(orderedIds: number[] = visibleTodoIdsInDomOrder()): void {
  selectedTodoIds = new Set(orderedIds);
  anchorTodoId = orderedIds.length ? orderedIds[orderedIds.length - 1] : null;
  syncCardClasses();
  updateBulkEditBar();
}

/** After a (re)render or filter change: drop selected ids whose cards are no longer on the board. */
export function pruneSelectionToRenderedBoard(): void {
  if (selectedTodoIds.size === 0) return;
  const present = new Set(visibleTodoIdsInDomOrder());
  let changed = false;
  for (const id of Array.from(selectedTodoIds)) {
    if (!present.has(id)) {
      selectedTodoIds.delete(id);
      changed = true;
    }
  }
  if (anchorTodoId != null && !present.has(anchorTodoId)) anchorTodoId = null;
  if (changed) updateBulkEditBar();
}

export function updateBulkEditBar(): void {
  const bar = document.getElementById("bulkEditBar");
  const btn = document.getElementById("bulkEditBarBtn");
  if (!bar || !btn) return;
  const n = selectedTodoIds.size;
  if (n >= 1 || selectMode) {
    bar.style.display = "";
    btn.textContent = n >= 1 ? selectionLabel(n) : "";
    btn.hidden = n < 1;
  } else {
    bar.style.display = "none";
    btn.textContent = "";
    btn.hidden = true;
  }
  const allBtn = document.getElementById("bulkSelectAllBtn") as HTMLButtonElement | null;
  if (allBtn) {
    const total = visibleTodoIdsInDomOrder().length;
    allBtn.textContent = t("board.select.all", { count: total });
    allBtn.hidden = !(n >= 1 || selectMode);
    allBtn.disabled = total === 0 || n === total;
  }
  const clearBtn = document.getElementById("bulkClearBtn") as HTMLButtonElement | null;
  if (clearBtn) clearBtn.hidden = n < 1;
  const hint = document.getElementById("bulkSelectHint");
  if (hint) {
    const missing = n >= 1 ? notLoadedTodoCount() : 0;
    hint.hidden = missing === 0;
    hint.textContent = missing ? t("board.select.notLoaded", { count: missing }) : "";
  }
  const archiveBtn = document.getElementById("bulkArchiveBarBtn") as HTMLButtonElement | null;
  if (archiveBtn) {
    const canArchive = getCurrentRole?.() === "maintainer" || isTemporaryBoard(getBoard());
    archiveBtn.hidden = n < 1 || !canArchive;
    archiveBtn.disabled = n > ARCHIVE_BATCH_MAX;
    archiveBtn.title = n > ARCHIVE_BATCH_MAX ? t("board.bulkArchive.limit") : "";
  }
}

function selectedLocalIds(): number[] {
  const ids = new Set(selectedTodoIds);
  const localIds: number[] = [];
  for (const todos of Object.values(getBoard()?.columns || {})) {
    for (const todo of todos) {
      if (ids.has(todo.id) && !todo.archivedAt) localIds.push(todo.localId);
    }
  }
  return localIds;
}

async function archiveSelection(): Promise<void> {
  const slug = getSlug();
  const localIds = selectedLocalIds();
  const canArchive = getCurrentRole?.() === "maintainer" || isTemporaryBoard(getBoard());
  if (!slug || !canArchive || localIds.length === 0) return;
  if (localIds.length > ARCHIVE_BATCH_MAX) {
    showToast(t("board.bulkArchive.limit"));
    return;
  }
  setBulkUpdating(true);
  try {
    recordLocalMutation();
    const result = await archiveTodos(slug, localIds);
    clearTodoMultiSelection();
    showToast(t("board.bulkArchive.archivedMultiple", { count: result.transitionedCount }));
    await invalidateBoard(slug, getTagsFromUrl(), getSearch(), getSprintIdFromUrl(), getAssigneeFromUrl(), getSortFromUrl(), getPriorityFromUrl(), true);
  } catch (error) {
    showToast(apiErrorMessage(error, { fallbackKey: "board.bulkArchive.failed" }));
    updateBulkEditBar();
  } finally {
    setBulkUpdating(false);
  }
}

export function toggleTodoSelection(id: number): void {
  if (selectedTodoIds.has(id)) selectedTodoIds.delete(id);
  else selectedTodoIds.add(id);
  anchorTodoId = id;
  updateBulkEditBar();
  const card = document.querySelector(`[data-todo-id="${id}"]`);
  if (card) card.classList.toggle("card--selected", selectedTodoIds.has(id));
}

export function ensureBulkEditUi(opts: {
  getRole: () => string | null;
  syncSelectionClasses: (selectedIds: ReadonlySet<number>) => void;
}): void {
  getCurrentRole = opts.getRole;
  if (bulkEditUiInitialized) return;
  bulkEditUiInitialized = true;
  initBulkEditDialog(() => {
    clearTodoMultiSelection();
    updateBulkEditBar();
  });
  document.addEventListener(I18N_LOCALE_CHANGED, () => {
    updateBulkEditBar();
  });
  const barBtn = document.getElementById("bulkEditBarBtn");
  barBtn?.addEventListener("click", () => {
    void openBulkEditDialog(Array.from(selectedTodoIds), {
      role: opts.getRole(),
      onPruned: (remaining) => {
        selectedTodoIds = new Set(remaining);
        updateBulkEditBar();
        opts.syncSelectionClasses(selectedTodoIds);
      },
    });
  });
  document.getElementById("bulkArchiveBarBtn")?.addEventListener("click", () => {
    void archiveSelection();
  });
  document.getElementById("bulkSelectAllBtn")?.addEventListener("click", () => {
    selectAllVisible();
  });
  document.getElementById("bulkClearBtn")?.addEventListener("click", () => {
    clearTodoMultiSelection();
  });
  document.addEventListener("click", (e) => {
    if (!(e.target as HTMLElement | null)?.closest("#boardSelectToggle")) return;
    if (selectMode) clearTodoMultiSelection();
    else setSelectMode(true);
  });
  document.addEventListener("keydown", (e) => {
    if (!document.querySelector(".board") || getCurrentRole?.() === "viewer") return;
    if (document.querySelector("dialog[open]")) return;
    const target = e.target as HTMLElement | null;
    if (target?.closest("input, textarea, select, [contenteditable=''], [contenteditable='true']")) return;
    if (e.key === "Escape" && (selectMode || selectedTodoIds.size > 0)) {
      clearTodoMultiSelection();
      return;
    }
    if ((e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey && e.key.toLowerCase() === "a") {
      e.preventDefault();
      selectAllVisible();
    }
  });
}
