package propagation

// TODO(rachid): fix test
// func TestCatchup(t *testing.T) {
// 	reactors, _ := testBlockPropReactors(3)
// 	reactor1 := reactors[0]
// 	reactor2 := reactors[1]
// 	reactor3 := reactors[2]

// 	// setting the proposal for height 8 round 1
// 	compactBlock := createCompactBlock(8, 1)
// 	reactor1.AddProposal(compactBlock)
// 	reactor2.AddProposal(compactBlock)
// 	reactor3.AddProposal(compactBlock)

// 	// setting the proposal for height 9 round 0
// 	compactBlock = createCompactBlock(9, 1)
// 	reactor1.AddProposal(compactBlock)
// 	reactor2.AddProposal(compactBlock)
// 	reactor3.AddProposal(compactBlock)

// 	// setting the proposal for height 10 round 0
// 	compactBlock = createCompactBlock(10, 0)
// 	reactor1.AddProposal(compactBlock)
// 	reactor2.AddProposal(compactBlock)
// 	reactor3.AddProposal(compactBlock)

// 	// setting the proposal for height 10 round 1
// 	compactBlock = createCompactBlock(10, 1)
// 	reactor1.AddProposal(compactBlock)
// 	reactor2.AddProposal(compactBlock)
// 	reactor3.AddProposal(compactBlock)

// 	// setting the first reactor current height and round
// 	reactor1.currentHeight = 8
// 	reactor1.currentRound = 0

// 	// handle the compact block
// 	reactor1.handleCompactBlock(compactBlock, reactor1.self)

// 	time.Sleep(200 * time.Millisecond)

// 	// check if reactor 1 sent wants to all the connected peers
// 	_, has := reactor2.getPeer(reactor1.self).GetWants(9, 1)
// 	require.True(t, has)

// 	_, has = reactor2.getPeer(reactor1.self).GetWants(10, 0)
// 	require.True(t, has)

// 	_, has = reactor3.getPeer(reactor1.self).GetWants(9, 1)
// 	require.True(t, has)

// 	_, has = reactor3.getPeer(reactor1.self).GetWants(10, 0)
// 	require.True(t, has)
// }

// func createCompactBlock(height int64, round int32) *proptypes.CompactBlock {
// 	return &proptypes.CompactBlock{
// 		BpHash:    cmtrand.Bytes(32),
// 		Signature: cmtrand.Bytes(64),
// 		LastLen:   0,
// 		Blobs: []proptypes.TxMetaData{
// 			{Hash: cmtrand.Bytes(32)},
// 			{Hash: cmtrand.Bytes(32)},
// 		},
// 		Proposal: types.Proposal{
// 			BlockID: types.BlockID{
// 				Hash:          nil,
// 				PartSetHeader: types.PartSetHeader{Total: 30},
// 			},
// 			Height: height,
// 			Round:  round,
// 		},
// 	}
// }
