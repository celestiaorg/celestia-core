package mempool

// NoTx is a zero-size stand-in for a transaction used by the TxCount stub
// messages. Its Unmarshal accepts (and discards) any wire payload, and the
// slice-of-NoTx that gogoproto generates costs no memory per entry — only the
// slice header grows.
type NoTx struct{}

func (NoTx) Marshal() ([]byte, error)                 { return nil, nil }
func (NoTx) MarshalTo([]byte) (int, error)            { return 0, nil }
func (NoTx) MarshalToSizedBuffer([]byte) (int, error) { return 0, nil }
func (NoTx) Size() int                                { return 0 }

func (*NoTx) Unmarshal([]byte) error { return nil }
