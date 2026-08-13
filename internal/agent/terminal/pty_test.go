package terminal

import (
	"errors"
	"testing"
)

func TestClassifySu(t *testing.T) {
	tests := []struct {
		name     string
		prompted bool
		authFail bool
		code     int
		out      string
		want     error
	}{
		{"success", true, false, 0, "Password:\nok\n", nil},
		{"bad password", true, true, 1, "Password:\nsu: Authentication failure\n", ErrAuthFailed},
		{"bad password text only", true, false, 1, "Password:\nsu: Authentication failure\n", ErrAuthFailed},
		{"incorrect password", true, false, 1, "su: incorrect password\n", ErrAuthFailed},
		{"bad user", false, false, 1, "su: user nosuchuser does not exist\n", ErrInvalidUser},
		{"unknown user", false, false, 1, "su: unknown user xyz\n", ErrInvalidUser},
		{"generic failure", false, false, 1, "Usage: su [options]\n", ErrSuFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifySu(tt.prompted, tt.authFail, tt.code, tt.out)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("classifySu = %v, want nil", got)
				}
				return
			}
			if !errors.Is(got, tt.want) && got == nil {
				t.Fatalf("classifySu = nil, want %v", tt.want)
			}
		})
	}
}

func TestIsAuthFailure(t *testing.T) {
	cases := map[string]bool{
		"su: Authentication failure":       true,
		"Password:":                        false,
		"su: user foo does not exist":      false,
		"password for 2kfi: ":              false,
		"Too many authentication failures": true,
		"Authentication failure":           true,
	}
	for in, want := range cases {
		if got := isAuthFailure(in); got != want {
			t.Errorf("isAuthFailure(%q) = %v, want %v", in, got, want)
		}
	}
}
