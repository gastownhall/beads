#!/usr/bin/env python3
"""Exercise the workflow's Bash push step with real Git and one local remote.

Each attempt also gives the step a GITHUB_OUTPUT file: the step writes
pushed=1 there only once its push has landed, and the PR step after it reads
that to decide whether to refresh an open PR, so a run that pushed nothing
must leave it empty.
"""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--fixture', type=Path, required=True)
    args = parser.parse_args()
    script = sys.stdin.read()
    assert script.strip(), 'Missing actual production Bash step'
    root = args.fixture
    root.mkdir(exist_ok=True)
    real_git = shutil.which('git')
    env = dict(os.environ, GIT_CONFIG_NOSYSTEM='1', GIT_CONFIG_GLOBAL=os.devnull,
               GIT_TERMINAL_PROMPT='0', GIT_AUTHOR_NAME='fixture',
               GIT_AUTHOR_EMAIL='fixture@example.invalid', GIT_COMMITTER_NAME='fixture',
               GIT_COMMITTER_EMAIL='fixture@example.invalid')
    for key in list(env):
        if key.startswith('GIT_') and key not in {
                'GIT_CONFIG_NOSYSTEM', 'GIT_CONFIG_GLOBAL', 'GIT_TERMINAL_PROMPT',
                'GIT_AUTHOR_NAME', 'GIT_AUTHOR_EMAIL', 'GIT_COMMITTER_NAME',
                'GIT_COMMITTER_EMAIL'}:
            del env[key]
    remote = root / 'remote.git'
    ref = 'refs/heads/bot/worker-env-sync'
    auth_url = 'https://x-access-token:fixture-placeholder@github.com/fixture/owned.git'

    def git(cwd, *command, check=True):
        return subprocess.run([real_git, *command], cwd=cwd, env=env, text=True,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                              timeout=20, check=check)

    def files(cwd, version):
        for path, value in [('tools/rbe/worker-env.txt', f'fixture {version}\n'),
                            ('platforms/BUILD.bazel', f'worker-env: {version}\n')]:
            target = cwd / path
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(value)

    if not remote.exists():
        git(root, 'init', '--bare', str(remote))
        git(remote, 'config', 'core.hooksPath', 'hooks')
        seed = root / 'seed'
        seed.mkdir()
        git(seed, 'init')
        git(seed, 'config', 'core.hooksPath', '.git/hooks')
        files(seed, 'old')
        (seed / 'untouched.txt').write_text('preserve\n')
        git(seed, 'add', '.')
        git(seed, 'commit', '-m', 'fixture base')
        base = git(seed, 'rev-parse', 'HEAD').stdout.strip()
        (root / 'base').write_text(base)
        git(seed, 'push', remote.as_uri(), 'HEAD:refs/heads/main')
    base = (root / 'base').read_text()
    git(remote, 'update-ref', '-d', ref)
    wrapper_dir = root / 'bin'
    wrapper_dir.mkdir(exist_ok=True)
    wrapper = wrapper_dir / 'git'
    wrapper.write_text('''#!/usr/bin/env python3
import json, os, subprocess, sys
args = sys.argv[1:]
with open(os.environ['FIXTURE_CALLS'], 'a') as log:
    log.write(json.dumps(args) + '\\n')
args = [os.environ['FIXTURE_REMOTE'] if a == os.environ['FIXTURE_AUTH_URL'] else a for a in args]
if args and args[0] == 'push' and os.environ.get('FIXTURE_RACE_SHA'):
    subprocess.run([os.environ['FIXTURE_GIT'], '-C', os.environ['FIXTURE_BARE'],
                    'update-ref', os.environ['FIXTURE_REF'], os.environ['FIXTURE_RACE_SHA']], check=True)
os.execv(os.environ['FIXTURE_GIT'], [os.environ['FIXTURE_GIT'], *args])
''')
    wrapper.chmod(0o755)
    counter = 0

    def head():
        return git(remote, 'rev-parse', ref).stdout.strip()

    def attempt(label, version, remote_url=None, race=None):
        nonlocal counter
        counter += 1
        checkout = Path(tempfile.mkdtemp(prefix=f'fixed-{label}-', dir=root))
        git(checkout, 'init')
        git(checkout, 'config', 'core.hooksPath', '.git/hooks')
        git(checkout, 'fetch', '--no-tags', remote.as_uri(), base)
        git(checkout, 'checkout', '-b', 'main', 'FETCH_HEAD')
        files(checkout, version)
        calls = checkout / '.git' / 'calls.jsonl'
        output = checkout / '.git' / 'github_output'
        output.write_text('')
        run_env = dict(env, PATH=str(wrapper_dir) + os.pathsep + env['PATH'],
                       REPO='fixture/owned', PUSH_TOKEN='fixture-placeholder',
                       BRANCH='bot/worker-env-sync', COMMIT_SUBJECT='fixture sync',
                       FIXTURE_CALLS=str(calls), FIXTURE_REMOTE=remote_url or remote.as_uri(),
                       FIXTURE_GIT=real_git, FIXTURE_AUTH_URL=auth_url,
                       FIXTURE_BARE=str(remote), FIXTURE_REF=ref,
                       FIXTURE_RACE_SHA=race or '',
                       GITHUB_OUTPUT=str(output),
                       GIT_AUTHOR_DATE=f'2001-01-01T00:00:{counter:02d}Z',
                       GIT_COMMITTER_DATE=f'2001-01-01T00:00:{counter:02d}Z')
        result = subprocess.run(['bash', '--noprofile', '--norc', '-eo', 'pipefail', '-c', script],
                                cwd=checkout, env=run_env, text=True, stdout=subprocess.PIPE,
                                stderr=subprocess.PIPE, timeout=30)
        commands = [json.loads(line) for line in calls.read_text().splitlines()]
        pushed = output.read_text()
        print(json.dumps({'case': label, 'exit': result.returncode,
                          'stdout': result.stdout, 'stderr': result.stderr,
                          'github_output': pushed, 'git_calls': commands}), flush=True)
        return result, commands, checkout, pushed

    result, calls, checkout, pushed = attempt('first', 'new')
    assert result.returncode == 0 and pushed == 'pushed=1\n'
    assert any(f'--force-with-lease={ref}:' in c for c in calls)
    first = head()
    count = git(remote, 'rev-list', '--count', ref).stdout
    result, calls, checkout, pushed = attempt('repeat', 'new')
    assert result.returncode == 0 and pushed == ''
    assert head() == first and git(remote, 'rev-list', '--count', ref).stdout == count
    assert git(checkout, 'rev-parse', 'HEAD').stdout.strip() == base
    assert not any(c[0] in ('commit', 'push') for c in calls)
    result, calls, _, pushed = attempt('changed', 'newer')
    assert result.returncode == 0 and head() != first and pushed == 'pushed=1\n'
    assert any(f'--force-with-lease={ref}:{first}' in c for c in calls)
    changed = head()
    tree = git(remote, 'rev-parse', f'{changed}^{{tree}}').stdout.strip()
    race = git(remote, 'commit-tree', tree, '-p', changed, '-m', 'concurrent fixture change').stdout.strip()
    result, calls, _, pushed = attempt('race', 'latest', race=race)
    assert result.returncode != 0 and 'branch moved' in result.stdout and head() == race and pushed == ''
    assert any(f'--force-with-lease={ref}:{changed}' in c for c in calls)
    result, calls, checkout, pushed = attempt('read-failure', 'latest',
                                              remote_url=(root / 'missing.git').as_uri())
    assert result.returncode != 0 and head() == race and pushed == ''
    assert not any(c[0] in ('commit', 'push') for c in calls)
    assert git(checkout, 'rev-parse', 'HEAD').stdout.strip() == base
    # ls-remote patterns can also match a ref's slash-delimited suffix.
    # Reject an ambiguous response rather than leasing a different branch.
    git(remote, 'update-ref', 'refs/heads/decoy/' + ref, race)
    result, calls, checkout, pushed = attempt('ambiguous-ref', 'latest')
    assert result.returncode != 0 and 'Unexpected sync branch response' in result.stdout and pushed == ''
    assert head() == race and not any(c[0] in ('commit', 'push') for c in calls)
    for path, value in [('tools/rbe/worker-env.txt', 'fixture newer\n'),
                        ('platforms/BUILD.bazel', 'worker-env: newer\n'),
                        ('untouched.txt', 'preserve\n')]:
        assert git(remote, 'show', f'{race}:{path}').stdout == value
    print('PASS: first create, identical repeat without commit/push, changed files with explicit lease, race rejection, read failure without commit/push, ambiguous ref rejected; pushed=1 only after a landed push')


if __name__ == '__main__':
    main()
