---
id: find-duplicates
title: bd find-duplicates
slug: /cli-reference/find-duplicates
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc find-duplicates`

## bd find-duplicates

Find issues that are semantically similar but not exact duplicates.

Unlike 'bd duplicates' which finds exact content matches, find-duplicates
uses text similarity or AI to find issues that discuss the same topic
with different wording.

Approaches:
  mechanical  Token-based text similarity (default, no API key needed)
  ai          LLM-based semantic comparison (requires ANTHROPIC_API_KEY or ai.api_key)

The mechanical approach tokenizes titles and descriptions, then computes
Jaccard similarity between all issue pairs. It's fast and free but may
miss semantically similar issues with very different wording.

The AI approach sends candidate pairs to Claude for semantic comparison.
It first uses mechanical pre-filtering to reduce the number of API calls,
then asks the LLM to judge whether the remaining pairs are true duplicates.

Examples:
  bd find-duplicates                       # Mechanical similarity (default)
  bd find-duplicates --threshold 0.4       # Lower threshold = more results
  bd find-duplicates --method ai           # Use AI for semantic comparison
  bd find-duplicates --status open         # Only check open issues
  bd find-duplicates --limit 20            # Show top 20 pairs
  bd find-duplicates --json                # JSON output

```
bd find-duplicates [flags]
```

**Aliases:** find-dups

**Flags:**

```
  -n, --limit int         Maximum number of pairs to show (default 50)
      --method string     Detection method: mechanical, ai (default "mechanical")
      --model string      AI model to use (only with --method ai; default from config ai.model)
  -s, --status string     Filter by status (default: non-closed)
      --threshold float   Similarity threshold (0.0-1.0, lower = more results) (default 0.5)
```
