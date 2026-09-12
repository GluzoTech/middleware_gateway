package webhook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/auth"
	"github.com/gluzo/integration-gateway/app/correlation"
	"github.com/gluzo/integration-gateway/app/event"
	"github.com/gluzo/integration-gateway/app/httpserver/middleware"
	"github.com/gluzo/integration-gateway/app/idempotency"
	"github.com/gluzo/integration-gateway/app/intlog"
	"github.com/gluzo/integration-gateway/app/queue"
)

// Handler processes webhooks for one platform.
//
// The request must already be authenticated: the auth middleware stores the
// platform and principal on the context. The handler then parses, validates,
// claims idempotency and publishes. It answers 2xx for every well-formed
// authenticated event, including duplicates, because senders such as
// EasyEcom treat any 4xx/5xx as a failed delivery and eventually disable the
// trigger.
type Handler struct {
	parser      Parser
	idempotency idempotency.Store
	publisher   queue.Publisher
	logger      *slog.Logger
	recorder    intlog.Recorder
	now         func() time.Time
	newJobID    func() string
}

// Option configures a Handler.
type Option func(*Handler)

// WithRecorder sets the integration log recorder that receives one
// WEBHOOK_RECEIVED entry per event.
func WithRecorder(r intlog.Recorder) Option {
	return func(h *Handler) {
		if r != nil {
			h.recorder = r
		}
	}
}

// NewHandler wires a handler for parser's platform.
func NewHandler(parser Parser, store idempotency.Store, publisher queue.Publisher, logger *slog.Logger, opts ...Option) *Handler {
	h := &Handler{
		parser:      parser,
		idempotency: store,
		publisher:   publisher,
		logger:      logger,
		recorder:    intlog.Nop{},
		now:         time.Now,
		newJobID:    uuid.NewString,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Platform names the platform this handler serves; the router mounts it at
// /webhooks/<platform>.
func (h *Handler) Platform() string { return h.parser.Platform() }

// eventResult is the per-event outcome returned to the sender.
type eventResult struct {
	ExternalOrderID string `json:"external_order_id,omitempty"`
	CorrelationID   string `json:"correlation_id"`
	Status          string `json:"status"` // accepted | duplicate
}

// Handle is the Gin handler for POST /webhooks/<platform>[/:event].
func (h *Handler) Handle(c *gin.Context) {
	ctx := c.Request.Context()
	requestID := correlation.FromContext(ctx)

	platform, ok := auth.PlatformFromContext(ctx)
	if !ok {
		h.reject(c, http.StatusUnauthorized, "unauthenticated request reached webhook handler")
		return
	}
	principal, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		h.reject(c, http.StatusUnauthorized, "unauthenticated request reached webhook handler")
		return
	}
	if platform.Name != h.parser.Platform() {
		h.reject(c, http.StatusForbidden, fmt.Sprintf("platform %q may not post %s webhooks", platform.Name, h.parser.Platform()))
		return
	}

	eventType, ok := EventTypeFromPath(c.Param("event"))
	if !ok {
		h.reject(c, http.StatusBadRequest, fmt.Sprintf("unknown event %q", c.Param("event")))
		return
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		if middleware.IsBodyTooLarge(err) {
			h.reject(c, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		h.reject(c, http.StatusBadRequest, "could not read request body")
		return
	}

	events, err := h.parser.Parse(eventType, body)
	if err != nil {
		h.reject(c, http.StatusBadRequest, apperror.Classify(err).Message)
		return
	}
	if len(events) == 0 {
		h.logger.InfoContext(ctx, "webhook carried no events",
			slog.String("platform", platform.Name),
			slog.String("event_type", eventType),
			slog.String("correlation_id", requestID),
		)
		c.JSON(http.StatusOK, gin.H{"status": "empty", "correlation_id": requestID})
		return
	}

	receivedAt := h.now()
	results := make([]eventResult, 0, len(events))
	accepted := 0
	for i := range events {
		ev := &events[i]
		ev.Platform = platform.Name
		ev.EventType = eventType
		ev.IntegrationID = principal.Integration.ID.String()
		ev.ReceivedAt = receivedAt
		// The first event shares the request's correlation ID so the HTTP
		// log line and the workflow line up; further events in a batch get
		// their own.
		ev.CorrelationID = requestID
		if i > 0 {
			ev.CorrelationID = correlation.New()
		}
		if err := event.Validate(*ev); err != nil {
			h.reject(c, http.StatusBadRequest, fmt.Sprintf("event %d: %v", i, err))
			return
		}

		result, err := h.admit(c, *ev)
		if err != nil {
			h.logger.ErrorContext(ctx, "webhook admission failed",
				slog.String("platform", platform.Name),
				slog.String("event_type", eventType),
				slog.String("correlation_id", ev.CorrelationID),
				slog.String("error", err.Error()),
			)
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error":          "temporarily unavailable",
				"correlation_id": requestID,
			})
			return
		}
		if result.Status == "accepted" {
			accepted++
		}
		results = append(results, result)
	}

	code := http.StatusOK
	overall := "duplicate"
	if accepted > 0 {
		code = http.StatusAccepted
		overall = "accepted"
	}
	c.JSON(code, gin.H{
		"status":         overall,
		"correlation_id": requestID,
		"events":         results,
	})
}

// admit claims the idempotency key and publishes the job. On a duplicate it
// reports the earlier correlation ID; on a publish failure it releases the
// claim so the sender's retry can be accepted.
func (h *Handler) admit(c *gin.Context, ev event.Event) (eventResult, error) {
	ctx := c.Request.Context()
	claim, err := h.idempotency.Claim(ctx, idempotency.Record{
		Key:           ev.IdempotencyKey,
		CorrelationID: ev.CorrelationID,
		Platform:      ev.Platform,
		EventType:     ev.EventType,
		Status:        idempotency.StatusAccepted,
	})
	if err != nil {
		return eventResult{}, err
	}
	if !claim.Accepted {
		h.logger.InfoContext(ctx, "duplicate webhook ignored",
			slog.String("platform", ev.Platform),
			slog.String("event_type", ev.EventType),
			slog.String("external_order_id", ev.ExternalOrderID),
			slog.String("idempotency_key", ev.IdempotencyKey),
			slog.String("correlation_id", ev.CorrelationID),
			slog.String("original_correlation_id", claim.Existing.CorrelationID),
			slog.String("original_status", string(claim.Existing.Status)),
		)
		h.recorder.Record(ctx, intlog.Entry{
			Timestamp:       h.now(),
			CorrelationID:   claim.Existing.CorrelationID,
			Platform:        ev.Platform,
			IntegrationID:   ev.IntegrationID,
			ExternalOrderID: ev.ExternalOrderID,
			Action:          intlog.ActionWebhookReceived,
			Status:          intlog.StatusDuplicate,
			Details: map[string]any{
				"event_type":               ev.EventType,
				"duplicate_correlation_id": ev.CorrelationID,
				"original_status":          string(claim.Existing.Status),
			},
		})
		return eventResult{ExternalOrderID: ev.ExternalOrderID, CorrelationID: claim.Existing.CorrelationID, Status: "duplicate"}, nil
	}

	payload, err := json.Marshal(ev)
	if err != nil {
		_ = h.idempotency.Release(ctx, ev.IdempotencyKey)
		return eventResult{}, fmt.Errorf("encode event: %w", err)
	}
	job := queue.Job{
		ID:             h.newJobID(),
		CorrelationID:  ev.CorrelationID,
		EventType:      ev.EventType,
		Platform:       ev.Platform,
		IntegrationID:  ev.IntegrationID,
		IdempotencyKey: ev.IdempotencyKey,
		Payload:        payload,
		EnqueuedAt:     h.now(),
	}
	if err := h.publisher.Publish(ctx, job); err != nil {
		if releaseErr := h.idempotency.Release(ctx, ev.IdempotencyKey); releaseErr != nil {
			err = errors.Join(err, releaseErr)
		}
		return eventResult{}, fmt.Errorf("publish job: %w", err)
	}

	h.logger.InfoContext(ctx, "webhook accepted",
		slog.String("platform", ev.Platform),
		slog.String("event_type", ev.EventType),
		slog.String("external_order_id", ev.ExternalOrderID),
		slog.String("routing_key", ev.RoutingKey.String()),
		slog.String("integration_id", ev.IntegrationID),
		slog.String("job_id", job.ID),
		slog.String("correlation_id", ev.CorrelationID),
	)
	h.recorder.Record(ctx, intlog.Entry{
		Timestamp:       h.now(),
		CorrelationID:   ev.CorrelationID,
		Platform:        ev.Platform,
		IntegrationID:   ev.IntegrationID,
		ExternalOrderID: ev.ExternalOrderID,
		Action:          intlog.ActionWebhookReceived,
		Status:          intlog.StatusSuccess,
		Details: map[string]any{
			"event_type":  ev.EventType,
			"routing_key": ev.RoutingKey.String(),
			"job_id":      job.ID,
		},
	})
	return eventResult{ExternalOrderID: ev.ExternalOrderID, CorrelationID: ev.CorrelationID, Status: "accepted"}, nil
}

func (h *Handler) reject(c *gin.Context, status int, reason string) {
	ctx := c.Request.Context()
	h.logger.WarnContext(ctx, "webhook rejected",
		slog.Int("status", status),
		slog.String("reason", reason),
		slog.String("client_ip", c.ClientIP()),
		slog.String("correlation_id", correlation.FromContext(ctx)),
	)
	c.AbortWithStatusJSON(status, gin.H{
		"error":          strings.ToLower(http.StatusText(status)),
		"reason":         reason,
		"correlation_id": correlation.FromContext(ctx),
	})
}
