#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use std::process::Command;
use std::thread;
use std::time::Duration;

/// Resolve the `bd` binary path at runtime.
/// Priority: BD_PATH env var → common install locations → PATH fallback.
fn bd_bin() -> String {
    if let Ok(path) = std::env::var("BD_PATH") {
        return path;
    }

    #[cfg(target_os = "windows")]
    let candidates: &[&str] = &["bd.exe", "bd"];

    #[cfg(not(target_os = "windows"))]
    let candidates: Vec<String> = {
        let mut v = vec![];
        if let Ok(home) = std::env::var("HOME") {
            v.push(format!("{}/.local/bin/bd", home));
            v.push(format!("{}/bin/bd", home));
        }
        v.push("/usr/local/bin/bd".to_string());
        v.push("/opt/homebrew/bin/bd".to_string());
        v.push("/usr/bin/bd".to_string());
        v.push("bd".to_string());
        v
    };

    #[cfg(not(target_os = "windows"))]
    for candidate in &candidates {
        if std::path::Path::new(candidate).exists() {
            return candidate.clone();
        }
    }

    #[cfg(target_os = "windows")]
    for candidate in candidates {
        if std::path::Path::new(candidate).exists() {
            return candidate.to_string();
        }
    }

    "bd".to_string()
}

/// Run a bd CLI command directly (no shell) to avoid startup overhead.
fn run_bd(args: &[&str]) -> Result<String, String> {
    let output = Command::new(bd_bin())
        .args(args)
        .output()
        .map_err(|e| format!("Failed to spawn bd: {}", e))?;

    let raw = String::from_utf8_lossy(&output.stdout).to_string();
    let stderr = String::from_utf8_lossy(&output.stderr).to_string();

    if !output.status.success() && raw.trim().is_empty() {
        return Err(format!("bd error: {}", stderr.trim()));
    }

    Ok(strip_to_json(&raw))
}

fn strip_to_json(raw: &str) -> String {
    if let Some(pos) = raw.find(|c| c == '[' || c == '{') {
        raw[pos..].to_string()
    } else {
        raw.to_string()
    }
}

/// Ensure the Dolt server is up by running `bd prime`.
/// Retries up to 3 times with a short wait between attempts.
#[tauri::command]
fn initialize() -> Result<String, String> {
    for attempt in 1..=3u64 {
        let result = Command::new(bd_bin()).arg("prime").output();

        match result {
            Ok(out) if out.status.success() => {
                return Ok("ready".to_string());
            }
            Ok(out) => {
                let stderr = String::from_utf8_lossy(&out.stderr).to_string();
                if attempt < 3 {
                    thread::sleep(Duration::from_millis(800 * attempt));
                } else {
                    return Err(format!(
                        "bd prime failed after 3 attempts: {}",
                        stderr.trim()
                    ));
                }
            }
            Err(e) => {
                return Err(format!("Failed to run bd prime: {}", e));
            }
        }
    }
    Err("bd prime did not succeed".to_string())
}

/// List beads. Optionally filter by status (open|in_progress|closed|blocked|deferred).
/// Excludes ibeads (type:ibead label) from the main list.
#[tauri::command]
fn get_beads(status: Option<String>, search: Option<String>) -> Result<String, String> {
    let status_str = status.unwrap_or_default();

    let raw = if !status_str.is_empty() && status_str != "all" {
        run_bd(&["list", "--json", "--limit", "200", "--status", &status_str])?
    } else {
        run_bd(&["list", "--json", "--limit", "200"])?
    };

    let mut beads: Vec<serde_json::Value> =
        serde_json::from_str(&raw).map_err(|e| format!("JSON parse error: {}", e))?;

    // Filter out ibeads
    beads.retain(|b| {
        let labels = b["labels"].as_array();
        !labels
            .map(|l| l.iter().any(|v| v.as_str() == Some("type:ibead")))
            .unwrap_or(false)
    });

    // Apply search filter client-side
    if let Some(q) = &search {
        let q_lower = q.to_lowercase();
        if !q_lower.is_empty() {
            beads.retain(|b| {
                let title = b["title"].as_str().unwrap_or("").to_lowercase();
                let desc = b["description"].as_str().unwrap_or("").to_lowercase();
                let id = b["id"].as_str().unwrap_or("").to_lowercase();
                title.contains(&q_lower) || desc.contains(&q_lower) || id.contains(&q_lower)
            });
        }
    }

    serde_json::to_string(&beads).map_err(|e| e.to_string())
}

/// Get ibeads (children with type:ibead label) for a given parent bead ID.
#[tauri::command]
fn get_ibeads(parent_id: String) -> Result<String, String> {
    let raw = run_bd(&["list", "--json", "--parent", &parent_id, "--limit", "50"])?;

    if raw.trim().is_empty() || raw.trim() == "[]" || raw.trim() == "null" {
        return Ok("[]".to_string());
    }

    let mut beads: Vec<serde_json::Value> =
        serde_json::from_str(&raw).map_err(|e| format!("JSON parse error: {}", e))?;

    // Keep only ibeads
    beads.retain(|b| {
        let labels = b["labels"].as_array();
        labels
            .map(|l| l.iter().any(|v| v.as_str() == Some("type:ibead")))
            .unwrap_or(false)
    });

    serde_json::to_string(&beads).map_err(|e| e.to_string())
}

/// Get bead stats summary.
#[tauri::command]
fn get_stats() -> Result<String, String> {
    run_bd(&["status", "--json"])
}

/// Update a bead. Only fields that are Some are sent to bd update.
#[tauri::command]
fn update_bead(
    id: String,
    title: Option<String>,
    description: Option<String>,
    acceptance_criteria: Option<String>,
    notes: Option<String>,
    status: Option<String>,
    priority: Option<String>,
    assignee: Option<String>,
) -> Result<(), String> {
    let mut cmd = Command::new(bd_bin());
    cmd.arg("update").arg(&id);
    if let Some(v) = &title { cmd.arg("--title").arg(v); }
    if let Some(v) = &description { cmd.arg("--description").arg(v); }
    if let Some(v) = &acceptance_criteria { cmd.arg("--acceptance").arg(v); }
    if let Some(v) = &notes { cmd.arg("--notes").arg(v); }
    if let Some(v) = &status { cmd.arg("--status").arg(v); }
    if let Some(v) = &priority { cmd.arg("--priority").arg(v); }
    if let Some(v) = &assignee { cmd.arg("--assignee").arg(v); }

    let output = cmd
        .output()
        .map_err(|e| format!("Failed to run bd update: {}", e))?;

    if !output.status.success() {
        let stderr = String::from_utf8_lossy(&output.stderr);
        return Err(format!("bd update failed: {}", stderr.trim()));
    }
    Ok(())
}

fn main() {
    tauri::Builder::default()
        .invoke_handler(tauri::generate_handler![
            initialize,
            get_beads,
            get_ibeads,
            get_stats,
            update_bead
        ])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
