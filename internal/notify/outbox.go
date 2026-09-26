// Package notify is a durable, transport-agnostic outbox for beads that need
// someone told about them.
//
// Firing a due date and DELIVERING it are different problems. `bd due sweep`
// answers "what came due"; it is a report, and a report nobody reads is the
// same as no clock at all. This package is the queue between the two: each
// fired bead becomes a durable row addressed to a seat (its assignee), written
// to an append-only JSONL ledger under .beads/notify/.
//
// It deliberately knows nothing about HOW a seat is reached. Rows are pulled
// with `bd notify drain` and either printed or handed to a command of the
// operator's choosing; the transport lives outside beads, which is where
// routing policy belongs.
package notify

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// DirName is the directory under .beads that holds the outbox.
	DirName = "notify"
	// OutboxFile is the append-only JSONL ledger of notify rows.
	OutboxFile = "outbox.jsonl"
	// SeqFile is the monotonic seq counter for new rows.
	SeqFile = "seq"
	// AckFile maps seat -> last acked seq (JSON object).
	AckFile = "acked.json"
)

// Kind names why a row was enqueued.
type Kind string

const (
	// KindDue is a bead whose due date arrived.
	KindDue Kind = "due"
	// KindEscalate is a bead whose miss count reached the escalation
	// threshold and whose priority was raised.
	KindEscalate Kind = "escalate"
	// KindDefer is a bead whose dated defer expired and which returned to open.
	KindDefer Kind = "defer"
	// KindManual is a row someone enqueued by hand with `bd notify send`.
	KindManual Kind = "manual"
)

// UnassignedSeat is the routing key when a bead has no assignee. Unowned work
// coming due is still news, so it gets a seat of its own rather than being
// dropped: a row that vanishes because nobody claimed the bead is exactly the
// notification most worth keeping.
const UnassignedSeat = "unassigned"

// Record is one seat-addressed notify row.
type Record struct {
	Seq       int64  `json:"seq"`
	Seat      string `json:"seat"`
	IssueID   string `json:"issue_id"`
	Kind      Kind   `json:"kind"`
	Title     string `json:"title,omitempty"`
	CreatedAt string `json:"created_at"`
}

// Outbox is the workspace-local notify ledger.
type Outbox struct {
	dir string
	mu  sync.Mutex
}

// Open returns the outbox rooted at beadsDir (.beads path). Creates the
// notify directory on first use.
func Open(beadsDir string) (*Outbox, error) {
	if strings.TrimSpace(beadsDir) == "" {
		return nil, fmt.Errorf("notify: empty beads dir")
	}
	dir := filepath.Join(beadsDir, DirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("notify: create %s: %w", dir, err)
	}
	return &Outbox{dir: dir}, nil
}

// Dir returns the notify directory path.
func (o *Outbox) Dir() string { return o.dir }

// Enqueue appends one row for seat/issue. Empty seat becomes UnassignedSeat.
func (o *Outbox) Enqueue(seat, issueID string, kind Kind, title string) (Record, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	seat = strings.TrimSpace(seat)
	if seat == "" {
		seat = UnassignedSeat
	}
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return Record{}, fmt.Errorf("notify: empty issue id")
	}
	if kind == "" {
		kind = KindDue
	}

	seq, err := o.nextSeqLocked()
	if err != nil {
		return Record{}, err
	}
	rec := Record{
		Seq:       seq,
		Seat:      seat,
		IssueID:   issueID,
		Kind:      kind,
		Title:     strings.TrimSpace(title),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return Record{}, err
	}
	path := filepath.Join(o.dir, OutboxFile)
	// #nosec G304 -- path is o.dir joined with a package constant filename.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return Record{}, fmt.Errorf("notify: open outbox: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return Record{}, fmt.Errorf("notify: write outbox: %w", err)
	}
	return rec, nil
}

// EnqueueSeatBatch enqueues one row per id under the same seat.
func (o *Outbox) EnqueueSeatBatch(seat string, ids []string, kind Kind, titles map[string]string) ([]Record, error) {
	out := make([]Record, 0, len(ids))
	for _, id := range ids {
		title := ""
		if titles != nil {
			title = titles[id]
		}
		rec, err := o.Enqueue(seat, id, kind, title)
		if err != nil {
			return out, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// Pending returns unacked records, optionally filtered by seat.
// If seat is empty, all seats are included. Sorted by seq ascending.
func (o *Outbox) Pending(seat string) ([]Record, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	acks, err := o.loadAcksLocked()
	if err != nil {
		return nil, err
	}
	all, err := o.readAllLocked()
	if err != nil {
		return nil, err
	}
	seat = strings.TrimSpace(seat)
	var out []Record
	for _, rec := range all {
		if seat != "" && !strings.EqualFold(rec.Seat, seat) {
			continue
		}
		if rec.Seq <= acks[strings.ToLower(rec.Seat)] {
			continue
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

// AckSeat marks all records for seat with seq <= upTo as delivered.
func (o *Outbox) AckSeat(seat string, upTo int64) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	seat = strings.TrimSpace(seat)
	if seat == "" {
		return fmt.Errorf("notify: ack requires a seat")
	}
	if upTo <= 0 {
		return fmt.Errorf("notify: ack requires seq > 0")
	}
	acks, err := o.loadAcksLocked()
	if err != nil {
		return err
	}
	key := strings.ToLower(seat)
	if upTo > acks[key] {
		acks[key] = upTo
	}
	return o.saveAcksLocked(acks)
}

// Seats returns distinct seats that have ever appeared in the outbox.
func (o *Outbox) Seats() ([]string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	all, err := o.readAllLocked()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, rec := range all {
		k := strings.ToLower(rec.Seat)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, rec.Seat)
	}
	sort.Strings(out)
	return out, nil
}

func (o *Outbox) nextSeqLocked() (int64, error) {
	path := filepath.Join(o.dir, SeqFile)
	var cur int64
	// #nosec G304 -- path is o.dir joined with a package constant filename.
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		// A seq file that has been truncated or corrupted reads as 0, which
		// would restart numbering and let a new row look already-acked. Refuse
		// instead: the ledger is still intact, and a human can read the file.
		if _, scanErr := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &cur); scanErr != nil {
			return 0, fmt.Errorf("notify: unreadable seq file %s: %w", path, scanErr)
		}
	case !os.IsNotExist(err):
		return 0, err
	}
	cur++
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%d\n", cur)), 0o600); err != nil {
		return 0, err
	}
	return cur, nil
}

func (o *Outbox) readAllLocked() ([]Record, error) {
	path := filepath.Join(o.dir, OutboxFile)
	// #nosec G304 -- path is o.dir joined with a package constant filename.
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []Record
	sc := bufio.NewScanner(f)
	// large titles possible
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // skip corrupt; ledger is append-only
		}
		out = append(out, rec)
	}
	return out, sc.Err()
}

func (o *Outbox) loadAcksLocked() (map[string]int64, error) {
	path := filepath.Join(o.dir, AckFile)
	// #nosec G304 -- path is o.dir joined with a package constant filename.
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]int64{}, nil
		}
		return nil, err
	}
	out := map[string]int64{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("notify: parse acks: %w", err)
	}
	// normalize keys
	norm := map[string]int64{}
	for k, v := range out {
		norm[strings.ToLower(k)] = v
	}
	return norm, nil
}

func (o *Outbox) saveAcksLocked(acks map[string]int64) error {
	path := filepath.Join(o.dir, AckFile)
	data, err := json.MarshalIndent(acks, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
