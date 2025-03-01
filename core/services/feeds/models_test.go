package feeds

import (
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/guregu/null.v4"
)

func Test_NewChainType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		give    string
		want    ChainType
		wantErr error
	}{
		{
			name: "EVM Chain Type",
			give: "EVM",
			want: ChainTypeEVM,
		},
		{
			name:    "Invalid Chain Type",
			give:    "",
			want:    ChainTypeUnknown,
			wantErr: errors.New("invalid chain type"),
		},
	}

	for _, tt := range tests {
		tt := tt

		t.Run(tt.name, func(t *testing.T) {
			ct, err := NewChainType(tt.give)

			assert.Equal(t, tt.want, ct)
			if tt.wantErr != nil {
				assert.EqualError(t, err, tt.wantErr.Error())
			}
		})
	}
}

func Test_FluxMonitorConfig_Value(t *testing.T) {
	t.Parallel()

	cfg := FluxMonitorConfig{Enabled: true}
	want := `{"enabled":true}`

	val, err := cfg.Value()
	require.NoError(t, err)

	actual, ok := val.([]byte)
	require.True(t, ok)

	assert.Equal(t, want, string(actual))
}

func Test_FluxMonitorConfig_Scan(t *testing.T) {
	t.Parallel()

	var (
		give = `{"enabled":true}`
		want = FluxMonitorConfig{Enabled: true}
	)

	var actual FluxMonitorConfig
	err := actual.Scan([]byte(give))
	require.NoError(t, err)

	assert.Equal(t, want, actual)
}

func Test_OCR1Config_Value(t *testing.T) {
	t.Parallel()

	var (
		give = OCR1Config{
			Enabled:     true,
			IsBootstrap: false,
			Multiaddr:   null.StringFrom("multiaddr"),
			P2PPeerID:   null.StringFrom("peerid"),
			KeyBundleID: null.StringFrom("ocrkeyid"),
		}
		want = `{"enabled":true,"is_bootstrap":false,"multiaddr":"multiaddr","p2p_peer_id":"peerid","key_bundle_id":"ocrkeyid"}`
	)

	val, err := give.Value()
	require.NoError(t, err)

	actual, ok := val.([]byte)
	require.True(t, ok)

	assert.Equal(t, want, string(actual))
}

func Test_OCR1Config_Scan(t *testing.T) {
	t.Parallel()

	var (
		give = `{"enabled":true,"is_bootstrap":false,"multiaddr":"multiaddr","p2p_peer_id":"peerid","key_bundle_id":"ocrkeyid"}`
		want = OCR1Config{
			Enabled:     true,
			IsBootstrap: false,
			Multiaddr:   null.StringFrom("multiaddr"),
			P2PPeerID:   null.StringFrom("peerid"),
			KeyBundleID: null.StringFrom("ocrkeyid"),
		}
	)

	var actual OCR1Config
	err := actual.Scan([]byte(give))
	require.NoError(t, err)

	assert.Equal(t, want, actual)
}

func Test_OCR2Config_Value(t *testing.T) {
	t.Parallel()

	var (
		give = OCR2Config{
			Enabled:     true,
			IsBootstrap: false,
			Multiaddr:   null.StringFrom("multiaddr"),
			P2PPeerID:   null.StringFrom("peerid"),
			KeyBundleID: null.StringFrom("ocrkeyid"),
		}
		want = `{"enabled":true,"is_bootstrap":false,"multiaddr":"multiaddr","p2p_peer_id":"peerid","key_bundle_id":"ocrkeyid"}`
	)

	val, err := give.Value()
	require.NoError(t, err)

	actual, ok := val.([]byte)
	require.True(t, ok)

	assert.Equal(t, want, string(actual))
}

func Test_OCR2Config_Scan(t *testing.T) {
	t.Parallel()

	var (
		give = `{"enabled":true,"is_bootstrap":false,"multiaddr":"multiaddr","p2p_peer_id":"peerid","key_bundle_id":"ocrkeyid"}`
		want = OCR2Config{
			Enabled:     true,
			IsBootstrap: false,
			Multiaddr:   null.StringFrom("multiaddr"),
			P2PPeerID:   null.StringFrom("peerid"),
			KeyBundleID: null.StringFrom("ocrkeyid"),
		}
	)

	var actual OCR2Config
	err := actual.Scan([]byte(give))
	require.NoError(t, err)

	assert.Equal(t, want, actual)
}

func Test_JobProposal_CanEditDefinition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status SpecStatus
		want   bool
	}{
		{
			name:   "pending",
			status: SpecStatusPending,
			want:   true,
		},
		{
			name:   "cancelled",
			status: SpecStatusCancelled,
			want:   true,
		},
		{
			name:   "approved",
			status: SpecStatusApproved,
			want:   false,
		},
		{
			name:   "rejected",
			status: SpecStatusRejected,
			want:   false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			jp := &JobProposalSpec{Status: tc.status}
			assert.Equal(t, tc.want, jp.CanEditDefinition())
		})
	}
}
