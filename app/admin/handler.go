// Package admin serves the protected operator interface: searching the
// integration log, viewing a correlation ID's execution timeline and
// workflow state, and resuming failed runs.
//
// Every route is mounted behind auth.RequireAdminToken by the router. The
// HTML views are rendered server-side with html/template, so nothing from
// the logs is ever interpreted by the browser.
package admin

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/gluzo/integration-gateway/app/correlation"
	"github.com/gluzo/integration-gateway/app/intlog"
	"github.com/gluzo/integration-gateway/app/reconcile"
	"github.com/gluzo/integration-gateway/app/worker"
	"github.com/gluzo/integration-gateway/app/workflow"
)

//go:embed templates/*.html
var templateFS embed.FS

// Resumer restarts a failed run; the worker implements it.
type Resumer interface {
	Resume(ctx context.Context, correlationID string) error
}

// Scanner reports runs that are not progressing; app/reconcile implements it.
type Scanner interface {
	Scan(ctx context.Context) ([]reconcile.Finding, error)
	Threshold() time.Duration
}

// Handler serves the admin routes.
type Handler struct {
	reader  *intlog.Reader
	states  workflow.Repository
	resumer Resumer
	scanner Scanner
	logger  *slog.Logger
	tmpl    *template.Template
	now     func() time.Time
}

// WithScanner mounts the reconciliation view. Without one the route reports
// that reconciliation is unavailable rather than showing an empty page that
// looks like good news.
func WithScanner(s Scanner) Option {
	return func(h *Handler) { h.scanner = s }
}

// Option configures a Handler.
type Option func(*Handler)

// NewHandler builds the handler. resumer may be nil on intake-only
// instances, in which case resume requests are refused.
func NewHandler(reader *intlog.Reader, states workflow.Repository, resumer Resumer, logger *slog.Logger, opts ...Option) (*Handler, error) {
	if reader == nil {
		return nil, errors.New("admin: log reader is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	tmpl, err := template.New("admin").Funcs(template.FuncMap{
		// Statuses arrive as plain strings from log entries and as named
		// types from workflow state; accept both.
		"mark": func(v any) string { return statusMark(fmt.Sprint(v)) },
		"ms":   func(ms int64) string { return strconv.FormatInt(ms, 10) },
	}).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("admin: parse templates: %w", err)
	}
	h := &Handler{reader: reader, states: states, resumer: resumer, logger: logger, tmpl: tmpl, now: time.Now}
	for _, opt := range opts {
		opt(h)
	}
	return h, nil
}

// Register mounts the routes on g, which the router has already protected.
func (h *Handler) Register(g *gin.RouterGroup) {
	g.GET("/logs", h.Logs)
	g.GET("/logs/search", h.Search)
	g.GET("/logs/:correlationId", h.Timeline)
	g.GET("/workflows/:correlationId", h.Workflow)
	g.POST("/workflows/:correlationId/resume", h.Resume)
	g.GET("/reconcile", h.Reconcile)
}

// Reconcile lists runs that are not progressing.
//
// The scan runs on request rather than from a cache: an operator opening this
// page is usually reacting to something, and a stale answer is worse than a
// slow one.
func (h *Handler) Reconcile(c *gin.Context) {
	if h.scanner == nil {
		h.fail(c, http.StatusServiceUnavailable, "reconciliation is not available on this instance")
		return
	}
	findings, err := h.scanner.Scan(c.Request.Context())
	if err != nil {
		h.logger.ErrorContext(c.Request.Context(), "reconciliation scan failed", slog.String("error", err.Error()))
		h.fail(c, http.StatusInternalServerError, "reconciliation scan failed")
		return
	}

	summary := reconcile.Summarise(findings)
	if wantsJSON(c, c.Query("format")) {
		c.JSON(http.StatusOK, gin.H{
			"threshold":      h.scanner.Threshold().String(),
			"total":          summary.Total,
			"needing_action": summary.NeedingAction,
			"findings":       toViews(findings),
		})
		return
	}
	h.render(c, "reconcile.html", gin.H{"View": gin.H{
		"Threshold":      h.scanner.Threshold().String(),
		"Findings":       toViews(findings),
		"Summary":        summary,
		"OldestStuckFor": summary.OldestStuckFor.Round(time.Second).String(),
	}})
}

// findingView is a Finding with its durations already rendered, so the
// template holds no formatting logic.
type findingView struct {
	reconcile.Finding
	StuckForText  string `json:"stuck_for"`
	VendorMessage string `json:"vendor_message,omitempty"`
	NeedsOperator bool   `json:"needs_operator"`
}

func toViews(findings []reconcile.Finding) []findingView {
	out := make([]findingView, 0, len(findings))
	for _, f := range findings {
		out = append(out, findingView{
			Finding:       f,
			StuckForText:  f.StuckFor.Round(time.Second).String(),
			VendorMessage: f.VendorMessage(),
			NeedsOperator: f.NeedsOperator(),
		})
	}
	return out
}

// searchForm is what the search page and JSON endpoint accept.
type searchForm struct {
	CorrelationID   string `form:"correlation_id"`
	OrderID         string `form:"order_id"`
	ExternalOrderID string `form:"external_order_id"`
	Integration     string `form:"integration"`
	Platform        string `form:"platform"`
	Workflow        string `form:"workflow"`
	Action          string `form:"action"`
	Status          string `form:"status"`
	DateFrom        string `form:"date_from"`
	DateTo          string `form:"date_to"`
	ErrorsOnly      bool   `form:"errors_only"`
	Limit           int    `form:"limit"`
	Format          string `form:"format"`
}

func (f searchForm) query() (intlog.Query, error) {
	q := intlog.Query{
		CorrelationID:   strings.TrimSpace(f.CorrelationID),
		OrderID:         f.OrderID,
		ExternalOrderID: f.ExternalOrderID,
		Integration:     f.Integration,
		Platform:        f.Platform,
		Workflow:        f.Workflow,
		Action:          f.Action,
		Status:          f.Status,
		ErrorsOnly:      f.ErrorsOnly,
		Limit:           f.Limit,
	}
	if q.CorrelationID != "" && !correlation.IsValid(q.CorrelationID) {
		return q, errors.New("correlation_id is not a valid identifier")
	}
	var err error
	if q.From, err = parseDay(f.DateFrom); err != nil {
		return q, fmt.Errorf("date_from: %w", err)
	}
	if q.To, err = parseDay(f.DateTo); err != nil {
		return q, fmt.Errorf("date_to: %w", err)
	}
	if !q.From.IsZero() && !q.To.IsZero() && q.To.Before(q.From) {
		return q, errors.New("date_to is before date_from")
	}
	return q, nil
}

func parseDay(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, errors.New("expected YYYY-MM-DD")
	}
	return t.UTC(), nil
}

// Logs renders the search page, or JSON when format=json is requested.
func (h *Handler) Logs(c *gin.Context) {
	var form searchForm
	if err := c.ShouldBindQuery(&form); err != nil {
		h.fail(c, http.StatusBadRequest, "invalid query: "+err.Error())
		return
	}
	if wantsJSON(c, form.Format) {
		h.Search(c)
		return
	}
	q, err := form.query()
	var entries []intlog.Entry
	var problem string
	if err != nil {
		problem = err.Error()
	} else if hasFilters(form) {
		entries, err = h.reader.Search(c.Request.Context(), q)
		if err != nil {
			h.logger.ErrorContext(c.Request.Context(), "admin log search failed", slog.String("error", err.Error()))
			problem = "search failed; see server logs"
		}
	}
	days, _ := h.reader.Days()
	h.render(c, "search.html", gin.H{
		"Form":    form,
		"Entries": entries,
		"Problem": problem,
		"Days":    days,
		"Now":     h.now().UTC().Format(time.RFC3339),
	})
}

// Search answers JSON for the same query parameters as Logs.
func (h *Handler) Search(c *gin.Context) {
	var form searchForm
	if err := c.ShouldBindQuery(&form); err != nil {
		h.fail(c, http.StatusBadRequest, "invalid query: "+err.Error())
		return
	}
	q, err := form.query()
	if err != nil {
		h.fail(c, http.StatusBadRequest, err.Error())
		return
	}
	entries, err := h.reader.Search(c.Request.Context(), q)
	if err != nil {
		h.logger.ErrorContext(c.Request.Context(), "admin log search failed", slog.String("error", err.Error()))
		h.fail(c, http.StatusInternalServerError, "search failed")
		return
	}
	if entries == nil {
		entries = []intlog.Entry{}
	}
	c.JSON(http.StatusOK, gin.H{"count": len(entries), "entries": entries})
}

// timelineView is the data behind the timeline page and JSON.
type timelineView struct {
	CorrelationID string          `json:"correlation_id"`
	State         *workflow.State `json:"state,omitempty"`
	Entries       []intlog.Entry  `json:"entries"`
	CanResume     bool            `json:"can_resume"`
}

// Timeline shows everything recorded for one correlation ID, chronologically.
func (h *Handler) Timeline(c *gin.Context) {
	id := c.Param("correlationId")
	if !correlation.IsValid(id) {
		h.fail(c, http.StatusBadRequest, "invalid correlation id")
		return
	}
	entries, err := h.reader.Timeline(c.Request.Context(), id)
	if err != nil {
		h.logger.ErrorContext(c.Request.Context(), "admin timeline failed", slog.String("error", err.Error()))
		h.fail(c, http.StatusInternalServerError, "timeline failed")
		return
	}
	view := timelineView{CorrelationID: id, Entries: entries}
	if view.Entries == nil {
		view.Entries = []intlog.Entry{}
	}
	if h.states != nil {
		st, err := h.states.Get(c.Request.Context(), id)
		if err == nil {
			view.State = st
			view.CanResume = h.resumer != nil && st.Status == workflow.StatusFailed
		} else if !errors.Is(err, workflow.ErrStateNotFound) {
			h.logger.WarnContext(c.Request.Context(), "admin timeline: load state", slog.String("error", err.Error()))
		}
	}
	if view.State == nil && len(entries) == 0 {
		h.fail(c, http.StatusNotFound, "no records for correlation id")
		return
	}
	if wantsJSON(c, c.Query("format")) {
		c.JSON(http.StatusOK, view)
		return
	}
	h.render(c, "timeline.html", gin.H{"View": view})
}

// Workflow returns the persisted state of a run.
func (h *Handler) Workflow(c *gin.Context) {
	id := c.Param("correlationId")
	if !correlation.IsValid(id) {
		h.fail(c, http.StatusBadRequest, "invalid correlation id")
		return
	}
	if h.states == nil {
		h.fail(c, http.StatusNotFound, "workflow state is not available on this instance")
		return
	}
	st, err := h.states.Get(c.Request.Context(), id)
	if errors.Is(err, workflow.ErrStateNotFound) {
		h.fail(c, http.StatusNotFound, "no workflow state for correlation id")
		return
	}
	if err != nil {
		h.fail(c, http.StatusInternalServerError, "load state failed")
		return
	}
	c.JSON(http.StatusOK, st)
}

// Resume restarts a failed run in the background and answers 202. The
// outcome shows up in the timeline; the request never waits on retries.
func (h *Handler) Resume(c *gin.Context) {
	id := c.Param("correlationId")
	if !correlation.IsValid(id) {
		h.fail(c, http.StatusBadRequest, "invalid correlation id")
		return
	}
	if h.resumer == nil || h.states == nil {
		h.fail(c, http.StatusServiceUnavailable, "this instance runs no worker; resume from a worker instance")
		return
	}
	st, err := h.states.Get(c.Request.Context(), id)
	if errors.Is(err, workflow.ErrStateNotFound) {
		h.fail(c, http.StatusNotFound, "no workflow state for correlation id")
		return
	}
	if err != nil {
		h.fail(c, http.StatusInternalServerError, "load state failed")
		return
	}
	if st.Status.IsFinal() {
		h.fail(c, http.StatusConflict, "run already finished")
		return
	}

	h.logger.InfoContext(c.Request.Context(), "admin requested resume",
		slog.String("correlation_id", id), slog.String("client_ip", c.ClientIP()))
	go func() {
		if err := h.resumer.Resume(context.WithoutCancel(c.Request.Context()), id); err != nil {
			if errors.Is(err, worker.ErrRunInProgress) || errors.Is(err, worker.ErrRunFinished) {
				h.logger.Info("admin resume not applicable", slog.String("correlation_id", id), slog.String("reason", err.Error()))
				return
			}
			h.logger.Warn("admin resume ended in failure", slog.String("correlation_id", id), slog.String("error", err.Error()))
		}
	}()

	if wantsJSON(c, c.Query("format")) || !strings.Contains(c.GetHeader("Accept"), "text/html") {
		c.JSON(http.StatusAccepted, gin.H{"status": "resume_started", "correlation_id": id})
		return
	}
	c.Redirect(http.StatusSeeOther, "/admin/logs/"+id)
}

func (h *Handler) render(c *gin.Context, name string, data gin.H) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	c.Status(http.StatusOK)
	if err := h.tmpl.ExecuteTemplate(c.Writer, name, data); err != nil {
		h.logger.ErrorContext(c.Request.Context(), "admin template failed", slog.String("template", name), slog.String("error", err.Error()))
	}
}

func (h *Handler) fail(c *gin.Context, status int, reason string) {
	c.AbortWithStatusJSON(status, gin.H{
		"error":          strings.ToLower(http.StatusText(status)),
		"reason":         reason,
		"correlation_id": correlation.FromContext(c.Request.Context()),
	})
}

func wantsJSON(c *gin.Context, format string) bool {
	if strings.EqualFold(format, "json") {
		return true
	}
	accept := c.GetHeader("Accept")
	return strings.Contains(accept, "application/json") && !strings.Contains(accept, "text/html")
}

func hasFilters(f searchForm) bool {
	return f.CorrelationID != "" || f.OrderID != "" || f.ExternalOrderID != "" || f.Integration != "" ||
		f.Platform != "" || f.Workflow != "" || f.Action != "" || f.Status != "" || f.DateFrom != "" || f.DateTo != "" || f.ErrorsOnly
}

// statusMark renders the glyph used in the timeline.
func statusMark(status string) string {
	switch status {
	case intlog.StatusSuccess:
		return "✓"
	case intlog.StatusFailed:
		return "✗"
	case intlog.StatusSkipped, intlog.StatusDuplicate:
		return "–"
	case intlog.StatusStarted:
		return "▶"
	default:
		return "·"
	}
}
