package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTLSFingerprintProfileValidateRejectsMalformedValues(t *testing.T) {
	tests := []struct {
		name    string
		profile *TLSFingerprintProfile
		field   string
	}{
		{
			name:    "duplicate cipher suite",
			profile: &TLSFingerprintProfile{Name: "test", CipherSuites: []uint16{4865, 4865}},
			field:   "cipher_suites",
		},
		{
			name:    "zero curve",
			profile: &TLSFingerprintProfile{Name: "test", Curves: []uint16{0}},
			field:   "curves",
		},
		{
			name:    "duplicate ALPN",
			profile: &TLSFingerprintProfile{Name: "test", ALPNProtocols: []string{"h2", "h2"}},
			field:   "alpn_protocols",
		},
		{
			name:    "invalid ALPN control character",
			profile: &TLSFingerprintProfile{Name: "test", ALPNProtocols: []string{"h2\n"}},
			field:   "alpn_protocols",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.profile.Validate()
			require.Error(t, err)
			require.Equal(t, tt.field, err.(*ValidationError).Field)
		})
	}
}

func TestTLSFingerprintProfileValidateTrimsNameAndAcceptsEmptyDefaults(t *testing.T) {
	profile := &TLSFingerprintProfile{Name: "  bun-default  "}
	require.NoError(t, profile.Validate())
	require.Equal(t, "bun-default", profile.Name)
}
