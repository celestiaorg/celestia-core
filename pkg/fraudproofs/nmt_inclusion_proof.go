package fraudproofs

import (
	nmtproto "github.com/celestiaorg/celestia-core/proto/tendermint/nmt"
	"github.com/celestiaorg/nmt"
)

func NmtInclusionProofFromProto(pproof nmtproto.Proof) nmt.Proof {
	return nmt.NewInclusionProof(int(pproof.Start), int(pproof.End), pproof.Nodes, true)
}

func NmtInclusionProofToProto(proof nmt.Proof) nmtproto.Proof {
	return nmtproto.Proof{
		Start:    int32(proof.Start()),
		End:      int32(proof.End()),
		Nodes:    proof.Nodes(),
		LeafHash: proof.LeafHash(),
	}
}
