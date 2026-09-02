package program

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// requestHashPattern is the exact durable shape of a recorded request hash:
// one lowercase SHA-256 hex digest and nothing else.
var requestHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// NormalizeRequest collapses a change request onto the exact text its hash
// covers. Leading, trailing, and repeated whitespace — including the line
// breaks a shell heredoc adds — carry no meaning in a request, so two
// retries of the same request normalize onto one string. Case is meaning, so
// it is preserved.
func NormalizeRequest(request string) string {
	return strings.Join(strings.Fields(request), " ")
}

// RequestHash returns the SHA-256 of a normalized change request. It is the
// durable identity of one CEO request, so an identical retry finds the
// follow-up the first attempt already created instead of opening a second one.
func RequestHash(request string) (string, error) {
	normalized := NormalizeRequest(request)
	if normalized == "" {
		return "", errors.New("change request is empty")
	}
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:]), nil
}

// ValidateRequestHash accepts exactly one lowercase 64-character hex digest.
func ValidateRequestHash(hash string) error {
	if !requestHashPattern.MatchString(hash) {
		return fmt.Errorf(
			"invalid request hash %q: want exactly 64 lowercase hex characters", hash,
		)
	}
	return nil
}

// FindFollowUp returns the live follow-up item that already records this exact
// request against this exact original item. A canceled follow-up is never
// reused: the CEO asked for the same change again after it was withdrawn, so
// the retry deserves its own item.
func (p Program) FindFollowUp(originalID, requestHash string) (WorkItem, bool) {
	if originalID == "" || requestHash == "" {
		return WorkItem{}, false
	}
	for _, item := range p.Items {
		if item.FollowUpOf != originalID || item.RequestHash != requestHash {
			continue
		}
		if item.Status == ItemCancelled {
			continue
		}
		return item, true
	}
	return WorkItem{}, false
}

// validateFollowUp checks one item's follow-up reference against the full item
// set. Both fields travel together: a reference with no request cannot be
// deduplicated, and a request with no reference names nothing.
func validateFollowUp(item WorkItem, items map[string]WorkItem) []error {
	var errs []error
	if item.FollowUpOf == "" && item.RequestHash == "" {
		return nil
	}
	if item.FollowUpOf == "" {
		errs = append(errs, fmt.Errorf("item %q has request_hash without follow_up_of", item.ID))
	}
	if item.RequestHash == "" {
		errs = append(errs, fmt.Errorf("item %q has follow_up_of without request_hash", item.ID))
	}
	if item.RequestHash != "" {
		if err := ValidateRequestHash(item.RequestHash); err != nil {
			errs = append(errs, fmt.Errorf("item %q request_hash: %w", item.ID, err))
		}
	}
	if item.FollowUpOf == "" {
		return errs
	}
	if item.FollowUpOf == item.ID {
		errs = append(errs, fmt.Errorf("item %q cannot be a follow-up of itself", item.ID))
		return errs
	}
	if _, exists := items[item.FollowUpOf]; !exists {
		errs = append(errs, fmt.Errorf(
			"item %q follow_up_of %q does not exist", item.ID, item.FollowUpOf,
		))
	}
	return errs
}

// followUpCycle returns the first follow-up chain that loops back on itself.
// A follow-up chain is an ordering claim like a dependency, so a cycle would
// leave every item in it waiting on the next one forever.
func followUpCycle(ordered []WorkItem, items map[string]WorkItem) []string {
	const (
		unvisited = 0
		active    = 1
		done      = 2
	)
	state := make(map[string]int, len(items))
	var path []string
	var walk func(id string) []string
	walk = func(id string) []string {
		switch state[id] {
		case active:
			start := 0
			for i, visited := range path {
				if visited == id {
					start = i
					break
				}
			}
			return append(append([]string(nil), path[start:]...), id)
		case done:
			return nil
		}
		state[id] = active
		path = append(path, id)
		item, exists := items[id]
		if exists && item.FollowUpOf != "" {
			if _, referenced := items[item.FollowUpOf]; referenced {
				if cycle := walk(item.FollowUpOf); len(cycle) > 0 {
					return cycle
				}
			}
		}
		path = path[:len(path)-1]
		state[id] = done
		return nil
	}
	for _, item := range ordered {
		if cycle := walk(item.ID); len(cycle) > 0 {
			return cycle
		}
	}
	return nil
}
