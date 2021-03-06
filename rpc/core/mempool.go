package core

import (
	"fmt"
	ctypes "github.com/tendermint/tendermint/rpc/core/types"
	"github.com/tendermint/tendermint/state"
	"github.com/tendermint/tendermint/types"
)

//-----------------------------------------------------------------------------

// Note: tx must be signed
func BroadcastTx(tx types.Tx) (*ctypes.Receipt, error) {
	err := mempoolReactor.BroadcastTx(tx)
	if err != nil {
		return nil, fmt.Errorf("Error broadcasting transaction: %v", err)
	}

	txHash := types.TxId(mempoolReactor.Mempool.GetState().ChainID, tx)
	var createsContract uint8
	var contractAddr []byte
	// check if creates new contract
	if callTx, ok := tx.(*types.CallTx); ok {
		if len(callTx.Address) == 0 {
			createsContract = 1
			contractAddr = state.NewContractAddress(callTx.Input.Address, callTx.Input.Sequence)
		}
	}
	return &ctypes.Receipt{txHash, createsContract, contractAddr}, nil
}

func ListUnconfirmedTxs() ([]types.Tx, error) {
	return mempoolReactor.Mempool.GetProposalTxs(), nil
}
