# Skip Sender Check Implementation for Lotus eth_call/eth_estimateGas

## Executive Summary

This document describes the implementation of Geth-compatible "skip sender check" functionality in Lotus, enabling `eth_call` and `eth_estimateGas` to simulate transactions from contract addresses or non-existent addresses.

**Status**: ✅ IMPLEMENTED

---

## Quick Start: How to Build and Test

### Build

```bash
export LIBRARY_PATH="/opt/homebrew/lib:$LIBRARY_PATH"
export CGO_LDFLAGS="-L/opt/homebrew/lib"
export CGO_CFLAGS="-I/opt/homebrew/include"
```

```bash
# Standard build (requires hwloc library installed)
make all

# Or build just the packages that changed
go build ./chain/stmgr/... ./node/impl/eth/...
```

```bash
sudo make install
```

### Run Tests (No Chain Sync Required!)

```bash
# Run all skip sender check tests
go test -v ./itests -run "TestEthCallFromContractAddress|TestEthCallFromNonExistentAddress|TestEthEstimateGasFromContract|TestEthCallFromEOAStillWorks|TestEthCallFromNonExistentAddressWithValue|TestEthCallFromNilAddress" -count=1

# Run individual tests
go test -v ./itests -run TestEthCallFromContractAddress -count=1
go test -v ./itests -run TestEthCallFromNonExistentAddress -count=1
go test -v ./itests -run TestEthEstimateGasFromContract -count=1
go test -v ./itests -run TestEthEstimateGasFromNonExistentAddress -count=1
go test -v ./itests -run "TestEthCallFromNonExistentAddressWithValue" -count=1
go test -v ./itests -run "TestEthCallFromNilAddress" -count=1     

# Run regression test (EOA still works)
go test -v ./itests -run TestEthCallFromEOAStillWorks -count=1

# Run all ETH API tests to ensure no regressions
go test -v ./itests -run "TestETH|TestEth" -count=1
```

---

## 1. Background: How Geth Does It

Geth uses two flags in `core.Message`:

```go
type Message struct {
    // ...
    SkipNonceChecks       bool  // Skip nonce validation
    SkipTransactionChecks bool  // Skip EOA verification + gas limit caps
}
```

For `eth_call`:

- Both flags set to `true`
- Gas price set to 0 (so balance check becomes `balance >= value`)
- Dummy/no signature required
- Any address (contract, non-existent) can be the sender

---

## 2. Current Lotus Architecture

### Call Flow for eth_call

```
EthCall (gas.go:240)
    └─> ToFilecoinMessage (eth_types.go:272)
    └─> applyMessage (gas.go:265)
        └─> StateManager.ApplyOnStateWithGas (call.go:66)
            └─> callInternal (call.go:97) [checkGas=true]
                └─> stTree.GetActor(msg.From) ← FAILS if actor doesn't exist
                └─> FVM.ApplyMessage (fvm.go:416) ← FVM validates sender type
```

### Key Problem Areas

1. **Line 219-222 in call.go**: `stTree.GetActor(msg.From)` fails if the actor doesn't exist
2. **FVM validation**: The FVM C code validates that sender is a valid account actor type

---

## 3. Recommended Implementation

### Option A: Add `skipSenderValidation` Flag (Recommended)

This mirrors Geth's approach and provides explicit control.

#### 3.1 Modify StateManager API

**File**: [chain/stmgr/call.go](chain/stmgr/call.go)

Add a new method:

```go
// ApplyOnStateWithGasSkipSenderValidation applies the message without sender validation.
// Used for eth_call/eth_estimateGas simulation where the sender may be a contract or non-existent.
func (sm *StateManager) ApplyOnStateWithGasSkipSenderValidation(
    ctx context.Context,
    stateCid cid.Cid,
    msg *types.Message,
    ts *types.TipSet,
) (*api.InvocResult, error) {
    return sm.callInternal(ctx, msg, nil, ts, stateCid, sm.GetNetworkVersion, true, execNoMessages, true)
}
```

#### 3.2 Modify callInternal

**File**: [chain/stmgr/call.go](chain/stmgr/call.go) (lines 97-295)

Add `skipSenderValidation bool` parameter:

```go
func (sm *StateManager) callInternal(
    ctx context.Context,
    msg *types.Message,
    priorMsgs []types.ChainMsg,
    ts *types.TipSet,
    stateCid cid.Cid,
    nvGetter rand.NetworkVersionGetter,
    checkGas bool,
    strategy execMessageStrategy,
    skipSenderValidation bool,  // NEW PARAMETER
) (*api.InvocResult, error) {
```

Modify the actor lookup (around line 219):

```go
fromActor, err := stTree.GetActor(msg.From)
if err != nil {
    if skipSenderValidation {
        // Create a synthetic actor for simulation
        fromActor = &types.Actor{
            Code:    builtin.EthAccountActorCodeID,  // Pretend it's an EthAccount
            Balance: abi.NewTokenAmount(1e18),       // Give it enough balance
            Nonce:   0,
        }
    } else {
        return nil, xerrors.Errorf("call raw get actor: %s", err)
    }
}
```

Modify the execution path (around line 240):

```go
if checkGas {
    if skipSenderValidation {
        // Use ApplyImplicitMessage which bypasses sender validation
        ret, err = vmi.ApplyImplicitMessage(ctx, msg)
        if err != nil && ret == nil {
            return nil, xerrors.Errorf("apply message failed: %w", err)
        }
        // Calculate approximate gas info
        gasInfo = MakeMsgGasCost(msg, ret)
    } else {
        // Existing signed message path...
        fromKey, err := sm.ResolveToDeterministicAddress(ctx, msg.From, ts)
        // ... rest of existing code
    }
}
```

#### 3.3 Modify applyMessage in gas.go

**File**: [node/impl/eth/gas.go](node/impl/eth/gas.go) (lines 265-296)

```go
func (e *ethGas) applyMessage(ctx context.Context, msg *types.Message, tsk types.TipSetKey) (res *api.InvocResult, err error) {
    // ... existing code ...

    // Use the new method that skips sender validation
    res, err = e.stateManager.ApplyOnStateWithGasSkipSenderValidation(ctx, st, msg, ts)
    if err != nil {
        return nil, xerrors.Errorf("ApplyWithGasOnState failed: %w", err)
    }
    // ...
}
```

### Option B: Always Use ApplyImplicitMessage for eth_call (Simpler)

A simpler but less granular approach:

**File**: [chain/stmgr/call.go](chain/stmgr/call.go)

In `callInternal`, when called from eth_call paths, always use:

```go
ret, err = vmi.ApplyImplicitMessage(ctx, msg)
```

instead of the `ApplyMessage` path with dummy signatures.

**Pros**: Minimal code changes
**Cons**: Less control, may affect gas estimation accuracy

---

## 4. Testing Without Chain Sync

### 4.1 Use the Integration Test Framework

Lotus has a complete integration test framework that runs **without syncing any chain**.

```bash
# Run existing ETH tests to verify setup
go test -v ./itests -run TestETH -count=1

# Run a specific test
go test -v ./itests -run TestEthCallFromContract -count=1
```

### 4.2 Create a New Test

**File**: `itests/eth_skip_sender_test.go`

```go
package itests

import (
    "context"
    "encoding/hex"
    "os"
    "testing"
    "time"

    "github.com/stretchr/testify/require"

    "github.com/filecoin-project/lotus/chain/types"
    "github.com/filecoin-project/lotus/chain/types/ethtypes"
    "github.com/filecoin-project/lotus/itests/kit"
)

// TestEthCallFromContractAddress verifies eth_call works when 'from' is a contract
func TestEthCallFromContractAddress(t *testing.T) {
    blockTime := 100 * time.Millisecond
    client, _, ens := kit.EnsembleMinimal(t, kit.MockProofs(), kit.ThroughRPC())
    ens.InterconnectAll().BeginMining(blockTime)

    ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
    defer cancel()

    // Deploy a contract
    contractHex, err := os.ReadFile("./contracts/SimpleCoin.hex")
    require.NoError(t, err)
    contract, err := hex.DecodeString(string(contractHex))
    require.NoError(t, err)

    // Create account and deploy
    _, ethAddr, deployer := client.EVM().NewAccount()
    kit.SendFunds(ctx, t, client, deployer, types.FromFil(1000))

    contractAddr := client.EVM().DeployContract(ctx, deployer, contract)

    // Test: eth_call with contract as sender (should work with skip sender check)
    blkParam := ethtypes.NewEthBlockNumberOrHashFromPredefined("latest")

    _, err = client.EthCall(ctx, ethtypes.EthCall{
        From: &contractAddr,  // Contract address as sender!
        To:   &ethAddr,
        Data: []byte{},
    }, blkParam)

    // This should succeed with the skip sender check implementation
    require.NoError(t, err, "eth_call from contract address should succeed")
}

// TestEthCallFromNonExistentAddress verifies eth_call works with non-existent sender
func TestEthCallFromNonExistentAddress(t *testing.T) {
    blockTime := 100 * time.Millisecond
    client, _, ens := kit.EnsembleMinimal(t, kit.MockProofs(), kit.ThroughRPC())
    ens.InterconnectAll().BeginMining(blockTime)

    ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
    defer cancel()

    // Create a random non-existent address
    nonExistentAddr := ethtypes.EthAddress{0xde, 0xad, 0xbe, 0xef, 0x00, 0x00, 0x00, 0x00,
                                            0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
                                            0x00, 0x00, 0x00, 0x01}

    // Create a real account to receive
    _, ethAddr, deployer := client.EVM().NewAccount()
    kit.SendFunds(ctx, t, client, deployer, types.FromFil(1000))

    // Test: eth_call with non-existent sender
    blkParam := ethtypes.NewEthBlockNumberOrHashFromPredefined("latest")

    _, err := client.EthCall(ctx, ethtypes.EthCall{
        From: &nonExistentAddr,  // Non-existent address!
        To:   &ethAddr,
        Data: []byte{},
    }, blkParam)

    require.NoError(t, err, "eth_call from non-existent address should succeed")
}

// TestEthEstimateGasFromContract verifies gas estimation works from contract address
func TestEthEstimateGasFromContract(t *testing.T) {
    blockTime := 100 * time.Millisecond
    client, _, ens := kit.EnsembleMinimal(t, kit.MockProofs(), kit.ThroughRPC())
    ens.InterconnectAll().BeginMining(blockTime)

    ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
    defer cancel()

    // Deploy a contract
    contractHex, err := os.ReadFile("./contracts/SimpleCoin.hex")
    require.NoError(t, err)
    contract, err := hex.DecodeString(string(contractHex))
    require.NoError(t, err)

    _, ethAddr, deployer := client.EVM().NewAccount()
    kit.SendFunds(ctx, t, client, deployer, types.FromFil(1000))

    contractAddr := client.EVM().DeployContract(ctx, deployer, contract)

    // Test: estimate gas with contract as sender
    blkParam := ethtypes.NewEthBlockNumberOrHashFromPredefined("latest")
    gasParams, err := json.Marshal(ethtypes.EthEstimateGasParams{
        Tx: ethtypes.EthCall{
            From: &contractAddr,
            To:   &ethAddr,
            Data: []byte{},
        },
        BlkParam: &blkParam,
    })
    require.NoError(t, err)

    gas, err := client.EthEstimateGas(ctx, gasParams)
    require.NoError(t, err, "gas estimation from contract should succeed")
    require.Greater(t, uint64(gas), uint64(0), "should return non-zero gas")
}
```

### 4.3 Run Tests

```bash
# Run just the skip sender tests
go test -v ./itests -run "TestEthCall.*Contract\|TestEthCall.*NonExistent\|TestEthEstimate.*Contract" -count=1

# Run with verbose output and race detection
go test -v -race ./itests -run TestEthCallFromContractAddress -count=1
```

---

## 5. File Change Summary (IMPLEMENTED)

| File                                                                | Change                                                                                                                                                                                                                                              | Status  |
| ------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| [chain/stmgr/call.go](../chain/stmgr/call.go)                       | Added `skipSenderValidation` parameter to `callInternal`, added `ApplyOnStateWithGasSkipSenderValidation` method, synthetic actor creation for non-existent senders, actor code modification for contract senders to make them appear as EthAccount | ✅ Done |
| [node/impl/eth/api.go](../node/impl/eth/api.go)                     | Added `ApplyOnStateWithGasSkipSenderValidation` to StateManager interface                                                                                                                                                                           | ✅ Done |
| [node/impl/eth/gas.go](../node/impl/eth/gas.go)                     | Updated `applyMessage` to use new skip validation method, added fallback gas estimation path for skip-sender scenarios                                                                                                                              | ✅ Done |
| [itests/eth_skip_sender_test.go](../itests/eth_skip_sender_test.go) | New integration tests for skip sender check feature (5 tests)                                                                                                                                                                                       | ✅ Done |

---

## 6. Build and Test Commands

```bash
# Build Lotus
make clean && make all

# Run unit tests for modified packages
go test -v ./chain/stmgr/... -count=1
go test -v ./node/impl/eth/... -count=1

# Run integration tests (no chain sync needed!)
go test -v ./itests -run TestEthCall -count=1

# Run specific new tests
go test -v ./itests -run TestEthCallFromContract -count=1
go test -v ./itests -run TestEthCallFromNonExistent -count=1

# Run all ETH-related tests
go test -v ./itests -run "TestETH|TestEth" -count=1

# Run with race detector
go test -v -race ./itests -run TestEthCallFromContract -count=1
```

---

## 7. Verification Checklist

After implementation, verify:

- [x] `eth_call` succeeds when `from` is a deployed contract address
- [x] `eth_call` succeeds when `from` is a non-existent address
- [x] `eth_call` succeeds when `from` is omitted (nil)
- [x] `eth_estimateGas` succeeds for all the above cases
- [x] Regular `eth_call` with normal EOA still works correctly
- [x] Gas estimation values are reasonable
- [x] No state mutations occur (these are simulations)
- [ ] Existing ETH API tests still pass
   ```bash
    go test -v ./itests -run "TestETH|TestEth" -count=1 2>&1 | grep -e "--- FAIL"                                                    
    --- FAIL: TestEthOpenRPCConformance (1.44s)
        --- FAIL: TestEthOpenRPCConformance/eth_call_latest (0.00s)
            --- FAIL: TestEthOpenRPCConformance/eth_call_latest/v1 (0.00s)
            --- FAIL: TestEthOpenRPCConformance/eth_call_latest/v2 (0.00s)
    ```

---

## 8. Comparison: Geth vs Lotus

| Feature                    | Geth       | Lotus (Current) | Lotus (After Change) |
| -------------------------- | ---------- | --------------- | -------------------- |
| eth_call from EOA          | Yes        | Yes             | Yes                  |
| eth_call from contract     | Yes        | No              | Yes                  |
| eth_call from non-existent | Yes        | No              | Yes                  |
| Skip nonce check           | Yes (flag) | Partial         | Yes                  |
| Skip EOA check             | Yes (flag) | No              | Yes                  |
| Zero gas price             | Yes        | Yes             | Yes                  |

---

## 9. Risks and Mitigations

1. **Gas estimation accuracy**: Using `ApplyImplicitMessage` may slightly affect gas estimates

   - Mitigation: Add gas overhead buffer in estimation

2. **State consistency**: Synthetic actors shouldn't persist

   - Mitigation: Verify with tests that state tree isn't modified

3. **Security**: Could allow simulation of privileged operations
   - Mitigation: This is expected behavior for simulations; real transactions still validate

---

## 10. References

- Geth source: `core/state_transition.go` - `SkipNonceChecks`, `SkipTransactionChecks`
- Lotus FVM: `chain/vm/fvm.go` - `ApplyMessage` vs `ApplyImplicitMessage`
- Lotus StateManager: `chain/stmgr/call.go` - `callInternal`
- Integration tests: `itests/kit/` - Test framework utilities
