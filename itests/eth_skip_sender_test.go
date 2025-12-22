package itests

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/filecoin-project/go-address"

	"github.com/filecoin-project/lotus/chain/types"
	"github.com/filecoin-project/lotus/chain/types/ethtypes"
	"github.com/filecoin-project/lotus/itests/kit"
)

// decodeUint256 decodes an EthBytes result (which is hex-encoded) into a uint64.
// Returns the decoded value as both big.Int and uint64 (for values that fit).
func decodeUint256(result ethtypes.EthBytes) *big.Int {
	// EthBytes is already the raw bytes, just interpret as big-endian uint256
	return new(big.Int).SetBytes(result)
}

// TestEthCallFromContractAddress verifies eth_call works when 'from' is a contract address.
// This tests the skip sender validation feature that allows simulating calls from
// contract addresses, matching Geth's behavior.
func TestEthCallFromContractAddress(t *testing.T) {
	blockTime := 100 * time.Millisecond
	client, _, ens := kit.EnsembleMinimal(t, kit.MockProofs(), kit.ThroughRPC())
	ens.InterconnectAll().BeginMining(blockTime)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Create account and fund it
	_, ethAddr, deployer := client.EVM().NewAccount()
	kit.SendFunds(ctx, t, client, deployer, types.FromFil(1000))

	// Deploy a contract using the helper that returns Filecoin addresses
	filename := "contracts/SimpleCoin.hex"
	_, contractFilAddr := client.EVM().DeployContractFromFilename(ctx, filename)

	// Get the contract's delegated address (f4 address) from the actor state
	// This is required because eth_call expects a delegated address (protocol 4), not an ID address
	actor, err := client.StateGetActor(ctx, contractFilAddr, types.EmptyTSK)
	require.NoError(t, err)
	require.NotNil(t, actor.DelegatedAddress, "contract should have a delegated address")

	contractEthAddr, err := ethtypes.EthAddressFromFilecoinAddress(*actor.DelegatedAddress)
	require.NoError(t, err)

	// Test: eth_call with contract as sender (should work with skip sender check)
	blkParam := ethtypes.NewEthBlockNumberOrHashFromPredefined("latest")

	// Call from contract address to EOA - this should succeed with skip sender validation
	result, err := client.EthCall(ctx, ethtypes.EthCall{
		From: &contractEthAddr, // Contract address as sender!
		To:   &ethAddr,
		Data: []byte{},
	}, blkParam)

	// This should succeed with the skip sender check implementation
	require.NoError(t, err, "eth_call from contract address should succeed")
	t.Logf("eth_call from contract succeeded, result: %x", result)
}

// TestEthCallFromNonExistentAddress verifies eth_call works with a non-existent sender address.
// This tests that we can simulate calls from addresses that don't exist on chain.
func TestEthCallFromNonExistentAddress(t *testing.T) {
	blockTime := 100 * time.Millisecond
	client, _, ens := kit.EnsembleMinimal(t, kit.MockProofs(), kit.ThroughRPC())
	ens.InterconnectAll().BeginMining(blockTime)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Create a random non-existent address (this address has never been used)
	nonExistentAddr := ethtypes.EthAddress{
		0xde, 0xad, 0xbe, 0xef, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x01,
	}

	// Create a real account to be the target
	_, ethAddr, deployer := client.EVM().NewAccount()
	kit.SendFunds(ctx, t, client, deployer, types.FromFil(1000))

	// Test: eth_call with non-existent sender
	blkParam := ethtypes.NewEthBlockNumberOrHashFromPredefined("latest")

	result, err := client.EthCall(ctx, ethtypes.EthCall{
		From: &nonExistentAddr, // Non-existent address!
		To:   &ethAddr,
		Data: []byte{},
	}, blkParam)

	require.NoError(t, err, "eth_call from non-existent address should succeed")
	t.Logf("eth_call from non-existent address succeeded, result: %x", result)
}

// TestEthCallFromNonExistentAddress verifies eth_call works with a non-existent sender address.
// This tests that we can simulate calls from addresses that don't exist on chain while validating that they can't send money to the target address.
func TestEthCallFromNonExistentAddressWithValue(t *testing.T) {
	blockTime := 100 * time.Millisecond
	client, _, ens := kit.EnsembleMinimal(t, kit.MockProofs(), kit.ThroughRPC())
	ens.InterconnectAll().BeginMining(blockTime)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Create a random non-existent address (this address has never been used)
	nonExistentAddr := ethtypes.EthAddress{
		0xde, 0xad, 0xbe, 0xef, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x01,
	}

	// Create a real account to be the target
	_, ethAddr, deployer := client.EVM().NewAccount()
	kit.SendFunds(ctx, t, client, deployer, types.FromFil(1000))

	// Test: eth_call with non-existent sender
	blkParam := ethtypes.NewEthBlockNumberOrHashFromPredefined("latest")

	result, err := client.EthCall(ctx, ethtypes.EthCall{
		From:  &nonExistentAddr, // Non-existent address!
		To:    &ethAddr,
		Data:  []byte{},
		Value: ethtypes.EthBigInt(types.NewInt(1)),
	}, blkParam)

	require.Error(t, err, "eth_call with value from non-existent address should fail")
	t.Logf("eth_call with value from non-existent address failed, result: %x", result)
}

// TestEthEstimateGasFromContract verifies gas estimation works when sender is a contract.
func TestEthEstimateGasFromContract(t *testing.T) {
	blockTime := 100 * time.Millisecond
	client, _, ens := kit.EnsembleMinimal(t, kit.MockProofs(), kit.ThroughRPC())
	ens.InterconnectAll().BeginMining(blockTime)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Create account and fund it
	_, ethAddr, deployer := client.EVM().NewAccount()
	kit.SendFunds(ctx, t, client, deployer, types.FromFil(1000))

	// Deploy a contract
	filename := "contracts/SimpleCoin.hex"
	_, contractFilAddr := client.EVM().DeployContractFromFilename(ctx, filename)

	// Get the contract's delegated address (f4 address) from the actor state
	actor, err := client.StateGetActor(ctx, contractFilAddr, types.EmptyTSK)
	require.NoError(t, err)
	require.NotNil(t, actor.DelegatedAddress, "contract should have a delegated address")

	contractEthAddr, err := ethtypes.EthAddressFromFilecoinAddress(*actor.DelegatedAddress)
	require.NoError(t, err)

	// Test: estimate gas with contract as sender
	blkParam := ethtypes.NewEthBlockNumberOrHashFromPredefined("latest")
	gasParams, err := json.Marshal(ethtypes.EthEstimateGasParams{
		Tx: ethtypes.EthCall{
			From: &contractEthAddr,
			To:   &ethAddr,
			Data: []byte{},
		},
		BlkParam: &blkParam,
	})
	require.NoError(t, err)

	gas, err := client.EthEstimateGas(ctx, gasParams)
	require.NoError(t, err, "gas estimation from contract should succeed")
	require.Greater(t, uint64(gas), uint64(0), "should return non-zero gas")
	t.Logf("Gas estimation from contract: %d", gas)
}

// TestEthEstimateGasFromNonExistentAddress verifies gas estimation works from non-existent address.
func TestEthEstimateGasFromNonExistentAddress(t *testing.T) {
	blockTime := 100 * time.Millisecond
	client, _, ens := kit.EnsembleMinimal(t, kit.MockProofs(), kit.ThroughRPC())
	ens.InterconnectAll().BeginMining(blockTime)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Create a random non-existent address
	nonExistentAddr := ethtypes.EthAddress{
		0xca, 0xfe, 0xba, 0xbe, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x02,
	}

	// Create a real account to be the target
	_, ethAddr, deployer := client.EVM().NewAccount()
	kit.SendFunds(ctx, t, client, deployer, types.FromFil(1000))

	// Test: estimate gas with non-existent sender
	blkParam := ethtypes.NewEthBlockNumberOrHashFromPredefined("latest")
	gasParams, err := json.Marshal(ethtypes.EthEstimateGasParams{
		Tx: ethtypes.EthCall{
			From: &nonExistentAddr,
			To:   &ethAddr,
			Data: []byte{},
		},
		BlkParam: &blkParam,
	})
	require.NoError(t, err)

	gas, err := client.EthEstimateGas(ctx, gasParams)
	require.NoError(t, err, "gas estimation from non-existent address should succeed")
	require.Greater(t, uint64(gas), uint64(0), "should return non-zero gas")
	t.Logf("Gas estimation from non-existent address: %d", gas)
}

// TestEthCallFromEOAStillWorks verifies that normal eth_call from EOA still works correctly.
// This is a regression test to ensure the skip sender validation doesn't break normal operation.
func TestEthCallFromEOAStillWorks(t *testing.T) {
	blockTime := 100 * time.Millisecond
	client, _, ens := kit.EnsembleMinimal(t, kit.MockProofs(), kit.ThroughRPC())
	ens.InterconnectAll().BeginMining(blockTime)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Create two accounts
	_, ethAddr1, deployer1 := client.EVM().NewAccount()
	_, ethAddr2, _ := client.EVM().NewAccount()
	kit.SendFunds(ctx, t, client, deployer1, types.FromFil(1000))

	// Test: normal eth_call from EOA to EOA
	blkParam := ethtypes.NewEthBlockNumberOrHashFromPredefined("latest")

	result, err := client.EthCall(ctx, ethtypes.EthCall{
		From: &ethAddr1, // Normal EOA
		To:   &ethAddr2,
		Data: []byte{},
	}, blkParam)

	require.NoError(t, err, "eth_call from EOA should still work")
	t.Logf("eth_call from EOA succeeded, result: %x", result)
}

// TestEthCallFromNilAddress verifies that eth_call works with a nil sender address (from is nil).
func TestEthCallFromNilAddress(t *testing.T) {
	blockTime := 100 * time.Millisecond
	client, _, ens := kit.EnsembleMinimal(t, kit.MockProofs(), kit.ThroughRPC())
	ens.InterconnectAll().BeginMining(blockTime)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	_, ethAddr, _ := client.EVM().NewAccount()

	// Test: normal eth_call from nil address to EOA
	blkParam := ethtypes.NewEthBlockNumberOrHashFromPredefined("latest")

	result, err := client.EthCall(ctx, ethtypes.EthCall{
		From: nil, // Nil address
		To:   &ethAddr,
		Data: []byte{},
	}, blkParam)

	require.NoError(t, err, "eth_call from nil address should still work")
	t.Logf("eth_call from nil address succeeded, result: %x", result)
}

// TestEthCallFromNonExistentAddressWithValueDetailed verifies specific error handling for value transfers
// from non-existent addresses. This ensures proper error types are returned.
func TestEthCallFromNonExistentAddressWithValueDetailed(t *testing.T) {
	blockTime := 100 * time.Millisecond
	client, _, ens := kit.EnsembleMinimal(t, kit.MockProofs(), kit.ThroughRPC())
	ens.InterconnectAll().BeginMining(blockTime)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Create a random non-existent address
	nonExistentAddr := ethtypes.EthAddress{
		0xab, 0xcd, 0xef, 0x12, 0x34, 0x56, 0x78, 0x90,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x03,
	}

	// Create a real account to be the target
	_, ethAddr, deployer := client.EVM().NewAccount()
	kit.SendFunds(ctx, t, client, deployer, types.FromFil(1000))

	// Test: eth_call with value from non-existent sender should fail
	blkParam := ethtypes.NewEthBlockNumberOrHashFromPredefined("latest")

	// Try to send 1000 FIL (a large amount to ensure failure)
	_, err := client.EthCall(ctx, ethtypes.EthCall{
		From:  &nonExistentAddr,
		To:    &ethAddr,
		Data:  []byte{},
		Value: ethtypes.EthBigInt(types.FromFil(1000)),
	}, blkParam)

	// Should fail - verify error is not nil and contains relevant information
	require.Error(t, err, "eth_call with large value from non-existent address should fail")
	require.Contains(t, err.Error(), "insufficient", "error should mention insufficient funds or balance")
	t.Logf("Error message for value transfer from non-existent address: %v", err)
}

// TestEthCallContractToContract tests calling from one contract to another contract.
// This is a critical scenario for DeFi applications where contracts interact.
func TestEthCallContractToContract(t *testing.T) {
	blockTime := 100 * time.Millisecond
	client, _, ens := kit.EnsembleMinimal(t, kit.MockProofs(), kit.ThroughRPC())
	ens.InterconnectAll().BeginMining(blockTime)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Create account and fund it
	_, _, deployer := client.EVM().NewAccount()
	kit.SendFunds(ctx, t, client, deployer, types.FromFil(1000))

	// Deploy two contracts
	filename := "contracts/SimpleCoin.hex"
	_, contract1FilAddr := client.EVM().DeployContractFromFilename(ctx, filename)
	_, contract2FilAddr := client.EVM().DeployContractFromFilename(ctx, filename)

	// Get both contracts' delegated addresses
	actor1, err := client.StateGetActor(ctx, contract1FilAddr, types.EmptyTSK)
	require.NoError(t, err)
	require.NotNil(t, actor1.DelegatedAddress, "contract 1 should have a delegated address")

	contract1EthAddr, err := ethtypes.EthAddressFromFilecoinAddress(*actor1.DelegatedAddress)
	require.NoError(t, err)

	actor2, err := client.StateGetActor(ctx, contract2FilAddr, types.EmptyTSK)
	require.NoError(t, err)
	require.NotNil(t, actor2.DelegatedAddress, "contract 2 should have a delegated address")

	contract2EthAddr, err := ethtypes.EthAddressFromFilecoinAddress(*actor2.DelegatedAddress)
	require.NoError(t, err)

	// Test: Call from contract1 to contract2 - both are contracts!
	blkParam := ethtypes.NewEthBlockNumberOrHashFromPredefined("latest")

	// Build calldata for sendCoin(address,uint256) with amount 0
	// sendCoin with 0 amount won't revert and won't emit Transfer event
	sendCoinSelector := kit.EthFunctionHash("sendCoin(address,uint256)")
	// Pad contract1's address to 32 bytes as the receiver argument
	paddedAddr := make([]byte, 32)
	copy(paddedAddr[12:], contract1EthAddr[:]) // addresses are 20 bytes, left-pad with zeros
	// Amount is 0, so just 32 zero bytes
	paddedAmount := make([]byte, 32)
	calldata := append(sendCoinSelector, paddedAddr...)
	calldata = append(calldata, paddedAmount...)

	// Get the current block number before the call to check for events later
	currentBlock, err := client.EthBlockNumber(ctx)
	require.NoError(t, err)

	result, err := client.EthCall(ctx, ethtypes.EthCall{
		From: &contract1EthAddr, // Contract as sender
		To:   &contract2EthAddr, // Contract as receiver
		Data: calldata,          // Valid sendCoin(address,0) call
	}, blkParam)

	require.NoError(t, err, "eth_call from contract to contract should succeed")
	t.Logf("eth_call from contract to contract succeeded, result: %x", result)

	// Verify no Transfer event was emitted (eth_call is simulation only)
	// Check logs for the Transfer event topic starting from the block before our call
	transferTopic := kit.EthTopicHash("Transfer(address,address,uint256)")
	fromBlock := fmt.Sprintf("0x%x", currentBlock)
	filter := ethtypes.EthFilterSpec{
		FromBlock: &fromBlock,
		Topics:    []ethtypes.EthHashList{{transferTopic}},
	}
	logs, err := client.EthGetLogs(ctx, &filter)
	require.NoError(t, err)
	require.Empty(t, logs.Results, "eth_call should not emit any events - it's a simulation")
}

// TestEthCallWithContractMethodData tests calling a contract method with actual data payload.
// This ensures the skip sender validation works with complex contract interactions.
func TestEthCallWithContractMethodData(t *testing.T) {
	blockTime := 100 * time.Millisecond
	client, _, ens := kit.EnsembleMinimal(t, kit.MockProofs(), kit.ThroughRPC())
	ens.InterconnectAll().BeginMining(blockTime)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Create account and fund it
	_, _, deployer := client.EVM().NewAccount()
	kit.SendFunds(ctx, t, client, deployer, types.FromFil(1000))

	// Deploy SimpleCoin contract
	filename := "contracts/SimpleCoin.hex"
	_, contractFilAddr := client.EVM().DeployContractFromFilename(ctx, filename)

	actor, err := client.StateGetActor(ctx, contractFilAddr, types.EmptyTSK)
	require.NoError(t, err)
	require.NotNil(t, actor.DelegatedAddress)

	contractEthAddr, err := ethtypes.EthAddressFromFilecoinAddress(*actor.DelegatedAddress)
	require.NoError(t, err)

	// Create a non-existent address to use as sender
	nonExistentAddr := ethtypes.EthAddress{
		0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x04,
	}

	// Build calldata for sendCoin(address,uint256) with amount 0
	// This tests that skip sender validation works with actual contract method calls
	// sendCoin with 0 amount won't revert and won't emit Transfer event
	sendCoinSelector := kit.EthFunctionHash("sendCoin(address,uint256)")
	// Pad the contract address to 32 bytes as the receiver argument
	paddedAddr := make([]byte, 32)
	copy(paddedAddr[12:], contractEthAddr[:]) // addresses are 20 bytes, left-pad with zeros
	// Amount is 0, so just 32 zero bytes
	paddedAmount := make([]byte, 32)
	methodData := append(sendCoinSelector, paddedAddr...)
	methodData = append(methodData, paddedAmount...)

	// Get the current block number before the call to check for events later
	currentBlock, err := client.EthBlockNumber(ctx)
	require.NoError(t, err)

	blkParam := ethtypes.NewEthBlockNumberOrHashFromPredefined("latest")

	// Call contract method from non-existent address with valid method data
	result, err := client.EthCall(ctx, ethtypes.EthCall{
		From: &nonExistentAddr,
		To:   &contractEthAddr,
		Data: methodData,
	}, blkParam)

	// Should succeed - simulation works with valid method call from non-existent address
	require.NoError(t, err, "eth_call with method data from non-existent address should succeed")
	t.Logf("eth_call with method data succeeded, result: %x", result)

	// Verify no Transfer event was emitted (eth_call is simulation only)
	transferTopic := kit.EthTopicHash("Transfer(address,address,uint256)")
	fromBlock := fmt.Sprintf("0x%x", currentBlock)
	filter := ethtypes.EthFilterSpec{
		FromBlock: &fromBlock,
		Topics:    []ethtypes.EthHashList{{transferTopic}},
	}
	logs, err := client.EthGetLogs(ctx, &filter)
	require.NoError(t, err)
	require.Empty(t, logs.Results, "eth_call should not emit any events - it's a simulation")
}

// TestEthEstimateGasLargeValue tests gas estimation with large gas values to verify
// the overflow protection in the gas margin calculation.
func TestEthEstimateGasLargeValue(t *testing.T) {
	blockTime := 100 * time.Millisecond
	client, _, ens := kit.EnsembleMinimal(t, kit.MockProofs(), kit.ThroughRPC())
	ens.InterconnectAll().BeginMining(blockTime)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Create accounts
	_, ethAddr1, deployer1 := client.EVM().NewAccount()
	_, ethAddr2, _ := client.EVM().NewAccount()
	kit.SendFunds(ctx, t, client, deployer1, types.FromFil(1000))

	// Test: estimate gas for a simple transfer
	blkParam := ethtypes.NewEthBlockNumberOrHashFromPredefined("latest")
	gasParams, err := json.Marshal(ethtypes.EthEstimateGasParams{
		Tx: ethtypes.EthCall{
			From: &ethAddr1,
			To:   &ethAddr2,
			Data: []byte{},
		},
		BlkParam: &blkParam,
	})
	require.NoError(t, err)

	gas, err := client.EthEstimateGas(ctx, gasParams)
	require.NoError(t, err, "gas estimation should succeed")
	require.Greater(t, uint64(gas), uint64(0), "should return non-zero gas")

	// Verify gas value is reasonable (not overflowed)
	// Gas for simple transfer should be less than block gas limit
	require.Less(t, uint64(gas), uint64(10_000_000_000), "gas should not be astronomically high (overflow)")
	t.Logf("Gas estimation result: %d (with 25%% margin)", gas)
}

// TestEthEstimateGasFromContractWithData tests gas estimation from a contract address
// with actual contract call data to ensure the fallback path works correctly.
func TestEthEstimateGasFromContractWithData(t *testing.T) {
	blockTime := 100 * time.Millisecond
	client, _, ens := kit.EnsembleMinimal(t, kit.MockProofs(), kit.ThroughRPC())
	ens.InterconnectAll().BeginMining(blockTime)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Create account and fund it
	_, _, deployer := client.EVM().NewAccount()
	kit.SendFunds(ctx, t, client, deployer, types.FromFil(1000))

	// Deploy a contract
	filename := "contracts/SimpleCoin.hex"
	_, contractFilAddr := client.EVM().DeployContractFromFilename(ctx, filename)

	// Get the contract's delegated address
	actor, err := client.StateGetActor(ctx, contractFilAddr, types.EmptyTSK)
	require.NoError(t, err)
	require.NotNil(t, actor.DelegatedAddress, "contract should have a delegated address")

	contractEthAddr, err := ethtypes.EthAddressFromFilecoinAddress(*actor.DelegatedAddress)
	require.NoError(t, err)

	// Create a target address
	_, targetEthAddr, _ := client.EVM().NewAccount()

	// Prepare arbitrary method call data to test data payload handling
	// Using a non-existent method selector to avoid side effects
	methodData := []byte{0xca, 0xfe, 0xba, 0xbe}

	// Test: estimate gas with contract as sender and method data
	blkParam := ethtypes.NewEthBlockNumberOrHashFromPredefined("latest")
	gasParams, err := json.Marshal(ethtypes.EthEstimateGasParams{
		Tx: ethtypes.EthCall{
			From: &contractEthAddr,
			To:   &targetEthAddr,
			Data: methodData,
		},
		BlkParam: &blkParam,
	})
	require.NoError(t, err)

	gas, err := client.EthEstimateGas(ctx, gasParams)
	require.NoError(t, err, "gas estimation from contract with data should succeed")
	require.Greater(t, uint64(gas), uint64(0), "should return non-zero gas")
	t.Logf("Gas estimation from contract with data: %d", gas)
}

// TestEthCallPreservesChainState verifies that synthetic actor creation doesn't
// affect the actual chain state (changes are ephemeral in buffered blockstore).
func TestEthCallPreservesChainState(t *testing.T) {
	blockTime := 100 * time.Millisecond
	client, _, ens := kit.EnsembleMinimal(t, kit.MockProofs(), kit.ThroughRPC())
	ens.InterconnectAll().BeginMining(blockTime)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Create a target account
	_, ethAddr, deployer := client.EVM().NewAccount()
	kit.SendFunds(ctx, t, client, deployer, types.FromFil(1000))

	// Use a non-existent address as sender
	nonExistentAddr := ethtypes.EthAddress{
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x05,
	}

	blkParam := ethtypes.NewEthBlockNumberOrHashFromPredefined("latest")

	// Perform eth_call from non-existent address (creates synthetic actor in buffered store)
	result, err := client.EthCall(ctx, ethtypes.EthCall{
		From: &nonExistentAddr,
		To:   &ethAddr,
		Data: []byte{},
	}, blkParam)
	require.NoError(t, err, "eth_call should succeed")
	t.Logf("First eth_call succeeded, result: %x", result)

	// Perform another eth_call with the same non-existent address
	// If state was persisted, this would fail or behave differently
	result2, err := client.EthCall(ctx, ethtypes.EthCall{
		From: &nonExistentAddr,
		To:   &ethAddr,
		Data: []byte{},
	}, blkParam)
	require.NoError(t, err, "second eth_call should also succeed")
	t.Logf("Second eth_call succeeded, result: %x", result2)

	// Both calls should succeed and produce the same result (state is not persisted)
	require.Equal(t, result, result2, "repeated calls from non-existent address should produce same results")

	// Verify the non-existent address still doesn't exist on chain
	// Try to look up the address - it should not exist
	filAddr, err := nonExistentAddr.ToFilecoinAddress()
	require.NoError(t, err)

	_, err = client.StateGetActor(ctx, filAddr, types.EmptyTSK)
	require.Error(t, err, "non-existent address should still not exist on chain after eth_call")
	require.Contains(t, err.Error(), "actor not found", "should get actor not found error")
	t.Logf("Verified: synthetic actor was not persisted to chain state")
}

// TestEthCallSendCoinPreservesBalance verifies that sendCoin via eth_call doesn't
// actually transfer tokens - the balance change is ephemeral and not persisted.
// This deploys a SimpleCoin contract, simulates sending coins to a non-existent address,
// and then verifies the recipient's balance is still 0 in a subsequent call.
func TestEthCallSendCoinPreservesBalance(t *testing.T) {
	blockTime := 100 * time.Millisecond
	client, _, ens := kit.EnsembleMinimal(t, kit.MockProofs(), kit.ThroughRPC())
	ens.InterconnectAll().BeginMining(blockTime)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Use the default wallet address to deploy the contract
	// The constructor sets balances[tx.origin] = 10000, so the deployer gets the tokens
	deployer := client.DefaultKey.Address

	// Deploy SimpleCoin contract
	contractHex, err := os.ReadFile("contracts/SimpleCoin.hex")
	require.NoError(t, err)
	contract, err := hex.DecodeString(string(contractHex))
	require.NoError(t, err)

	deployResult := client.EVM().DeployContract(ctx, deployer, contract)
	contractFilAddr, err := address.NewIDAddress(deployResult.ActorID)
	require.NoError(t, err)

	// Get the contract's delegated address
	actor, err := client.StateGetActor(ctx, contractFilAddr, types.EmptyTSK)
	require.NoError(t, err)
	require.NotNil(t, actor.DelegatedAddress, "contract should have a delegated address")

	contractEthAddr, err := ethtypes.EthAddressFromFilecoinAddress(*actor.DelegatedAddress)
	require.NoError(t, err)

	// Create a non-existent sender address (this will be used as msg.sender for sendCoin)
	// Using skip-sender-validation, we can simulate calls from this non-existent address
	nonExistentSender := ethtypes.EthAddress{
		0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88,
		0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00,
		0x00, 0x00, 0x00, 0x07,
	}

	// Create a non-existent recipient address
	nonExistentRecipient := ethtypes.EthAddress{
		0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00, 0x11,
		0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99,
		0x00, 0x00, 0x00, 0x06,
	}

	blkParam := ethtypes.NewEthBlockNumberOrHashFromPredefined("latest")
	getBalanceSelector := kit.EthFunctionHash("getBalance(address)")

	// Step 1: Check initial balance of non-existent recipient (should be 0)
	paddedRecipient := make([]byte, 32)
	copy(paddedRecipient[12:], nonExistentRecipient[:])
	getBalanceCalldata := append(getBalanceSelector, paddedRecipient...)

	initialBalance, err := client.EthCall(ctx, ethtypes.EthCall{
		From: nil,
		To:   &contractEthAddr,
		Data: getBalanceCalldata,
	}, blkParam)
	require.NoError(t, err)
	t.Logf("Initial balance of non-existent recipient: %d", decodeUint256(initialBalance))

	// Verify initial balance is 0 (32 bytes of zeros)
	expectedZeroBalance := make([]byte, 32)
	require.Equal(t, expectedZeroBalance, []byte(initialBalance), "initial balance should be 0")

	// Step 2: Simulate sendCoin from non-existent sender to non-existent recipient (amount = 100)
	// This tests that even if we simulate a transfer, no state is persisted
	sendCoinSelector := kit.EthFunctionHash("sendCoin(address,uint256)")
	paddedAmount := make([]byte, 32)
	paddedAmount[31] = 100 // Send 100 coins
	sendCoinCalldata := append(sendCoinSelector, paddedRecipient...)
	sendCoinCalldata = append(sendCoinCalldata, paddedAmount...)

	// This eth_call simulates the transfer using a non-existent sender as msg.sender
	// The call will "succeed" but return false (sender has no balance to send)
	// Most importantly, NO state change should be persisted
	sendResult, err := client.EthCall(ctx, ethtypes.EthCall{
		From: &nonExistentSender,
		To:   &contractEthAddr,
		Data: sendCoinCalldata,
	}, blkParam)
	require.NoError(t, err)
	t.Logf("sendCoin simulation result (bool): %d", decodeUint256(sendResult))

	// Step 3: Check balance again - should STILL be 0 (eth_call doesn't persist state)
	balanceAfterSend, err := client.EthCall(ctx, ethtypes.EthCall{
		From: nil,
		To:   &contractEthAddr,
		Data: getBalanceCalldata,
	}, blkParam)
	require.NoError(t, err)
	t.Logf("Balance after simulated sendCoin: %d", decodeUint256(balanceAfterSend))

	// The balance should still be 0 because eth_call doesn't persist state changes
	require.Equal(t, expectedZeroBalance, []byte(balanceAfterSend),
		"balance should still be 0 after eth_call simulation - state changes are not persisted")

	// Step 4: Verify no Transfer event was emitted on chain
	transferTopic := kit.EthTopicHash("Transfer(address,address,uint256)")
	earliest := "earliest"
	filter := ethtypes.EthFilterSpec{
		FromBlock: &earliest,
		Address:   []ethtypes.EthAddress{contractEthAddr},
		Topics:    []ethtypes.EthHashList{{transferTopic}},
	}
	logs, err := client.EthGetLogs(ctx, &filter)
	require.NoError(t, err)
	require.Empty(t, logs.Results, "no Transfer events should be emitted from eth_call simulations")

	t.Logf("Verified: sendCoin via eth_call did not persist balance changes or emit events")

	// Step 5: Now do a REAL transaction - deployer sends coins to nonExistentRecipient
	// This will create the recipient on-chain and give them actual tokens
	t.Logf("Step 5: Sending real transaction from deployer to nonExistentRecipient")

	// Build input data for sendCoin(nonExistentRecipient, 500)
	realSendAmount := make([]byte, 32)
	realSendAmount[30] = 0x01 // 256
	realSendAmount[31] = 0xF4 // 500 = 0x01F4
	realSendCalldata := append(paddedRecipient, realSendAmount...)

	// Invoke the contract method via actual transaction
	_, txWait, err := client.EVM().InvokeContractByFuncName(ctx, deployer, contractFilAddr, "sendCoin(address,uint256)", realSendCalldata)
	require.NoError(t, err)
	require.True(t, txWait.Receipt.ExitCode.IsSuccess(), "real sendCoin transaction should succeed")
	t.Logf("Real sendCoin transaction succeeded")

	// Step 6: Verify nonExistentRecipient now has a balance (the address now exists with tokens)
	recipientBalanceAfterReal, err := client.EthCall(ctx, ethtypes.EthCall{
		From: nil,
		To:   &contractEthAddr,
		Data: getBalanceCalldata,
	}, blkParam)
	require.NoError(t, err)
	t.Logf("Recipient balance after real transaction: %d", decodeUint256(recipientBalanceAfterReal))

	// Should now have 500 tokens (0x1F4 = 500)
	expectedBalance500 := make([]byte, 32)
	expectedBalance500[30] = 0x01
	expectedBalance500[31] = 0xF4
	require.Equal(t, expectedBalance500, []byte(recipientBalanceAfterReal),
		"recipient should have 500 tokens after real transaction")

	// Step 7: Get deployer's eth address for the return transfer
	deployerIdAddr, err := client.StateLookupID(ctx, deployer, types.EmptyTSK)
	require.NoError(t, err)
	deployerEthAddr, err := ethtypes.EthAddressFromFilecoinAddress(deployerIdAddr)
	require.NoError(t, err)

	// Build calldata for sendCoin back to deployer
	paddedDeployer := make([]byte, 32)
	copy(paddedDeployer[12:], deployerEthAddr[:])
	returnSendCalldata := append(sendCoinSelector, paddedDeployer...)
	returnSendCalldata = append(returnSendCalldata, realSendAmount...) // Try to send 500 back

	// Step 8: Simulate eth_call sendCoin FROM the now-existing recipient back to deployer
	// This should "succeed" in simulation but NOT persist any state changes
	t.Logf("Step 8: Simulating eth_call sendCoin from (now existing) recipient back to deployer")
	simulatedReturn, err := client.EthCall(ctx, ethtypes.EthCall{
		From: &nonExistentRecipient, // This address now exists on-chain!
		To:   &contractEthAddr,
		Data: returnSendCalldata,
	}, blkParam)
	require.NoError(t, err)
	t.Logf("Simulated sendCoin from recipient to deployer result (bool): %d", decodeUint256(simulatedReturn))

	// Step 9: Verify recipient's balance is STILL 500 (eth_call didn't persist the transfer)
	recipientBalanceAfterSimulation, err := client.EthCall(ctx, ethtypes.EthCall{
		From: nil,
		To:   &contractEthAddr,
		Data: getBalanceCalldata,
	}, blkParam)
	require.NoError(t, err)
	t.Logf("Recipient balance after eth_call simulation: %d", decodeUint256(recipientBalanceAfterSimulation))

	require.Equal(t, expectedBalance500, []byte(recipientBalanceAfterSimulation),
		"recipient balance should still be 500 after eth_call simulation - state changes are not persisted")

	t.Logf("Verified: eth_call from existing address also does not persist state changes")
}
