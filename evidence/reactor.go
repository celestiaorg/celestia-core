package evidence

import (
	"fmt"
	"sync"
	"time"

	"github.com/cosmos/gogoproto/proto"

	clist "github.com/cometbft/cometbft/libs/clist"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/p2p"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

const (
	EvidenceChannel = byte(0x38)

	maxMsgSize = 1048576 // 1MB TODO make it configurable

	// broadcast all uncommitted evidence this often. This sets when the reactor
	// goes back to the start of the list and begins sending the evidence again.
	// Most evidence should be committed in the very next block that is why we wait
	// just over the block production rate before sending evidence again.
	broadcastEvidenceIntervalS = 10
	// If a message fails wait this much before sending it again
	peerRetryMessageIntervalMS = 100
	// maxWaitingMessagesPerPeer and maxWaitingBytesPerPeer bound what one
	// peer can have waiting in the reactor's message queue. An honest peer
	// sends each piece of pending evidence in a message of its own, a few
	// hundred bytes for a duplicate vote, so it stays well within both.
	maxWaitingMessagesPerPeer = 16
	maxWaitingBytesPerPeer    = maxMsgSize
	// defaultMaxPeers is the default peer limit: 40 inbound plus 10 outbound.
	defaultMaxPeers = 50
	// ReactorIncomingMessageQueueSize the size of the reactor's message queue.
	// It has room for what every peer can have waiting with the default
	// peer limit.
	ReactorIncomingMessageQueueSize = defaultMaxPeers * maxWaitingMessagesPerPeer
)

// Reactor handles evpool evidence broadcasting amongst peers.
type Reactor struct {
	p2p.BaseReactor
	evpool   *Pool
	eventBus *types.EventBus
	// waiting tracks the messages each peer has waiting in the incoming
	// queue.
	waiting *waitingMessages
}

// NewReactor returns a new Reactor with the given config and evpool.
func NewReactor(evpool *Pool) *Reactor {
	evR := &Reactor{
		evpool:  evpool,
		waiting: newWaitingMessages(),
	}
	evR.BaseReactor = *p2p.NewBaseReactor("Evidence", evR,
		p2p.WithIncomingQueueSize(ReactorIncomingMessageQueueSize),
		p2p.WithQueueingFunc(evR.queueWithinPeerBudget),
	)
	return evR
}

// queueWithinPeerBudget queues a message without blocking. A blocking queue
// stalls the sending peer's whole connection while another peer's message is
// being processed.
//
// Each peer has a budget of messages and bytes that can wait in the queue, so
// that one peer cannot fill the queue and crowd out the others. A message is
// dropped, without being decoded, if it does not fit its peer's budget or if
// the queue is full. Dropped evidence is sent again by the peer's broadcast
// routine.
func (evR *Reactor) queueWithinPeerBudget(e p2p.UnprocessedEnvelope) {
	peerID := e.Src.ID()
	messageSize := len(e.Message)
	if !evR.waiting.add(peerID, messageSize) {
		return
	}
	if !evR.TryQueueEnvelope(e) {
		evR.waiting.removeNewest(peerID)
	}
}

// RemovePeer implements Reactor.
func (evR *Reactor) RemovePeer(peer p2p.Peer, _ interface{}) {
	// A message that fails to decode never reaches Receive. It stops the
	// peer instead, so forget the peer's waiting messages here.
	evR.waiting.removePeer(peer.ID())
}

// waitingMessages tracks, for each peer, the sizes of the messages it has
// waiting in the reactor's incoming queue. It is safe for concurrent use.
type waitingMessages struct {
	mtx sync.Mutex
	// sizesByPeer holds the size of each waiting message, oldest first.
	sizesByPeer map[p2p.ID][]int
}

func newWaitingMessages() *waitingMessages {
	return &waitingMessages{sizesByPeer: make(map[p2p.ID][]int)}
}

// add records a message of the given size for the peer and reports whether it
// fits the peer's budget. A peer with nothing waiting can always add a
// message.
func (w *waitingMessages) add(peerID p2p.ID, messageSize int) bool {
	w.mtx.Lock()
	defer w.mtx.Unlock()

	sizes := w.sizesByPeer[peerID]
	if len(sizes) >= maxWaitingMessagesPerPeer {
		return false
	}
	waitingBytes := 0
	for _, size := range sizes {
		waitingBytes += size
	}
	if len(sizes) > 0 && waitingBytes+messageSize > maxWaitingBytesPerPeer {
		return false
	}
	w.sizesByPeer[peerID] = append(sizes, messageSize)
	return true
}

// removeOldest forgets the peer's oldest waiting message. Messages of one peer
// leave the queue in the order they entered it.
func (w *waitingMessages) removeOldest(peerID p2p.ID) {
	w.mtx.Lock()
	defer w.mtx.Unlock()

	sizes := w.sizesByPeer[peerID]
	if len(sizes) <= 1 {
		delete(w.sizesByPeer, peerID)
		return
	}
	w.sizesByPeer[peerID] = sizes[1:]
}

// removeNewest forgets the message the peer added last.
func (w *waitingMessages) removeNewest(peerID p2p.ID) {
	w.mtx.Lock()
	defer w.mtx.Unlock()

	sizes := w.sizesByPeer[peerID]
	if len(sizes) <= 1 {
		delete(w.sizesByPeer, peerID)
		return
	}
	w.sizesByPeer[peerID] = sizes[:len(sizes)-1]
}

// removePeer forgets all waiting messages of the peer.
func (w *waitingMessages) removePeer(peerID p2p.ID) {
	w.mtx.Lock()
	defer w.mtx.Unlock()
	delete(w.sizesByPeer, peerID)
}

// SetLogger sets the Logger on the reactor and the underlying Evidence.
func (evR *Reactor) SetLogger(l log.Logger) {
	evR.Logger = l
	evR.evpool.SetLogger(l)
}

// GetChannels implements Reactor.
// It returns the list of channels for this reactor.
func (evR *Reactor) GetChannels() []*p2p.ChannelDescriptor {
	return []*p2p.ChannelDescriptor{
		{
			ID:                  EvidenceChannel,
			Priority:            6,
			SendQueueCapacity:   10,
			RecvMessageCapacity: maxMsgSize,
			MessageType:         &cmtproto.EvidenceList{},
			// No RecvMessagePrecheck on purpose. Decoding can briefly use
			// more memory than the message size, but invalid evidence
			// disconnects the peer and the inbound peer limit caps the
			// total. See PROTOCO-2767.
		},
	}
}

// AddPeer implements Reactor.
func (evR *Reactor) AddPeer(peer p2p.Peer) {
	go evR.broadcastEvidenceRoutine(peer)
}

// Receive implements Reactor.
// It adds any received evidence to the evpool.
func (evR *Reactor) Receive(e p2p.Envelope) {
	// The message has left the queue, so it no longer counts against its
	// peer's budget.
	evR.waiting.removeOldest(e.Src.ID())

	evis, err := evidenceListFromProto(e.Message)
	if err != nil {
		evR.Logger.Error("Error decoding message", "src", e.Src, "chId", e.ChannelID, "err", err)
		evR.Switch.StopPeerForError(e.Src, err, evR.String())
		return
	}

	for _, ev := range evis {
		err := evR.evpool.AddEvidence(ev)
		switch err.(type) {
		case *types.ErrInvalidEvidence:
			evR.Logger.Error(err.Error())
			// punish peer
			evR.Switch.StopPeerForError(e.Src, err, evR.String())
			return
		case nil:
		default:
			// continue to the next piece of evidence
			evR.Logger.Error("Evidence has not been added", "evidence", evis, "err", err)
		}
	}
}

// SetEventBus implements events.Eventable.
func (evR *Reactor) SetEventBus(b *types.EventBus) {
	evR.eventBus = b
}

// Modeled after the mempool routine.
// - Evidence accumulates in a clist.
// - Each peer has a routine that iterates through the clist,
// sending available evidence to the peer.
// - If we're waiting for new evidence and the list is not empty,
// start iterating from the beginning again.
func (evR *Reactor) broadcastEvidenceRoutine(peer p2p.Peer) {
	var next *clist.CElement
	for {
		// This happens because the CElement we were looking at got garbage
		// collected (removed). That is, .NextWait() returned nil. Go ahead and
		// start from the beginning.
		if next == nil {
			select {
			case <-evR.evpool.EvidenceWaitChan(): // Wait until evidence is available
				if next = evR.evpool.EvidenceFront(); next == nil {
					continue
				}
			case <-peer.Quit():
				return
			case <-evR.Quit():
				return
			}
		} else if !peer.IsRunning() || !evR.IsRunning() {
			return
		}

		ev := next.Value.(types.Evidence)
		evis := evR.prepareEvidenceMessage(peer, ev)
		if len(evis) > 0 {
			evR.Logger.Debug("Gossiping evidence to peer", "ev", ev, "peer", peer)
			evp, err := evidenceListToProto(evis)
			if err != nil {
				panic(err)
			}

			success := peer.Send(p2p.Envelope{
				ChannelID: EvidenceChannel,
				Message:   evp,
			})
			if !success {
				time.Sleep(peerRetryMessageIntervalMS * time.Millisecond)
				continue
			}
		}

		afterCh := time.After(time.Second * broadcastEvidenceIntervalS)
		select {
		case <-afterCh:
			// start from the beginning every tick.
			// TODO: only do this if we're at the end of the list!
			next = nil
		case <-next.NextWaitChan():
			// see the start of the for loop for nil check
			next = next.Next()
		case <-peer.Quit():
			return
		case <-evR.Quit():
			return
		}
	}
}

// Returns the message to send to the peer, or nil if the evidence is invalid for the peer.
// If message is nil, we should sleep and try again.
func (evR Reactor) prepareEvidenceMessage(
	peer p2p.Peer,
	ev types.Evidence,
) (evis []types.Evidence) {

	// make sure the peer is up to date
	evHeight := ev.Height()
	peerState, ok := peer.Get(types.PeerStateKey).(PeerState)
	if !ok {
		// Peer does not have a state yet. We set it in the consensus reactor, but
		// when we add peer in Switch, the order we call reactors#AddPeer is
		// different every time due to us using a map. Sometimes other reactors
		// will be initialized before the consensus reactor. We should wait a few
		// milliseconds and retry.
		return nil
	}

	// NOTE: We only send evidence to peers where
	// peerHeight - maxAge < evidenceHeight < peerHeight
	var (
		peerHeight   = peerState.GetHeight()
		params       = evR.evpool.State().ConsensusParams.Evidence
		ageNumBlocks = peerHeight - evHeight
	)

	if peerHeight <= evHeight { // peer is behind. sleep while he catches up
		return nil
	} else if ageNumBlocks > params.MaxAgeNumBlocks { // evidence is too old relative to the peer, skip

		// NOTE: if evidence is too old for an honest peer, then we're behind and
		// either it already got committed or it never will!
		evR.Logger.Info("Not sending peer old evidence",
			"peerHeight", peerHeight,
			"evHeight", evHeight,
			"maxAgeNumBlocks", params.MaxAgeNumBlocks,
			"lastBlockTime", evR.evpool.State().LastBlockTime,
			"maxAgeDuration", params.MaxAgeDuration,
			"peer", peer,
		)

		return nil
	}

	// send evidence
	return []types.Evidence{ev}
}

// PeerState describes the state of a peer.
type PeerState interface {
	GetHeight() int64
}

// encodemsg takes a array of evidence
// returns the byte encoding of the List Message
func evidenceListToProto(evis []types.Evidence) (*cmtproto.EvidenceList, error) {
	evi := make([]cmtproto.Evidence, len(evis))
	for i := 0; i < len(evis); i++ {
		ev, err := types.EvidenceToProto(evis[i])
		if err != nil {
			return nil, err
		}
		evi[i] = *ev
	}
	epl := cmtproto.EvidenceList{
		Evidence: evi,
	}
	return &epl, nil
}

func evidenceListFromProto(m proto.Message) ([]types.Evidence, error) {
	lm := m.(*cmtproto.EvidenceList)

	evis := make([]types.Evidence, len(lm.Evidence))
	for i := 0; i < len(lm.Evidence); i++ {
		ev, err := types.EvidenceFromProto(&lm.Evidence[i])
		if err != nil {
			return nil, err
		}
		evis[i] = ev
	}

	for i, ev := range evis {
		if err := ev.ValidateBasic(); err != nil {
			return nil, fmt.Errorf("invalid evidence (#%d): %v", i, err)
		}
	}

	return evis, nil
}

func (evR *Reactor) OnStop() {
	evR.BaseReactor.OnStop()
}
