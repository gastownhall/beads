# Publishing beads-mcp to PyPI

This guide covers how to build and publish the beads-mcp package to the Python Package Index (PyPI).

## Prerequisites

1. **PyPI Account**: Create accounts on both:
   - Test PyPI: https://test.pypi.org/account/register/
   - PyPI: https://pypi.org/account/register/

2. **API Tokens**: Generate API tokens for authentication:
   - Test PyPI: https://test.pypi.org/manage/account/token/
   - PyPI: https://pypi.org/manage/account/token/

3. **Build Tools**: Install the Python build tools:
   ```bash
   uv pip install --upgrade build twine
   ```

## Building the Package

1. **Clean previous builds** (if any):
   ```bash
   rm -rf dist/ build/ src/*.egg-info
   ```

2. **Build the distribution packages**:
   ```bash
   python -m build
   ```

   This creates both:
   - `dist/beads_mcp-0.9.4-py3-none-any.whl` (wheel)
   - `dist/beads-mcp-0.9.4.tar.gz` (source distribution)

3. **Verify the build**:
   ```bash
   tar -tzf dist/beads-mcp-0.9.4.tar.gz
   ```

   Should include:
   - Source files in `src/beads_mcp/`
   - `README.md`
   - `LICENSE`
   - `pyproject.toml`

## Testing the Package

### Test on Test PyPI First

1. **Upload to Test PyPI**:
   ```bash
   python -m twine upload --repository testpypi dist/*
   ```

   When prompted, use:
   - Username: `__token__`
   - Password: Your Test PyPI API token (including the `pypi-` prefix)

2. **Install from Test PyPI**:
   ```bash
   # In a fresh virtual environment
   uv venv test-env
   source test-env/bin/activate

   # Install from Test PyPI
   pip install --index-url https://test.pypi.org/simple/ beads-mcp

   # Test it works
   beads-mcp --help
   ```

3. **Verify the installation**:
   ```bash
   python -c "import beads_mcp; print(beads_mcp.__version__)"
   ```

## Publishing to PyPI

Once you've verified the package works on Test PyPI:

1. **Upload to PyPI**:
   ```bash
   python -m twine upload dist/*
   ```

   Use:
   - Username: `__token__`
   - Password: Your PyPI API token

2. **Verify on PyPI**:
   - Visit https://pypi.org/project/beads-mcp/
   - Check that the README displays correctly
   - Verify all metadata is correct

3. **Test installation**:
   ```bash
   # In a fresh environment
   pip install beads-mcp
   beads-mcp --help
   ```

## Updating the README Installation Instructions

After publishing, users can install simply with:

```bash
pip install beads-mcp
# or with uv
uv pip install beads-mcp
```

Update the README.md to reflect this simpler installation method.

## Version Management

When releasing a new version:

1. Update version in `src/beads_mcp/__init__.py`
2. Update version in `pyproject.toml`
3. Use the version bump script from the parent project:
   ```bash
   cd ../..
   ./scripts/update-versions.sh 0.9.5
   ```
4. Create a git tag:
   ```bash
   git tag v0.9.5
   git push origin v0.9.5
   ```
5. Clean, rebuild, and republish to PyPI

## Troubleshooting

### Package Already Exists

PyPI doesn't allow re-uploading the same version. If you need to fix something:
1. Increment the version number (even for minor fixes)
2. Rebuild and re-upload

### Missing Files in Distribution

If files are missing from the built package, create a `MANIFEST.in`:
```
include README.md
include LICENSE
recursive-include src/beads_mcp *.py
```

### Authentication Errors

- Ensure you're using `__token__` as the username (exactly)
- Token should include the `pypi-` prefix
- Check token hasn't expired

### Test PyPI vs Production

Test PyPI is completely separate from production PyPI:
- Different accounts
- Different tokens
- Different package versions (can have different versions on each)

Always test on Test PyPI first!

## Continuous Deployment (Future)

Consider setting up GitHub Actions to automate this:
1. On tag push (e.g., `v0.9.5`)
2. Run tests
3. Build package
4. Publish to PyPI

See `.github/workflows/` in the parent project for examples.

## Resources

- [Python Packaging Guide](https://packaging.python.org/tutorials/packaging-projects/)
- [PyPI Documentation](https://pypi.org/help/)
- [Twine Documentation](https://twine.readthedocs.io/)
