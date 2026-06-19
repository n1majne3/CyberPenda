package runner_test

import (
	"testing"

	"pentest/internal/runner"
)

func TestRewriteLoopbackTargets(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		sandbox bool
		want    string
	}{
		{
			name:    "host runner is a no-op even with loopback url",
			in:      "http://127.0.0.1:3000/x",
			sandbox: false,
			want:    "http://127.0.0.1:3000/x",
		},
		{
			name:    "sandbox rewrites 127.0.0.1 url with path",
			in:      "http://127.0.0.1:3000/x",
			sandbox: true,
			want:    "http://host.docker.internal:3000/x",
		},
		{
			name:    "sandbox rewrites https localhost url keeping port",
			in:      "https://localhost:8443",
			sandbox: true,
			want:    "https://host.docker.internal:8443",
		},
		{
			name:    "sandbox rewrites bare host port token 127.0.0.1",
			in:      "127.0.0.1:3000",
			sandbox: true,
			want:    "host.docker.internal:3000",
		},
		{
			name:    "sandbox rewrites bare localhost port token",
			in:      "localhost:8080",
			sandbox: true,
			want:    "host.docker.internal:8080",
		},
		{
			name:    "sandbox rewrites multiple targets in one goal",
			in:      "curl http://127.0.0.1:3000 and localhost:8080",
			sandbox: true,
			want:    "curl http://host.docker.internal:3000 and host.docker.internal:8080",
		},
		{
			name:    "sandbox leaves external domain untouched",
			in:      "http://example.com:3000",
			sandbox: true,
			want:    "http://example.com:3000",
		},
		{
			name:    "sandbox leaves private ip untouched",
			in:      "http://192.168.1.5:3000",
			sandbox: true,
			want:    "http://192.168.1.5:3000",
		},
		{
			name:    "sandbox does not rewrite localhost-looking subdomain",
			in:      "http://localhost-evil.com:3000",
			sandbox: true,
			want:    "http://localhost-evil.com:3000",
		},
		{
			name:    "sandbox does not double rewrite host.docker.internal url",
			in:      "http://host.docker.internal:3000",
			sandbox: true,
			want:    "http://host.docker.internal:3000",
		},
		{
			name:    "sandbox leaves plain text without target untouched",
			in:      "Recon the web app and find the score board",
			sandbox: true,
			want:    "Recon the web app and find the score board",
		},
		{
			name:    "sandbox rewrites localhost without port but not as substring",
			in:      "connect to localhost now",
			sandbox: true,
			want:    "connect to host.docker.internal now",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := runner.RewriteLoopbackTargets(tc.in, tc.sandbox)
			if got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}
