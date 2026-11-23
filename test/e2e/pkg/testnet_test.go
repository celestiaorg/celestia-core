package e2e

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewTestnetFromManifestPerturbations(t *testing.T) {
	testCases := []struct {
		name    string
		perturb []string
		wantErr string
	}{
		{name: "latency", perturb: []string{"latency"}},
		{name: "partition", perturb: []string{"partition"}},
		{name: "all", perturb: []string{"disconnect", "kill", "pause", "restart", "latency", "partition"}},
		{name: "unknown", perturb: []string{"flood"}, wantErr: `invalid perturbation "flood"`},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			m := Manifest{Nodes: map[string]*ManifestNode{
				"validator01": {},
				"validator02": {Perturb: tc.perturb},
			}}
			ifd, err := NewDockerInfrastructureData(m)
			require.NoError(t, err)
			testnet, err := NewTestnetFromManifest(m, filepath.Join(t.TempDir(), "testnet.toml"), ifd)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Len(t, testnet.LookupNode("validator02").Perturbations, len(tc.perturb))
		})
	}
}
