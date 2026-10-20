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

func main() {
	var (
		remote     = flag.String("rpc", "http://127.0.0.1:26657", "CometBFT RPC address")
		from       = flag.Int64("from", 0, "first height to scan (default: the node's earliest height)")
		to         = flag.Int64("to", 0, "last height to scan (default: the node's latest height)")
		stride     = flag.Int64("stride", 1, "scan every Nth height, for sampling")
		workers    = flag.Int("workers", 16, "concurrent block fetches")
		outPath    = flag.String("out", "blobtx-scan-findings.jsonl", "file to append findings to")
		checkpoint = flag.String("checkpoint", "blobtx-scan.checkpoint", "file recording the last fully scanned height; resumes from it if present")
		maxRawSize = flag.Int("max-raw-size", 4096, "include raw tx hex in findings up to this many bytes")
	)
	flag.Parse()
	if *stride < 1 || *workers < 1 {
		log.Fatal("stride and workers must be at least 1")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	client := &rpcClient{remote: strings.TrimSuffix(*remote, "/"), http: &http.Client{Timeout: 2 * time.Minute}}
	status := new(ctypes.ResultStatus)
	if err := client.get(ctx, "/status", status); err != nil {
		log.Fatalf("status: %v", err)
	}
	start, end := *from, *to
	if start == 0 {
		start = status.SyncInfo.EarliestBlockHeight
	}
	if end == 0 {
		end = status.SyncInfo.LatestBlockHeight
	}
	if done, ok := readCheckpoint(*checkpoint); ok && done >= start {
		log.Printf("resuming after checkpoint height %d", done)
		start = done + *stride
	}
	log.Printf("chain %s: scanning heights %d..%d (stride %d, earliest available %d)",
		status.NodeInfo.Network, start, end, *stride, status.SyncInfo.EarliestBlockHeight)

	out, err := os.OpenFile(*outPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatalf("open findings file: %v", err)
	}
	defer out.Close()
	w := bufio.NewWriter(out)
	defer w.Flush()

	s := &stats{ByAppVersion: map[uint64]int64{}, ByReason: map[string]int64{}}
	const batch = 1000
	began := time.Now()
	for lo := start; lo <= end; lo += batch * *stride {
		hi := min(lo+(batch-1)*(*stride), end)
		findings, err := scanBatch(ctx, client, lo, hi, *stride, *workers, *maxRawSize, s)
		if err != nil {
			log.Fatalf("scan %d..%d: %v", lo, hi, err)
		}
		for _, f := range findings {
			bz, _ := json.Marshal(f)
			fmt.Fprintln(w, string(bz))
		}
		if err := w.Flush(); err != nil {
			log.Fatalf("write findings: %v", err)
		}
		writeCheckpoint(*checkpoint, hi)
		logProgress(s, start, hi, end, began)
	}
	s.mu.Lock()
	summary, _ := json.MarshalIndent(s, "", "  ")
	s.mu.Unlock()
	fmt.Println(string(summary))
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

func readCheckpoint(path string) (int64, bool) {
	bz, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	h, err := strconv.ParseInt(strings.TrimSpace(string(bz)), 10, 64)
	return h, err == nil
}

func writeCheckpoint(path string, height int64) {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatInt(height, 10)+"\n"), 0o644); err != nil {
		log.Fatalf("write checkpoint: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Fatalf("write checkpoint: %v", err)
	}
}

func logProgress(s *stats, start, done, end int64, began time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	elapsed := time.Since(began).Seconds()
	rate := float64(s.Blocks) / elapsed
	var eta time.Duration
	if done < end && rate > 0 {
		frac := float64(done-start+1) / float64(end-start+1)
		eta = time.Duration((elapsed/frac - elapsed) * float64(time.Second)).Round(time.Second)
	}
	log.Printf("height %d/%d: %d blocks, %d txs, %d legacy blob txs, %d go-square blob txs, %d findings (%.0f blocks/s, eta %s)",
		done, end, s.Blocks, s.Txs, s.LegacyBlobs, s.SquareBlobs, s.Findings, rate, eta)
}
