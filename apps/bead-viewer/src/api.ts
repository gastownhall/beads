import { invoke } from "@tauri-apps/api/core";
import type { Bead, Stats } from "./types";

export async function initialize(): Promise<void> {
  await invoke<string>("initialize");
}

export async function fetchBeads(
  status?: string,
  search?: string
): Promise<Bead[]> {
  const raw = await invoke<string>("get_beads", {
    status: status ?? "all",
    search: search ?? "",
  });
  return JSON.parse(raw) as Bead[];
}

export async function fetchIbeads(parentId: string): Promise<Bead[]> {
  const raw = await invoke<string>("get_ibeads", { parentId });
  return JSON.parse(raw) as Bead[];
}

export interface BeadUpdate {
  title?: string;
  description?: string;
  acceptance_criteria?: string;
  notes?: string;
  status?: string;
  priority?: string;
  assignee?: string;
}

export async function updateBead(id: string, changes: BeadUpdate): Promise<void> {
  await invoke<void>("update_bead", { id, ...changes });
}

export async function fetchStats(): Promise<Stats> {
  const raw = await invoke<string>("get_stats");
  // strip to JSON in case of warning text
  const start = raw.search(/[{[]/);
  const json = start >= 0 ? raw.slice(start) : raw;
  return JSON.parse(json) as Stats;
}
