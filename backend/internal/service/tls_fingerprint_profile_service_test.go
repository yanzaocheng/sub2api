package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/stretchr/testify/require"
)

func TestResolveTLSProfileStrictFailsWhenExplicitProfileIsMissing(t *testing.T) {
	svc := &TLSFingerprintProfileService{localCache: map[int64]*model.TLSFingerprintProfile{}}
	account := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"enable_tls_fingerprint":     true,
			"tls_fingerprint_profile_id": int64(42),
		},
	}

	profile, err := svc.ResolveTLSProfileStrict(account)
	require.Error(t, err)
	require.Nil(t, profile)
	require.Contains(t, err.Error(), "profile 42")
}

func TestResolveTLSProfileStrictUsesBuiltInDefaultWhenUnbound(t *testing.T) {
	svc := &TLSFingerprintProfileService{localCache: map[int64]*model.TLSFingerprintProfile{}}
	account := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{"enable_tls_fingerprint": true},
	}

	profile, err := svc.ResolveTLSProfileStrict(account)
	require.NoError(t, err)
	require.NotNil(t, profile)
	require.Contains(t, profile.Name, "Built-in Default")
}
