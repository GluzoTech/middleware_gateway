package apperror

const maxDetailLength = 500

// Info is a serialisable snapshot of an error for workflow state files and
// execution logs. It carries what a remote API said, never credentials.
type Info struct {
	Category        Category `json:"category"`
	Message         string   `json:"message"`
	Retryable       bool     `json:"retryable"`
	Integration     string   `json:"integration,omitempty"`
	Operation       string   `json:"operation,omitempty"`
	HTTPStatus      int      `json:"http_status,omitempty"`
	ExternalCode    string   `json:"external_code,omitempty"`
	ExternalMessage string   `json:"external_message,omitempty"`
	// Detail is the text of the wrapped cause, bounded in length.
	Detail string `json:"detail,omitempty"`
}

// InfoOf snapshots err after classification; nil for a nil error.
func InfoOf(err error) *Info {
	e := Classify(err)
	if e == nil {
		return nil
	}
	info := &Info{
		Category:        e.Category,
		Message:         e.Message,
		Retryable:       e.Retryable,
		Integration:     e.Integration,
		Operation:       e.Operation,
		HTTPStatus:      e.HTTPStatus,
		ExternalCode:    e.ExternalCode,
		ExternalMessage: e.ExternalMessage,
	}
	if e.Err != nil {
		detail := e.Err.Error()
		if len(detail) > maxDetailLength {
			detail = detail[:maxDetailLength] + "..."
		}
		info.Detail = detail
	}
	return info
}

// String renders the snapshot for log lines.
func (i *Info) String() string {
	if i == nil {
		return ""
	}
	s := string(i.Category) + ": " + i.Message
	if i.Detail != "" {
		s += " (" + i.Detail + ")"
	}
	return s
}
