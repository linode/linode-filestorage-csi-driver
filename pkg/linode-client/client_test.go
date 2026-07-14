package linodeclient

import (
	"strings"
	"testing"
)

func TestConfigUserAgent(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		want    string
		wantErr string
	}{
		{
			name: "defaults to driver user agent",
			config: Config{
				DriverVersion: "v1.2.3",
			},
			want: "LinodeFileStorageCSI/v1.2.3",
		},
		{
			name: "accepts custom user agent with version",
			config: Config{
				UserAgent:     "custom-agent v1.2.3",
				DriverVersion: "v1.2.3",
			},
			want: "custom-agent v1.2.3",
		},
		{
			name: "rejects missing driver version",
			config: Config{
				UserAgent: "custom-agent",
			},
			wantErr: "driver version cannot be empty",
		},
		{
			name: "rejects custom user agent without version",
			config: Config{
				UserAgent:     "custom-agent",
				DriverVersion: "v1.2.3",
			},
			wantErr: "must include driver version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.config.userAgent()
			if tt.wantErr != "" {
				if err == nil {
					t.Fatal("expected error")
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("userAgent() error = %q, want substring %q", err.Error(), tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("userAgent() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("userAgent() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewLinodeClientRequiresDriverVersion(t *testing.T) {
	_, err := NewLinodeClient(&Config{LinodeToken: "token"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "driver version cannot be empty") {
		t.Fatalf("NewLinodeClient() error = %q, want driver version error", err.Error())
	}
}
