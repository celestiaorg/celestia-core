package statesync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/config"
	cmtjson "github.com/cometbft/cometbft/libs/json"
	"github.com/cometbft/cometbft/p2p"
	ssproto "github.com/cometbft/cometbft/proto/tendermint/statesync"
)

// Only contact operator-configured servers, even when one returns a redirect.
var snapshotHTTPClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}}

type rpcOffer abci.Snapshot

func (s *rpcOffer) UnmarshalJSON(b []byte) error {
	return cmtjson.Unmarshal(b, (*abci.Snapshot)(s))
}

// An RPC provider participates in the same snapshot selection, verification,
// chunk retry and sender rejection as a P2P provider.
type rpcSnapshotPeer struct {
	httpClient  *http.Client
	ctx         context.Context
	id          p2p.ID
	base        string
	syncer      *syncer
	maxChunks   uint32
	discovering atomic.Bool
}

func newRPCSnapshotPeer(ctx context.Context, i int, server string, s *syncer, cfg config.StateSyncConfig) *rpcSnapshotPeer {
	base := strings.TrimRight(strings.TrimPrefix(server, "tcp://"), "/")
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	client := snapshotHTTPClient
	if path, unix := strings.CutPrefix(server, "unix://"); unix {
		base = "http://localhost"
		client = &http.Client{CheckRedirect: snapshotHTTPClient.CheckRedirect, Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, "unix", path)
			},
			IdleConnTimeout: 30 * time.Second,
		}}
	}
	return &rpcSnapshotPeer{httpClient: client, ctx: ctx, id: p2p.ID(fmt.Sprintf("rpc-%d", i)), base: base, syncer: s, maxChunks: cfg.MaxSnapshotChunks}
}

func (p *rpcSnapshotPeer) ID() p2p.ID { return p.id }

func (p *rpcSnapshotPeer) get(path string, maxBytes int, result any) error {
	ctx, cancel := context.WithTimeout(p.ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if err != nil {
		return err
	}
	if len(body) > maxBytes {
		return errors.New("snapshot RPC response too large")
	}
	var envelope struct{ Result json.RawMessage }
	if err := json.Unmarshal(body, &envelope); err != nil {
		return err
	}
	if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return errors.New("missing snapshot RPC result")
	}
	return json.Unmarshal(envelope.Result, result)
}

func (p *rpcSnapshotPeer) discover() {
	if !p.discovering.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer p.discovering.Store(false)
		// A fixed array prevents a malformed list from allocating an
		// unbounded number of offer objects before the per-peer quota.
		var resp struct{ Snapshots [recentSnapshots]*rpcOffer }
		if err := p.get("/snapshots", 2*snapshotMsgSize, &resp); err != nil {
			return
		}
		for _, s := range resp.Snapshots {
			if s == nil || s.Height > math.MaxInt64-2 {
				continue
			}
			msg := &ssproto.SnapshotsResponse{Height: s.Height, Format: s.Format, Hash: s.Hash, Chunks: s.Chunks, Metadata: s.Metadata}
			if msg.Size() > snapshotMsgSize || validateMsg(msg, p.maxChunks) != nil {
				continue
			}
			_, _ = p.syncer.AddSnapshot(p, &snapshot{Height: s.Height, Format: s.Format, Chunks: s.Chunks, Hash: s.Hash, Metadata: s.Metadata})
		}
	}()
}

// Send runs on one of the bounded chunk fetchers.
func (p *rpcSnapshotPeer) Send(e p2p.Envelope) bool {
	req, ok := e.Message.(*ssproto.ChunkRequest)
	if !ok || e.ChannelID != ChunkChannel {
		return false
	}
	path := fmt.Sprintf("/snapshot_chunk?height=%d&format=%d&index=%d", req.Height, req.Format, req.Index)
	var resp abci.ResponseLoadSnapshotChunk
	if err := p.get(path, 2*chunkMsgSize, &resp); err != nil || len(resp.Chunk) == 0 || len(resp.Chunk) > chunkMsgSize {
		return false
	}
	added, err := p.syncer.AddChunk(&chunk{Height: req.Height, Format: req.Format, Index: req.Index, Chunk: resp.Chunk, Sender: p.id})
	return added && err == nil
}
