---
id: kv
title: bd kv
slug: /cli-reference/kv
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc kv`

## bd kv

Commands for working with the beads key-value store.

The key-value store is useful for storing flags, environment variables,
or other user-defined data that persists across sessions.

Examples:
  bd kv set mykey myvalue    # Set a value
  bd kv get mykey            # Get a value
  bd kv clear mykey          # Delete a key
  bd kv list                 # List all key-value pairs

```
bd kv
```

### bd kv clear

Delete a key from the beads key-value store.

Examples:
  bd kv clear feature_flag
  bd kv clear api_endpoint

```
bd kv clear <key>
```

### bd kv get

Get a value from the beads key-value store.

Examples:
  bd kv get feature_flag
  bd kv get api_endpoint

```
bd kv get <key>
```

### bd kv list

List all key-value pairs in the beads key-value store.

Examples:
  bd kv list
  bd kv list --json

```
bd kv list
```

### bd kv set

Set a key-value pair in the beads key-value store.

This is useful for storing flags, environment variables, or other
user-defined data that persists across sessions.

Examples:
  bd kv set feature_flag true
  bd kv set api_endpoint https://api.example.com
  bd kv set max_retries 3

```
bd kv set <key> <value>
```
