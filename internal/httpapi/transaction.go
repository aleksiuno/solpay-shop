package httpapi

import (
	"encoding/base64"
	"net/http"
	"uuid"

	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/solanapay"
)

// orderTransaction answers a body of {"account": "<address>"} with the transaction with which
// that account pays what remains of the order whose id is in the path, for a browser wallet to
// sign and send: {"transaction": "<base64>", "message": "..."}, the shapes of a Solana Pay
// transaction request's POST. The body is checked before the order is read, and the order before
// the RPC node is asked for a blockhash.
func (a api) orderTransaction(w http.ResponseWriter, r *http.Request) {
	// Every answer says whether the order exists, and a transaction names its reference.
	w.Header().Set("Cache-Control", "no-store")
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		a.fail(w, r, order.ErrNotFound)
		return
	}
	var req struct {
		Account string `json:"account"`
	}
	if msg := decode(w, r, &req); msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}
	switch {
	case req.Account == "":
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "account is required")
		return
	case solanapay.CheckAddress(req.Account) != nil:
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "account is not a base58 address of 32 bytes")
		return
	case req.Account == a.Merchant: // an address has one base58 spelling
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "account is the shop's own address, which cannot pay itself")
		return
	}
	o, err := a.Orders.Get(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	// The payment block's clock, so the transaction is there exactly when the block is.
	if !o.Payable(a.Now()) {
		writeError(w, http.StatusConflict, codeNotPayable,
			"the order can be paid only while it is pending or underpaid, before its deadline")
		return
	}
	tx, msg, err := a.Transactions.Build(r.Context(), o, req.Account)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Transaction string `json:"transaction"`
		Message     string `json:"message"`
	}{base64.StdEncoding.EncodeToString(tx), msg})
}
