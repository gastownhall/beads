import { useState } from "react";
import type { Bead } from "../types";
import type { BeadUpdate } from "../api";

const STATUSES = ["open", "in_progress", "blocked", "deferred", "closed"];
const PRIORITIES = [
  { value: "0", label: "P0 — Critical" },
  { value: "1", label: "P1 — High" },
  { value: "2", label: "P2 — Medium" },
  { value: "3", label: "P3 — Low" },
  { value: "4", label: "P4 — Backlog" },
];

interface Props {
  bead: Bead;
  onSave: (id: string, changes: BeadUpdate) => Promise<void>;
  onClose: () => void;
}

export function BeadEditModal({ bead, onSave, onClose }: Props) {
  const [form, setForm] = useState({
    title: bead.title ?? "",
    status: bead.status ?? "open",
    priority: String(bead.priority ?? 2),
    assignee: bead.assignee ?? bead.owner ?? "",
    description: bead.description ?? "",
    acceptance_criteria: bead.acceptance_criteria ?? "",
    notes: bead.notes ?? "",
  });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const set = (field: keyof typeof form) => (
    e: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>
  ) => setForm((prev) => ({ ...prev, [field]: e.target.value }));

  const handleSave = async () => {
    // Only send fields that actually changed
    const changes: BeadUpdate = {};
    if (form.title !== (bead.title ?? "")) changes.title = form.title;
    if (form.status !== bead.status) changes.status = form.status;
    if (form.priority !== String(bead.priority ?? 2)) changes.priority = form.priority;
    if (form.assignee !== (bead.assignee ?? bead.owner ?? "")) changes.assignee = form.assignee;
    if (form.description !== (bead.description ?? "")) changes.description = form.description;
    if (form.acceptance_criteria !== (bead.acceptance_criteria ?? "")) changes.acceptance_criteria = form.acceptance_criteria;
    if (form.notes !== (bead.notes ?? "")) changes.notes = form.notes;

    if (Object.keys(changes).length === 0) {
      onClose();
      return;
    }

    setSaving(true);
    setError(null);
    try {
      await onSave(bead.id, changes);
      onClose();
    } catch (e) {
      setError(String(e));
      setSaving(false);
    }
  };

  const handleBackdrop = (e: React.MouseEvent) => {
    if (e.target === e.currentTarget) onClose();
  };

  return (
    <div className="modal-overlay" onClick={handleBackdrop}>
      <div className="modal-panel">
        {/* Modal header */}
        <div className="modal-header">
          <div className="modal-title">
            <span className="modal-bead-id">{bead.id}</span>
            Edit bead
          </div>
          <button className="modal-close-btn" onClick={onClose}>✕</button>
        </div>

        {/* Form */}
        <div className="modal-body">
          <div className="form-row">
            <label className="form-label">Title</label>
            <input
              className="form-input"
              value={form.title}
              onChange={set("title")}
              autoFocus
            />
          </div>

          <div className="form-row-group">
            <div className="form-row">
              <label className="form-label">Status</label>
              <select className="form-select" value={form.status} onChange={set("status")}>
                {STATUSES.map((s) => (
                  <option key={s} value={s}>{s.replace("_", " ")}</option>
                ))}
              </select>
            </div>
            <div className="form-row">
              <label className="form-label">Priority</label>
              <select className="form-select" value={form.priority} onChange={set("priority")}>
                {PRIORITIES.map((p) => (
                  <option key={p.value} value={p.value}>{p.label}</option>
                ))}
              </select>
            </div>
            <div className="form-row">
              <label className="form-label">Assignee</label>
              <input
                className="form-input"
                value={form.assignee}
                onChange={set("assignee")}
                placeholder="unassigned"
              />
            </div>
          </div>

          <div className="form-row">
            <label className="form-label">Description</label>
            <textarea
              className="form-textarea"
              rows={4}
              value={form.description}
              onChange={set("description")}
              placeholder="What needs to be done…"
            />
          </div>

          <div className="form-row">
            <label className="form-label">Acceptance Criteria</label>
            <textarea
              className="form-textarea"
              rows={3}
              value={form.acceptance_criteria}
              onChange={set("acceptance_criteria")}
              placeholder="How do we know it's done…"
            />
          </div>

          <div className="form-row">
            <label className="form-label">Notes</label>
            <textarea
              className="form-textarea"
              rows={3}
              value={form.notes}
              onChange={set("notes")}
              placeholder="Additional context…"
            />
          </div>

          {error && <div className="error-msg">{error}</div>}
        </div>

        {/* Footer */}
        <div className="modal-footer">
          <button className="modal-cancel-btn" onClick={onClose} disabled={saving}>
            Cancel
          </button>
          <button className="modal-save-btn" onClick={handleSave} disabled={saving}>
            {saving ? <><div className="spinner step-spinner" /> Saving…</> : "Save changes"}
          </button>
        </div>
      </div>
    </div>
  );
}
