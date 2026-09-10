// Command catalog regenerates and validates the authenticated historical module catalog.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	modulePath          = "github.com/steveyegge/beads"
	minimumVersion      = "v0.9.1"
	maximumVersion      = "v1.1.2"
	proxyURL            = "https://proxy.golang.org"
	githubRepository    = "gastownhall/beads"
	expectedVersions    = 122
	expectedRepoOnly    = 49
	expectedRepoPre     = 3
	expectedReleases    = 92
	expectedLinuxAssets = 89
	expectedTagDrift    = 13
	expectedDriftAssets = 11

	// This pins the complete reviewed catalog identity. A generator run may
	// expose upstream drift, but offline validation rejects even well-formed
	// substitutions until this digest is deliberately updated.
	expectedCatalogSHA256 = "298dd489a6274d80ac42e1fb14c993444159f3193a290f8da37afcb3e2eaf10d"
)

var (
	stableRE = regexp.MustCompile(`^v(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$`)
	preRE    = regexp.MustCompile(`^v(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})-[0-9A-Za-z][0-9A-Za-z.-]*$`)
	h1RE     = regexp.MustCompile(`^h1:[A-Za-z0-9+/]{43}=$`)
	hashRE   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	shaRE    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	assetRE  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type Catalog struct {
	SchemaVersion      int        `json:"schema_version"`
	Module             string     `json:"module"`
	Versions           []Entry    `json:"versions"`
	RepositoryTagDrift []TagDrift `json:"repository_tag_drift"`
	Exclusions         Exclusions `json:"exclusions"`
}

type Entry struct {
	Version       string         `json:"version"`
	Sum           string         `json:"sum"`
	GoModSum      string         `json:"go_mod_sum"`
	Origin        Origin         `json:"origin"`
	SourceZip     SourceZip      `json:"source_zip"`
	GitHubRelease *ReleaseRecord `json:"github_release,omitempty"`
}

type Origin struct {
	Hash string `json:"hash"`
	Ref  string `json:"ref"`
}
type SourceZip struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type Exclusions struct {
	ProxyPrereleases          []string `json:"proxy_prereleases"`
	RepositoryOnlyStable      []string `json:"repository_only_stable_tags"`
	RepositoryOnlyPrereleases []string `json:"repository_only_prerelease_tags"`
}
type ReleaseRecord struct {
	HTMLURL         string       `json:"html_url"`
	SourceRelation  string       `json:"source_relation"`
	LinuxAMD64Asset *AssetRecord `json:"linux_amd64_asset,omitempty"`
}
type TagDrift struct {
	Version     string `json:"version"`
	CurrentHash string `json:"current_hash"`
}
type AssetRecord struct {
	Size   int64  `json:"size"`
	Name   string `json:"name"`
	Digest string `json:"digest"`
}
type downloadJSON struct {
	Path, Version, Error, Zip, Sum, GoModSum string
	Origin                                   Origin
}
type githubRelease struct {
	TagName    string `json:"tag_name"`
	HTMLURL    string `json:"html_url"`
	Draft      bool
	Prerelease bool
	Assets     []githubAsset
}
type githubAsset struct {
	Size         int64
	Name, Digest string
}

func main() {
	if len(os.Args) != 3 || (os.Args[1] != "generate" && os.Args[1] != "validate") {
		fmt.Fprintln(os.Stderr, "usage: catalog generate|validate <manifest.json>")
		os.Exit(2)
	}
	var err error
	if os.Args[1] == "generate" {
		err = generate(os.Args[2])
	} else {
		err = validateFile(os.Args[2])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	proxyVersions, err := fetchProxyVersions(ctx)
	if err != nil {
		return err
	}
	tagHashes, err := fetchGitHubTagHashes(ctx)
	if err != nil {
		return err
	}
	versions, exclusions, err := classifyCatalogVersions(
		proxyVersions, historicalRepositoryOnlyTags(), tagHashes)
	if err != nil {
		return err
	}
	releases, err := fetchGitHubReleases(ctx)
	if err != nil {
		return err
	}
	releaseByVersion := map[string]*githubRelease{}
	for i := range releases {
		r := &releases[i]
		if r.Draft || r.Prerelease || !slices.Contains(versions, r.TagName) {
			continue
		}
		if releaseByVersion[r.TagName] != nil {
			return fmt.Errorf("duplicate published release %s", r.TagName)
		}
		releaseByVersion[r.TagName] = r
	}
	tmp, err := os.MkdirTemp("", "beads-release-catalog-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	downloads, err := downloadModules(ctx, versions, tmp)
	if err != nil {
		return err
	}
	entries := make([]Entry, 0, len(versions))
	tagDrift := make([]TagDrift, 0, expectedTagDrift)
	for _, version := range versions {
		tagHash := tagHashes[version]
		if tagHash == "" && version != "v0.56.0" {
			return fmt.Errorf("%s: canonical repository tag is missing", version)
		}
		entry, err := catalogEntry(downloads[version], releaseByVersion[version], tagHash)
		if err != nil {
			return fmt.Errorf("%s: %w", version, err)
		}
		entries = append(entries, entry)
		if tagHash != "" && tagHash != entry.Origin.Hash {
			tagDrift = append(tagDrift, TagDrift{Version: version, CurrentHash: tagHash})
		}
	}
	catalog := Catalog{
		SchemaVersion:      1,
		Module:             modulePath,
		Versions:           entries,
		RepositoryTagDrift: tagDrift,
		Exclusions:         exclusions,
	}
	if err := validateCatalog(catalog); err != nil {
		return err
	}
	raw, err := encodeCatalog(catalog)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil { //nolint:gosec // G306: the checked catalog is public repository data.
		return err
	}
	// Print the recomputed identity digest so a maintainer can deliberately
	// update expectedCatalogSHA256 without a separate manual sha256sum step.
	fmt.Printf("catalog SHA-256: %x\n", sha256.Sum256(raw))
	return nil
}

// The proxy protocol says @v/list is newline-delimited and omits pseudo-versions.
// https://go.dev/ref/mod#goproxy-protocol
func fetchProxyVersions(ctx context.Context) ([]string, error) {
	return commandLines(ctx, "curl", "-fsSL", "--max-time", "30", proxyURL+"/"+modulePath+"/@v/list")
}

func fetchGitHubReleases(ctx context.Context) ([]githubRelease, error) {
	out, err := exec.CommandContext(ctx, "gh", "api", "--paginate", "--jq", ".[]",
		"repos/"+githubRepository+"/releases?per_page=100").Output()
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	var releases []githubRelease
	for {
		var release githubRelease
		if err := dec.Decode(&release); errors.Is(err, io.EOF) {
			return releases, nil
		} else if err != nil {
			return nil, err
		}
		releases = append(releases, release)
	}
}

// go mod download authenticates the module ZIP and go.mod using GOSUMDB.
// https://pkg.go.dev/cmd/go#hdr-Download_modules_to_local_cache
func downloadModules(ctx context.Context, versions []string, tmp string) (map[string]downloadJSON, error) {
	args := []string{"mod", "download", "-json"}
	for _, version := range versions {
		args = append(args, modulePath+"@"+version)
	}
	cmd := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), args...)
	cmd.Dir = tmp
	cmd.Env = append(os.Environ(), "GO111MODULE=on", "GOFLAGS=", "GOWORK=off", "GOTOOLCHAIN=local",
		"GOMODCACHE="+filepath.Join(tmp, "mod"), "GOPROXY="+proxyURL, "GOSUMDB=sum.golang.org",
		"GOPRIVATE=", "GONOPROXY=")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go mod download: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	result := map[string]downloadJSON{}
	dec := json.NewDecoder(&stdout)
	for {
		var item downloadJSON
		if err := dec.Decode(&item); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		if item.Error != "" {
			return nil, errors.New(item.Error)
		}
		if item.Path != modulePath || !slices.Contains(versions, item.Version) {
			return nil, fmt.Errorf("unexpected module %s@%s", item.Path, item.Version)
		}
		if _, exists := result[item.Version]; exists {
			return nil, fmt.Errorf("duplicate module %s", item.Version)
		}
		result[item.Version] = item
	}
	if len(result) != len(versions) {
		return nil, fmt.Errorf("downloaded %d modules, want %d", len(result), len(versions))
	}
	return result, nil
}

func catalogEntry(download downloadJSON, release *githubRelease, tagHash string) (Entry, error) {
	f, err := os.Open(download.Zip)
	if err != nil {
		return Entry{}, err
	}
	h := sha256.New()
	size, copyErr := io.Copy(h, f)
	closeErr := f.Close()
	if copyErr != nil {
		return Entry{}, copyErr
	}
	if closeErr != nil {
		return Entry{}, closeErr
	}
	entry := Entry{
		Version:   download.Version,
		Sum:       download.Sum,
		GoModSum:  download.GoModSum,
		Origin:    download.Origin,
		SourceZip: SourceZip{SHA256: fmt.Sprintf("%x", h.Sum(nil)), Size: size},
	}
	if release == nil {
		return entry, nil
	}
	if release.TagName != download.Version {
		return Entry{}, fmt.Errorf("release tag %s does not match", release.TagName)
	}
	if !hashRE.MatchString(tagHash) {
		return Entry{}, errors.New("release tag has no resolved commit hash")
	}
	relation := "matches_proxy_origin"
	if tagHash != download.Origin.Hash {
		relation = "tag_drift"
	}
	record := &ReleaseRecord{
		HTMLURL:        release.HTMLURL,
		SourceRelation: relation,
	}
	want := "beads_" + strings.TrimPrefix(download.Version, "v") + "_linux_amd64.tar.gz"
	for _, asset := range release.Assets {
		if asset.Name != want {
			continue
		}
		if record.LinuxAMD64Asset != nil {
			return Entry{}, fmt.Errorf("duplicate asset %s", want)
		}
		record.LinuxAMD64Asset = &AssetRecord{Size: asset.Size, Name: asset.Name, Digest: asset.Digest}
	}
	entry.GitHubRelease = record
	return entry, nil
}

func classifyVersions(proxyVersions, repositoryTags []string) ([]string, Exclusions, error) {
	proxyAll, proxyStable := map[string]bool{}, map[string]bool{}
	var stable, proxyPre []string
	for _, version := range proxyVersions {
		if proxyAll[version] {
			return nil, Exclusions{}, fmt.Errorf("duplicate proxy version %s", version)
		}
		proxyAll[version] = true
		if !inScope(version) {
			continue
		}
		switch {
		case isStable(version):
			stable = append(stable, version)
			proxyStable[version] = true
		case isPrerelease(version):
			proxyPre = append(proxyPre, version)
		default:
			return nil, Exclusions{}, fmt.Errorf("unexpected proxy version %s", version)
		}
	}
	var repoStable, repoPre []string
	seen := map[string]bool{}
	for _, tag := range repositoryTags {
		if seen[tag] {
			return nil, Exclusions{}, fmt.Errorf("duplicate repository tag %s", tag)
		}
		seen[tag] = true
		if !inScope(tag) {
			continue
		}
		if isStable(tag) && !proxyStable[tag] {
			repoStable = append(repoStable, tag)
		} else if isPrerelease(tag) && !proxyAll[tag] {
			repoPre = append(repoPre, tag)
		}
	}
	for _, list := range [][]string{stable, proxyPre, repoStable, repoPre} {
		slices.SortFunc(list, versionCompare)
	}
	return stable, Exclusions{proxyPre, repoStable, repoPre}, nil
}

func classifyCatalogVersions(proxyVersions, archivedTags []string, remoteTags map[string]string) ([]string, Exclusions, error) {
	repositoryTags := append([]string(nil), archivedTags...)
	for tag := range remoteTags {
		repositoryTags = append(repositoryTags, tag)
	}
	return classifyVersions(proxyVersions, repositoryTags)
}

func validateCatalog(c Catalog) error {
	if c.SchemaVersion != 1 || c.Module != modulePath {
		return errors.New("catalog header does not match the pinned scope")
	}
	if len(c.Versions) != expectedVersions {
		return fmt.Errorf("versions = %d, want %d", len(c.Versions), expectedVersions)
	}
	seen, byVersion, releases, assets, err := validateVersionEntries(c.Versions)
	if err != nil {
		return err
	}
	driftSet, driftAssets, err := validateTagDrift(c.RepositoryTagDrift, byVersion)
	if err != nil {
		return err
	}
	if err := validateSourceRelations(c.Versions, driftSet); err != nil {
		return err
	}
	if !seen["v0.56.0"] || releases != expectedReleases || assets != expectedLinuxAssets ||
		driftAssets != expectedDriftAssets {
		return fmt.Errorf("release provenance = %d/%d/%d/%d, want %d/%d/%d/%d",
			releases, assets, len(driftSet), driftAssets,
			expectedReleases, expectedLinuxAssets, expectedTagDrift, expectedDriftAssets)
	}
	return validateExclusions(c.Exclusions, seen)
}

// validateVersionEntries checks each catalog version's scope, ordering, and
// authenticated provenance, returning the seen set, the version index, and the
// running GitHub release and linux/amd64 asset counts.
func validateVersionEntries(versions []Entry) (map[string]bool, map[string]Entry, int, int, error) {
	seen, byVersion, releases, assets := map[string]bool{}, map[string]Entry{}, 0, 0
	for i, entry := range versions {
		if !isStable(entry.Version) || !inScope(entry.Version) || seen[entry.Version] {
			return nil, nil, 0, 0, fmt.Errorf("invalid or duplicate version %s", entry.Version)
		}
		if i > 0 && versionCompare(versions[i-1].Version, entry.Version) >= 0 {
			return nil, nil, 0, 0, errors.New("versions are not semantically sorted")
		}
		seen[entry.Version] = true
		byVersion[entry.Version] = entry
		hasRelease, hasAsset, err := validateEntryProvenance(entry)
		if err != nil {
			return nil, nil, 0, 0, err
		}
		if hasRelease {
			releases++
		}
		if hasAsset {
			assets++
		}
	}
	return seen, byVersion, releases, assets, nil
}

// validOriginRef checks the authenticated origin ref against the exact shapes
// the catalog uses: the version's own tag ref, or the known main-branch origin
// for the two versions published from refs/heads/main. Accepting any "refs/"
// prefix would let a generator typo or wrong ref family pass unchecked even
// though origin.ref is part of the authenticated provenance record.
func validOriginRef(version, ref string) bool {
	return ref == "refs/tags/"+version || ref == "refs/heads/main"
}

// validateEntryProvenance validates one entry's authenticated sums, origin, and
// source zip, plus any GitHub release and its linux/amd64 asset. It reports
// whether the entry carries a release and an asset so the caller can count them.
func validateEntryProvenance(entry Entry) (hasRelease, hasAsset bool, err error) {
	if !h1RE.MatchString(entry.Sum) || !h1RE.MatchString(entry.GoModSum) ||
		!hashRE.MatchString(entry.Origin.Hash) || !validOriginRef(entry.Version, entry.Origin.Ref) ||
		!shaRE.MatchString(entry.SourceZip.SHA256) || entry.SourceZip.Size <= 0 {
		return false, false, fmt.Errorf("%s: invalid authenticated provenance", entry.Version)
	}
	if entry.GitHubRelease == nil {
		return false, false, nil
	}
	r := entry.GitHubRelease
	if r.HTMLURL != "https://github.com/"+githubRepository+"/releases/tag/"+entry.Version {
		return false, false, fmt.Errorf("%s: invalid release", entry.Version)
	}
	if r.LinuxAMD64Asset == nil {
		return true, false, nil
	}
	a := r.LinuxAMD64Asset
	want := "beads_" + strings.TrimPrefix(entry.Version, "v") + "_linux_amd64.tar.gz"
	if a.Size <= 0 || a.Name != want || !assetRE.MatchString(a.Digest) {
		return true, false, fmt.Errorf("%s: invalid release asset", entry.Version)
	}
	return true, true, nil
}

// validateTagDrift checks the repository tag-drift records against the catalog
// entries, returning the drifted-version set and the count of drift entries that
// also carry a linux/amd64 asset.
func validateTagDrift(driftRecords []TagDrift, byVersion map[string]Entry) (map[string]bool, int, error) {
	if len(driftRecords) != expectedTagDrift {
		return nil, 0, fmt.Errorf("repository tag drift = %d, want %d", len(driftRecords), expectedTagDrift)
	}
	driftSet, driftAssets := map[string]bool{}, 0
	for i, drift := range driftRecords {
		entry, ok := byVersion[drift.Version]
		if !ok || driftSet[drift.Version] || !hashRE.MatchString(drift.CurrentHash) ||
			drift.CurrentHash == entry.Origin.Hash ||
			(i > 0 && versionCompare(driftRecords[i-1].Version, drift.Version) >= 0) {
			return nil, 0, fmt.Errorf("invalid or unsorted repository tag drift %s", drift.Version)
		}
		driftSet[drift.Version] = true
		if entry.GitHubRelease != nil && entry.GitHubRelease.LinuxAMD64Asset != nil {
			driftAssets++
		}
	}
	return driftSet, driftAssets, nil
}

// validateSourceRelations checks that every GitHub release records the source
// relation implied by whether its version drifted from the proxy origin.
func validateSourceRelations(versions []Entry, driftSet map[string]bool) error {
	for _, entry := range versions {
		if entry.GitHubRelease == nil {
			continue
		}
		want := "matches_proxy_origin"
		if driftSet[entry.Version] {
			want = "tag_drift"
		}
		if entry.GitHubRelease.SourceRelation != want {
			return fmt.Errorf("%s: invalid release source relation", entry.Version)
		}
	}
	return nil
}

// validateExclusions checks the proxy-prerelease and repository-only exclusion
// lists against the included version set.
func validateExclusions(ex Exclusions, seen map[string]bool) error {
	if err := validateExcluded("proxy prereleases", ex.ProxyPrereleases, 2, isPrerelease, seen); err != nil {
		return err
	}
	if err := validateExcluded("repository-only stable tags", ex.RepositoryOnlyStable, expectedRepoOnly, isStable, seen); err != nil {
		return err
	}
	if err := validateExcluded("repository-only prerelease tags", ex.RepositoryOnlyPrereleases, expectedRepoPre, isPrerelease, seen); err != nil {
		return err
	}
	return nil
}

func validateExcluded(name string, versions []string, want int, valid func(string) bool, included map[string]bool) error {
	if len(versions) != want {
		return fmt.Errorf("%s = %d, want %d", name, len(versions), want)
	}
	seen := map[string]bool{}
	for i, version := range versions {
		if !valid(version) || !inScope(version) || included[version] || seen[version] ||
			(i > 0 && versionCompare(versions[i-1], version) >= 0) {
			return fmt.Errorf("invalid or unsorted %s entry %s", name, version)
		}
		seen[version] = true
	}
	return nil
}

func validateFile(path string) error {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: path is the explicit catalog CLI argument.
	if err != nil {
		return err
	}
	_, err = decodeCatalog(raw)
	return err
}

func decodeCatalog(raw []byte) (Catalog, error) {
	var catalog Catalog
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&catalog); err != nil {
		return Catalog{}, err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Catalog{}, errors.New("trailing JSON data")
	}
	if err := validateCatalog(catalog); err != nil {
		return Catalog{}, err
	}
	canonical, err := encodeCatalog(catalog)
	if err != nil || !bytes.Equal(raw, canonical) {
		return Catalog{}, errors.New("catalog JSON is not canonical")
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(raw))
	if digest != expectedCatalogSHA256 {
		return Catalog{}, fmt.Errorf("catalog identity digest = %s, want %s", digest, expectedCatalogSHA256)
	}
	return catalog, nil
}

func encodeCatalog(c Catalog) ([]byte, error) {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func commandLines(ctx context.Context, name string, args ...string) ([]string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
}

func fetchGitHubTagHashes(ctx context.Context) (map[string]string, error) {
	out, err := exec.CommandContext(ctx, "git", "ls-remote", "--tags", "https://github.com/"+githubRepository+".git").Output()
	if err != nil {
		return nil, err
	}
	direct, peeled := map[string]string{}, map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.HasPrefix(fields[1], "refs/tags/") {
			continue
		}
		tag := strings.TrimPrefix(fields[1], "refs/tags/")
		if strings.HasSuffix(tag, "^{}") {
			peeled[strings.TrimSuffix(tag, "^{}")] = fields[0]
		} else {
			direct[tag] = fields[0]
		}
	}
	for tag, hash := range peeled {
		direct[tag] = hash
	}
	return direct, nil
}

// These unauthenticated tags were observed in the historical full-clone
// snapshot used to establish the catalog, but are absent from both the Go
// proxy and the canonical remote today. They are archived only to make the
// exclusion decision explicit; they never expand the authenticated universe.
func historicalRepositoryOnlyTags() []string {
	return strings.Fields(`
v0.57.12
v0.57.13
v0.57.14
v0.57.15
v0.57.16
v0.57.17
v0.58.1
v0.58.2
v0.58.3
v0.58.4
v0.58.5
v0.58.6
v0.58.7
v0.58.8
v0.58.8-nosqlite
v0.58.9
v0.58.9-nosqlite
v0.58.10
v0.58.10-nosqlite
v0.59.1
v0.60.1
v0.60.2
v0.61.1
v0.61.2
v0.61.3
v0.61.4
v0.62.1
v0.62.2
v0.62.3
v0.62.4
v0.62.5
v0.62.6
v0.62.7
v0.62.8
v0.62.9
v0.62.10
v0.62.11
v0.62.12
v0.62.13
v0.62.14
v0.62.15
v0.62.16
v0.62.17
v0.62.18
v0.62.19
v0.62.20
v0.62.21
v0.62.22
v0.62.23
v0.62.24
v0.62.25
v0.62.26
`)
}

func isStable(version string) bool     { return stableRE.MatchString(version) }
func isPrerelease(version string) bool { return preRE.MatchString(version) }
func inScope(version string) bool {
	if !isStable(version) && !isPrerelease(version) {
		return false
	}
	n := numericVersion(version)
	return compareNumeric(n, numericVersion(minimumVersion)) >= 0 && compareNumeric(n, numericVersion(maximumVersion)) <= 0
}
func versionCompare(a, b string) int {
	if c := compareNumeric(numericVersion(a), numericVersion(b)); c != 0 {
		return c
	}
	return comparePrerelease(prereleaseTag(a), prereleaseTag(b))
}

// prereleaseTag returns the SemVer prerelease tail after the first '-', or the
// empty string for a stable version with no prerelease. The numeric core is
// compared separately by versionCompare, so only the tail is returned here.
func prereleaseTag(version string) string {
	if _, tail, found := strings.Cut(version, "-"); found {
		return tail
	}
	return ""
}

// comparePrerelease orders two prerelease tails by SemVer 2.0.0 precedence
// (spec item 11). An empty tail (a stable release) outranks any prerelease that
// shares the same numeric core. Dot-separated identifiers are compared left to
// right; when every shared identifier is equal, the longer identifier set wins.
// The prior lexicographic tiebreak misordered same-core prereleases such as
// v1.2.3-rc.10 before v1.2.3-rc.2, and both generate and validate share this
// comparator, so a wrong canonical order self-validated against the pinned
// digest.
func comparePrerelease(a, b string) int {
	if a == b {
		return 0
	}
	if a == "" { // a stable version outranks any prerelease of the same core
		return 1
	}
	if b == "" {
		return -1
	}
	aIdents, bIdents := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(aIdents) && i < len(bIdents); i++ {
		if c := comparePrereleaseIdent(aIdents[i], bIdents[i]); c != 0 {
			return c
		}
	}
	return compareInt(len(aIdents), len(bIdents))
}

// comparePrereleaseIdent compares one dot-separated prerelease identifier.
// All-numeric identifiers compare numerically and always rank below identifiers
// containing letters or hyphens; two non-numeric identifiers compare in ASCII
// order.
func comparePrereleaseIdent(a, b string) int {
	aNum, bNum := isNumericIdent(a), isNumericIdent(b)
	switch {
	case aNum && bNum:
		// SemVer forbids leading zeros in numeric identifiers, so the longer
		// digit run is the larger number; equal-width runs fall back to ASCII
		// order, which matches numeric order for equal-width digit strings.
		if c := compareInt(len(a), len(b)); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	case aNum: // numeric identifiers have lower precedence than alphanumeric
		return -1
	case bNum:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

// isNumericIdent reports whether s is a non-empty run of ASCII digits.
func isNumericIdent(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// compareInt returns -1, 0, or 1 as a is less than, equal to, or greater than b.
func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
func numericVersion(version string) [3]int {
	base := strings.SplitN(version, "-", 2)[0]
	match := stableRE.FindStringSubmatch(base)
	var result [3]int
	if len(match) != 4 {
		return result
	}
	var major, minor, patch int
	for i, component := range match[1:] {
		value, _ := strconv.Atoi(component)
		switch i {
		case 0:
			major = value
		case 1:
			minor = value
		case 2:
			patch = value
		}
	}
	return [3]int{major, minor, patch}
}
func compareNumeric(a, b [3]int) int {
	for _, pair := range [][2]int{{a[0], b[0]}, {a[1], b[1]}, {a[2], b[2]}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	return 0
}
