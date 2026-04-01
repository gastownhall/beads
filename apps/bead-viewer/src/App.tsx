import { useCallback, useEffect, useRef, useState } from "react";
import type { Bead, Stats } from "./types";
import { initialize, fetchBeads, fetchStats, updateBead } from "./api";
import type { BeadUpdate } from "./api";
import { BeadDetail } from "./components/BeadDetail";
import { BeadEditModal } from "./components/BeadEditModal";
import { statusColor } from "./components/utils";

const STATUS_FILTERS = [
  { label: "All", value: "all" },
  { label: "Open", value: "open" },
  { label: "In Progress", value: "in_progress" },
  { label: "Blocked", value: "blocked" },
  { label: "Closed", value: "closed" },
  { label: "Deferred", value: "deferred" },
];

export default function App() {
  const [ready, setReady] = useState(false);
  const [initError, setInitError] = useState<string | null>(null);
  const [initializing, setInitializing] = useState(true);
  const [beads, setBeads] = useState<Bead[]>([]);
  const [stats, setStats] = useState<Stats | null>(null);
  const [selected, setSelected] = useState<Bead | null>(null);
  const [status, setStatus] = useState("open");
  const [search, setSearch] = useState("");
  const [loading, setLoading] = useState(false);
  const [initialLoad, setInitialLoad] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const ibcache = useRef<Map<string, Bead[]>>(new Map());
  const [editingBead, setEditingBead] = useState<Bead | null>(null);

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    fetchBeads(status, search)
      .then((data) => {
        setBeads(data);
        setSelected((prev) =>
          prev && data.find((b) => b.id === prev.id) ? prev : null
        );
      })
      .catch((e: unknown) => setError(String(e)))
      .finally(() => {
        setLoading(false);
        setInitialLoad(false);
      });
  }, [status, search]);

  const runInit = useCallback(() => {
    setInitializing(true);
    setInitError(null);
    setReady(false);
    setInitialLoad(true);
    initialize()
      .then(() => {
        setReady(true);
        return fetchStats();
      })
      .then(setStats)
      .catch((e: unknown) => setInitError(String(e)))
      .finally(() => setInitializing(false));
  }, []);

  const reloadAll = useCallback(() => {
    ibcache.current.clear();
    load();
    fetchStats().then(setStats).catch(() => {});
  }, [load]);

  const handleBeadSaved = useCallback(async (id: string, changes: BeadUpdate) => {
    await updateBead(id, changes);

    const applyChanges = (b: Bead): Bead => {
      if (b.id !== id) return b;
      return {
        ...b,
        title: changes.title ?? b.title,
        description: changes.description !== undefined ? changes.description : b.description,
        acceptance_criteria: changes.acceptance_criteria !== undefined ? changes.acceptance_criteria : b.acceptance_criteria,
        notes: changes.notes !== undefined ? changes.notes : b.notes,
        status: (changes.status as Bead["status"]) ?? b.status,
        priority: changes.priority !== undefined ? Number(changes.priority) : b.priority,
        assignee: changes.assignee !== undefined ? changes.assignee : b.assignee,
      };
    };

    setBeads((prev) => prev.map(applyChanges));
    setSelected((prev) => (prev ? applyChanges(prev) : null));
    for (const [parentId, ibeads] of ibcache.current.entries()) {
      ibcache.current.set(parentId, ibeads.map(applyChanges));
    }
  }, []);

  // Initialize on mount
  useEffect(() => { runInit(); }, []); // eslint-disable-line react-hooks/exhaustive-deps

  // Reload beads when filter or search changes (debounce search), but only after init
  useEffect(() => {
    if (!ready) return;
    const timer = setTimeout(load, search ? 300 : 0);
    return () => clearTimeout(timer);
  }, [load, search, ready]);

  // ── Launch screens ────────────────────────────────────────────────────────

  const launchBusy = initializing || initialLoad;

  if (initError || !ready || initialLoad) {
    return (
      <div className="launch-screen">
        <div className="launch-logo">
          <span className="dot" />
          Bead Viewer
          <span className="launch-subtitle">— GGV3</span>
        </div>

        <div className="launch-steps">
          <div className={`launch-step ${ready || initError ? (initError ? "error" : "done") : "active"}`}>
            {initError ? (
              <span className="step-icon">✕</span>
            ) : ready ? (
              <span className="step-icon">✓</span>
            ) : (
              <div className="spinner step-spinner" />
            )}
            <span>Connecting to bead database</span>
          </div>
          {!initError && (
            <div className={`launch-step ${ready ? "active" : "pending"}`}>
              {!ready ? (
                <span className="step-icon step-pending">·</span>
              ) : (
                <div className="spinner step-spinner" />
              )}
              <span>Loading beads</span>
            </div>
          )}
        </div>

        {initError && (
          <>
            <div className="launch-error">{initError}</div>
            <div className="launch-hint">
              Run <code>bd prime</code> in terminal, then retry.
            </div>
          </>
        )}

        <button
          className="launch-reload-btn"
          onClick={runInit}
          disabled={launchBusy}
        >
          {initializing ? <div className="spinner step-spinner" /> : "↺"}
          {initializing ? "Connecting…" : initialLoad ? "Loading…" : "Retry"}
        </button>
      </div>
    );
  }

  return (
    <>
      {/* Header */}
      <header className="header">
        <div className="header-logo">
          <span className="dot" />
          Bead Viewer
          <span style={{ color: "var(--text-muted)", fontWeight: 400, fontSize: 12 }}>
            — GGV3
          </span>
        </div>

        {stats && (
          <div className="header-stats">
            <div className="stat-chip">
              <span
                className="dot"
                style={{ background: "var(--status-open)" }}
              />
              {stats.open_issues} open
            </div>
            <div className="stat-chip">
              <span
                className="dot"
                style={{ background: "var(--status-in-progress)" }}
              />
              {stats.in_progress_issues} active
            </div>
            <div className="stat-chip">
              <span
                className="dot"
                style={{ background: "var(--status-blocked)" }}
              />
              {stats.blocked_issues} blocked
            </div>
            <div className="stat-chip" style={{ color: "var(--text-muted)" }}>
              {stats.total_issues} total
            </div>
          </div>
        )}

        <button
          className={`header-reload-btn ${loading ? "spinning" : ""}`}
          onClick={reloadAll}
          disabled={loading}
          title="Reload bead database"
        >
          <span className="reload-icon">↺</span>
        </button>
      </header>

      {/* Body */}
      <div className="layout">
        {/* Sidebar */}
        <aside className="sidebar">
          <div className="sidebar-controls">
            <input
              className="search-input"
              placeholder="Search beads…"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
            <div className="filter-tabs">
              {STATUS_FILTERS.map((f) => (
                <button
                  key={f.value}
                  className={`filter-tab ${status === f.value ? "active" : ""}`}
                  onClick={() => setStatus(f.value)}
                >
                  {f.label}
                </button>
              ))}
            </div>
          </div>

          {loading && (
            <div className="loading">
              <div className="spinner" />
              Loading…
            </div>
          )}

          {error && <div className="error-msg">{error}</div>}

          {!loading && !error && (
            <div className="sidebar-list">
              {beads.length === 0 ? (
                <div
                  className="loading"
                  style={{ color: "var(--text-muted)" }}
                >
                  No beads found
                </div>
              ) : (
                beads.map((b) => (
                  <div
                    key={b.id}
                    className={`bead-row ${selected?.id === b.id ? "selected" : ""}`}
                    onClick={() => setSelected(b)}
                  >
                    <div className="bead-row-header">
                      <span
                        className="status-dot"
                        style={{ background: statusColor(b.status) }}
                      />
                      <span className="bead-id">{b.id}</span>
                    </div>
                    <div className="bead-row-title">{b.title}</div>
                    <div className="bead-row-meta">
                      {b.issue_type !== "task" && (
                        <span className="bead-type-badge">{b.issue_type}</span>
                      )}
                    </div>
                  </div>
                ))
              )}
            </div>
          )}
        </aside>

        {/* Detail panel */}
        <main className="detail-panel">
          {selected ? (
            <BeadDetail bead={selected} ibcache={ibcache} onEditBead={setEditingBead} />
          ) : (
            <div className="empty-state">
              <div className="icon">◈</div>
              <div style={{ color: "var(--text-secondary)", fontSize: 13 }}>
                Select a bead to view details
              </div>
              <div style={{ fontSize: 11 }}>
                {beads.length} bead{beads.length !== 1 ? "s" : ""} loaded
              </div>
            </div>
          )}
        </main>
      </div>

      {editingBead && (
        <BeadEditModal
          bead={editingBead}
          onSave={handleBeadSaved}
          onClose={() => setEditingBead(null)}
        />
      )}
    </>
  );
}
