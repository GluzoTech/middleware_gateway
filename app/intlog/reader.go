package intlog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Search limits.
const (
	DefaultSearchLimit = 200
	MaxSearchLimit     = 2000
	// DefaultSearchDays bounds an unqualified search.
	DefaultSearchDays = 7
	// maxLineBytes bounds one JSONL line when scanning.
	maxLineBytes = 4 << 20
)

// Query filters log entries. String fields match case-insensitively;
// From and To are inclusive day bounds in UTC.
type Query struct {
	CorrelationID   string
	OrderID         string
	ExternalOrderID string
	Integration     string
	Platform        string
	Workflow        string
	Action          string
	Status          string
	ErrorsOnly      bool
	From            time.Time
	To              time.Time
	Limit           int
}

// Reader searches the JSONL files written by FileRecorder.
type Reader struct {
	root string
	now  func() time.Time
}

// NewReader reads logs under root.
func NewReader(root string) *Reader {
	return &Reader{root: root, now: time.Now}
}

// Days lists the available day directories, newest first.
func (r *Reader) Days() ([]string, error) {
	entries, err := os.ReadDir(r.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("intlog: list days: %w", err)
	}
	var days []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := time.Parse(dayLayout, e.Name()); err == nil {
			days = append(days, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	return days, nil
}

// Search returns entries matching q, newest first, capped at q.Limit.
//
// A query that names neither a correlation ID nor a date range covers the
// last DefaultSearchDays days; a correlation ID lookup covers every day.
func (r *Reader) Search(ctx context.Context, q Query) ([]Entry, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	if limit > MaxSearchLimit {
		limit = MaxSearchLimit
	}
	from, to := q.From, q.To
	if from.IsZero() && to.IsZero() && q.CorrelationID == "" {
		to = r.now().UTC()
		from = to.AddDate(0, 0, -(DefaultSearchDays - 1))
	}

	days, err := r.Days()
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, day := range days {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if !dayInRange(day, from, to) {
			continue
		}
		file := IntegrationFile
		if q.ErrorsOnly {
			file = ErrorFile
		}
		entries, err := r.readFile(filepath.Join(r.root, day, file), func(e Entry) bool { return matches(e, q) })
		if err != nil {
			return nil, err
		}
		// Newest first within the day.
		for i := len(entries) - 1; i >= 0 && len(out) < limit; i-- {
			out = append(out, entries[i])
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// Timeline returns every entry for correlationID in chronological order.
func (r *Reader) Timeline(ctx context.Context, correlationID string) ([]Entry, error) {
	if strings.TrimSpace(correlationID) == "" {
		return nil, errors.New("intlog: correlation id is required")
	}
	days, err := r.Days()
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, day := range days {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		entries, err := r.readFile(filepath.Join(r.root, day, IntegrationFile), func(e Entry) bool { return e.CorrelationID == correlationID })
		if err != nil {
			return nil, err
		}
		out = append(out, entries...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Timestamp.Before(out[j].Timestamp) })
	return out, nil
}

// readFile scans one JSONL file, keeping entries that satisfy keep.
// Undecodable lines are skipped rather than failing the whole search.
func (r *Reader) readFile(path string, keep func(Entry) bool) ([]Entry, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("intlog: open %s: %w", path, err)
	}
	defer f.Close()

	var out []Entry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), maxLineBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		if keep(e) {
			out = append(out, e)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("intlog: read %s: %w", path, err)
	}
	return out, nil
}

func dayInRange(day string, from, to time.Time) bool {
	d, err := time.Parse(dayLayout, day)
	if err != nil {
		return false
	}
	if !from.IsZero() && d.Before(from.UTC().Truncate(24*time.Hour)) {
		return false
	}
	if !to.IsZero() && d.After(to.UTC().Truncate(24*time.Hour)) {
		return false
	}
	return true
}

func matches(e Entry, q Query) bool {
	if q.CorrelationID != "" && e.CorrelationID != q.CorrelationID {
		return false
	}
	if q.ErrorsOnly && e.Status != StatusFailed && e.Error == nil {
		return false
	}
	checks := []struct{ want, got string }{
		{q.OrderID, e.OrderID},
		{q.ExternalOrderID, e.ExternalOrderID},
		{q.Integration, e.Integration},
		{q.Platform, e.Platform},
		{q.Workflow, e.Workflow},
		{q.Action, e.Action},
		{q.Status, e.Status},
	}
	for _, c := range checks {
		if c.want != "" && !strings.EqualFold(strings.TrimSpace(c.want), c.got) {
			return false
		}
	}
	return true
}
