import type { Bead } from "../types";
import { statusColor } from "./utils";

interface Props {
  ibead: Bead;
  onEdit: (bead: Bead) => void;
}

export function IbeadCard({ ibead, onEdit }: Props) {
  const meta = ibead.metadata ?? {};
  const domain = meta.domain ?? "unknown";
  const taskList = Array.isArray(meta.task_list) ? meta.task_list : [];
  const pass = meta.gitnexus_pass;

  return (
    <div className="ibead-card">
      <div className="ibead-card-header">
        <div>
          <span className="domain-tag">{domain}</span>
        </div>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div className="ibead-id">{ibead.id}</div>
        </div>
        <button className="edit-btn edit-btn-sm" onClick={() => onEdit(ibead)}>✎</button>
      </div>

      <div className="ibead-title">{ibead.title}</div>

      <div className="ibead-footer">
        <div className="ibead-badges">
          <span
            className="status-badge"
            style={{
              background: `${statusColor(ibead.status)}22`,
              color: statusColor(ibead.status),
              fontSize: "10px",
              padding: "2px 7px",
            }}
          >
            {ibead.status.replace("_", " ")}
          </span>
          {pass !== undefined && (
            <span className="pass-badge">pass {pass}</span>
          )}
          {meta.generated_by && (
            <span className="pass-badge" style={{ opacity: 0.7 }}>
              {String(meta.generated_by)}
            </span>
          )}
        </div>
      </div>

      {taskList.length > 0 && (
        <div className="task-list-section">
          <div className="task-list-label">Task list ({taskList.length})</div>
          <div className="task-list-items">
            {taskList.map((item, i) => (
              <div key={i} className="task-item">
                {item}
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
