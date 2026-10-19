// Package review holds the durable identity and state of a pull request that
// `relay review` reviews on the user's behalf. A review is keyed by the pull
// request (host, owner, repository, number), so re-running the command for the
// same pull request reuses its checkout and its recorded findings.
package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ronaknnathani/relay/internal/project"
)

const defaultHost = "github.com"

// Ref identifies one pull request.
type Ref struct {
	Host   string // host[:port], as gh's --hostname expects
	Owner  string
	Repo   string
	Number int
}

var (
	nameSegment = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	shortRef    = regexp.MustCompile(`^([^/#\s]+)/([^/#\s]+)#([0-9]+)$`)
)

// ParseRef accepts a pull request URL (https://<host>/<owner>/<repo>/pull/<n>,
// optionally followed by a tab path such as /files, a query, or a fragment) or
// the short form <owner>/<repo>#<n>, which resolves against github.com.
func ParseRef(raw string) (Ref, error) {
	raw = strings.TrimSpace(raw)
	if m := shortRef.FindStringSubmatch(raw); m != nil {
		return newRef(defaultHost, m[1], m[2], m[3], raw)
	}
	withScheme := raw
	if !strings.Contains(raw, "://") {
		withScheme = "https://" + raw
	}
	parsed, err := url.Parse(withScheme)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return Ref{}, invalidRef(raw)
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) < 4 || segments[2] != "pull" {
		return Ref{}, invalidRef(raw)
	}
	return newRef(parsed.Host, segments[0], segments[1], segments[3], raw)
}

func newRef(host, owner, repo, number, raw string) (Ref, error) {
	n, err := strconv.Atoi(number)
	if err != nil || n <= 0 || !validName(owner) || !validName(repo) {
		return Ref{}, invalidRef(raw)
	}
	return Ref{
		Host:   strings.ToLower(strings.TrimSuffix(host, ".")),
		Owner:  strings.ToLower(owner),
		Repo:   strings.ToLower(strings.TrimSuffix(repo, ".git")),
		Number: n,
	}, nil
}

func validName(name string) bool {
	return nameSegment.MatchString(name) && name != "." && name != ".."
}

func invalidRef(raw string) error {
	return fmt.Errorf(
		"%q is not a pull request reference; use https://github.com/<owner>/<repo>/pull/<number> or <owner>/<repo>#<number>",
		raw,
	)
}

// hostname is Host without a port. Git remotes for the same server often use a
// different port (SSH) than the web URL, so remotes match on hostname alone.
func (r Ref) hostname() string {
	return (&url.URL{Host: r.Host}).Hostname()
}

// Repository returns HOST/OWNER/REPO, the form gh accepts for --repo and clone.
func (r Ref) Repository() string {
	return r.Host + "/" + r.Owner + "/" + r.Repo
}

// String returns the familiar owner/repo#number form.
func (r Ref) String() string {
	return fmt.Sprintf("%s/%s#%d", r.Owner, r.Repo, r.Number)
}

// URL returns the canonical pull request URL.
func (r Ref) URL() string {
	return fmt.Sprintf("https://%s/%s/%s/pull/%d", r.Host, r.Owner, r.Repo, r.Number)
}

// RepoDir returns the state directory shared by every review of this repository.
func (r Ref) RepoDir() string {
	return filepath.Join(Dir(), r.Host, r.Owner, r.Repo)
}

// StateDir returns ~/.relay/reviews/<host>/<owner>/<repo>/<number>.
func (r Ref) StateDir() string {
	return filepath.Join(r.RepoDir(), strconv.Itoa(r.Number))
}

// Dir returns ~/.relay/reviews.
func Dir() string { return filepath.Join(project.RelayDir(), "reviews") }

// MatchesRemote reports whether a git remote URL points at r's repository.
// It understands https, ssh://, and scp-style (git@host:owner/repo) URLs.
func (r Ref) MatchesRemote(remoteURL string) bool {
	host, path, ok := splitRemote(strings.TrimSpace(remoteURL))
	if !ok || !strings.EqualFold(strings.TrimSuffix(host, "."), r.hostname()) {
		return false
	}
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) != 2 {
		return false
	}
	return strings.EqualFold(segments[0], r.Owner) &&
		strings.EqualFold(strings.TrimSuffix(segments[1], ".git"), r.Repo)
}

func splitRemote(remote string) (host, path string, ok bool) {
	if strings.Contains(remote, "://") {
		parsed, err := url.Parse(remote)
		if err != nil {
			return "", "", false
		}
		return parsed.Hostname(), parsed.Path, parsed.Hostname() != ""
	}
	if at := strings.LastIndexByte(remote, '@'); at >= 0 {
		remote = remote[at+1:]
	}
	host, path, ok = strings.Cut(remote, ":")
	return host, path, ok && host != "" && path != ""
}

// Manifest is the CLI-owned record of one pull request review. The review
// session keeps its prose artifacts (context, findings, rounds) beside it.
type Manifest struct {
	URL        string `json:"url"`
	Host       string `json:"host"`
	Owner      string `json:"owner"`
	Repo       string `json:"repo"`
	Number     int    `json:"number"`
	Title      string `json:"title"`
	Author     string `json:"author"`
	State      string `json:"state"`
	BaseRef    string `json:"base_ref"`
	BaseSHA    string `json:"base_sha"`
	HeadSHA    string `json:"head_sha"`
	SourceRepo string `json:"source_repo"`
	Remote     string `json:"remote"`
	Checkout   string `json:"checkout"`
	Agent      string `json:"agent,omitempty"`
	Created    string `json:"created"`
	Updated    string `json:"updated"`
}

// ManifestPath returns the review.json path inside a review state dir.
func ManifestPath(stateDir string) string { return filepath.Join(stateDir, "review.json") }

// Load reads the manifest in stateDir. A missing manifest returns found=false.
func Load(stateDir string) (m Manifest, found bool, err error) {
	data, err := os.ReadFile(ManifestPath(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return Manifest{}, false, nil
	}
	if err != nil {
		return Manifest{}, false, fmt.Errorf("read review manifest: %w", err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, false, fmt.Errorf("parse review manifest %s: %w", ManifestPath(stateDir), err)
	}
	return m, true, nil
}

// Save atomically writes the manifest, stamping Created on first save and
// Updated on every save.
func Save(stateDir string, m Manifest) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if m.Created == "" {
		m.Created = now
	}
	m.Updated = now
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode review manifest: %w", err)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("create review state dir: %w", err)
	}
	tmp, err := os.CreateTemp(stateDir, ".review-*.json")
	if err != nil {
		return fmt.Errorf("write review manifest: %w", err)
	}
	_, writeErr := tmp.Write(append(data, '\n'))
	if err := errors.Join(writeErr, tmp.Close()); err != nil {
		return errors.Join(fmt.Errorf("write review manifest: %w", err), os.Remove(tmp.Name()))
	}
	if err := os.Rename(tmp.Name(), ManifestPath(stateDir)); err != nil {
		return errors.Join(fmt.Errorf("write review manifest: %w", err), os.Remove(tmp.Name()))
	}
	return nil
}
