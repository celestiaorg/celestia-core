package types

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/cometbft/cometbft/crypto"
	cmtbytes "github.com/cometbft/cometbft/libs/bytes"
	cmtjson "github.com/cometbft/cometbft/libs/json"
	cmtos "github.com/cometbft/cometbft/libs/os"
	cmttime "github.com/cometbft/cometbft/types/time"
)

const (
	// MaxChainIDLen is a maximum length of the chain ID.
	MaxChainIDLen = 50
)

//------------------------------------------------------------
// core types for a genesis definition
// NOTE: any changes to the genesis definition should
// be reflected in the documentation:
// docs/core/using-cometbft.md

// GenesisValidator is an initial validator.
type GenesisValidator struct {
	Address Address       `json:"address"`
	PubKey  crypto.PubKey `json:"pub_key"`
	Power   int64         `json:"power"`
	Name    string        `json:"name"`
}

// GenesisDoc defines the initial conditions for a CometBFT blockchain, in particular its validator set.
type GenesisDoc struct {
	GenesisTime     time.Time          `json:"genesis_time"`
	ChainID         string             `json:"chain_id"`
	InitialHeight   int64              `json:"initial_height"`
	ConsensusParams *ConsensusParams   `json:"consensus_params,omitempty"`
	Validators      []GenesisValidator `json:"validators,omitempty"`
	AppHash         cmtbytes.HexBytes  `json:"app_hash"`
	AppState        json.RawMessage    `json:"app_state,omitempty"`
}

// SaveAs is a utility method for saving GenensisDoc as a JSON file.
func (genDoc *GenesisDoc) SaveAs(file string) error {
	genDocBytes, err := cmtjson.MarshalIndent(genDoc, "", "  ")
	if err != nil {
		return err
	}
	return cmtos.WriteFile(file, genDocBytes, 0644)
}

// ValidatorHash returns the hash of the validator set contained in the GenesisDoc
func (genDoc *GenesisDoc) ValidatorHash() []byte {
	vals := make([]*Validator, len(genDoc.Validators))
	for i, v := range genDoc.Validators {
		vals[i] = NewValidator(v.PubKey, v.Power)
	}
	vset := NewValidatorSet(vals)
	return vset.Hash()
}

// ValidateAndComplete checks that all necessary fields are present
// and fills in defaults for optional fields left empty
func (genDoc *GenesisDoc) ValidateAndComplete() error {
	if genDoc.ChainID == "" {
		return errors.New("genesis doc must include non-empty chain_id")
	}
	if len(genDoc.ChainID) > MaxChainIDLen {
		return fmt.Errorf("chain_id in genesis doc is too long (max: %d)", MaxChainIDLen)
	}
	if genDoc.InitialHeight < 0 {
		return fmt.Errorf("initial_height cannot be negative (got %v)", genDoc.InitialHeight)
	}
	if genDoc.InitialHeight == 0 {
		genDoc.InitialHeight = 1
	}

	if genDoc.ConsensusParams == nil {
		genDoc.ConsensusParams = DefaultConsensusParams()
	} else if err := genDoc.ConsensusParams.ValidateBasic(); err != nil {
		return err
	}

	for i, v := range genDoc.Validators {
		if v.Power == 0 {
			return fmt.Errorf("the genesis file cannot contain validators with no voting power: %v", v)
		}
		if len(v.Address) > 0 && !bytes.Equal(v.PubKey.Address(), v.Address) {
			return fmt.Errorf("incorrect address for validator %v in the genesis file, should be %v", v, v.PubKey.Address())
		}
		if len(v.Address) == 0 {
			genDoc.Validators[i].Address = v.PubKey.Address()
		}
	}

	if genDoc.GenesisTime.IsZero() {
		genDoc.GenesisTime = cmttime.Now()
	}

	return nil
}

//------------------------------------------------------------
// Make genesis state from file

// GenesisDocFromJSON unmarshalls JSON data into a GenesisDoc.
func GenesisDocFromJSON(jsonBlob []byte) (*GenesisDoc, error) {
	genDoc := GenesisDoc{}
	err := cmtjson.Unmarshal(jsonBlob, &genDoc)
	if err != nil {
		return nil, err
	}

	// If the genesis version is v1, we need to override the app version in the
	// genesis doc with the the app version from the actual genesis file because
	// cmtjson didn't unmarshal it correctly.
	genesisVersion, err := getGenesisVersion(jsonBlob)
	if err != nil {
		return nil, err
	}
	if genesisVersion == GenesisVersion1 {
		var v1 genesisDocv1
		if err := json.Unmarshal(jsonBlob, &v1); err != nil {
			return nil, fmt.Errorf("failed to unmarshal genesis doc v1: %w", err)
		}
		if v1.ChainID == MochaChainID {
			genDoc.ConsensusParams.Version.App = 1
		} else {
			appVersion, err := strconv.ParseUint(v1.ConsensusParams.Version.AppVersion, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("failed to parse app version: %w", err)
			}
			// Override the version with the one from the genesis doc v1
			fmt.Printf("Overriding genDoc.ConsensusParams.Version.App to %d\n", appVersion)
			genDoc.ConsensusParams.Version.App = appVersion
		}
	}

	if err := genDoc.ValidateAndComplete(); err != nil {
		return nil, err
	}
	return &genDoc, nil
}

// GenesisDocFromFile reads JSON data from a file and unmarshalls it into a GenesisDoc.
func GenesisDocFromFile(genDocFile string) (*GenesisDoc, error) {
	jsonBlob, err := os.ReadFile(genDocFile)
	if err != nil {
		return nil, fmt.Errorf("couldn't read GenesisDoc file: %w", err)
	}
	genDoc, err := GenesisDocFromJSON(jsonBlob)
	if err != nil {
		return nil, fmt.Errorf("error reading GenesisDoc at %s: %w", genDocFile, err)
	}
	return genDoc, nil
}

const (
	MochaChainID = "mocha-4"
)

type GenesisVersion int

const (
	GenesisVersion1 GenesisVersion = iota
	GenesisVersion2
)

type genesisDocv1 struct {
	ChainID         string `json:"chain_id"`
	ConsensusParams struct {
		Version struct {
			AppVersion string `json:"app_version"`
		} `json:"version"`
	} `json:"consensus_params"`
}

type genesisDocv2 struct {
	ChainID   string `json:"chain_id"`
	Consensus struct {
		Params struct {
			Version struct {
				App string `json:"app"`
			} `json:"version"`
		} `json:"params"`
	} `json:"consensus"`
}

// getGenesisVersion returns the genesis version for the given genDoc.
func getGenesisVersion(genDoc []byte) (GenesisVersion, error) {
	var v1 genesisDocv1
	if err := json.Unmarshal(genDoc, &v1); err == nil {
		// The mocha genesis file contains an empty version field so we need to
		// special case it.
		if v1.ChainID == MochaChainID {
			return GenesisVersion1, nil
		}
		if v1.ConsensusParams.Version.AppVersion != "" {
			return GenesisVersion1, nil
		}
	}

	var v2 genesisDocv2
	if err := json.Unmarshal(genDoc, &v2); err == nil {
		if v2.Consensus.Params.Version.App != "" {
			return GenesisVersion2, nil
		}
	}

	return 0, errors.New("failed to determine genesis version")
}
