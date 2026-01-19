#!/usr/bin/env -S uv run
# /// script
# requires-python = ">=3.10"
# dependencies = [
#   "anthropic>=0.18.0",
#   "openai>=1.0.0",
#   "python-dotenv>=1.0.0",
#   "typer>=0.9.0",
# ]
# ///

"""
Generate a weekly Beads newsletter based on changelog, commits, and changes.

Usage:
    python generate-newsletter.py
    python generate-newsletter.py --model gpt-4o
    python generate-newsletter.py --since 2025-12-15
    python generate-newsletter.py --days 30
    python generate-newsletter.py --from-release v0.39 --to-release v0.48

Environment Variables:
    AI_MODEL        - The AI model to use (default: "claude-opus-4-1-20250805", e.g., "gpt-4o")
    ANTHROPIC_API_KEY or OPENAI_API_KEY - API credentials
    AUTO_COMMIT     - If "true", automatically commit and push the newsletter

Configuration File:
    .env            - Optional dotenv file in project root for API keys
"""

import os
import re
import subprocess
import sys
from datetime import datetime, timedelta
from pathlib import Path
from typing import Optional

import typer

# Load environment variables from .env file if it exists
try:
    from dotenv import load_dotenv
    load_dotenv()
except ImportError:
    pass  # python-dotenv is optional, env vars can be set directly

# Try to import anthropic, fall back to openai if available
try:
    from anthropic import Anthropic
    ANTHROPIC_AVAILABLE = True
except ImportError:
    ANTHROPIC_AVAILABLE = False

try:
    from openai import OpenAI
    OPENAI_AVAILABLE = True
except ImportError:
    OPENAI_AVAILABLE = False


# Newsletter prompt template
NEWSLETTER_PROMPT_TEMPLATE = """Generate a Beads newsletter covering the period from {since_date} to {until_date}.

Reporting Period:
- Date Range: {since_date} to {until_date}
- {version_info}

## Recent Commits (last 50)
{commit_summary}

## Changelog for {version}
{changelog}

{new_commands_text}

{breaking_text}

Please write a newsletter that includes:
1. **Header with release versions** - ALWAYS state the release versions AND beginning & end of reporting range at the top: "v0.47.0 - v0.48.0, 2026-Jan-10 to 2026-Jan-17"
2. A brief intro about the release period
3. **New Commands & Options** section - describe what each does, why users should care, and show brief example
4. **Breaking Changes** section (if any) - explain what changed and why, migration path if applicable
5. Major Features & Bug Fixes (3-5 most significant)
6. Minor improvements/changes
7. Getting started section
8. Link to full changelog.md and GH release page

Default to writing in narrative paragraphs, use bullets sparingly.
Mention dates, significant commit hashes, and link to the relevant docs adjacent to their sections.
Keep it 500 to 1000 words.
Use emoji where appropriate.
Format in Markdown.
"""


def check_git_branch() -> Optional[str]:
    """Check current git branch and warn if not on main."""
    try:
        result = subprocess.run(
            ["git", "rev-parse", "--abbrev-ref", "HEAD"],
            capture_output=True,
            text=True,
            check=True
        )
        current_branch = result.stdout.strip()
        return current_branch
    except subprocess.CalledProcessError:
        # Not in a git repo or git not available
        return None


def get_all_versions() -> list[tuple[str, datetime]]:
    """Extract all versions and dates from CHANGELOG.md."""
    changelog_path = Path(__file__).parent.parent / "CHANGELOG.md"

    with open(changelog_path) as f:
        content = f.read()

    # Match version pattern like [0.44.0] - 2026-01-04
    pattern = r'## \[(\d+\.\d+\.\d+)\] - (\d{4}-\d{2}-\d{2})'
    matches = re.finditer(pattern, content)

    versions = []
    for match in matches:
        version = match.group(1)
        date_str = match.group(2)
        date = datetime.strptime(date_str, "%Y-%m-%d")
        versions.append((version, date))

    return versions


def get_version_by_release(version_str: str) -> tuple[str, datetime]:
    """Find a specific version by version string (e.g., 'v0.44.0' or '0.44.0')."""
    versions = get_all_versions()
    
    # Normalize the input (remove 'v' prefix if present)
    normalized = version_str.lstrip('v')
    
    for version, date in versions:
        if version == normalized:
            return version, date
    
    raise ValueError(f"Version {version_str} not found in CHANGELOG.md")


def get_previous_version() -> tuple[str, datetime]:
    """Extract the most recent version and date from CHANGELOG.md."""
    versions = get_all_versions()
    if versions:
        return versions[0]
    raise ValueError("Could not find any versions in CHANGELOG.md")


def get_commits_since(since_date: datetime) -> list[dict]:
    """Get git commits since the given date."""
    result = subprocess.run(
        ["git", "log", f"--since={since_date.strftime('%Y-%m-%d')}", "--oneline", "--format=%h|%s|%an|%ai"],
        capture_output=True,
        text=True
    )
    
    commits = []
    for line in result.stdout.strip().split('\n'):
        if line:
            parts = line.split('|', 3)
            if len(parts) >= 2:
                commit_date = None
                if len(parts) >= 4:
                    try:
                        # Parse as naive datetime (extract just the date part)
                        date_str = parts[3].strip().split()[0]
                        commit_date = datetime.strptime(date_str, "%Y-%m-%d")
                    except (ValueError, AttributeError, IndexError):
                        pass
                
                commits.append({
                    'hash': parts[0],
                    'subject': parts[1],
                    'author': parts[2] if len(parts) > 2 else 'unknown',
                    'date': commit_date,
                })
    
    return commits


def get_changelog_section(version: str) -> str:
    """Extract the changelog section for a specific version."""
    changelog_path = Path(__file__).parent.parent / "CHANGELOG.md"
    
    with open(changelog_path) as f:
        content = f.read()
    
    # Find the section for this version
    pattern = rf'## \[{re.escape(version)}\].*?(?=## \[|\Z)'
    match = re.search(pattern, content, re.DOTALL)
    
    if match:
        return match.group(0)
    
    return ""


def extract_new_commands(from_version: str, to_version: str) -> list[dict]:
    """Extract new commands added between versions by diffing cmd/ directory.
    
    Returns list of dicts with: name, short_desc, file_path
    """
    try:
        # Normalize version strings (remove 'v' prefix if present)
        from_ver = from_version.lstrip('v')
        to_ver = to_version.lstrip('v')
        
        # Try with v prefix first, then without
        for prefix in ['v', '']:
            result = subprocess.run(
                ["git", "diff", f"{prefix}{from_ver}..{prefix}{to_ver}", "--name-only", "--", "cmd/bd"],
                capture_output=True,
                text=True,
                check=False
            )
            
            if result.returncode == 0:
                break
        
        if result.returncode != 0:
            # Versions don't exist as tags, return empty
            return []
        
        changed_files = result.stdout.strip().split('\n') if result.stdout.strip() else []
        
        commands = []
        seen = set()
        
        for file_path in changed_files:
            if not file_path or file_path.endswith('_test.go'):
                continue
            
            # Read the file to extract cobra.Command definitions
            try:
                full_path = Path(__file__).parent.parent / file_path
                content = full_path.read_text()
                
                # Look for patterns like: var someCmd = &cobra.Command{ ... Use: "commandname" ... Short: "description"
                cmd_pattern = r'var\s+(\w+Cmd)\s*=\s*&cobra\.Command\{[^}]*?Use:\s*["\']([^"\']+)["\'][^}]*?Short:\s*["\']([^"\']+)["\']'
                for match in re.finditer(cmd_pattern, content, re.DOTALL):
                    var_name = match.group(1)
                    use = match.group(2)
                    short = match.group(3)
                    
                    # Extract just the command name (first word)
                    cmd_name = use.split()[0]
                    
                    # Skip if we've seen this one
                    if cmd_name not in seen:
                        seen.add(cmd_name)
                        commands.append({
                            'name': cmd_name,
                            'short': short,
                            'file': file_path
                        })
            except (FileNotFoundError, IOError):
                continue
        
        return sorted(commands, key=lambda x: x['name'])[:5]  # Top 5
    except (subprocess.CalledProcessError, Exception):
        return []


def extract_breaking_changes(changelog_section: str) -> list[dict]:
    """Extract breaking changes from changelog section.
    
    Returns list of dicts with: title, description
    """
    if not changelog_section:
        return []
    
    breaking = []
    
    # Look for "Breaking" subsection
    breaking_pattern = r'###\s+Breaking[^\n]*\n(.*?)(?=###|\Z)'
    breaking_match = re.search(breaking_pattern, changelog_section, re.IGNORECASE | re.DOTALL)
    
    if breaking_match:
        breaking_text = breaking_match.group(1)
        # Split by top-level bullet points (not indented)
        # Matches: - **Title** followed by optional inline description or newline
        items = re.findall(r'^[-*]\s+\*\*([^*]+)\*\*\s*(?:-\s+)?([^\n]*)', breaking_text, re.MULTILINE)
        for title, description in items[:5]:  # Top 5
            # Filter out empty descriptions or nested bullet continuations
            desc = description.strip()
            if desc and not desc.startswith('-'):
                breaking.append({
                    'title': title.strip(),
                    'description': desc
                })
    
    return breaking


def find_docs_for_command(command_name: str) -> str:
    """Find documentation for a command in README, docs/, or CHANGELOG.
    
    Returns relevant excerpt or empty string.
    """
    # Search in order of priority
    search_files = [
        Path(__file__).parent.parent / "README.md",
    ]
    
    # Add docs files
    docs_dir = Path(__file__).parent.parent / "docs"
    if docs_dir.exists():
        search_files.extend(sorted(docs_dir.glob("*.md")))
    
    for file_path in search_files:
        try:
            content = file_path.read_text()
            # Look for command mention with context
            pattern = rf'`{re.escape(command_name)}`|bd\s+{re.escape(command_name)}'
            if re.search(pattern, content, re.IGNORECASE):
                # Find the paragraph/section containing this command
                matches = re.finditer(rf'[^\n]*{re.escape(command_name)}[^\n]*', content, re.IGNORECASE)
                for match in matches:
                    start = max(0, match.start() - 200)
                    end = min(len(content), match.end() + 200)
                    excerpt = content[start:end].strip()
                    if len(excerpt) > 20:  # Filter out noise
                        return excerpt
        except (FileNotFoundError, IOError):
            continue
    
    return ""


def get_model_pricing(model: str) -> tuple[float, float]:
    """Get pricing for a model (input_cost, output_cost per 1M tokens).
    
    Returns tuple of (input_price_per_1m, output_price_per_1m).
    Prices in dollars per million tokens.
    """
    model_lower = model.lower()
    
    # Anthropic models
    if 'opus-4-1' in model_lower or 'opus-4.1' in model_lower:
        return (15.0, 45.0)
    elif 'opus' in model_lower:
        return (15.0, 45.0)
    elif 'sonnet-4-5' in model_lower:
        return (3.0, 15.0)
    elif 'sonnet' in model_lower:
        return (3.0, 15.0)
    elif 'haiku-4-5' in model_lower:
        return (0.80, 4.0)
    elif 'haiku' in model_lower:
        return (0.80, 4.0)
    
    # OpenAI models
    elif 'gpt-4o' in model_lower:
        return (5.0, 15.0)
    elif 'gpt-4-turbo' in model_lower:
        return (10.0, 30.0)
    elif 'gpt-4' in model_lower:
        return (30.0, 60.0)
    elif 'gpt-3.5' in model_lower:
        return (0.50, 1.50)
    
    # Default: unknown pricing
    return (0.0, 0.0)


def get_model_cost_info(model: str) -> str:
    """Get cost information for a model.
    
    Returns a string with relative cost tier (cheap, moderate, expensive) and approximate cost.
    """
    model_lower = model.lower()
    input_price, output_price = get_model_pricing(model)
    
    if input_price == 0:
        return f"{model} (cost unknown)"
    
    # Anthropic models
    if 'opus-4-1' in model_lower or 'opus-4.1' in model_lower:
        return f"claude-opus-4-1 (${input_price}/${output_price} per 1M input/output tokens)"
    elif 'opus' in model_lower:
        return f"claude-opus (${input_price}/${output_price} per 1M input/output tokens)"
    elif 'sonnet-4-5' in model_lower:
        return f"claude-sonnet-4-5 (${input_price}/${output_price} per 1M input/output tokens)"
    elif 'sonnet' in model_lower:
        return f"claude-sonnet (${input_price}/${output_price} per 1M input/output tokens)"
    elif 'haiku-4-5' in model_lower:
        return f"claude-haiku-4-5 (${input_price}/${output_price} per 1M input/output tokens)"
    elif 'haiku' in model_lower:
        return f"claude-haiku (${input_price}/${output_price} per 1M input/output tokens)"
    
    # OpenAI models
    elif 'gpt-4o' in model_lower:
        return f"gpt-4o (${input_price}/${output_price} per 1M input/output tokens)"
    elif 'gpt-4-turbo' in model_lower:
        return f"gpt-4-turbo (${input_price}/${output_price} per 1M input/output tokens)"
    elif 'gpt-4' in model_lower:
        return f"gpt-4 (${input_price}/${output_price} per 1M input/output tokens)"
    elif 'gpt-3.5' in model_lower:
        return f"gpt-3.5-turbo (${input_price}/${output_price} per 1M input/output tokens)"
    
    return f"{model} (cost unknown)"


def calculate_cost(model: str, input_tokens: int, output_tokens: int) -> float:
    """Calculate the actual cost of a generation.
    
    Returns cost in dollars.
    """
    input_price, output_price = get_model_pricing(model)
    input_cost = (input_tokens / 1_000_000) * input_price
    output_cost = (output_tokens / 1_000_000) * output_price
    return input_cost + output_cost


def detect_ai_provider(model: str) -> str:
    """Detect AI provider from model name."""
    model_lower = model.lower()
    if 'claude' in model_lower:
        return 'anthropic'
    elif 'gpt' in model_lower or 'openai' in model_lower:
        return 'openai'
    elif 'o1' in model_lower or 'o3' in model_lower:
        return 'openai'  # OpenAI reasoning models
    else:
        return 'anthropic'  # Default


def get_ai_client(provider: str):
    """Get AI client based on provider."""
    if provider == 'anthropic':
        api_key = os.environ.get('ANTHROPIC_API_KEY')
        if not api_key:
            raise ValueError("ANTHROPIC_API_KEY environment variable not set")
        client = Anthropic(api_key=api_key)
        return client
    elif provider == 'openai':
        api_key = os.environ.get('OPENAI_API_KEY')
        if not api_key:
            raise ValueError("OPENAI_API_KEY environment variable not set")
        client = OpenAI(api_key=api_key)
        return client
    else:
        raise ValueError(f"Unknown provider: {provider}")


def build_newsletter_prompt(commits: list[dict], changelog: str, version: str, since_date: datetime, 
                           until_date: datetime = None,
                           new_commands: list[dict] = None, breaking_changes: list[dict] = None,
                           from_version: str = None, to_version: str = None) -> str:
    """Build the newsletter prompt from components.
    
    Returns the formatted prompt string.
    """
    if until_date is None:
        until_date = datetime.now()
    
    commit_summary = "\n".join([f"- {c['subject']}" for c in commits[:50]])
    
    # Build structured sections
    new_commands_text = ""
    if new_commands:
        new_commands_text = "## New Commands & Options\n"
        for cmd in new_commands:
            new_commands_text += f"- **{cmd['name']}** - {cmd['short']}\n"
    
    breaking_text = ""
    if breaking_changes:
        breaking_text = "## Breaking Changes\n"
        for change in breaking_changes:
            breaking_text += f"- **{change['title']}** - {change['description']}\n"
    
    # Build version info for header
    version_info = ""
    if from_version and to_version:
        version_info = f"Release Range: v{from_version} to v{to_version}"
    elif from_version:
        version_info = f"Starting from: v{from_version}"
    elif version and version != "Newsletter":
        version_info = f"Version: {version}"
    else:
        version_info = f"Period: {version}"
    
    since_str = since_date.strftime('%B %d, %Y')
    until_str = until_date.strftime('%B %d, %Y')
    
    return NEWSLETTER_PROMPT_TEMPLATE.format(
        since_date=since_str,
        until_date=until_str,
        version_info=version_info,
        commit_summary=commit_summary,
        version=version,
        changelog=changelog[:3000],
        new_commands_text=new_commands_text,
        breaking_text=breaking_text,
    )


def generate_with_claude(client, commits: list[dict], changelog: str, version: str, since_date: datetime, 
                        until_date: datetime = None,
                        new_commands: list[dict] = None, breaking_changes: list[dict] = None,
                        from_version: str = None, to_version: str = None) -> tuple[str, int, int]:
     """Generate newsletter using Claude."""
     prompt = build_newsletter_prompt(commits, changelog, version, since_date, until_date,
                                     new_commands, breaking_changes, from_version, to_version)

     response = client.messages.create(
         model="claude-opus-4-1-20250805",
         max_tokens=4000,
         messages=[
             {"role": "user", "content": prompt}
         ]
     )
     
     # Extract token usage
     input_tokens = response.usage.input_tokens
     output_tokens = response.usage.output_tokens
     
     return response.content[0].text, input_tokens, output_tokens


def generate_with_openai(client, commits: list[dict], changelog: str, version: str, since_date: datetime,
                         until_date: datetime = None,
                         new_commands: list[dict] = None, breaking_changes: list[dict] = None,
                         from_version: str = None, to_version: str = None) -> tuple[str, int, int]:
     """Generate newsletter using OpenAI."""
     prompt = build_newsletter_prompt(commits, changelog, version, since_date, until_date,
                                     new_commands, breaking_changes, from_version, to_version)

     response = client.chat.completions.create(
         model="gpt-4o",
         messages=[
             {"role": "user", "content": prompt}
         ],
         max_tokens=4000
     )
     
     # Extract token usage
     input_tokens = response.usage.prompt_tokens
     output_tokens = response.usage.completion_tokens
     
     return response.choices[0].message.content, input_tokens, output_tokens


def generate_newsletter(
    model: Optional[str] = None,
    since_date: Optional[datetime] = None,
    until_date: Optional[datetime] = None,
    version: Optional[str] = None,
    from_version: Optional[str] = None,
    to_version: Optional[str] = None,
) -> tuple[str, str, datetime, datetime, int, int, float]:
    """Generate newsletter content.
    
    Returns:
        (newsletter_content, version_range, since_date, until_date)
    """
    # Determine the time period
    if since_date is None or until_date is None:
        # Use the current version as reference
        curr_version, curr_date = get_previous_version()
        
        if since_date is None:
            # Check if we should use last week or since last release
            week_ago = datetime.now() - timedelta(days=7)
            since_date = curr_date if curr_date > week_ago else week_ago
        
        if until_date is None:
            until_date = datetime.now()
        
        version = curr_version
    else:
        # since_date and until_date are provided
        if version is None:
            version = "Newsletter"
    
    # Get commits in the specified date range
    commits = get_commits_since(since_date)
    # Filter commits to be before until_date (both are naive datetimes)
    commits = [c for c in commits if c.get('date') is None or c['date'] <= until_date]
    
    # Get changelog for this version if applicable
    changelog = ""
    if version and version != "Newsletter":
        changelog = get_changelog_section(version)
    
    # Extract new commands and breaking changes if we have version info
    new_commands = []
    breaking_changes = []
    
    if from_version and to_version:
        # Extract from actual version range
        new_commands = extract_new_commands(from_version, to_version)
        if changelog:
            breaking_changes = extract_breaking_changes(changelog)
    elif version and version != "Newsletter":
        # Try to extract from changelog if version is available
        if changelog:
            breaking_changes = extract_breaking_changes(changelog)
    
    # Determine AI model
    if model is None:
        model = os.environ.get('AI_MODEL', 'claude-opus-4-1-20250805')
    
    # Detect provider and generate
    provider = detect_ai_provider(model)
    
    cost_info = get_model_cost_info(model)
    typer.echo(f"Using AI provider: {provider}")
    typer.echo(f"Model: {cost_info}")
    typer.echo(f"Period: {since_date.strftime('%Y-%m-%d')} to {until_date.strftime('%Y-%m-%d')}")
    typer.echo(f"Found {len(commits)} commits")
    if new_commands:
        typer.echo(f"Found {len(new_commands)} new commands")
    if breaking_changes:
        typer.echo(f"Found {len(breaking_changes)} breaking changes")
    
    client = get_ai_client(provider)
    
    if provider == 'anthropic':
        newsletter, input_tokens, output_tokens = generate_with_claude(client, commits, changelog, version, since_date, until_date, new_commands, breaking_changes, from_version, to_version)
    else:
        newsletter, input_tokens, output_tokens = generate_with_openai(client, commits, changelog, version, since_date, until_date, new_commands, breaking_changes, from_version, to_version)
    
    # Calculate actual cost
    actual_cost = calculate_cost(model, input_tokens, output_tokens)
    
    return newsletter, version, since_date, until_date, input_tokens, output_tokens, actual_cost


app = typer.Typer(help="Generate a weekly Beads newsletter based on changelog and commits")


@app.command()
def main(
    model: Optional[str] = typer.Option(None, "--model", "-m", help="AI model to use (default: claude-opus-4-1-20250805, e.g., gpt-4o, claude-sonnet-4-5-20250929)"),
    output: str = typer.Option("NEWSLETTER.md", "--output", "-o", help="Output file"),
    dry_run: bool = typer.Option(False, "--dry-run", help="Print to stdout instead of writing file"),
    force: bool = typer.Option(False, "--force", "-f", help="Skip branch check warning"),
    since: Optional[str] = typer.Option(None, "--since", help="Start date (YYYY-MM-DD) or relative (e.g., 14d for last 14 days)"),
    days: Optional[int] = typer.Option(None, "--days", help="Generate for the last N days"),
    from_release: Optional[str] = typer.Option(None, "--from-release", help="Start from a specific release (e.g., v0.39.0 or 0.39.0)"),
    to_release: Optional[str] = typer.Option(None, "--to-release", help="End at a specific release (e.g., v0.48.0 or 0.48.0)"),
):
    """Generate a newsletter for a specified time period or release range.
    
    Examples:
        # Generate for last week (default)
        python generate-newsletter.py
        
        # Generate for last 30 days
        python generate-newsletter.py --days 30
        
        # Generate since a specific date
        python generate-newsletter.py --since 2025-12-15
        
        # Generate between two releases
        python generate-newsletter.py --from-release v0.39.0 --to-release v0.48.0
    """
    # Check git branch before proceeding
    if not force:
        current_branch = check_git_branch()
        if current_branch is None or current_branch == 'HEAD':
            typer.echo("⚠️  WARNING: You are in detached HEAD state (not on any branch)", err=True)
            typer.echo("   Releases are made from the 'main' branch.", err=True)
            typer.echo("   The newsletter will be generated from the current commit's CHANGELOG.md.", err=True)
            typer.echo("   This may result in an outdated newsletter if you're not on the latest main.", err=True)
            if not typer.confirm("Continue anyway?"):
                typer.echo("Aborted.")
                raise typer.Exit(0)
        elif current_branch != 'main':
            typer.echo(f"⚠️  WARNING: You are on branch '{current_branch}', not 'main'", err=True)
            typer.echo("   Releases are made from the 'main' branch.", err=True)
            typer.echo("   The newsletter will be generated from the current branch's CHANGELOG.md.", err=True)
            typer.echo("   This may result in an outdated newsletter if your branch is behind main.", err=True)
            if not typer.confirm("Continue anyway?"):
                typer.echo("Aborted.")
                raise typer.Exit(0)

    try:
        # Determine time period based on arguments
        since_date: Optional[datetime] = None
        until_date: Optional[datetime] = None
        version: Optional[str] = None

        if from_release and to_release:
            # Both releases specified
            start_ver, start_date = get_version_by_release(from_release)
            end_ver, end_date = get_version_by_release(to_release)
            since_date = start_date
            until_date = end_date
            version = f"{start_ver} to {end_ver}"
        elif from_release:
            # Only from_release specified
            start_ver, start_date = get_version_by_release(from_release)
            since_date = start_date
            until_date = datetime.now()
            version = f"{start_ver} to present"
        elif to_release:
            # Only to_release specified (generate up to that release)
            end_ver, end_date = get_version_by_release(to_release)
            since_date = datetime(2000, 1, 1)  # From beginning
            until_date = end_date
            version = f"up to {end_ver}"
        elif days:
            # Last N days
            until_date = datetime.now()
            since_date = until_date - timedelta(days=days)
            version = f"Last {days} days"
        elif since:
            # Parse since parameter
            until_date = datetime.now()
            if since.endswith('d'):
                # Relative: e.g., "14d"
                num_days = int(since[:-1])
                since_date = until_date - timedelta(days=num_days)
                version = f"Last {num_days} days"
            else:
                # Absolute date
                since_date = datetime.strptime(since, "%Y-%m-%d")
                version = f"Since {since}"

        newsletter, version_str, start, end, input_tokens, output_tokens, actual_cost = generate_newsletter(
            model=model,
            since_date=since_date,
            until_date=until_date,
            version=version,
            from_version=from_release,
            to_version=to_release,
        )

        # Display generation summary
        typer.echo("")
        typer.echo("=== Generation Summary ===")
        typer.echo(f"Tokens used: {input_tokens:,} input + {output_tokens:,} output = {input_tokens + output_tokens:,} total")
        typer.echo(f"Estimated cost: ${actual_cost:.4f}")

        if dry_run:
            typer.echo("")
            typer.echo(newsletter)
        else:
            Path(output).write_text(newsletter)
            typer.echo(f"Newsletter written to {output}")

            # Optionally commit and push
            if os.environ.get('AUTO_COMMIT', '').lower() == 'true':
                subprocess.run(['git', 'add', output], check=True)
                commit_msg = f'docs: update newsletter for {version_str}'
                subprocess.run(['git', 'commit', '-m', commit_msg], check=True)
                subprocess.run(['git', 'push'], check=True)
                typer.echo("Committed and pushed newsletter")

    except Exception as e:
        typer.echo(f"Error: {e}", err=True)
        raise typer.Exit(1)


if __name__ == "__main__":
    app()
