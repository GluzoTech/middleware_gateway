// Package dto holds the building blocks shared by Uniware API contracts.
//
// Every Uniware response reports success in the body: {successful, message,
// errors[], warnings[]}. HTTP 200 with successful=false is an application
// error and is handled as such by the client.
package dto

// Response is the common envelope of Uniware responses.
type Response struct {
	Successful bool      `json:"successful"`
	Message    string    `json:"message"`
	Errors     []Error   `json:"errors"`
	Warnings   []Warning `json:"warnings"`
}

// Error is one entry of the errors array.
type Error struct {
	Code        int            `json:"code"`
	FieldName   string         `json:"fieldName"`
	Description string         `json:"description"`
	Message     string         `json:"message"`
	ErrorParams map[string]any `json:"errorParams"`
}

// Warning is one entry of the warnings array.
type Warning struct {
	Code        int    `json:"code"`
	Message     string `json:"message"`
	Description string `json:"description"`
}

// FirstError returns the first reported error, or nil.
func (r Response) FirstError() *Error {
	if len(r.Errors) == 0 {
		return nil
	}
	e := r.Errors[0]
	return &e
}

// Text renders an error for logs and error messages.
func (e Error) Text() string {
	switch {
	case e.Description != "" && e.Message != "" && e.Description != e.Message:
		return e.Description + ": " + e.Message
	case e.Description != "":
		return e.Description
	default:
		return e.Message
	}
}
