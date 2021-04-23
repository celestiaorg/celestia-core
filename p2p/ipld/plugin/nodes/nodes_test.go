package nodes

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"

	shell "github.com/ipfs/go-ipfs-api"
	"github.com/ipfs/go-verifcid"
	mh "github.com/multiformats/go-multihash"

	"github.com/lazyledger/nmt"
	"github.com/lazyledger/rsmt2d"
)

func TestMultihasherIsRegistered(t *testing.T) {
	if _, ok := mh.Codes[Sha256Namespace8Flagged]; !ok {
		t.Fatalf("code not registered in multihash.Codes: %X", Sha256Namespace8Flagged)
	}
}

func TestVerifCidAllowsCustomMultihasher(t *testing.T) {
	if ok := verifcid.IsGoodHash(Sha256Namespace8Flagged); !ok {
		t.Fatalf("code not allowed by verifcid verifcid.IsGoodHash(%X): %v", Sha256Namespace8Flagged, ok)
	}
}

func TestDataSquareRowOrColumnRawInputParserCidEqNmtRoot(t *testing.T) {
	tests := []struct {
		name     string
		leafData [][]byte
	}{
		{"16 leaves", generateRandNamespacedRawData(16, namespaceSize, shareSize)},
		{"32 leaves", generateRandNamespacedRawData(32, namespaceSize, shareSize)},
		{"extended row", generateExtendedRow(t)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := createByteBufFromRawData(t, tt.leafData)

			gotNodes, err := DataSquareRowOrColumnRawInputParser(buf, 0, 0)
			if err != nil {
				t.Errorf("DataSquareRowOrColumnRawInputParser() unexpected error = %v", err)
				return
			}

			multiHashOverhead := 4
			lastNodeCid := gotNodes[len(gotNodes)-1].Cid()
			gotHash, wantHash := lastNodeCid.Hash(), nmt.Sha256Namespace8FlaggedLeaf(tt.leafData[0])
			if !bytes.Equal(gotHash[multiHashOverhead:], wantHash) {
				t.Errorf("first node's hash does not match the Cid\ngot: %v\nwant: %v", gotHash[multiHashOverhead:], wantHash)
			}
			nodePrefixOffset := 1 // leaf / inner node prefix is one byte
			lastLeafNodeData := gotNodes[len(gotNodes)-1].RawData()
			if gotData, wantData := lastLeafNodeData[nodePrefixOffset:], tt.leafData[0]; !bytes.Equal(gotData, wantData) {
				t.Errorf("first node's data does not match the leaf's data\ngot: %v\nwant: %v", gotData, wantData)
			}
		})
	}
}

func TestNodeCollector(t *testing.T) {
	tests := []struct {
		name     string
		leafData [][]byte
	}{
		{"16 leaves", generateRandNamespacedRawData(16, namespaceSize, shareSize)},
		{"32 leaves", generateRandNamespacedRawData(32, namespaceSize, shareSize)},
		{"extended row", generateExtendedRow(t)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			collector := newNodeCollector()
			n := nmt.New(sha256.New(), nmt.NamespaceIDSize(namespaceSize), nmt.NodeVisitor(collector.visit))

			for _, share := range tt.leafData {
				err := n.Push(share)
				if err != nil {
					t.Errorf("nmt.Push() unexpected error = %v", err)
					return
				}
			}

			rootDigest := n.Root()

			gotNodes := collector.ipldNodes()

			rootNodeCid := gotNodes[0].Cid()
			multiHashOverhead := 4
			lastNodeHash := rootNodeCid.Hash()
			if got, want := lastNodeHash[multiHashOverhead:], rootDigest.Bytes(); !bytes.Equal(got, want) {
				t.Errorf("hashes don't match\ngot: %v\nwant: %v", got, want)
			}

			if mustCidFromNamespacedSha256(rootDigest.Bytes()).String() != rootNodeCid.String() {
				t.Error("root cid nod and hash not identical")
			}

			lastNodeCid := gotNodes[len(gotNodes)-1].Cid()
			gotHash, wantHash := lastNodeCid.Hash(), nmt.Sha256Namespace8FlaggedLeaf(tt.leafData[0])
			if !bytes.Equal(gotHash[multiHashOverhead:], wantHash) {
				t.Errorf("first node's hash does not match the Cid\ngot: %v\nwant: %v", gotHash[multiHashOverhead:], wantHash)
			}
			nodePrefixOffset := 1 // leaf / inner node prefix is one byte
			lastLeafNodeData := gotNodes[len(gotNodes)-1].RawData()
			if gotData, wantData := lastLeafNodeData[nodePrefixOffset:], tt.leafData[0]; !bytes.Equal(gotData, wantData) {
				t.Errorf("first node's data does not match the leaf's data\ngot: %v\nwant: %v", gotData, wantData)
			}

			// ensure that every leaf was collected
			hasMap := make(map[string]bool)
			for _, node := range gotNodes {
				hasMap[node.Cid().String()] = true
			}
			hasher := nmt.NewNmtHasher(sha256.New(), namespaceSize, true)
			for _, leaf := range tt.leafData {
				leafCid := mustCidFromNamespacedSha256(hasher.HashLeaf(leaf))
				_, has := hasMap[leafCid.String()]
				if !has {
					t.Errorf("leaf CID not found in collected nodes. missing: %s", leafCid.String())
				}
			}
		})
	}
}

func TestDagPutWithPlugin(t *testing.T) {
	t.Skip("Requires running ipfs daemon (serving the HTTP Api) with the plugin compiled and installed")

	t.Log("Warning: running this test writes to your local IPFS block store!")

	const numLeaves = 32
	data := generateRandNamespacedRawData(numLeaves, namespaceSize, shareSize)
	buf := createByteBufFromRawData(t, data)
	printFirst := 10
	t.Logf("first leaf, nid: %x, data: %x...", data[0][:namespaceSize], data[0][namespaceSize:namespaceSize+printFirst])
	n := nmt.New(sha256.New())
	for _, share := range data {
		err := n.Push(share)
		if err != nil {
			t.Errorf("nmt.Push() unexpected error = %v", err)
			return
		}
	}

	sh := shell.NewLocalShell()
	cid, err := sh.DagPut(buf, "raw", DagParserFormatName)
	if err != nil {
		t.Fatalf("DagPut() failed: %v", err)
	}
	// convert Nmt tree root to CID and verify it matches the CID returned by DagPut
	treeRootBytes := n.Root().Bytes()
	nmtCid, err := CidFromNamespacedSha256(treeRootBytes)
	if err != nil {
		t.Fatalf("cidFromNamespacedSha256() failed: %v", err)
	}
	if nmtCid.String() != cid {
		t.Errorf("CIDs from Nmt and plugin do not match: got %v, want: %v", cid, nmtCid.String())
	}
	// print out cid s.t. it can be used on the commandline
	t.Logf("Stored with cid: %v\n", cid)

	// DagGet leaf by leaf:
	for i, wantShare := range data {
		gotLeaf := &nmtLeafNode{}
		path := leafIdxToPath(cid, i)
		if err := sh.DagGet(path, gotLeaf); err != nil {
			t.Errorf("DagGet(%s) failed: %v", path, err)
		}
		if gotShare := gotLeaf.Data; !bytes.Equal(gotShare, wantShare) {
			t.Errorf("DagGet returned different data than pushed, got: %v, want: %v", gotShare, wantShare)
		}
	}
}

func generateExtendedRow(t *testing.T) [][]byte {
	origData := generateRandNamespacedRawData(16, namespaceSize, shareSize)
	origDataWithoutNamespaces := make([][]byte, 16)
	for i, share := range origData {
		origDataWithoutNamespaces[i] = share[namespaceSize:]
	}

	extendedData, err := rsmt2d.ComputeExtendedDataSquare(
		origDataWithoutNamespaces,
		rsmt2d.NewRSGF8Codec(),
		rsmt2d.NewDefaultTree,
	)
	if err != nil {
		t.Fatalf("rsmt2d.Encode(): %v", err)
		return nil
	}
	extendedRow := extendedData.Row(0)
	for i, rowCell := range extendedRow {
		if i < len(origData)/4 {
			nid := origData[i][:namespaceSize]
			extendedRow[i] = append(nid, rowCell...)
		} else {
			maxNid := bytes.Repeat([]byte{0xFF}, namespaceSize)
			extendedRow[i] = append(maxNid, rowCell...)
		}
	}
	return extendedRow
}

//nolint:unused
// when it actually used in the code ;)
func leafIdxToPath(cid string, idx int) string {
	// currently this fmt directive assumes 32 leaves:
	bin := fmt.Sprintf("%05b", idx)
	path := strings.Join(strings.Split(bin, ""), "/")
	return cid + "/" + path
}

func createByteBufFromRawData(t *testing.T, leafData [][]byte) *bytes.Buffer {
	buf := bytes.NewBuffer(make([]byte, 0))
	for _, share := range leafData {
		_, err := buf.Write(share)
		if err != nil {
			t.Fatalf("buf.Write() unexpected error = %v", err)
			return nil
		}
	}
	return buf
}

func generateRandNamespacedRawData(total int, nidSize int, leafSize int) [][]byte {
	data := make([][]byte, total)
	for i := 0; i < total; i++ {
		nid := make([]byte, nidSize)
		rand.Read(nid)
		data[i] = nid
	}
	sortByteArrays(data)
	for i := 0; i < total; i++ {
		d := make([]byte, leafSize)
		rand.Read(d)
		data[i] = append(data[i], d...)
	}

	return data
}

func sortByteArrays(src [][]byte) {
	sort.Slice(src, func(i, j int) bool { return bytes.Compare(src[i], src[j]) < 0 })
}
