// Package mailbox provides durable filesystem mailboxes for managed workers.
package mailbox

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ronaknnathani/relay/internal/patrollock"
)

// Box identifies an unread mailbox.
type Box string

// Supported unread mailboxes.
const (
	Inbox  Box = "inbox"
	Outbox Box = "outbox"
)

// Kind identifies the purpose of a mailbox message.
type Kind string

// Supported message kinds.
const (
	KindQuestion    Kind = "question"
	KindConflict    Kind = "conflict"
	KindPlan        Kind = "plan"
	KindPROpen      Kind = "pr-open"
	KindDecision    Kind = "decision"
	KindFeedback    Kind = "feedback"
	KindInstruction Kind = "instruction"
)

// Actor identifies a mailbox endpoint.
type Actor string

// Supported mailbox actors.
const (
	ActorWorker Actor = "worker"
	ActorCTO    Actor = "cto"
)

// Message is one durable mailbox message.
type Message struct {
	ID      string   `json:"id"`
	Kind    Kind     `json:"kind"`
	Program string   `json:"program"`
	Item    string   `json:"item"`
	From    Actor    `json:"from"`
	To      Actor    `json:"to"`
	Body    string   `json:"body"`
	Options []string `json:"options"`
	// AutomatedBy names the bounded automated turn that wrote this message.
	// From stays a routing endpoint, so the reader still knows whether the CTO
	// or the worker side wrote it; AutomatedBy records that no human did.
	AutomatedBy string `json:"automated_by,omitempty"`
	ReplyTo     string `json:"reply_to,omitempty"`
	DecisionID  string `json:"decision_id,omitempty"`
	CreatedAt   string `json:"created_at"`
}

var (
	now                    = time.Now
	randomReader io.Reader = rand.Reader
	safeID                 = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	itemID                 = regexp.MustCompile(`^w[1-9][0-9]*$`)
	decisionID             = regexp.MustCompile(`^d[1-9][0-9]*$`)
	automatedBy            = regexp.MustCompile(`^cto-automated:[a-z0-9]{1,32}$`)
	// reserveTimeout bounds how long one mailbox writer waits for another to
	// release the directory lock before reporting contention.
	reserveTimeout = 10 * time.Second
)

// Ensure creates the durable mailbox directory layout beneath projectDir.
func Ensure(projectDir string) error {
	for _, box := range []Box{Inbox, Outbox} {
		for _, processed := range []bool{false, true} {
			path, err := boxDir(projectDir, box, processed)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(path, 0o755); err != nil {
				return fmt.Errorf("ensure mailbox directory %s: %w", path, err)
			}
		}
	}
	path := notifiedDir(projectDir)
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("ensure mailbox directory %s: %w", path, err)
	}
	return nil
}

// IsNotified reports whether a message has a durable doorbell marker.
func IsNotified(projectDir, id string) (bool, error) {
	if err := validateID("message id", id); err != nil {
		return false, err
	}
	path := filepath.Join(notifiedDir(projectDir), id)
	if _, err := os.Stat(path); err == nil {
		return true, nil
	} else if os.IsNotExist(err) {
		return false, nil
	} else {
		return false, fmt.Errorf("stat notification marker %s: %w", path, err)
	}
}

// MarkNotified durably marks a message as having rung the worker doorbell.
func MarkNotified(projectDir, id string) (bool, error) {
	if err := validateID("message id", id); err != nil {
		return false, err
	}
	if err := Ensure(projectDir); err != nil {
		return false, err
	}
	path := filepath.Join(notifiedDir(projectDir), id)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("create notification marker %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return true, fmt.Errorf("close notification marker %s: %w", path, err)
	}
	return true, nil
}

// Send validates and atomically writes an unread message.
func Send(projectDir string, box Box, message Message) (Message, error) {
	dir, err := boxDir(projectDir, box, false)
	if err != nil {
		return Message{}, err
	}
	if err := Ensure(projectDir); err != nil {
		return Message{}, err
	}
	created := now().UTC()
	if message.ID == "" {
		message.ID, err = newID(created)
		if err != nil {
			return Message{}, err
		}
	}
	if message.CreatedAt == "" {
		message.CreatedAt = created.Format(time.RFC3339Nano)
	}
	if message.Options == nil {
		message.Options = []string{}
	}
	if err := validateMessage(box, message); err != nil {
		return Message{}, err
	}
	data, err := json.MarshalIndent(message, "", "  ")
	if err != nil {
		return Message{}, fmt.Errorf("encode mailbox message %q: %w", message.ID, err)
	}
	data = append(data, '\n')
	if err := writeExclusive(filepath.Join(dir, message.ID+".json"), data); err != nil {
		return Message{}, err
	}
	return message, nil
}

// List returns unread messages ordered by created_at and then id.
func List(projectDir string, box Box) ([]Message, error) {
	dir, err := boxDir(projectDir, box, false)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("list mailbox %s: %w", dir, err)
	}
	messages := make([]Message, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		message, err := readMessage(filepath.Join(dir, entry.Name()), box)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	sort.Slice(messages, func(i, j int) bool {
		left, _ := time.Parse(time.RFC3339Nano, messages[i].CreatedAt)
		right, _ := time.Parse(time.RFC3339Nano, messages[j].CreatedAt)
		if left.Equal(right) {
			return messages[i].ID < messages[j].ID
		}
		return left.Before(right)
	})
	return messages, nil
}

// Find returns one unread message by id.
func Find(projectDir string, box Box, id string) (Message, error) {
	if err := validateID("message id", id); err != nil {
		return Message{}, err
	}
	dir, err := boxDir(projectDir, box, false)
	if err != nil {
		return Message{}, err
	}
	return readMessage(filepath.Join(dir, id+".json"), box)
}

// Acknowledge atomically moves an unread message into the processed mailbox.
func Acknowledge(projectDir string, box Box, id string) (retErr error) {
	if err := validateID("message id", id); err != nil {
		return err
	}
	sourceDir, err := boxDir(projectDir, box, false)
	if err != nil {
		return err
	}
	targetDir, err := boxDir(projectDir, box, true)
	if err != nil {
		return err
	}
	if err := Ensure(projectDir); err != nil {
		return err
	}
	source := filepath.Join(sourceDir, id+".json")
	target := filepath.Join(targetDir, id+".json")
	if _, err := readMessage(source, box); err != nil {
		return fmt.Errorf("acknowledge mailbox message %q: %w", id, err)
	}
	release, err := reserve(lockPath(targetDir))
	if err != nil {
		return fmt.Errorf("acknowledge mailbox message %q: %w", id, err)
	}
	defer func() {
		if err := release(); err != nil {
			retErr = errors.Join(retErr, err)
		}
	}()
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("acknowledge mailbox message %q: processed target %s already exists", id, target)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("acknowledge mailbox message %q: stat processed target %s: %w", id, target, err)
	}
	if err := os.Rename(source, target); err != nil {
		return fmt.Errorf("acknowledge mailbox message %q: move %s to %s: %w", id, source, target, err)
	}
	return nil
}

func newID(created time.Time) (string, error) {
	random := make([]byte, 12)
	if _, err := io.ReadFull(randomReader, random); err != nil {
		return "", fmt.Errorf("generate mailbox message id: %w", err)
	}
	return created.Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(random), nil
}

func writeExclusive(path string, data []byte) (retErr error) {
	release, err := reserve(lockPath(filepath.Dir(path)))
	if err != nil {
		return fmt.Errorf("reserve mailbox message %s: %w", path, err)
	}
	defer func() {
		if err := release(); err != nil {
			retErr = errors.Join(retErr, err)
		}
	}()
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("create mailbox message %s: %w", path, os.ErrExist)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("create mailbox message %s: stat target: %w", path, err)
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".mail-*")
	if err != nil {
		return fmt.Errorf("create atomic mailbox message in %s: %w", filepath.Dir(path), err)
	}
	tempPath := file.Name()
	if err := file.Chmod(0o644); err != nil {
		return fmt.Errorf("chmod mailbox message %s: %w", tempPath, closeAndRemove(file, tempPath, err))
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write mailbox message %s: %w", tempPath, closeAndRemove(file, tempPath, err))
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync mailbox message %s: %w", tempPath, closeAndRemove(file, tempPath, err))
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close mailbox message %s: %w", tempPath, errors.Join(err, os.Remove(tempPath)))
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("publish mailbox message %s: %w", path, errors.Join(err, os.Remove(tempPath)))
	}
	return nil
}

// lockPath returns the single kernel lock guarding one mailbox directory. One
// lock per directory keeps the mailbox free of per-message lock litter, and
// mailbox writes are rare enough that serializing a directory costs nothing.
func lockPath(dir string) string {
	return filepath.Join(dir, ".mailbox.lock")
}

// reserve takes the kernel advisory lock guarding one mailbox directory. The
// kernel releases it when the holder exits, so a writer interrupted by SIGINT
// or killed by SIGKILL can never permanently block a later send or acknowledge
// the way a leftover O_EXCL marker file did.
func reserve(path string) (func() error, error) {
	lock, err := patrollock.AcquireWait(path, reserveTimeout)
	if err != nil {
		if errors.Is(err, patrollock.ErrLocked) {
			return nil, fmt.Errorf(
				"another mailbox writer has held %s for longer than %s; retry once it finishes: %w",
				path, reserveTimeout, err,
			)
		}
		return nil, err
	}
	return lock.Release, nil
}

func readMessage(path string, box Box) (Message, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Message{}, fmt.Errorf("read mailbox message %s: %w", path, err)
	}
	var message Message
	if err := json.Unmarshal(data, &message); err != nil {
		return Message{}, fmt.Errorf("parse mailbox message %s: %w", path, err)
	}
	if err := validateMessage(box, message); err != nil {
		return Message{}, fmt.Errorf("validate mailbox message %s: %w", path, err)
	}
	filenameID := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if message.ID != filenameID {
		return Message{}, fmt.Errorf(
			"validate mailbox message %s: id %q does not match filename id %q",
			path, message.ID, filenameID,
		)
	}
	return message, nil
}

func closeAndRemove(file *os.File, path string, cause error) error {
	return errors.Join(cause, file.Close(), os.Remove(path))
}

func validateMessage(box Box, message Message) error {
	var errs []error
	if err := validateID("message id", message.ID); err != nil {
		errs = append(errs, err)
	}
	if !safeSlug(message.Program) {
		errs = append(errs, fmt.Errorf("message program %q is not a valid slug", message.Program))
	}
	if !itemID.MatchString(message.Item) {
		errs = append(errs, fmt.Errorf("message item %q must be a numbered work item id", message.Item))
	}
	if strings.TrimSpace(message.Body) == "" {
		errs = append(errs, errors.New("message body is required"))
	}
	for i, option := range message.Options {
		if strings.TrimSpace(option) == "" {
			errs = append(errs, fmt.Errorf("message option %d is empty", i+1))
		}
	}
	if message.ReplyTo != "" {
		if err := validateID("message reply_to", message.ReplyTo); err != nil {
			errs = append(errs, err)
		}
	}
	if message.AutomatedBy != "" && !automatedBy.MatchString(message.AutomatedBy) {
		errs = append(errs, fmt.Errorf(
			"message automated_by %q must name a bounded automated turn as cto-automated:<session-prefix>",
			message.AutomatedBy,
		))
	}
	if message.DecisionID != "" && !decisionID.MatchString(message.DecisionID) {
		errs = append(errs, fmt.Errorf("message decision_id %q must be a numbered decision id", message.DecisionID))
	}
	if _, err := time.Parse(time.RFC3339Nano, message.CreatedAt); err != nil {
		errs = append(errs, fmt.Errorf("message created_at %q: %w", message.CreatedAt, err))
	}
	if !validRoute(box, message) {
		errs = append(errs, fmt.Errorf(
			"message kind %q from %q to %q is unsupported for %s",
			message.Kind, message.From, message.To, box,
		))
	}
	return errors.Join(errs...)
}

func validateID(field, value string) error {
	if !safeID.MatchString(value) || strings.Contains(value, "..") {
		return fmt.Errorf("%s %q is not filename-safe", field, value)
	}
	return nil
}

func safeSlug(value string) bool {
	return value != "" &&
		!strings.ContainsAny(value, `/\`) &&
		!strings.Contains(value, "..") &&
		!strings.HasPrefix(value, ".")
}

func validRoute(box Box, message Message) bool {
	switch box {
	case Outbox:
		if message.From != ActorWorker || message.To != ActorCTO {
			return false
		}
		switch message.Kind {
		case KindQuestion, KindConflict, KindPlan, KindPROpen:
			return true
		}
	case Inbox:
		if message.From != ActorCTO || message.To != ActorWorker {
			return false
		}
		switch message.Kind {
		case KindDecision, KindFeedback, KindInstruction:
			return true
		}
	}
	return false
}

func boxDir(projectDir string, box Box, processed bool) (string, error) {
	switch box {
	case Inbox, Outbox:
	default:
		return "", fmt.Errorf("unsupported mailbox %q: want inbox or outbox", box)
	}
	parts := []string{projectDir, "mail"}
	if processed {
		parts = append(parts, "processed")
	}
	parts = append(parts, string(box))
	return filepath.Join(parts...), nil
}

func notifiedDir(projectDir string) string {
	return filepath.Join(projectDir, "mail", "notified")
}
