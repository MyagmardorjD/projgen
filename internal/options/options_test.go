package options

import (
	"strings"
	"testing"
)

func valid() Options {
	return Options{
		Name:         "order-service",
		Module:       "github.com/techpartners/order-service",
		Language:     "go",
		Framework:    "gin",
		Architecture: "clean",
		Database:     "postgresql",
		Extras:       []string{"docker", "docker-compose"},
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Options)
		wantErr string
	}{
		{"valid", func(*Options) {}, ""},
		{"no extras", func(o *Options) { o.Extras = nil }, ""},
		{"bad name", func(o *Options) { o.Name = "Order Service" }, "name"},
		{"empty module", func(o *Options) { o.Module = "" }, "module"},
		{"unknown language", func(o *Options) { o.Language = "cobol" }, "language"},
		{"framework of other language", func(o *Options) { o.Framework = "spring-boot" }, "not available for go"},
		{"unknown architecture", func(o *Options) { o.Architecture = "mvc" }, "architecture"},
		{"unknown database", func(o *Options) { o.Database = "oracle" }, "database"},
		{"unknown extra", func(o *Options) { o.Extras = []string{"jenkins"} }, "extra \"jenkins\""},
		{"duplicate extra", func(o *Options) { o.Extras = []string{"docker", "docker"} }, "listed twice"},
		{"compose without docker", func(o *Options) { o.Extras = []string{"docker-compose"} }, "requires docker"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := valid()
			tt.mutate(&o)
			err := o.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidate_ReportsAllProblems(t *testing.T) {
	o := Options{Name: "X", Language: "go", Framework: "gin", Architecture: "?", Database: "?"}
	err := o.Validate()
	for _, want := range []string{"name", "module", "architecture", "database"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got %v", want, err)
		}
	}
}
