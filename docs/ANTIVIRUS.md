# Antivirus False Positives

## Overview

Some antivirus software may flag beads (`bd` or `bd.exe`) as malicious. This is a **false positive** - beads is a legitimate, open-source command-line tool for issue tracking.

## Why This Happens

Go binaries (including beads) are sometimes flagged by antivirus software due to:

1. **Heuristic detection**: Some malware is written in Go, causing antivirus ML models to flag Go-specific binary patterns as suspicious
2. **Behavioral analysis**: CLI tools that modify files and interact with git may trigger behavioral detection
3. **Unsigned binaries**: Without code signing, new executables may be treated with suspicion

This is a **known industry-wide problem** affecting many legitimate Go projects. See the [Go project issues](https://github.com/golang/go/issues/16292) for examples.

## Known Issues

### Kaspersky Antivirus

**Detection**: `PDM:Trojan.Win32.Generic`
**Affected versions**: bd.exe v0.23.1 and potentially others
**Component**: System Watcher (Proactive Defense Module)

Kaspersky's PDM (Proactive Defense Module) uses behavioral analysis that commonly triggers false positives on Go executables.

## Solutions for Users

### Option 1: Add Exclusion (Recommended)

Add beads to your antivirus exclusion list:

**Kaspersky:**
1. Open Kaspersky and go to Settings
2. Navigate to Threats and Exclusions → Manage Exclusions
3. Click Add → Add path to exclusion
4. Add the directory containing `bd.exe` (e.g., `C:\Users\YourName\AppData\Local\bd\`)
5. Select which components the exclusion applies to (scan, monitoring, etc.)

**Windows Defender:**
1. Open Windows Security
2. Go to Virus & threat protection → Manage settings
3. Scroll to Exclusions → Add or remove exclusions
4. Add the beads installation directory or the specific `bd.exe` file

**Other antivirus software:**
- Look for "Exclusions", "Whitelist", or "Trusted Applications" settings
- Add the beads installation directory or executable

### Option 2: Verify File Integrity

Before adding an exclusion, verify the downloaded file is legitimate:

1. Download beads from the [official GitHub releases](https://github.com/steveyegge/beads/releases)
2. Verify the SHA256 checksum matches the `checksums.txt` file in the release
3. Check the file is signed (future releases will include code signing)

**Verify checksum (Windows PowerShell):**
```powershell
Get-FileHash bd.exe -Algorithm SHA256
```

**Verify checksum (macOS/Linux):**
```bash
shasum -a 256 bd
```

Compare the output with the checksum in `checksums.txt` from the release page.

### Option 3: Report False Positive

Help improve detection accuracy by reporting the false positive:

**Kaspersky:**
1. Visit [Kaspersky Threat Intelligence Portal](https://opentip.kaspersky.com/)
2. Upload the `bd.exe` file for analysis
3. Mark it as a false positive
4. Reference: beads is open-source CLI tool (https://github.com/steveyegge/beads)

**Windows Defender:**
1. Go to [Microsoft Security Intelligence](https://www.microsoft.com/en-us/wdsi/filesubmission)
2. Submit the file as a false positive
3. Provide details about the legitimate software

**Other vendors:**
- Check their website for false positive submission forms
- Most major vendors have a process for reviewing flagged files

## For Developers/Distributors

If you're building beads from source or distributing it:

### Current Build Configuration

Beads releases are built with optimizations to reduce false positives:

```yaml
ldflags:
  - -s -w  # Strip debug symbols and DWARF info
```

These flags are already applied in the official builds.

### Code Signing

Windows releases are signed with an Authenticode certificate when available. Code signing:
- Reduces false positive rates over time
- Builds reputation with SmartScreen/antivirus vendors
- Provides tamper verification

**Verify a signed binary (Windows PowerShell):**
```powershell
# Check if the binary is signed
Get-AuthenticodeSignature .\bd.exe

# Expected output for signed binary:
# SignerCertificate: [Certificate details]
# Status: Valid
```

**Verify a signed binary (Linux/macOS with osslsigncode):**
```bash
# Install osslsigncode if not available
# Ubuntu/Debian: apt-get install osslsigncode
# macOS: brew install osslsigncode

osslsigncode verify -in bd.exe
```

**Note:** Code signing requires an EV (Extended Validation) certificate, which involves a verification process. If a release is not signed, it means the certificate was not available at build time. Follow the checksum verification steps above to verify authenticity.

### Alternative Build Methods

Some users report success with:
```bash
go build -ldflags "-s -w" -o bd ./cmd/bd
```

However, results vary by antivirus vendor and version.

## Frequently Asked Questions

### Is beads safe to use?

Yes. Beads is:
- Open source (all code is auditable on [GitHub](https://github.com/steveyegge/beads))
- Signed releases include checksums for verification
- Used by developers worldwide
- A simple CLI tool for issue tracking

### Why don't you just fix the code to avoid detection?

The issue isn't specific to beads' code - it's a characteristic of Go binaries in general. Changing code won't reliably prevent heuristic/behavioral detection. The proper solutions are:
1. Code signing (builds trust over time)
2. Whitelist applications with antivirus vendors
3. User reports of false positives

### Will this be fixed in future releases?

We've implemented:
- **Code signing infrastructure** for Windows releases (requires EV certificate)
- **Build optimizations** to reduce heuristic triggers
- **Documentation** for users to add exclusions and report false positives

Still in progress:
- Acquiring an EV code signing certificate
- Submitting beads to antivirus vendor whitelists

False positives may still occur with new releases until the certificate builds reputation with antivirus vendors. This typically takes several months of consistent signed releases.

### Should I disable my antivirus?

**No.** Instead:
1. Add beads to your antivirus exclusions (safe and recommended)
2. Keep your antivirus enabled for other threats
3. Verify checksums of downloaded files before adding exclusions

## Reporting Issues

If you encounter a new antivirus false positive:

1. Open an issue on [GitHub](https://github.com/steveyegge/beads/issues)
2. Include:
   - Antivirus software name and version
   - Detection/threat name
   - Beads version (`bd version`)
   - Operating system

This helps us track and address false positives across different antivirus vendors.

## References

- [Kaspersky False Positive Guide](https://support.kaspersky.com/1870)
- [Go Binary False Positives Discussion](https://www.linkedin.com/pulse/go-false-positives-melle-boudewijns)
- [Go Project Issue Tracker](https://github.com/golang/go/issues/16292)
- [Kaspersky Community Forum](https://forum.kaspersky.com/topic/pdmtrojanwin32generic-54425/)
