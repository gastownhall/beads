#!/usr/bin/env node
/**
 * ibead-research.mjs — Batch re-evaluation of ibeads across all open/in-progress beads.
 *
 * Scans all open and in-progress beads. For each:
 *   - Re-runs GitNexus impact analysis (code may have shifted since other beads were closed)
 *   - Adds new ibeads where splash radius has expanded
 *   - Flags existing ibeads that may be stale or already resolved
 *
 * Produces a structured summary report on completion.
 *
 * Usage:
 *   node scripts/ibead-research.mjs [--dry-run] [--json] [--bead <id>]
 *   npm run ibead:research
 *   npm run ibead:research -- --bead GGV3-abc123   (single bead only)
 */

import { spawnSync } from 'node:child_process';

function parseArgs(argv) {
  const parsed = { dryRun: false, json: false, bead: '' };
  for (let i = 0; i < argv.length; i++) {
    const t = argv[i];
    if (t === '--dry-run') parsed.dryRun = true;
    else if (t === '--json') parsed.json = true;
    else if (t === '--bead') parsed.bead = argv[++i] ?? '';
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

function runNode(scriptPath, args) {
  const result = spawnSync('node', [scriptPath, ...args], {
    cwd: process.cwd(),
    encoding: 'utf8',
    timeout: 60000,
  });
  if (result.error) throw new Error(`node error: ${result.error.message}`);
  if (result.status !== 0) {
    const detail = [result.stdout, result.stderr].filter(Boolean).join('\n').trim();
    throw new Error(`Script failed: ${detail}`);
  }
  return String(result.stdout || '').trim();
}

function parseJson(raw, label) {
  const str = String(raw || '');
  const match = str.match(/(\{[\s\S]*\}|\[[\s\S]*\])/);
  const jsonStr = match ? match[1] : str;
  try { return JSON.parse(jsonStr || '{}'); }
  catch (e) { throw new Error(`${label} returned invalid JSON: ${e.message}`); }
}

function toArray(val) {
  if (Array.isArray(val)) return val;
  if (Array.isArray(val?.issues)) return val.issues;
  if (Array.isArray(val?.data)) return val.data;
  if (typeof val === 'object' && val !== null) return Object.values(val).find(v => Array.isArray(v)) ?? [];
  return [];
}

function isIbead(bead) {
  const labels = bead.labels || [];
  return labels.includes('type:ibead') || (bead.title || '').match(/^IB-/);
}

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

function extractDomainsFromOutput(output) {
  if (!output) return [];
  const domains = new Set();
  const processMatches = output.matchAll(/Process:\s*([^\n]+)/gi);
  for (const m of processMatches) domains.add(m[1].trim().replace(/\s+/g, '-').toLowerCase());
  const fileMatches = output.matchAll(/(?:src|lib|app|packages)\/([^/\n]+)\//gi);
  for (const m of fileMatches) domains.add(m[1].trim().toLowerCase());
  return [...domains].slice(0, 5);
}

function detectStaleness(ibead, currentDomains) {
  const meta = ibead.metadata || {};
  const ibDomain = meta.domain || '';
  if (!ibDomain) return false;
  // If the ibead's domain no longer appears in current GitNexus results, flag as potentially stale
  return currentDomains.length > 0 && !currentDomains.some(d =>
    d.includes(ibDomain.split('-')[0]) || ibDomain.includes(d.split('-')[0])
  );
}

async function processBead(beadId, dryRun) {
  const result = {
    beadId,
    beadTitle: '',
    existingIbeads: 0,
    newIbeadsAdded: 0,
    staleIbeadsFlagged: 0,
    ibeadsNeedingAttention: [],
    errors: [],
  };

  try {
    // Fetch bead details (bd show returns an array)
    const beadRaw = runBd(['show', beadId, '--json']);
    const beadParsed = parseJson(beadRaw, `bd show ${beadId}`);
    const bead = Array.isArray(beadParsed) ? beadParsed[0] : beadParsed;
    result.beadTitle = bead?.title || beadId;

    // Get existing ibeads
    const childrenRaw = runBd(['children', beadId, '--json']);
    const allChildren = toArray(parseJson(childrenRaw, 'bd children'));
    const existingIbeads = allChildren.filter(c => isIbead(c) && c.status !== 'closed');
    result.existingIbeads = existingIbeads.length;

    // Re-run GitNexus for current blast radius
    const terms = extractTerms(result.beadTitle);
    const gnOutput = terms ? runGitNexus(['query', terms]) : null;
    const currentDomains = extractDomainsFromOutput(gnOutput);

    // Check for stale ibeads
    for (const ib of existingIbeads) {
      if (detectStaleness(ib, currentDomains)) {
        result.staleIbeadsFlagged++;
        result.ibeadsNeedingAttention.push({
          id: ib.id,
          reason: 'domain no longer in current blast radius — may be resolved',
          action: 'review and close if clean',
        });
      }
    }

    // Check if new domains emerged that don't have ibeads yet
    const existingDomains = new Set(
      existingIbeads.map(ib => (ib.metadata || {}).domain || '').filter(Boolean)
    );

    const newDomains = currentDomains.filter(d => !existingDomains.has(d));
    if (newDomains.length > 0 && existingIbeads.length < 5) {
      // Create new ibeads for emerging domains
      const toCreate = newDomains.slice(0, 5 - existingIbeads.length);
      if (!dryRun && toCreate.length > 0) {
        const parentIdShort = beadId.split('-').pop() || beadId;
        for (let i = 0; i < toCreate.length; i++) {
          const domain = toCreate[i];
          const ibTitle = `IB-${parentIdShort}-R${i + 1}: [${domain}] re-research ibead — ${result.beadTitle}`;
          const metadata = JSON.stringify({
            ibead: true,
            domain,
            depth: 1,
            gitnexus_pass: 1,
            task_list: [],
            parent_title: result.beadTitle,
            generated_by: 'ibead:research',
          });
          runBd(['create', ibTitle, '--parent', beadId, '--label', 'type:ibead', '--metadata', metadata]);
          result.newIbeadsAdded++;
          result.ibeadsNeedingAttention.push({
            id: `IB-${parentIdShort}-R${i + 1}`,
            reason: `new domain "${domain}" emerged from current GitNexus analysis`,
            action: 'run ibead:audit to populate task list',
          });
        }
      } else if (dryRun) {
        result.newIbeadsAdded = toCreate.length;
        for (const d of toCreate) {
          result.ibeadsNeedingAttention.push({
            id: '[dry-run]',
            reason: `new domain "${d}" would be created`,
            action: 'run without --dry-run to create',
          });
        }
      }
    }

  } catch (err) {
    result.errors.push(err.message);
  }

  return result;
}

async function main() {
  const args = parseArgs(process.argv.slice(2));

  console.log(`\nibead:research — ${new Date().toISOString().slice(0, 10)}`);
  if (args.dryRun) console.log(`  [dry-run mode]\n`);
  else console.log('');

  let beadIds = [];

  if (args.bead) {
    beadIds = [args.bead];
  } else {
    // Fetch all open + in_progress beads that are NOT ibeads
    const openRaw = runBd(['list', '--status=open', '--json']);
    const inProgressRaw = runBd(['list', '--status=in_progress', '--json']);

    const openBeads = toArray(parseJson(openRaw, 'bd list open'));
    const inProgressBeads = toArray(parseJson(inProgressRaw, 'bd list in_progress'));
    const allBeads = [...openBeads, ...inProgressBeads];

    beadIds = allBeads
      .filter(b => !isIbead(b))
      .map(b => b.id)
      .filter(Boolean);
  }

  if (beadIds.length === 0) {
    console.log('  No open/in-progress beads found.\n');
    process.exit(0);
  }

  console.log(`  Beads to scan: ${beadIds.length}\n`);

  const results = [];
  let totalNew = 0;
  let totalStale = 0;
  let totalAttention = 0;

  for (const beadId of beadIds) {
    process.stdout.write(`  Scanning ${beadId}...`);
    const result = await processBead(beadId, args.dryRun);
    results.push(result);

    totalNew += result.newIbeadsAdded;
    totalStale += result.staleIbeadsFlagged;
    totalAttention += result.ibeadsNeedingAttention.length;

    const status = result.errors.length > 0
      ? ` ❌ error: ${result.errors[0]}`
      : ` ✓ (${result.existingIbeads} ibeads, +${result.newIbeadsAdded} new, ${result.staleIbeadsFlagged} stale)`;
    console.log(status);
  }

  // Summary report
  const summary = {
    timestamp: new Date().toISOString(),
    beadsScanned: beadIds.length,
    ibeadsNew: totalNew,
    ibeadsStale: totalStale,
    ibeadsNeedingAttention: totalAttention,
    results,
    dryRun: args.dryRun,
  };

  console.log(`
ibead:research complete — ${summary.timestamp.slice(0, 10)}
  Beads scanned:              ${summary.beadsScanned}
  Ibeads added:               ${summary.ibeadsNew}
  Ibeads flagged stale:       ${summary.ibeadsStale}
  Ibeads needing attention:   ${summary.ibeadsNeedingAttention}
`);

  if (totalAttention > 0) {
    console.log('  Action required:');
    for (const r of results) {
      for (const item of r.ibeadsNeedingAttention) {
        console.log(`    ${item.id}  [${r.beadId}]  ${item.reason}`);
        console.log(`      → ${item.action}`);
      }
    }
    console.log('');
  }

  if (args.json) {
    process.stdout.write(`${JSON.stringify(summary, null, 2)}\n`);
  }
}

main().catch(err => {
  console.error(`ibead:research failed: ${err.message}`);
  process.exit(1);
});
