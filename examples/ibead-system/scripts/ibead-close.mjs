#!/usr/bin/env node
/**
 * ibead-close.mjs — Close gate enforcer for beads and ibeads.
 *
 * Enforces the mandatory close sequence:
 *   1. All child ibeads must be closed (parent beads only)
 *   2. codex:rescue reminder is surfaced
 *   3. lint + type-check must pass
 *   4. bd close is executed
 *
 * Usage:
 *   node scripts/ibead-close.mjs --bead <id> [--dry-run] [--json] [--skip-quality]
 *   npm run ibead:close -- --bead GGV3-abc123
 *   npm run ibead:close -- --bead IB-abc123-1   (for ibeads)
 */

import { spawnSync } from 'node:child_process';

function usage() {
  console.error('Usage: node scripts/ibead-close.mjs --bead <id> [--dry-run] [--json] [--skip-quality]');
  process.exit(2);
}

function parseArgs(argv) {
  const parsed = { bead: '', dryRun: false, json: false, skipQuality: false };
  for (let i = 0; i < argv.length; i++) {
    const t = argv[i];
    if (t === '--bead') parsed.bead = argv[++i] ?? '';
    else if (t === '--dry-run') parsed.dryRun = true;
    else if (t === '--json') parsed.json = true;
    else if (t === '--skip-quality') parsed.skipQuality = true;
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

function runNpm(script) {
  const result = spawnSync('npm', ['run', script], {
    cwd: process.cwd(),
    encoding: 'utf8',
    timeout: 120000,
    stdio: 'pipe',
  });
  return {
    ok: result.status === 0,
    stdout: String(result.stdout || '').trim(),
    stderr: String(result.stderr || '').trim(),
    status: result.status,
  };
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
  if (typeof val === 'object' && val !== null) return Object.values(val).filter(Array.isArray)[0] ?? [];
  return [];
}

function isIbead(bead) {
  const labels = bead.labels || [];
  return labels.includes('type:ibead') || (bead.title || '').match(/^IB-/);
}

function main() {
  const args = parseArgs(process.argv.slice(2));
  if (!args.bead) usage();

  const gate = {
    beadId: args.bead,
    beadTitle: '',
    isIbead: false,
    isParent: false,
    childIbeadsCheck: { required: false, passed: false, open: [] },
    codexRescuePrompted: false,
    lintPassed: false,
    typeCheckPassed: false,
    closed: false,
    errors: [],
    dryRun: args.dryRun,
  };

  // 1. Fetch bead details (bd show returns an array)
  const beadRaw = runBd(['show', args.bead, '--json']);
  const beadParsed = parseJson(beadRaw, `bd show ${args.bead}`);
  const bead = Array.isArray(beadParsed) ? beadParsed[0] : beadParsed;
  gate.beadTitle = bead?.title || args.bead;
  gate.isIbead = isIbead(bead);
  gate.isParent = !gate.isIbead;

  console.log(`\nibead:close — Close gate for ${args.bead}`);
  console.log(`  Title: "${gate.beadTitle}"`);
  console.log(`  Type: ${gate.isIbead ? 'ibead' : 'parent bead'}\n`);

  // 2. For parent beads: check all ibeads are closed
  if (gate.isParent) {
    gate.childIbeadsCheck.required = true;
    const childrenRaw = runBd(['children', args.bead, '--json']);
    const allChildren = toArray(parseJson(childrenRaw, 'bd children'));
    const ibeads = allChildren.filter(c => isIbead(c));
    const openIbeads = ibeads.filter(c => c.status !== 'closed');

    if (openIbeads.length > 0) {
      gate.childIbeadsCheck.passed = false;
      gate.childIbeadsCheck.open = openIbeads.map(c => c.id);
      gate.errors.push(`${openIbeads.length} ibead(s) still open: ${gate.childIbeadsCheck.open.join(', ')}`);
      console.log(`  ❌ GATE FAILED: Open ibeads must be closed first:`);
      for (const ib of openIbeads) {
        console.log(`     · ${ib.id}: ${ib.title}`);
      }
      console.log(`\n  Run: npm run ibead:close -- --bead <ibead-id>  for each one first.\n`);
    } else {
      gate.childIbeadsCheck.passed = true;
      console.log(`  ✓ All ibeads closed (${ibeads.length} total)`);
    }

    if (!gate.childIbeadsCheck.passed) {
      if (args.json) process.stdout.write(`${JSON.stringify(gate, null, 2)}\n`);
      process.exit(1);
    }
  }

  // 3. codex:rescue reminder (mandatory — agent must confirm it was run)
  gate.codexRescuePrompted = true;
  console.log(`  ⚡ REQUIRED: codex:rescue must be run before closing.`);
  console.log(`     Run /codex:rescue in your session if not already done.`);
  console.log(`     codex:rescue identifies work that was missed or needs hardening.\n`);

  // 4. Quality gate: lint
  if (!args.skipQuality) {
    console.log(`  Running lint...`);
    const lintResult = runNpm('lint');
    if (lintResult.ok) {
      gate.lintPassed = true;
      console.log(`  ✓ Lint passed`);
    } else {
      gate.lintPassed = false;
      gate.errors.push('lint failed');
      console.log(`  ❌ Lint failed:`);
      const errLines = (lintResult.stdout + '\n' + lintResult.stderr)
        .split('\n').filter(l => l.includes('error') || l.includes('Error')).slice(0, 10);
      for (const l of errLines) console.log(`     ${l}`);
    }

    // 5. Quality gate: type-check
    console.log(`  Running type-check...`);
    const typeResult = runNpm('type-check');
    if (typeResult.ok) {
      gate.typeCheckPassed = true;
      console.log(`  ✓ Type-check passed`);
    } else {
      gate.typeCheckPassed = false;
      gate.errors.push('type-check failed');
      console.log(`  ❌ Type-check failed:`);
      const errLines = (typeResult.stdout + '\n' + typeResult.stderr)
        .split('\n').filter(l => l.trim()).slice(0, 10);
      for (const l of errLines) console.log(`     ${l}`);
    }

    if (!gate.lintPassed || !gate.typeCheckPassed) {
      console.log(`\n  ❌ CLOSE BLOCKED: Fix lint/type errors before closing.\n`);
      if (args.json) process.stdout.write(`${JSON.stringify(gate, null, 2)}\n`);
      process.exit(1);
    }
  } else {
    gate.lintPassed = true;
    gate.typeCheckPassed = true;
    console.log(`  ⚠ Quality gates skipped (--skip-quality flag)`);
  }

  // 6. All gates passed — close the bead
  console.log(`\n  All gates passed. Closing ${args.bead}...`);

  if (!args.dryRun) {
    runBd(['close', args.bead]);
    gate.closed = true;
    console.log(`  ✓ ${args.bead} closed successfully.\n`);
  } else {
    gate.closed = false;
    console.log(`  [dry-run] Would close ${args.bead}\n`);
  }

  if (args.json) process.stdout.write(`${JSON.stringify(gate, null, 2)}\n`);
}

main();
