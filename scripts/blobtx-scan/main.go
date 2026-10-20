// Command blobtx-scan compares celestia-core's legacy BlobTx decoder with
// go-square's on every transaction in a range of blocks fetched over RPC.
//
// A disagreement is a transaction for which the two decoders would unwrap to
// different bytes. Replacing core's decoder with go-square's would change that
// transaction's hash, and therefore the block's DataHash, for those heights.
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	squaretx "github.com/celestiaorg/go-square/v4/tx"

	cmtjson "github.com/cometbft/cometbft/libs/json"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	"github.com/cometbft/cometbft/types"
)

// Finding records one transaction where the decoders disagree.
type Finding struct {
	Height     int64  `json:"height"`
	Index      int    `json:"index"`
	AppVersion uint64 `json:"app_version"`
	Size       int    `json:"size"`
	LegacyOK   bool   `json:"legacy_ok"`
	SquareOK   bool   `json:"square_ok"`
	SquareErr  string `json:"square_err,omitempty"`
	// LegacyHash and SquareHash are Tx.Hash under each decoder.
	LegacyHash string `json:"legacy_hash"`
	SquareHash string `json:"square_hash"`
	// RawTx is the hex-encoded transaction, kept for small transactions only.
	RawTx string `json:"raw_tx,omitempty"`
}

// stats is the running summary of a scan.
type stats struct {
	mu           sync.Mutex
	Blocks       int64            `json:"blocks"`
	Txs          int64            `json:"txs"`
	LegacyBlobs  int64            `json:"legacy_blob_txs"`
	SquareBlobs  int64            `json:"square_blob_txs"`
	Findings     int64            `json:"findings"`
	ByAppVersion map[uint64]int64 `json:"blocks_by_app_version"`
	ByReason     map[string]int64 `json:"findings_by_reason"`
}

// config holds the scan's command-line settings.
type config struct {
	remote     string
	from, to   int64
	stride     int64
	workers    int
	outPath    string
	cpPath     string
	maxRawSize int
}

func main() {
	var c config
	flag.StringVar(&c.remote, "rpc", "http://127.0.0.1:26657", "CometBFT RPC address")
	flag.Int64Var(&c.from, "from", 0, "first height to scan (default: the node's earliest height)")
	flag.Int64Var(&c.to, "to", 0, "last height to scan (default: the node's latest height)")
	flag.Int64Var(&c.stride, "stride", 1, "scan every Nth height, for sampling")
	flag.IntVar(&c.workers, "workers", 16, "concurrent block fetches")
	flag.StringVar(&c.outPath, "out", "blobtx-scan-findings.jsonl", "file to append findings to")
	flag.StringVar(&c.cpPath, "checkpoint", "blobtx-scan.checkpoint", "file recording the scan's progress and totals; resumes from it if present")
	flag.IntVar(&c.maxRawSize, "max-raw-size", 4096, "include raw tx hex in findings up to this many bytes")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	cp, err := run(ctx, c)
	if err != nil {
		log.Fatal(err)
	}
	summary, _ := json.MarshalIndent(cp, "", "  ")
	fmt.Println(string(summary))
}

// run scans the configured range, resuming from the checkpoint if one exists,
// and returns the final checkpoint with totals for the whole range.
func run(ctx context.Context, c config) (*checkpoint, error) {
	if c.stride < 1 || c.workers < 1 {
		return nil, errors.New("stride and workers must be at least 1")
	}
	client := &rpcClient{remote: strings.TrimSuffix(c.remote, "/"), http: &http.Client{Timeout: 2 * time.Minute}}
	status := new(ctypes.ResultStatus)
	if err := client.get(ctx, "/status", status); err != nil {
		return nil, fmt.Errorf("status: %w", err)
	}
	start, end := c.from, c.to
	if start == 0 {
		start = status.SyncInfo.EarliestBlockHeight
	}
	if end == 0 {
		end = status.SyncInfo.LatestBlockHeight
	}
	cp := &checkpoint{Chain: status.NodeInfo.Network, From: start, Stride: c.stride, Done: start - c.stride,
		Stats: &stats{ByAppVersion: map[uint64]int64{}, ByReason: map[string]int64{}}}
	prev, err := readCheckpoint(c.cpPath)
	switch {
	case err != nil:
		return nil, err
	case prev != nil:
		if prev.Chain != cp.Chain || prev.From != cp.From || prev.Stride != cp.Stride {
			return nil, fmt.Errorf("checkpoint %s is for chain %s from %d with stride %d; delete it or pass another -checkpoint",
				c.cpPath, prev.Chain, prev.From, prev.Stride)
		}
		cp = prev
		log.Printf("resuming after checkpoint height %d", cp.Done)
	default:
		if fi, err := os.Stat(c.outPath); err == nil && fi.Size() > 0 {
			return nil, fmt.Errorf("findings file %s already has entries but there is no checkpoint; delete it or pass another -out", c.outPath)
		}
	}
	cp.To = end
	s := cp.Stats
	start = cp.Done + c.stride
	log.Printf("chain %s: scanning heights %d..%d (stride %d, earliest available %d)",
		status.NodeInfo.Network, start, end, c.stride, status.SyncInfo.EarliestBlockHeight)

	out, err := os.OpenFile(c.outPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open findings file: %w", err)
	}
	defer out.Close()
	w := bufio.NewWriter(out)

	resumedBlocks := s.Blocks
	const batch = 1000
	began := time.Now()
	for lo := start; lo <= end; lo += batch * c.stride {
		hi := min(lo+(batch-1)*c.stride, end)
		findings, err := scanBatch(ctx, client, lo, hi, c.stride, c.workers, c.maxRawSize, s)
		if err != nil {
			return nil, fmt.Errorf("scan %d..%d: %w", lo, hi, err)
		}
		for _, f := range findings {
			bz, _ := json.Marshal(f)
			fmt.Fprintln(w, string(bz))
		}
		if err := w.Flush(); err != nil {
			return nil, fmt.Errorf("write findings: %w", err)
		}
		cp.Done = hi
		if err := writeCheckpoint(c.cpPath, cp); err != nil {
			return nil, err
		}
		logProgress(s, start, hi, end, resumedBlocks, began)
	}
	return cp, nil
}

// scanBatch fetches heights lo..hi and returns their findings sorted by height.
func scanBatch(ctx context.Context, client *rpcClient, lo, hi, stride int64, workers, maxRawSize int, s *stats) ([]Finding, error) {
	heights := make(chan int64)
	var (
		mu       sync.Mutex
		findings []Finding
		firstErr error
		wg       sync.WaitGroup
	)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for h := range heights {
				block, err := fetchBlock(ctx, client, h)
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					continue
				}
				fs := scanBlock(block, maxRawSize, s)
				mu.Lock()
				findings = append(findings, fs...)
				mu.Unlock()
			}
		}()
	}
	for h := lo; h <= hi; h += stride {
		select {
		case heights <- h:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(heights)
	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if firstErr != nil {
		return nil, firstErr
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Height != findings[j].Height {
			return findings[i].Height < findings[j].Height
		}
		return findings[i].Index < findings[j].Index
	})
	return findings, nil
}

// fetchBlock loads one block, retrying transient RPC errors with backoff.
func fetchBlock(ctx context.Context, client *rpcClient, height int64) (*types.Block, error) {
	var err error
	for attempt := range 10 {
		res := new(ctypes.ResultBlock)
		e := client.get(ctx, "/block?height="+strconv.FormatInt(height, 10), res)
		if e == nil && res.Block != nil {
			return res.Block, nil
		}
		err = e
		if err == nil {
			err = errors.New("empty block response")
		}
		wait := min(time.Duration(1<<attempt)*time.Second, time.Minute)
		var rl *rateLimitError
		if errors.As(err, &rl) && rl.retryAfter > wait {
			wait = rl.retryAfter
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
	return nil, fmt.Errorf("height %d: %w", height, err)
}

// rpcClient issues JSON-RPC GET requests, which some public endpoints accept
// when they reject POSTs.
type rpcClient struct {
	remote string
	http   *http.Client
}

// rateLimitError is returned for HTTP 429 responses.
type rateLimitError struct{ retryAfter time.Duration }

func (e *rateLimitError) Error() string { return "rate limited (429)" }

func (c *rpcClient) get(ctx context.Context, path string, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.remote+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		secs, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		return &rateLimitError{retryAfter: time.Duration(secs) * time.Second}
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	var rpcResp rpctypes.RPCResponse
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	if rpcResp.Error != nil {
		return fmt.Errorf("GET %s: %w", path, rpcResp.Error)
	}
	return cmtjson.Unmarshal(rpcResp.Result, result)
}

// scanBlock compares the decoders on every tx in block.
func scanBlock(block *types.Block, maxRawSize int, s *stats) []Finding {
	var findings []Finding
	var legacyBlobs, squareBlobs int64
	reasons := map[string]int64{}
	for i, tx := range block.Txs {
		legacyInner, legacyOK := legacyUnwrap(tx)
		squareInner, squareOK, squareErr := squareUnwrap(tx)
		if legacyOK {
			legacyBlobs++
		}
		if squareOK {
			squareBlobs++
		}
		if legacyOK == squareOK && string(legacyInner) == string(squareInner) {
			continue
		}
		f := Finding{
			Height:     block.Height,
			Index:      i,
			AppVersion: block.Version.App,
			Size:       len(tx),
			LegacyOK:   legacyOK,
			SquareOK:   squareOK,
			LegacyHash: hash(legacyInner),
			SquareHash: hash(squareInner),
		}
		if squareErr != nil {
			f.SquareErr = squareErr.Error()
		}
		if len(tx) <= maxRawSize {
			f.RawTx = hex.EncodeToString(tx)
		}
		findings = append(findings, f)
		reasons[reason(f)]++
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.Blocks++
	s.Txs += int64(len(block.Txs))
	s.LegacyBlobs += legacyBlobs
	s.SquareBlobs += squareBlobs
	s.Findings += int64(len(findings))
	s.ByAppVersion[block.Version.App]++
	for r, n := range reasons {
		s.ByReason[r] += n
	}
	return findings
}

// legacyUnwrap applies celestia-core's legacy rules, as used by Tx.Hash and
// block execution in v0.42.x and earlier.
func legacyUnwrap(tx types.Tx) ([]byte, bool) {
	bTx, ok := types.UnmarshalBlobTx(tx)
	if !ok {
		return tx, false
	}
	return bTx.Tx, true
}

// squareUnwrap applies go-square's validating decoder.
func squareUnwrap(tx types.Tx) ([]byte, bool, error) {
	bTx, isBlob, err := squaretx.UnmarshalBlobTx(tx)
	if !isBlob {
		return tx, false, nil
	}
	if err != nil {
		return tx, false, err
	}
	return bTx.Tx, true, nil
}

func hash(bz []byte) string {
	sum := sha256.Sum256(bz)
	return hex.EncodeToString(sum[:])
}

// reason buckets a finding by go-square's error, without blob indexes.
func reason(f Finding) string {
	switch {
	case f.SquareErr != "":
		r := f.SquareErr
		if i := strings.Index(r, ": blob "); i >= 0 {
			r = r[:i]
		}
		return r
	case f.LegacyOK && f.SquareOK:
		return "different inner tx"
	default:
		return "go-square accepts, legacy rejects"
	}
}

// checkpoint records a scan's settings, the last fully scanned height and
// the totals so far, so that a resumed scan reports the whole range.
type checkpoint struct {
	Chain  string `json:"chain"`
	From   int64  `json:"from"`
	To     int64  `json:"to"`
	Stride int64  `json:"stride"`
	Done   int64  `json:"done"`
	Stats  *stats `json:"stats"`
}

// readCheckpoint returns nil if path does not exist.
func readCheckpoint(path string) (*checkpoint, error) {
	bz, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read checkpoint: %w", err)
	}
	cp := new(checkpoint)
	if err := json.Unmarshal(bz, cp); err != nil || cp.Stats == nil {
		return nil, fmt.Errorf("read checkpoint %s: invalid contents", path)
	}
	return cp, nil
}

func writeCheckpoint(path string, cp *checkpoint) error {
	cp.Stats.mu.Lock()
	bz, err := json.Marshal(cp)
	cp.Stats.mu.Unlock()
	if err != nil {
		return fmt.Errorf("write checkpoint: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, bz, 0o644); err != nil {
		return fmt.Errorf("write checkpoint: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("write checkpoint: %w", err)
	}
	return nil
}

func logProgress(s *stats, start, done, end, resumedBlocks int64, began time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	elapsed := time.Since(began).Seconds()
	rate := float64(s.Blocks-resumedBlocks) / elapsed
	var eta time.Duration
	if done < end && rate > 0 {
		frac := float64(done-start+1) / float64(end-start+1)
		eta = time.Duration((elapsed/frac - elapsed) * float64(time.Second)).Round(time.Second)
	}
	log.Printf("height %d/%d: %d blocks, %d txs, %d legacy blob txs, %d go-square blob txs, %d findings (%.0f blocks/s, eta %s)",
		done, end, s.Blocks, s.Txs, s.LegacyBlobs, s.SquareBlobs, s.Findings, rate, eta)
}
