import { useEffect, useRef, useState } from "react";
import type { Bead } from "../types";
import { fetchIbeads } from "../api";
import { IbeadCard } from "./IbeadCard";
import { statusColor, priorityLabel, formatDate } from "./utils";

interface Props {
  bead: Bead;
  ibcache: React.MutableRefObject<Map<string, Bead[]>>;
  onEditBead: (bead: Bead) => void;
}

export function BeadDetail({ bead, ibcache, onEditBead }: Props) {
  const [ibeads, setIbeads] = useState<Bead[]>(() => ibcache.current.get(bead.id) ?? []);
  const [loading, setLoading] = useState(!ibcache.current.has(bead.id));
  const [error, setError] = useState<string | null>(null);
  const fetchingFor = useRef<string | null>(null);

  useEffect(() => {
    const cached = ibcache.current.get(bead.id);
    if (cached) {
      setIbeads(cached);
      setLoading(false);
      setError(null);
      return;
    }

    // Avoid double-fetching the same bead
    if (fetchingFor.current === bead.id) return;
    fetchingFor.current = bead.id;

    setError(null);
    setLoading(true);
    fetchIbeads(bead.id)
      .then((data) => {
        ibcache.current.set(bead.id, data);
        setIbeads(data);
      })
      .catch((e: unknown) => setError(String(e)))
      .finally(() => {
        setLoading(false);
        fetchingFor.current = null;
      });
  }, [bead.id, ibcache]);

  const nonIbLabels = bead.labels.filter((l) => l !== "type:ibead");

  return (
    <div className="bead-detail">
      {/* Header */}
      <div className="detail-header">
        <div className="detail-header-top">
          <div className="detail-id">{bead.id}</div>
          <button className="edit-btn" onClick={() => onEditBead(bead)}>✎ Edit</button>
        </div>
        <div className="detail-title">{bead.title}</div>
        <div className="detail-badges">
          <span
            className="status-badge"
            style={{
              background: `${statusColor(bead.status)}22`,
              color: statusColor(bead.status),
            }}
          >
            {bead.status.replace("_", " ")}
          </span>
          <span className="priority-badge">{priorityLabel(bead.priority)}</span>
          <span className="priority-badge" style={{ fontStyle: "italic" }}>
            {bead.issue_type}
          </span>
          {nonIbLabels.map((l) => (
            <span key={l} className="label-chip">
              {l}
            </span>
          ))}
        </div>
      </div>

      {/* Meta grid */}
      <div className="detail-meta-grid">
        <div className="meta-item">
          <span className="meta-label">Owner</span>
          <span className="meta-value">{bead.owner ?? bead.assignee ?? "—"}</span>
        </div>
        <div className="meta-item">
          <span className="meta-label">Created</span>
          <span className="meta-value">{formatDate(bead.created_at)}</span>
        </div>
        <div className="meta-item">
          <span className="meta-label">Updated</span>
          <span className="meta-value">{formatDate(bead.updated_at)}</span>
        </div>
        <div className="meta-item">
          <span className="meta-label">Dependencies</span>
          <span className="meta-value">{bead.dependency_count}</span>
        </div>
        <div className="meta-item">
          <span className="meta-label">Dependents</span>
          <span className="meta-value">{bead.dependent_count}</span>
        </div>
        {bead.parent && (
          <div className="meta-item">
            <span className="meta-label">Parent</span>
            <span className="meta-value" style={{ fontFamily: "monospace" }}>
              {bead.parent}
            </span>
          </div>
        )}
      </div>

      {/* Description */}
      {bead.description && (
        <div className="detail-section">
          <div className="section-title">Description</div>
          <div className="section-text">{bead.description}</div>
        </div>
      )}

      {/* Acceptance Criteria */}
      {bead.acceptance_criteria && (
        <div className="detail-section">
          <div className="section-title">Acceptance Criteria</div>
          <div className="section-text">{bead.acceptance_criteria}</div>
        </div>
      )}

      {/* Notes */}
      {bead.notes && bead.notes.trim() && (
        <div className="detail-section">
          <div className="section-title">Notes</div>
          <div className="section-text">{bead.notes}</div>
        </div>
      )}

      {/* Ibeads */}
      <div className="ibeads-section">
        <div className="ibeads-header">
          <span className="ibeads-title">Interconnected Beads</span>
          {!loading && (
            <span className="ibeads-count">{ibeads.length}</span>
          )}
        </div>

        {loading && (
          <div className="loading">
            <div className="spinner" />
            Loading ibeads…
          </div>
        )}

        {error && <div className="error-msg">{error}</div>}

        {!loading && !error && ibeads.length === 0 && (
          <div className="no-ibeads">No ibeads associated with this bead.</div>
        )}

        {!loading && ibeads.length > 0 && (
          <div className="ibeads-grid">
            {ibeads.map((ib) => (
              <IbeadCard key={ib.id} ibead={ib} onEdit={onEditBead} />
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
