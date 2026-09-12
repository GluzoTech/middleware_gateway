package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func noEnv(string) (string, bool) { return "", false }

func TestRunUsageAndValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr error
		wantOut string
	}{
		{"no arguments", nil, errUsage, ""},
		{"help", []string{"help"}, nil, "Usage:"},
		{"unknown command", []string{"frobnicate"}, errUsage, ""},
		{"platform without subcommand", []string{"platform"}, errUsage, ""},
		{"platform create without name", []string{"platform", "create"}, errUsage, ""},
		{"integration create missing flags", []string{"integration", "create", "--name", "x"}, errUsage, ""},
		{"token issue missing flags", []string{"token", "issue", "--integration", "x"}, errUsage, ""},
		{"token revoke bad id", []string{"token", "revoke", "--id", "not-a-uuid"}, errUsage, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(tt.args, &stdout, &stderr, noEnv)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.Contains(stdout.String(), tt.wantOut) {
				t.Fatalf("stdout %q does not contain %q", stdout.String(), tt.wantOut)
			}
		})
	}
}

func TestRunRequiresDatabaseURL(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run([]string{"platform", "list"}, &stdout, &stderr, noEnv)
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("err = %v, want DATABASE_URL requirement", err)
	}
}
