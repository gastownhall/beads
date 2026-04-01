#!/usr/bin/env node
/**
 * ibead-audit.mjs — Pass 2: Re-evaluate ibead blast radius after implementation-complete.
 *
 * Triggered when a parent bead reaches "implementation-complete" status.
 * Runs GitNexus impact analysis against actual changed files on the current branch,
 * updates each ibead's task_list in the bd store, and writes investigation items
 * to Task.md as sub-bullets under each ibead.
 *
 * Usage:
 *   node scripts/ibead-audit.mjs --parent <bead-id> [--dry-run] [--json]
 *   npm run ibead:audit -- --parent GGV3-abc123
 */

import fs from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';

const TASK_MD = path.join(process.cwd(), 'Task.md');

function usage() {
  console.error('Usage: node scripts/ibead-audit.mjs --parent <bead-id> [--dry-run] [--json]');
  process.exit(2);
}

function parseArgs(argv) {
  const parsed = { parent: '', dryRun: false, json: false };
  for (let i = 0; i < argv.length; i++) {
    const t = argv[i];
    if (t === '--parent') parsed.parent = argv[++i] ?? '';
    else if (t === '--dry-run') parsed.dryRun = true;
    else if (t === '--json') parsed.json = true;
  }
  return parsed;
}

function runBd(args) {
  const result = spawnSync('bd', args, { cwd: process.cwd(), encoding: 'utf8' });
  if (result.error) throw new Error(`bd error: ${result.error.message}`);
  if (result.status !== 0) {
    const detail = [result.stdout, result.stderr].filter(Boolean).join('\n').trim();
    throw new Error(`bd ${args[0]} failed: ${detail}`);
  }
  return String(result.stdout || '').trim();
}

function runGitNexus(args, timeout = 30000) {
  const result = spawnSync('npx', ['--yes', 'gitnexus', ...args], {
    cwd: process.cwd(),
    encoding: 'utf8',
    timeout,
  });
  if (result.error || result.status !== 0) return null;
  return String(result.stdout || '').trim();
}

function runGit(args) {
  const result = spawnSync('git', args, { cwd: process.cwd(), encoding: 'utf8' });
  if (result.error || result.status !== 0) return '';
  return String(result.stdout || '').trim();
}

function parseJson(raw, label) {
  const str = String(raw || '');
  const match = str.match(/(\{[\s\S]*\}|\[[\s\S]*\])/);
  const jsonStr = match ? match[1] : str;
  try { return JSON.parse(jsonStr || '{}'); }
  catch { throw new Error(`${label} returned invalid JSON`); }
}

function toArray(val) {
  if (Array.isArray(val)) return val;
  if (Array.isArray(val?.issues)) return val.issues;
  if (Array.isArray(val?.data)) return val.data;
  if (typeof val === 'object' && val !== null) return [val];
  return [];
}

/** Get changed files on current branch vs main */
function getChangedFiles() {
  const diff = runGit(['diff', '--name-only', 'origin/main...HEAD']);
  if (!diff) {
    // Fallback: staged + unstaged changes
    const staged = runGit(['diff', '--name-only', '--cached']);
    const unstaged = runGit(['diff', '--name-only']);
    return [...new Set([...staged.split('\n'), ...unstaged.split('\n')])]
      .filter(f => f.trim() && f.endsWith('.ts') || f.endsWith('.tsx') || f.endsWith('.mjs'));
  }
  return diff.split('\n').filter(f => f.trim());
}

/** Extract TypeScript symbol names from a file path */
function extractSymbolsFromFile(filePath) {
  if (!fs.existsSync(filePath)) return [];
  try {
    const content = fs.readFileSync(filePath, 'utf8');
    const symbols = [];

    // Export declarations
    const exportMatches = content.matchAll(/export\s+(?:async\s+)?(?:function|class|const|interface|type)\s+(\w+)/g);
    for (const m of exportMatches) symbols.push(m[1]);

    // Default exports with name
    const defaultMatch = content.match(/export default (?:class|function)\s+(\w+)/);
    if (defaultMatch) symbols.push(defaultMatch[1]);

    return symbols.slice(0, 5); // limit per file
  } catch {
    return [];
  }
}

/** Parse GitNexus impact output into investigation items */
function parseImpactToTaskList(impactOutput, domain) {
  if (!impactOutput) return [`investigate: verify ${domain} has no broken callers after changes`];

  const items = [];
  const lines = impactOutput.split('\n').filter(l => l.trim());

  for (const line of lines) {
    // Look for symbol references and callers
    const symbolMatch = line.match(/(?:caller|calls|imports?|references?|depends?).*?[:`]?\s*(\w+(?:\.\w+)?)/i);
    if (symbolMatch && symbolMatch[1].length > 3) {
      items.push(`investigate: ${symbolMatch[1]} — verify no breakage from parent task`);
    }

    // Look for file references
    const fileMatch = line.match(/(?:src|lib|app|packages)\/[^\s]+\.(ts|tsx|mjs)/);
    if (fileMatch) {
      items.push(`check: ${fileMatch[0]} — review for impact`);
    }

    if (items.length >= 5) break;
  }

  if (items.length === 0) {
    items.push(`investigate: audit ${domain} — no direct callers found, verify indirectly`);
  }

  return [...new Set(items)];
}

/** Update Task.md: add task_list sub-bullets under the ibead line */
function updateTaskMdWithTaskList(ibId, taskList) {
  if (!fs.existsSync(TASK_MD)) return;

  const content = fs.readFileSync(TASK_MD, 'utf8');
  const lines = content.split('\n');

  const ibLineIdx = lines.findIndex(l => l.includes(ibId));
  if (ibLineIdx === -1) return;

  // Check if task list already added
  if (ibLineIdx + 1 < lines.length && lines[ibLineIdx + 1].includes('investigate:')) return;

  const taskLines = taskList.map(item => `    - [ ] ${item}`);
  lines.splice(ibLineIdx + 1, 0, ...taskLines);
  fs.writeFileSync(TASK_MD, lines.join('\n'), 'utf8');
}

function main() {
  const args = parseArgs(process.argv.slice(2));
  if (!args.parent) usage();

  // 1. Fetch parent bead (bd show returns an array)
  const parentRaw = runBd(['show', args.parent, '--json']);
  const parentParsed = parseJson(parentRaw, `bd show ${args.parent}`);
  const parent = Array.isArray(parentParsed) ? parentParsed[0] : parentParsed;
  const parentTitle = parent?.title || args.parent;

  // 2. Get all child ibeads
  const childrenRaw = runBd(['children', args.parent, '--json']);
  const allChildren = toArray(parseJson(childrenRaw, 'bd children'));
  const ibeads = allChildren.filter(c => {
    const labels = c.labels || [];
    return labels.includes('type:ibead') || (c.title || '').includes('IB-');
  });

  if (ibeads.length === 0) {
    console.log(`⚠  No ibeads found for ${args.parent}. Run ibead:create first.`);
    process.exit(0);
  }

  // 3. Get changed files for accurate impact analysis
  const changedFiles = getChangedFiles();
  const allSymbols = [];
  for (const f of changedFiles.slice(0, 10)) {
    allSymbols.push(...extractSymbolsFromFile(f));
  }

  // 4. Run GitNexus detect-changes for branch diff
  const detectOutput = runGitNexus(['detect-changes', '--scope', 'compare', '--base-ref', 'main']);

  // 5. For each ibead, run impact analysis and update task_list
  const results = [];
  for (const ibead of ibeads) {
    const meta = ibead.metadata || {};
    const domain = meta.domain || ibead.title?.match(/\[([^\]]+)\]/)?.[1] || 'unknown';

    // Run impact on primary changed symbols relevant to this domain
    let taskList = [];
    const domainSymbols = allSymbols.filter(s =>
      s.toLowerCase().includes(domain.replace(/-/g, '').toLowerCase().slice(0, 6))
    );
    const symbolsToAnalyze = domainSymbols.length > 0 ? domainSymbols : allSymbols.slice(0, 2);

    for (const sym of symbolsToAnalyze.slice(0, 2)) {
      const impactOutput = runGitNexus(['impact', sym, '--direction', 'upstream']);
      const items = parseImpactToTaskList(impactOutput, domain);
      taskList.push(...items);
    }

    // Add detect-changes context
    if (detectOutput) {
      taskList.push(`verify: confirm ${domain} domain unaffected by branch diff`);
    }

    // Deduplicate and limit
    taskList = [...new Set(taskList)].slice(0, 5);
    if (taskList.length === 0) {
      taskList = [`investigate: audit ${domain} — no issues found, close if verified clean`];
    }

    // Update ibead metadata in bd
    const updatedMeta = JSON.stringify({
      ...meta,
      ibead: true,
      domain,
      gitnexus_pass: 2,
      task_list: taskList,
      audited_at: new Date().toISOString(),
      changed_files_count: changedFiles.length,
    });

    if (!args.dryRun) {
      runBd(['update', ibead.id, '--metadata', updatedMeta]);
      updateTaskMdWithTaskList(ibead.id, taskList);
    }

    results.push({ id: ibead.id, domain, taskList });
  }

  // 6. Output
  const summary = {
    parent: args.parent,
    parentTitle,
    changedFiles: changedFiles.length,
    symbolsAnalyzed: allSymbols.length,
    ibeadsUpdated: results.length,
    ibeads: results,
    pass: 2,
    nextStep: 'For each ibead: run codex:rescue, complete task_list items, then npm run ibead:close',
    dryRun: args.dryRun,
  };

  if (args.json) {
    process.stdout.write(`${JSON.stringify(summary, null, 2)}\n`);
  } else {
    console.log(`\nibead:audit — Pass 2 complete for ${args.parent}`);
    console.log(`  Parent: "${parentTitle}"`);
    console.log(`  Changed files analyzed: ${changedFiles.length}`);
    console.log(`  Symbols extracted: ${allSymbols.length}`);
    console.log(`  Ibeads updated: ${results.length}`);
    for (const r of results) {
      console.log(`\n    ${r.id}  [${r.domain}]`);
      for (const item of r.taskList) {
        console.log(`      · ${item}`);
      }
    }
    console.log(`\n  Next: for each ibead, run codex:rescue → complete task list → ibead:close\n`);
  }
}

main();
