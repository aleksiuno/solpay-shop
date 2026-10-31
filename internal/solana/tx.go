package solana

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	sol "github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

// SignatureInfo is one transaction that mentions an address, as getSignaturesForAddress lists it.
type SignatureInfo struct {
	Signature string
	Slot      int64
	BlockTime *time.Time // nil when the node does not know it
	Failed    bool
}

// Instruction is one top-level instruction. ProgramIndex and Accounts index Tx.Keys.
type Instruction struct {
	ProgramIndex int
	Accounts     []int
	Data         []byte
}

// Tx is a confirmed transaction reduced to what payment verification needs, in plain Go types.
type Tx struct {
	Signature    string
	Slot         int64
	BlockTime    *time.Time    // nil when the node does not know it
	Failed       bool          // it failed on chain; the fee was still charged
	Keys         []string      // base58: the static keys, then the loaded writable, then the loaded read-only
	Pre, Post    []int64       // lamport balances before and after, aligned with Keys
	Instructions []Instruction // top level only
}

// DecodeTxJSON decodes a getTransaction result requested with base64 encoding: the JSON value
// of the response's "result" field, as the recorded fixtures hold it.
func DecodeTxJSON(b []byte) (Tx, error) {
	var res *rpc.GetTransactionResult
	if err := json.Unmarshal(b, &res); err != nil {
		return Tx{}, fmt.Errorf("decode transaction: %w", err)
	}
	return toTx(res)
}

// toTx maps a getTransaction result to a Tx. It checks every index and converts every number,
// so a Tx is internally consistent: an index into Keys is in range, and no amount wrapped.
func toTx(res *rpc.GetTransactionResult) (Tx, error) {
	if res == nil || res.Transaction == nil || res.Meta == nil {
		return Tx{}, errors.New("decode transaction: the result has no transaction or no meta")
	}
	tx, err := res.Transaction.GetTransaction()
	if err != nil {
		return Tx{}, fmt.Errorf("decode transaction: %w", err)
	}
	if tx == nil || len(tx.Signatures) == 0 {
		return Tx{}, errors.New("decode transaction: no signature")
	}
	slot, err := toInt64(res.Slot)
	if err != nil {
		return Tx{}, fmt.Errorf("decode transaction slot: %w", err)
	}

	loaded := res.Meta.LoadedAddresses
	keys := make([]string, 0, len(tx.Message.AccountKeys)+len(loaded.Writable)+len(loaded.ReadOnly))
	for _, k := range tx.Message.AccountKeys {
		keys = append(keys, k.String())
	}
	for _, k := range loaded.Writable {
		keys = append(keys, k.String())
	}
	for _, k := range loaded.ReadOnly {
		keys = append(keys, k.String())
	}

	pre, err := toInt64s(res.Meta.PreBalances)
	if err != nil {
		return Tx{}, fmt.Errorf("decode balances: %w", err)
	}
	post, err := toInt64s(res.Meta.PostBalances)
	if err != nil {
		return Tx{}, fmt.Errorf("decode balances: %w", err)
	}
	if len(pre) != len(keys) || len(post) != len(keys) {
		return Tx{}, fmt.Errorf("decode balances: %d before and %d after for %d accounts", len(pre), len(post), len(keys))
	}

	insts := make([]Instruction, 0, len(tx.Message.Instructions))
	for i, ci := range tx.Message.Instructions {
		inst := Instruction{ProgramIndex: int(ci.ProgramIDIndex), Accounts: make([]int, len(ci.Accounts)), Data: []byte(ci.Data)}
		if inst.ProgramIndex >= len(keys) {
			return Tx{}, fmt.Errorf("decode instruction %d: program index %d of %d accounts", i, inst.ProgramIndex, len(keys))
		}
		for j, a := range ci.Accounts {
			if int(a) >= len(keys) {
				return Tx{}, fmt.Errorf("decode instruction %d: account index %d of %d accounts", i, a, len(keys))
			}
			inst.Accounts[j] = int(a)
		}
		insts = append(insts, inst)
	}

	return Tx{
		Signature:    tx.Signatures[0].String(),
		Slot:         slot,
		BlockTime:    blockTime(res.BlockTime),
		Failed:       res.Meta.Err != nil,
		Keys:         keys,
		Pre:          pre,
		Post:         post,
		Instructions: insts,
	}, nil
}

// toInt64 converts a uint64 from the node. Lamports and slots fit an int64 by far, so a larger
// value means a bad response, not a real amount.
func toInt64(u uint64) (int64, error) {
	if u > math.MaxInt64 {
		return 0, fmt.Errorf("%d overflows int64", u)
	}
	return int64(u), nil
}

func toInt64s(us []uint64) ([]int64, error) {
	out := make([]int64, len(us))
	for i, u := range us {
		v, err := toInt64(u)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func blockTime(t *sol.UnixTimeSeconds) *time.Time {
	if t == nil {
		return nil
	}
	utc := t.Time().UTC()
	return &utc
}
