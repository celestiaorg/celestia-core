package consensus

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"path/filepath"
	"time"

	"github.com/cosmos/gogoproto/proto"

	auto "github.com/cometbft/cometbft/libs/autofile"
	cmtjson "github.com/cometbft/cometbft/libs/json"
	"github.com/cometbft/cometbft/libs/log"
	cmtos "github.com/cometbft/cometbft/libs/os"
	"github.com/cometbft/cometbft/libs/service"
	cmtcons "github.com/cometbft/cometbft/proto/tendermint/consensus"
	cmttime "github.com/cometbft/cometbft/types/time"
)

const (
	// maxMsgSizeBytes is the maximum size of a WAL message, including time.Time and consensus message
	maxMsgSizeBytes = maxMsgSize + 24

	// walDefaultFlushInterval defines how often the WAL should be synced during periodic syncing
	walDefaultFlushInterval = 2 * time.Second
)

//--------------------------------------------------------
// Types and functions for saving consensus messages

// TimedWALMessage wraps WALMessage and adds Time for debugging purposes.
type TimedWALMessage struct {
	Time time.Time  `json:"time"` // Time of the message
	Msg  WALMessage `json:"msg"`  // Message content
}

// EndHeightMessage marks the end of the given height inside WAL.
// @internal used by scripts/wal2json utility.
type EndHeightMessage struct {
	Height int64 `json:"height"` // Height that ended
}

// WALMessage is an interface for messages that can be written to the WAL.
type WALMessage interface{}

func init() {
	cmtjson.RegisterType(msgInfo{}, "tendermint/wal/MsgInfo")
	cmtjson.RegisterType(timeoutInfo{}, "tendermint/wal/TimeoutInfo")
	cmtjson.RegisterType(EndHeightMessage{}, "tendermint/wal/EndHeightMessage")
}

//--------------------------------------------------------
// Write-ahead logger implementation

// WAL is an interface for any write-ahead logger.
type WAL interface {
	// Write writes a WALMessage to the log
	Write(WALMessage) error

	// WriteSync writes a WALMessage to the log and flushes it to disk
	WriteSync(WALMessage) error

	// FlushAndSync flushes and syncs the log to disk
	FlushAndSync() error

	// SearchForEndHeight searches for the EndHeightMessage with the given height
	SearchForEndHeight(height int64, options *WALSearchOptions) (rd io.ReadCloser, found bool, err error)

	// Service methods
	Start() error
	Stop() error
	Wait()
}

// BaseWAL is a write ahead logger that writes consensus messages to disk before they are processed.
// It can be used for crash-recovery and deterministic replay.
// The WAL is flushed and synced to disk every 2 seconds by default and when stopped.
type BaseWAL struct {
	service.BaseService

	group *auto.Group // Underlying file group
	enc   *WALEncoder // Encoder for writing messages

	flushTicker   *time.Ticker  // Ticker for periodic flushes
	flushInterval time.Duration // How often to flush to disk
}

// Ensure BaseWAL implements WAL interface
var _ WAL = &BaseWAL{}

// NewWAL returns a new write-ahead logger based on `baseWAL`, which implements
// WAL. It's flushed and synced to disk every 2s and once when stopped.
func NewWAL(walFile string, groupOptions ...func(*auto.Group)) (*BaseWAL, error) {
	err := cmtos.EnsureDir(filepath.Dir(walFile), 0o700)
	if err != nil {
		return nil, fmt.Errorf("failed to ensure WAL directory is in place: %w", err)
	}

	group, err := auto.OpenGroup(walFile, groupOptions...)
	if err != nil {
		return nil, err
	}
	wal := &BaseWAL{
		group:         group,
		enc:           NewWALEncoder(group),
		flushInterval: walDefaultFlushInterval,
	}
	wal.BaseService = *service.NewBaseService(nil, "baseWAL", wal)
	return wal, nil
}

// SetFlushInterval allows us to override the periodic flush interval for the WAL.
func (wal *BaseWAL) SetFlushInterval(i time.Duration) {
	wal.flushInterval = i
}

// Group returns the underlying autofile group.
func (wal *BaseWAL) Group() *auto.Group {
	return wal.group
}

// SetLogger sets the logger for the WAL.
func (wal *BaseWAL) SetLogger(l log.Logger) {
	wal.BaseService.Logger = l
	wal.group.SetLogger(l)
}

// OnStart implements service.BaseService by creating and starting a
// fileticker, which is used to periodically flush the WAL to disk.
func (wal *BaseWAL) OnStart() error {
	size, err := wal.group.Head.Size()
	if err != nil {
		return err
	} else if size == 0 {
		// If the WAL file is empty, write a special record to indicate the initial height
		if err := wal.WriteSync(EndHeightMessage{0}); err != nil {
			return err
		}
	}
	err = wal.group.Start()
	if err != nil {
		return err
	}
	wal.flushTicker = time.NewTicker(wal.flushInterval)
	go wal.processFlushTicks()
	return nil
}

// processFlushTicks handles periodic WAL flushes.
func (wal *BaseWAL) processFlushTicks() {
	for {
		select {
		case <-wal.flushTicker.C:
			if err := wal.FlushAndSync(); err != nil {
				wal.Logger.Error("Periodic WAL flush failed", "err", err)
			}
		case <-wal.Quit():
			return
		}
	}
}

// FlushAndSync flushes and fsync's the underlying group's data to disk.
// See auto#FlushAndSync
func (wal *BaseWAL) FlushAndSync() error {
	return wal.group.FlushAndSync()
}

// OnStop implements service.BaseService by stopping the fileticker
// and flushing/closing the WAL.
func (wal *BaseWAL) OnStop() {
	wal.flushTicker.Stop()
	if err := wal.FlushAndSync(); err != nil {
		wal.Logger.Error("Error on flush data to disk", "error", err)
	}
	if err := wal.group.Stop(); err != nil {
		wal.Logger.Error("Error trying to stop WAL", "error", err)
	}
	wal.group.Close()
}

// Wait waits for the underlying autofile group to finish shutting down
// so it's safe to cleanup files.
func (wal *BaseWAL) Wait() {
	wal.group.Wait()
}

// Write is called in newStep and for each receive on the
// peerMsgQueue and the timeoutTicker.
// NOTE: does not call fsync()
func (wal *BaseWAL) Write(msg WALMessage) error {
	if wal == nil {
		return nil
	}

	if err := wal.enc.Encode(&TimedWALMessage{cmttime.Now(), msg}); err != nil {
		wal.Logger.Error("Error writing msg to consensus WAL. WARNING: recovery may not be possible for the current height",
			"err", err, "msg", msg)
		return err
	}

	return nil
}

// WriteSync is called when we receive a msg from ourselves
// so that we write to disk before sending signed messages.
// NOTE: calls fsync()
func (wal *BaseWAL) WriteSync(msg WALMessage) error {
	if wal == nil {
		return nil
	}

	if err := wal.Write(msg); err != nil {
		return err
	}

	if err := wal.FlushAndSync(); err != nil {
		wal.Logger.Error(`WriteSync failed to flush consensus WAL.
		WARNING: may result in creating alternative proposals / votes for the current height iff the node restarted`,
			"err", err)
		return err
	}

	return nil
}

// WALSearchOptions are optional arguments to SearchForEndHeight.
type WALSearchOptions struct {
	// IgnoreDataCorruptionErrors set to true will result in skipping data corruption errors.
	IgnoreDataCorruptionErrors bool
}

// SearchForEndHeight searches for the EndHeightMessage with the given height
// and returns an auto.GroupReader, whenever it was found or not and an error.
// Group reader will be nil if found equals false.
//
// CONTRACT: caller must close group reader.
func (wal *BaseWAL) SearchForEndHeight(
	height int64,
	options *WALSearchOptions,
) (rd io.ReadCloser, found bool, err error) {
	var (
		msg *TimedWALMessage
		gr  *auto.GroupReader
	)
	lastHeightFound := int64(-1)

	// NOTE: starting from the last file in the group because we're usually
	// searching for the last height. See replay.go
	min, max := wal.group.MinIndex(), wal.group.MaxIndex()
	wal.Logger.Info("Searching for height", "height", height, "min", min, "max", max)
	for index := max; index >= min; index-- {
		gr, err = wal.group.NewReader(index)
		if err != nil {
			return nil, false, err
		}

		dec := NewWALDecoder(gr)
		for {
			msg, err = dec.Decode()
			if err == io.EOF {
				// OPTIMISATION: no need to look for height in older files if we've seen h < height
				if lastHeightFound > 0 && lastHeightFound < height {
					gr.Close()
					return nil, false, nil
				}
				// check next file
				break
			}
			if options.IgnoreDataCorruptionErrors && IsDataCorruptionError(err) {
				wal.Logger.Error("Corrupted entry. Skipping...", "err", err)
				// do nothing
				continue
			} else if err != nil {
				gr.Close()
				return nil, false, err
			}

			if m, ok := msg.Msg.(EndHeightMessage); ok {
				lastHeightFound = m.Height
				if m.Height == height { // found
					wal.Logger.Info("Found", "height", height, "index", index)
					return gr, true, nil
				}
			}
		}
		gr.Close()
	}

	return nil, false, nil
}

// A WALEncoder writes custom-encoded WAL messages to an output stream.
//
// Format: 4 bytes CRC sum + 4 bytes length + arbitrary-length value
type WALEncoder struct {
	wr io.Writer
}

// NewWALEncoder returns a new encoder that writes to wr.
func NewWALEncoder(wr io.Writer) *WALEncoder {
	return &WALEncoder{wr}
}

// Encode writes the custom encoding of v to the stream. It returns an error if
// the encoded size of v is greater than 1MB. Any error encountered
// during the write is also returned.
func (enc *WALEncoder) Encode(v *TimedWALMessage) error {
	pbMsg, err := WALToProto(v.Msg)
	if err != nil {
		return err
	}
	pv := cmtcons.TimedWALMessage{
		Time: v.Time,
		Msg:  pbMsg,
	}

	data, err := proto.Marshal(&pv)
	if err != nil {
		panic(fmt.Errorf("encode timed wall message failure: %w", err))
	}

	crc := crc32.Checksum(data, crc32c)
	length := uint32(len(data))
	if length > maxMsgSizeBytes {
		return fmt.Errorf("msg is too big: %d bytes, max: %d bytes", length, maxMsgSizeBytes)
	}
	totalLength := 8 + int(length)

	msg := make([]byte, totalLength)
	binary.BigEndian.PutUint32(msg[0:4], crc)
	binary.BigEndian.PutUint32(msg[4:8], length)
	copy(msg[8:], data)

	_, err = enc.wr.Write(msg)
	return err
}

// IsDataCorruptionError returns true if data has been corrupted inside WAL.
func IsDataCorruptionError(err error) bool {
	_, ok := err.(DataCorruptionError)
	return ok
}

// DataCorruptionError is an error that occures if data on disk was corrupted.
type DataCorruptionError struct {
	cause error
}

func (e DataCorruptionError) Error() string {
	return fmt.Sprintf("DataCorruptionError[%v]", e.cause)
}

func (e DataCorruptionError) Cause() error {
	return e.cause
}

// A WALDecoder reads and decodes custom-encoded WAL messages from an input
// stream. See WALEncoder for the format used.
//
// It will also compare the checksums and make sure data size is equal to the
// length from the header. If that is not the case, error will be returned.
type WALDecoder struct {
	rd io.Reader
}

// NewWALDecoder returns a new decoder that reads from rd.
func NewWALDecoder(rd io.Reader) *WALDecoder {
	return &WALDecoder{rd}
}

// Decode reads the next custom-encoded value from its reader and returns it.
func (dec *WALDecoder) Decode() (*TimedWALMessage, error) {
	b := make([]byte, 4)

	_, err := dec.rd.Read(b)
	if errors.Is(err, io.EOF) {
		return nil, err
	}
	if err != nil {
		return nil, DataCorruptionError{fmt.Errorf("failed to read checksum: %v", err)}
	}
	crc := binary.BigEndian.Uint32(b)

	b = make([]byte, 4)
	_, err = dec.rd.Read(b)
	if err != nil {
		return nil, DataCorruptionError{fmt.Errorf("failed to read length: %v", err)}
	}
	length := binary.BigEndian.Uint32(b)

	if length > maxMsgSizeBytes {
		return nil, DataCorruptionError{fmt.Errorf(
			"length %d exceeded maximum possible value of %d bytes",
			length,
			maxMsgSizeBytes)}
	}

	data := make([]byte, length)
	n, err := dec.rd.Read(data)
	if err != nil {
		return nil, DataCorruptionError{fmt.Errorf("failed to read data: %v (read: %d, wanted: %d)", err, n, length)}
	}

	// check checksum before decoding data
	actualCRC := crc32.Checksum(data, crc32c)
	if actualCRC != crc {
		return nil, DataCorruptionError{fmt.Errorf("checksums do not match: read: %v, actual: %v", crc, actualCRC)}
	}

	res := new(cmtcons.TimedWALMessage)
	err = proto.Unmarshal(data, res)
	if err != nil {
		return nil, DataCorruptionError{fmt.Errorf("failed to decode data: %v", err)}
	}

	walMsg, err := WALFromProto(res.Msg)
	if err != nil {
		return nil, DataCorruptionError{fmt.Errorf("failed to convert from proto: %w", err)}
	}
	tMsgWal := &TimedWALMessage{
		Time: res.Time,
		Msg:  walMsg,
	}

	return tMsgWal, err
}

type nilWAL struct{}

var _ WAL = nilWAL{}

func (nilWAL) Write(WALMessage) error     { return nil }
func (nilWAL) WriteSync(WALMessage) error { return nil }
func (nilWAL) FlushAndSync() error        { return nil }
func (nilWAL) SearchForEndHeight(int64, *WALSearchOptions) (rd io.ReadCloser, found bool, err error) {
	return nil, false, nil
}
func (nilWAL) Start() error { return nil }
func (nilWAL) Stop() error  { return nil }
func (nilWAL) Wait()        {}
