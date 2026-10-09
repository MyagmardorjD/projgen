package prompt

import (
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestAsk(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantErr   error
		wantFW    string
		wantArch  string
		wantDB    string
		wantExtra []string
	}{
		{
			name:     "all defaults",
			input:    "\n\n\n\n\n\n\n\n",
			wantFW:   "gin",
			wantArch: "layered",
			wantDB:   "postgresql",
		},
		{
			name:      "explicit choices with retry on bad input",
			input:     "order-service\n\n1\n9\n2\n2\n2\n1,2,2\ny\n",
			wantFW:    "echo",
			wantArch:  "clean",
			wantDB:    "mysql",
			wantExtra: []string{"docker", "docker-compose"},
		},
		{
			name:    "declined",
			input:   "\n\n\n\n\n\n\nn\n",
			wantErr: ErrCancelled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, err := New(strings.NewReader(tt.input), io.Discard).Ask()
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if o.Framework != tt.wantFW || o.Architecture != tt.wantArch || o.Database != tt.wantDB {
				t.Errorf("got %s/%s/%s, want %s/%s/%s", o.Framework, o.Architecture, o.Database, tt.wantFW, tt.wantArch, tt.wantDB)
			}
			if !slices.Equal(o.Extras, tt.wantExtra) {
				t.Errorf("extras = %v, want %v", o.Extras, tt.wantExtra)
			}
		})
	}
}

func TestAsk_InvalidCombination(t *testing.T) {
	// docker-compose (2) without docker (1)
	_, err := New(strings.NewReader("\n\n\n\n\n\n2\n"), io.Discard).Ask()
	if err == nil || !strings.Contains(err.Error(), "requires docker") {
		t.Fatalf("err = %v, want compose/docker error", err)
	}
}

func TestAsk_EndOfInput(t *testing.T) {
	if _, err := New(strings.NewReader(""), io.Discard).Ask(); err == nil {
		t.Fatal("expected an error when input ends")
	}
}
