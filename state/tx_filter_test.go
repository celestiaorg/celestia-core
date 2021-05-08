package state_test

// func TestTxFilter(t *testing.T) {
// 	genDoc := randomGenesisDoc()
// 	genDoc.ConsensusParams.Block.MaxBytes = 3000
// 	genDoc.ConsensusParams.Evidence.MaxBytes = 1500

// 	// Max size of Txs is much smaller than size of block,
// 	// since we need to account for commits and evidence.
// 	testCases := []struct {
// 		tx    types.Tx
// 		isErr bool
// 	}{
// 		{types.Tx(tmrand.Bytes(2139)), false},
// 		{types.Tx(tmrand.Bytes(2150)), true},
// 		{types.Tx(tmrand.Bytes(3000)), true},
// 	}

// 	for i, tc := range testCases {
// 		stateDB := memdb.NewDB()
// 		stateStore := sm.NewStore(stateDB)
// 		state, err := stateStore.LoadFromDBOrGenesisDoc(genDoc)
// 		require.NoError(t, err)

// 		f := sm.TxPreCheck(state)
// 		if tc.isErr {
// 			assert.NotNil(t, f(tc.tx), "#%v", i)
// 		} else {
// 			assert.Nil(t, f(tc.tx), "#%v", i)
// 		}
// 	}
// }
