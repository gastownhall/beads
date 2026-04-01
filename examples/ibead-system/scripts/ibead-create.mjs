#!/usr/bin/env node
/**
 * ibead-create.mjs — Pass 1: Generate interconnected beads (ibeads) for a parent bead.
 *
 * Runs GitNexus depth-1 impact analysis on the parent bead's title/description,
 * identifies 3–5 impacted domains, and creates ibead records in the bd store.
 * Also writes ibead lines to Task.md under the parent task.
 *
 * Usage:
 *   node scripts/ibead-create.mjs --parent <bead-id> [--dry-run] [--json]
 *   npm run ibead:create -- --parent GGV3-abc123
 */

import fs from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';

const TASK_MD = path.join(process.cwd(), 'Task.md');
const MAX_IBEADS = 5;
const MIN_IBEADS = 3;

function usage() {
  console.error('Usage: node scripts/ibead-create.mjs --parent <bead-id> [--dry-run] [--json]');
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

function runGitNexus(args) {
  const result = spawnSync('npx', ['--yes', 'gitnexus', ...args], {
    cwd: process.cwd(),
    encoding: 'utf8',
    timeout: 30000,
  });
  if (result.error || result.status !== 0) return null;
  return String(result.stdout || '').trim();
}

function parseJson(raw, label) {
  const str = String(raw || '');
  // bd may prepend warning text before JSON — extract the JSON object/array
  const match = str.match(/(\{[\s\S]*\}|\[[\s\S]*\])/);
  try { return JSON.parse(match ? match[1] : str || '{}'); }
  catch { throw new Error(`${label} returned invalid JSON: ${str.slice(0, 200)}`); }
}

/** Extract key search terms from a bead title/description */
function extractTerms(text) {
  const stopWords = new Set([
    'the', 'a', 'an', 'and', 'or', 'for', 'to', 'in', 'on', 'at', 'with',
    'add', 'fix', 'update', 'remove', 'refactor', 'implement', 'create',
    'new', 'old', 'get', 'set', 'use', 'make', 'build', 'run', 'test',
  ]);
  return text
    .toLowerCase()
    .replace(/[^a-z0-9\s]/g, ' ')
    .split(/\s+/)
    .filter(w => w.length > 3 && !stopWords.has(w))
    .slice(0, 6)
    .join(' ');
}

/** Parse GitNexus query output to extract domain groups */
function extractDomains(gitnexusOutput) {
  if (!gitnexusOutput) return [];
  const domains = new Map();

  // GitNexus returns process-grouped results — extract process/cluster names
  const processMatches = gitnexusOutput.matchAll(/Process:\s*([^\n]+)/gi);
  for (const m of processMatches) {
    const name = m[1].trim().replace(/\s+/g, '-').toLowerCase();
    if (name && !domains.has(name)) domains.set(name, { domain: name, symbols: [] });
  }

  // Also look for file path clusters as domain signals
  const fileMatches = gitnexusOutput.matchAll(/(?:src|lib|app|packages)\/([^/\n]+)\//gi);
  for (const m of fileMatches) {
    const name = m[1].trim().toLowerCase();
    if (name && !domains.has(name)) domains.set(name, { domain: name, symbols: [] });
  }

  return [...domains.values()].slice(0, MAX_IBEADS);
}

/** Fallback: generate domain stubs from bead title when GitNexus unavailable */
function fallbackDomains(beadTitle) {
  const DOMAIN_KEYWORDS = {
    'auth': 'auth-layer',
    'session': 'session-layer',
    'user': 'user-service',
    'api': 'api-contracts',
    'route': 'routing-layer',
    'db': 'database-layer',
    'database': 'database-layer',
    'mongo': 'database-layer',
    'ui': 'ui-components',
    'component': 'ui-components',
    'forum': 'forum-domain',
    'auction': 'auction-domain',
    'test': 'ci-coverage',
    'ci': 'ci-coverage',
    'security': 'security-layer',
    'payment': 'payments-domain',
    'email': 'notification-layer',
    'notify': 'notification-layer',
    'seo': 'seo-layer',
    'analytics': 'analytics-layer',
  };

  const lower = beadTitle.toLowerCase();
  const matched = new Set();
  for (const [keyword, domain] of Object.entries(DOMAIN_KEYWORDS)) {
    if (lower.includes(keyword)) matched.add(domain);
    if (matched.size >= MAX_IBEADS) break;
  }

  // Always include ci-coverage and api-contracts as baseline impact domains
  matched.add('ci-coverage');
  matched.add('api-contracts');

  return [...matched].slice(0, MAX_IBEADS).map(d => ({ domain: d, symbols: [] }));
}

/** Write ibead lines to Task.md under the parent bead */
function updateTaskMd(parentId, ibeads) {
  if (!fs.existsSync(TASK_MD)) return;

  const content = fs.readFileSync(TASK_MD, 'utf8');
  const lines = content.split('\n');

  // Find line containing parent bead ID
  const parentLineIdx = lines.findIndex(l => l.includes(parentId));
  if (parentLineIdx === -1) return; // Parent not in Task.md — skip

  // Check if ibeads already inserted for this parent
  const alreadyInserted = lines.some(l => l.includes(`IB-${parentId.split('-')[1]}`));
  if (alreadyInserted) return;

  // Build ibead lines (indented under parent)
  const ibadLines = ibeads.map(ib =>
    `  - [ ] ${ib.id}: [domain: ${ib.domain}] ${ib.title}`
  );

  // Insert after parent line
  lines.splice(parentLineIdx + 1, 0, ...ibadLines);
  fs.writeFileSync(TASK_MD, lines.join('\n'), 'utf8');
}

function main() {
  const args = parseArgs(process.argv.slice(2));
  if (!args.parent) usage();

  // 1. Fetch parent bead details (bd show returns an array)
  const parentRaw = runBd(['show', args.parent, '--json']);
  const parentParsed = parseJson(parentRaw, `bd show ${args.parent}`);
  const parent = Array.isArray(parentParsed) ? parentParsed[0] : parentParsed;
  const parentTitle = parent?.title || parent?.id || args.parent;
  const parentIdShort = args.parent.split('-').pop() || args.parent;

  // 2. Run GitNexus Pass 1 — query by bead terms
  const terms = extractTerms(parentTitle);
  let domains = [];

  if (terms) {
    const gnOutput = runGitNexus(['query', terms]);
    domains = extractDomains(gnOutput);
  }

  // Fallback if GitNexus unavailable or returns no domains
  if (domains.length < MIN_IBEADS) {
    const fallback = fallbackDomains(parentTitle);
    const existingDomainNames = new Set(domains.map(d => d.domain));
    for (const d of fallback) {
      if (!existingDomainNames.has(d.domain)) domains.push(d);
      if (domains.length >= MAX_IBEADS) break;
    }
  }

  domains = domains.slice(0, MAX_IBEADS);

  // 3. Create ibeads
  const created = [];
  for (let i = 0; i < domains.length; i++) {
    const { domain } = domains[i];
    const ibTitle = `IB-${parentIdShort}-${i + 1}: [${domain}] splash audit — ${parentTitle}`;
    const metadata = JSON.stringify({
      ibead: true,
      domain,
      depth: 1,
      gitnexus_pass: 1,
      task_list: [],
      parent_title: parentTitle,
    });

    if (args.dryRun) {
      created.push({ id: `DRY-IB-${parentIdShort}-${i + 1}`, domain, title: ibTitle });
      continue;
    }

    const raw = runBd([
      'create', ibTitle,
      '--parent', args.parent,
      '--label', 'type:ibead',
      '--metadata', metadata,
      '--json',
    ]);

    // Extract created ibead ID
    const parsed = parseJson(raw, 'bd create ibead');
    const id = parsed.id || parsed.issue_id || `IB-${parentIdShort}-${i + 1}`;
    created.push({ id, domain, title: ibTitle });
  }

  // 4. Update Task.md
  if (!args.dryRun) {
    updateTaskMd(args.parent, created);
  }

  // 5. Output
  const summary = {
    parent: args.parent,
    parentTitle,
    gitnexusTerms: terms,
    ibeadsCreated: created.length,
    ibeads: created,
    pass: 1,
    note: 'Pass 1 ibeads are approximations. Run ibead:audit after implementation-complete for accurate blast radius.',
    dryRun: args.dryRun,
  };

  if (args.json) {
    process.stdout.write(`${JSON.stringify(summary, null, 2)}\n`);
  } else {
    console.log(`\nibead:create — Pass 1 complete for ${args.parent}`);
    console.log(`  Parent: "${parentTitle}"`);
    console.log(`  GitNexus query terms: "${terms}"`);
    console.log(`  Ibeads created: ${created.length}`);
    for (const ib of created) {
      console.log(`    ${ib.id}  [${ib.domain}]`);
    }
    console.log(`\n  ⚡ Pass 1 complete. Run ibead:audit after implementation-complete for accurate blast radius.\n`);
  }
}

main();
