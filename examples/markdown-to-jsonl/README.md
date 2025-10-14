# Markdown to JSONL Converter

Convert markdown planning documents into `bd` issues.

## Overview

This example shows how to bridge the gap between markdown planning docs and tracked issues, without adding complexity to the `bd` core tool.

The converter script (`md2jsonl.py`) parses markdown files and outputs JSONL that can be imported into `bd`.

## Features

- ✅ **YAML Frontmatter** - Extract metadata (priority, type, assignee)
- ✅ **Headings as Issues** - Each H1/H2 becomes an issue
- ✅ **Task Lists** - Markdown checklists become sub-issues
- ✅ **Dependency Parsing** - Extract "blocks: bd-10" references
- ✅ **Customizable** - Modify the script for your conventions

## Usage

### Basic conversion

```bash
python md2jsonl.py feature.md | bd import
```

### Save to file first

```bash
python md2jsonl.py feature.md > issues.jsonl
bd import -i issues.jsonl
```

### Preview before importing

```bash
python md2jsonl.py feature.md | jq .
```

## Markdown Format

### Frontmatter (Optional)

```markdown
---
priority: 1
type: feature
assignee: alice
---
```

### Headings

Each heading becomes an issue:

```markdown
# Main Feature

Description of the feature...

## Sub-task 1

Details about sub-task...

## Sub-task 2

More details...
```

### Task Lists

Task lists are converted to separate issues:

```markdown
## Setup Tasks

- [ ] Install dependencies
- [x] Configure database
- [ ] Set up CI/CD
```

Creates 3 issues (second one marked as closed).

### Dependencies

Reference other issues in the description:

```markdown
## Implement API

This task requires the database schema to be ready first.

Dependencies:
- blocks: bd-5
- related: bd-10, bd-15
```

The script extracts these and creates dependency records.

## Example

See `example-feature.md` for a complete example.

```bash
# Convert the example
python md2jsonl.py example-feature.md > example-issues.jsonl

# View the output
cat example-issues.jsonl | jq .

# Import into bd
bd import -i example-issues.jsonl
```

## Customization

The script is intentionally simple so you can customize it for your needs:

1. **Different heading levels** - Modify which headings become issues (H1 only? H1-H3?)
2. **Custom metadata** - Parse additional frontmatter fields
3. **Labels** - Extract hashtags or keywords as labels
4. **Epic detection** - Top-level headings become epics
5. **Issue templates** - Map different markdown structures to issue types

## Limitations

This is a simple example, not a production tool:

- Basic YAML parsing (no nested structures)
- Simple dependency extraction (regex-based)
- No validation of referenced issue IDs
- Doesn't handle all markdown edge cases

For production use, you might want to:
- Use a proper YAML parser (`pip install pyyaml`)
- Use a markdown parser (`pip install markdown` or `python-markdown2`)
- Add validation and error handling
- Support more dependency formats

## Philosophy

This example demonstrates the **lightweight extension pattern**:

- ✅ Keep `bd` core focused and minimal
- ✅ Let users customize for their workflows
- ✅ Use existing import infrastructure
- ✅ Easy to understand and modify

Rather than adding markdown support to `bd` core (800+ LOC + dependencies + maintenance), we provide a simple converter that users can adapt.

## Contributing

Have improvements? Found a bug? This is just an example, but contributions are welcome!

Consider:
- Better error messages
- More markdown patterns
- Integration with popular markdown formats
- Support for GFM (GitHub Flavored Markdown) extensions

## See Also

- [bd README](../../README.md) - Main documentation
- [Python Agent Example](../python-agent/) - Full agent workflow
- [JSONL Format](../../TEXT_FORMATS.md) - Understanding bd's JSONL structure
