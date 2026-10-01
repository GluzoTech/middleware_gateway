package dotenv_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gluzo/integration-gateway/app/dotenv"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want map[string]string
	}{
		{
			name: "plain assignment",
			in:   "APP_PORT=8080\n",
			want: map[string]string{"APP_PORT": "8080"},
		},
		{
			name: "blank lines and comment lines are skipped",
			in:   "\n# a comment\n\n   # indented comment\nA=1\n",
			want: map[string]string{"A": "1"},
		},
		{
			name: "trailing comment is stripped with the padding before it",
			in:   "APP_ENV=development            # development | staging | production\n",
			want: map[string]string{"APP_ENV": "development"},
		},
		{
			name: "a hash with no space before it stays in the value",
			in:   "DATABASE_URL=postgres://u:pa#ss@host:5432/db?sslmode=disable\n",
			want: map[string]string{"DATABASE_URL": "postgres://u:pa#ss@host:5432/db?sslmode=disable"},
		},
		{
			name: "value is empty when only a comment follows the equals",
			in:   "VINCULUM_DUPLICATE_ORDER_CODES= # comma-separated responseCode values\n",
			want: map[string]string{"VINCULUM_DUPLICATE_ORDER_CODES": ""},
		},
		{
			name: "empty value",
			in:   "EASYECOM_SHIPMENT_STATUS_IDS=\n",
			want: map[string]string{"EASYECOM_SHIPMENT_STATUS_IDS": ""},
		},
		{
			name: "export prefix is accepted",
			in:   "export DATABASE_URL=postgres://localhost/db\n",
			want: map[string]string{"DATABASE_URL": "postgres://localhost/db"},
		},
		{
			name: "punctuation in an unquoted value is preserved",
			in:   "REDIS_PASSWORD=*M@BRf9|>02)\n",
			want: map[string]string{"REDIS_PASSWORD": "*M@BRf9|>02)"},
		},
		{
			name: "single quotes are literal",
			in:   `P='raw \n value # not a comment'` + "\n",
			want: map[string]string{"P": `raw \n value # not a comment`},
		},
		{
			name: "double quotes expand escapes and protect a hash",
			in:   `P="a\tb # kept"` + "\n",
			want: map[string]string{"P": "a\tb # kept"},
		},
		{
			name: "unknown escape is kept verbatim",
			in:   `P="C:\Users\W"` + "\n",
			want: map[string]string{"P": `C:\Users\W`},
		},
		{
			name: "text after a closing quote is discarded",
			in:   `P="value"   # a comment` + "\n",
			want: map[string]string{"P": "value"},
		},
		{
			name: "an equals sign in the value is not a separator",
			in:   "Q=a=b=c\n",
			want: map[string]string{"Q": "a=b=c"},
		},
		{
			name: "CRLF line endings",
			in:   "A=1\r\nB=2\r\n",
			want: map[string]string{"A": "1", "B": "2"},
		},
		{
			name: "a byte order mark does not corrupt the first key",
			in:   "\ufeffAPP_PORT=8080\n",
			want: map[string]string{"APP_PORT": "8080"},
		},
		{
			name: "last occurrence wins",
			in:   "A=1\nA=2\n",
			want: map[string]string{"A": "2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vars, err := dotenv.Parse(strings.NewReader(tt.in))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			got := make(map[string]string, len(vars))
			for _, v := range vars {
				got[v.Key] = v.Value
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d variables, want %d: %v", len(got), len(tt.want), got)
			}
			for k, want := range tt.want {
				if got[k] != want {
					t.Errorf("%s = %q, want %q", k, got[k], want)
				}
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"missing equals", "A=1\nJUST_A_WORD\n", "line 2: missing '='"},
		{"invalid name", "not a key=1\n", "line 1: invalid variable name"},
		{"name starting with a digit", "1BAD=x\n", "line 1: invalid variable name"},
		{"empty name", "=value\n", "line 1: invalid variable name"},
		{"unterminated double quote", "A=\"oops\n", "line 1: unterminated double quote"},
		{"unterminated single quote", "A='oops\n", "line 1: unterminated single quote"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := dotenv.Parse(strings.NewReader(tt.in))
			if err == nil {
				t.Fatal("expected an error")
			}
			if err.Error() != tt.want {
				t.Fatalf("error = %q, want %q", err, tt.want)
			}
		})
	}
}

// A parse error must not carry the offending line: environment files hold
// secrets and this message reaches stderr.
func TestParseErrorOmitsLineContents(t *testing.T) {
	const secret = "hunter2-do-not-log-me"
	_, err := dotenv.Parse(strings.NewReader("BAD KEY=" + secret + "\n"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked the value: %q", err)
	}
}

func TestLoadSetsOnlyUnsetVariables(t *testing.T) {
	path := writeEnvFile(t, "FROM_FILE=file-value\nALREADY_SET=file-value\n")
	t.Setenv("ALREADY_SET", "environment-value")

	loaded, err := dotenv.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded != path {
		t.Fatalf("loaded = %q, want %q", loaded, path)
	}
	if got := os.Getenv("FROM_FILE"); got != "file-value" {
		t.Errorf("FROM_FILE = %q, want file-value", got)
	}
	if got := os.Getenv("ALREADY_SET"); got != "environment-value" {
		t.Errorf("ALREADY_SET = %q, want the environment to win", got)
	}
	t.Cleanup(func() { _ = os.Unsetenv("FROM_FILE") })
}

// A key repeated in the file takes its last value: the first assignment must
// not make the second look as though the environment had supplied it.
func TestLoadRepeatedKeyTakesLastValue(t *testing.T) {
	path := writeEnvFile(t, "REPEATED=first\nREPEATED=second\n")

	if _, err := dotenv.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("REPEATED") })

	if got := os.Getenv("REPEATED"); got != "second" {
		t.Errorf("REPEATED = %q, want second", got)
	}
}

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	loaded, err := dotenv.Load(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded != "" {
		t.Fatalf("loaded = %q, want an empty path", loaded)
	}
}

func TestLoadUsesEnvFileVariable(t *testing.T) {
	path := writeEnvFile(t, "VIA_ENV_FILE=yes\n")
	t.Setenv(dotenv.PathVariable, path)

	loaded, err := dotenv.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded != path {
		t.Fatalf("loaded = %q, want %q", loaded, path)
	}
	t.Cleanup(func() { _ = os.Unsetenv("VIA_ENV_FILE") })

	if got := os.Getenv("VIA_ENV_FILE"); got != "yes" {
		t.Errorf("VIA_ENV_FILE = %q, want yes", got)
	}
}

func TestLoadReportsParseErrorWithPath(t *testing.T) {
	path := writeEnvFile(t, "A=1\nBROKEN\n")

	if _, err := dotenv.Load(path); err == nil {
		t.Fatal("expected an error")
	} else if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("error = %q, want it to name %s and line 2", err, path)
	}
}

func writeEnvFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	return path
}
