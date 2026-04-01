export function statusColor(status: string): string {
  switch (status) {
    case "open":
      return "var(--status-open)";
    case "in_progress":
      return "var(--status-in-progress)";
    case "closed":
      return "var(--status-closed)";
    case "blocked":
      return "var(--status-blocked)";
    case "deferred":
      return "var(--status-deferred)";
    default:
      return "var(--text-muted)";
  }
}

export function priorityLabel(p: number): string {
  if (p === 0) return "P0 — Critical";
  if (p === 1) return "P1 — High";
  if (p === 2) return "P2 — Medium";
  if (p === 3) return "P3 — Low";
  return `P${p}`;
}

export function formatDate(iso: string): string {
  try {
    return new Date(iso).toLocaleDateString("en-US", {
      month: "short",
      day: "numeric",
      year: "numeric",
    });
  } catch {
    return iso;
  }
}
