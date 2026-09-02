package core

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cfg "github.com/cometbft/cometbft/config"
)

// TestHeavySem covers the process-wide heavy-request budget: it honors the
// configured capacity, disables on a negative value, and is memoized so every
// transport draws from one shared budget.
func TestHeavySem(t *testing.T) {
	t.Run("default capacity when unset", func(t *testing.T) {
		env := &Environment{Config: cfg.RPCConfig{}}
		sem := env.HeavySem()
		require.NotNil(t, sem)
		assert.Equal(t, cfg.DefaultMaxConcurrentHeavyRequests, cap(sem))
	})

	t.Run("honors configured capacity", func(t *testing.T) {
		env := &Environment{Config: cfg.RPCConfig{MaxConcurrentHeavyRequests: 5}}
		assert.Equal(t, 5, cap(env.HeavySem()))
	})

	t.Run("negative disables the limit", func(t *testing.T) {
		env := &Environment{Config: cfg.RPCConfig{MaxConcurrentHeavyRequests: -1}}
		assert.Nil(t, env.HeavySem())
	})

	t.Run("shared: memoized instance and a single budget", func(t *testing.T) {
		env := &Environment{Config: cfg.RPCConfig{MaxConcurrentHeavyRequests: 1}}
		sem := env.HeavySem()
		require.NotNil(t, sem)
		require.True(t, sem == env.HeavySem(), "HeavySem must return the same memoized channel")

		// Fill the one slot through this reference; a fresh call must see the
		// same, now-full budget, proving it is shared and not reset per call.
		sem <- struct{}{}
		select {
		case env.HeavySem() <- struct{}{}:
			t.Fatal("budget must be shared across calls, not reset per call")
		default:
		}
	})
}

func TestPaginationPage(t *testing.T) {
	cases := []struct {
		totalCount int
		perPage    int
		page       int
		newPage    int
		expErr     bool
	}{
		{0, 10, 1, 1, false},

		{0, 10, 0, 1, false},
		{0, 10, 1, 1, false},
		{0, 10, 2, 0, true},

		{5, 10, -1, 0, true},
		{5, 10, 0, 1, false},
		{5, 10, 1, 1, false},
		{5, 10, 2, 0, true},
		{5, 10, 2, 0, true},

		{5, 5, 1, 1, false},
		{5, 5, 2, 0, true},
		{5, 5, 3, 0, true},

		{5, 3, 2, 2, false},
		{5, 3, 3, 0, true},

		{5, 2, 2, 2, false},
		{5, 2, 3, 3, false},
		{5, 2, 4, 0, true},
	}

	for _, c := range cases {
		p, err := validatePage(&c.page, c.perPage, c.totalCount)
		if c.expErr {
			assert.Error(t, err)
			continue
		}

		assert.Equal(t, c.newPage, p, fmt.Sprintf("%v", c))
	}

	// nil case
	p, err := validatePage(nil, 1, 1)
	if assert.NoError(t, err) {
		assert.Equal(t, 1, p)
	}
}

func TestPaginationPerPage(t *testing.T) {
	cases := []struct {
		totalCount int
		perPage    int
		newPerPage int
	}{
		{5, 0, defaultPerPage},
		{5, 1, 1},
		{5, 2, 2},
		{5, defaultPerPage, defaultPerPage},
		{5, maxPerPage - 1, maxPerPage - 1},
		{5, maxPerPage, maxPerPage},
		{5, maxPerPage + 1, maxPerPage},
	}
	env := &Environment{}
	for _, c := range cases {
		p := env.validatePerPage(&c.perPage)
		assert.Equal(t, c.newPerPage, p, fmt.Sprintf("%v", c))
	}

	// nil case
	p := env.validatePerPage(nil)
	assert.Equal(t, defaultPerPage, p)
}

func TestValidateUnconfirmedTxsPerPage(t *testing.T) {
	env := &Environment{}

	t.Run("should return default if input is nil", func(t *testing.T) {
		got := env.validateUnconfirmedTxsPerPage(nil)
		assert.Equal(t, defaultPerPage, got)
	})

	type testCase struct {
		input int
		want  int
	}

	cases := []testCase{
		{-2, defaultPerPage},
		{-1, -1}, // -1 is now a valid input and means query all unconfirmed txs
		{0, defaultPerPage},
		{1, 1},
		{10, 10},
		{30, 30},
		{defaultPerPage, defaultPerPage},
		{maxPerPage - 1, maxPerPage - 1},
		{maxPerPage, maxPerPage},
		{maxPerPage + 1, maxPerPage},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("should return %d if input is %d", c.want, c.input), func(t *testing.T) {
			got := env.validateUnconfirmedTxsPerPage(&c.input)
			assert.Equal(t, c.want, got)
		})
	}
}
