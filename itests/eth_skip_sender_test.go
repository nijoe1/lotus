package itests

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/filecoin-project/lotus/chain/types"
	"github.com/filecoin-project/lotus/chain/types/ethtypes"
	"github.com/filecoin-project/lotus/itests/kit"
)

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
		From: &nonExistentAddr, // Non-existent address!
		To:   &ethAddr,
		Data: []byte{},
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
