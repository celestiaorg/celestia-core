package cat

import (
	"errors"
	"fmt"

	time2 "time"

	"github.com/klauspost/compress/zstd"
)

var (
	zstdEncoder *zstd.Encoder
	zstdDecoder *zstd.Decoder
)

func init() {
	var err error
	zstdEncoder, err = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBetterCompression))
	if err != nil {
		panic("cat: failed to init zstd encoder: " + err.Error())
	}
	// Process-wide safety ceiling on a single decode allocation. The per-call
	// maxSize argument to decompressTx is the real per-tx cap; this just
	// protects against a malicious frame claiming an enormous window.
	zstdDecoder, err = zstd.NewReader(nil, zstd.WithDecoderMaxMemory(32<<20))
	if err != nil {
		panic("cat: failed to init zstd decoder: " + err.Error())
	}
}

var (
	errEmptyCompressedPayload  = errors.New("empty compressed tx payload")
	errMissingDecompressedSize = errors.New("missing decompressed_size on compressed Txs")
	errDecompressedTooLarge    = errors.New("tx decompressed size exceeds maximum")
)

// compressTx zstd-compresses the raw tx bytes. Returns the compressed payload.
func compressTx(raw []byte) []byte {
	n := time2.Now()
	out := zstdEncoder.EncodeAll(raw, nil)
	fmt.Print("compression time: ", time2.Since(n).Milliseconds())
	return out
}

// decompressTx zstd-decompresses payload, rejecting if the (claimed or actual)
// decompressed size exceeds maxSize. maxSize <= 0 disables the cap.
func decompressTx(payload []byte, claimedSize uint32, maxSize int) ([]byte, error) {
	if len(payload) == 0 {
		return nil, errEmptyCompressedPayload
	}
	if claimedSize == 0 {
		return nil, errMissingDecompressedSize
	}
	if maxSize > 0 && int(claimedSize) > maxSize {
		return nil, errDecompressedTooLarge
	}
	dst := make([]byte, 0, claimedSize)
	out, err := zstdDecoder.DecodeAll(payload, dst)
	if err != nil {
		return nil, err
	}
	if maxSize > 0 && len(out) > maxSize {
		return nil, errDecompressedTooLarge
	}
	return out, nil
}
