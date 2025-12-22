# Skip Sender Validation Implementation - Audit Report

**Audit Date**: 2025-12-17
**Auditor**: Claude Code
**Version**: Based on v1.34.3 with uncommitted changes

---

## 1. Executive Summary

This audit covers the implementation of "skip sender validation" functionality for `eth_call` and `eth_estimateGas` in Lotus. The changes enable Geth-compatible behavior where these RPC methods can simulate transactions from contract addresses or non-existent addresses.

### Risk Assessment

| Category          | Risk Level   | Notes                                                                                      |
| ----------------- | ------------ | ------------------------------------------------------------------------------------------ |
| Security          | **LOW**      | Changes are isolated to simulation code paths; no impact on consensus or real transactions |
| State Integrity   | **LOW**      | Uses buffered blockstore; changes are ephemeral                                            |
| API Compatibility | **POSITIVE** | Increases Geth compatibility                                                               |
| Regression Risk   | **MEDIUM**   | Multiple callers of modified functions need verification                                   |

---

## 2. Modified Files Summary

| File                                                                | Lines Changed | Type                 |
| ------------------------------------------------------------------- | ------------- | -------------------- |
| [chain/vm/vmi.go](../chain/vm/vmi.go)                               | +3            | Interface definition |
| [chain/vm/vm.go](../chain/vm/vm.go)                                 | +120          | LegacyVM impl        |
| [chain/vm/fvm.go](../chain/vm/fvm.go)                               | +10           | FVM impl             |
| [chain/vm/execution.go](../chain/vm/execution.go)                   | +7            | Executor wrapper     |
| [chain/stmgr/call.go](../chain/stmgr/call.go)                       | +30           | Core implementation  |
| [node/impl/eth/api.go](../node/impl/eth/api.go)                     | +4            | Interface update     |
| [node/impl/eth/gas.go](../node/impl/eth/gas.go)                     | +15 / -50     | Simplified gas est.  |
| [node/impl/gasutils/gasutils.go](../node/impl/gasutils/gasutils.go) | +80           | New gas functions    |
| [itests/eth_skip_sender_test.go](../itests/eth_skip_sender_test.go) | +200          | Tests                |

---

## 3. Detailed Analysis: chain/stmgr/call.go

### 3.1 Function Signature Change

```go
// BEFORE
func (sm *StateManager) callInternal(ctx context.Context, msg *types.Message,
    priorMsgs []types.ChainMsg, ts *types.TipSet, stateCid cid.Cid,
    nvGetter rand.NetworkVersionGetter, checkGas bool,
    strategy execMessageStrategy) (*api.InvocResult, error)

// AFTER
func (sm *StateManager) callInternal(ctx context.Context, msg *types.Message,
    priorMsgs []types.ChainMsg, ts *types.TipSet, stateCid cid.Cid,
    nvGetter rand.NetworkVersionGetter, checkGas bool,
    strategy execMessageStrategy, skipSenderValidation bool) (*api.InvocResult, error)
```

**Impact**: Internal function, but all callers must be updated to pass the new parameter.

### 3.2 New Public Method

```go
func (sm *StateManager) ApplyOnStateWithGasSkipSenderValidation(
    ctx context.Context, stateCid cid.Cid, msg *types.Message, ts *types.TipSet,
) (*api.InvocResult, error)
```

**Purpose**: Provides skip-sender-validation capability for eth_call/eth_estimateGas simulation.

### 3.3 Callers of callInternal (All Updated)

| Caller                                    | skipSenderValidation | Status                  |
| ----------------------------------------- | -------------------- | ----------------------- |
| `CallOnState`                             | `false`              | OK - unchanged behavior |
| `ApplyOnStateWithGas`                     | `false`              | OK - unchanged behavior |
| `ApplyOnStateWithGasSkipSenderValidation` | `true`               | NEW - skip validation   |
| `CallWithGas`                             | `false`              | OK - unchanged behavior |
| `CallAtStateAndVersion`                   | `false`              | OK - unchanged behavior |

### 3.4 New Code Paths When skipSenderValidation=true

#### Path A: Non-existent Sender Address (lines 231-274)

```go
if err != nil {  // GetActor failed
    if !skipSenderValidation {
        return nil, xerrors.Errorf("call raw get actor: %s", err)
    }
    // Create synthetic EthAccount actor
    // Register with Init actor via RegisterNewAddress
    // Set actor in state tree
    // Flush and recreate VM
}
```

**Analysis**:

- Creates a synthetic actor with `manifest.EthAccountKey` code
- Assigns 1e18 (1 FIL) balance for simulation
- Uses `RegisterNewAddress` to get an ID address from Init actor
- Flushes state tree and recreates VM with new state

**Concern**: `RegisterNewAddress` mutates the Init actor state. However, this is done on the buffered blockstore (line 170: `blockstore.NewTieredBstore(..., blockstore.NewMemorySync())`), so changes are ephemeral.

#### Path B: Existing Contract Address (lines 275-325)

```go
} else if skipSenderValidation {
    // Check if actor is already a valid sender type
    // If not (e.g., EVM contract), modify code to EthAccount
}
```

**Analysis**:

- Only triggers if actor exists but is NOT already EthAccount or Account
- Preserves original nonce and balance
- Ensures minimum 1e18 balance for simulation
- Flushes and recreates VM

### 3.5 Execution Path Change

```go
if checkGas {
    if skipSenderValidation {
        ret, err = vmi.ApplyImplicitMessage(ctx, msg)  // Bypasses FVM sender validation
        // Handle nil GasCosts
    } else {
        // Original path: ApplyMessage with signature
    }
}
```

**Analysis**:

- Uses `ApplyImplicitMessage` instead of `ApplyMessage`
- `ApplyImplicitMessage` does not validate sender type in FVM
- Added null check for `ret.GasCosts` since implicit messages may not populate it

---

## 4. Detailed Analysis: node/impl/eth/gas.go

### 4.1 Change in applyMessage

```go
// BEFORE
res, err = e.stateManager.ApplyOnStateWithGas(ctx, st, msg, ts)

// AFTER
res, err = e.stateManager.ApplyOnStateWithGasSkipSenderValidation(ctx, st, msg, ts)
```

**Impact**: ALL calls to `applyMessage` now use skip-sender-validation. This affects:

- `EthCall` (line 264)
- `EthEstimateGas` error recovery path (line 218)

### 4.2 EthEstimateGas Fallback Path Enhancement

```go
// BEFORE: Always returned error if GasEstimateMessageGas failed
// AFTER: If applyMessage succeeds, return gas estimate with 25% margin

if res != nil && res.MsgRct != nil {
    gasUsed := res.MsgRct.GasUsed
    gasWithMargin := gasUsed + gasUsed/4
    return ethtypes.EthUint64(gasWithMargin), nil
}
```

**Analysis**: This enables gas estimation for skip-sender scenarios where `GasEstimateMessageGas` fails but simulation succeeds.

---

## 5. Interface Changes: node/impl/eth/api.go

```go
type StateManager interface {
    // ... existing methods ...
    ApplyOnStateWithGasSkipSenderValidation(ctx context.Context, stateCid cid.Cid,
        msg *types.Message, ts *types.TipSet) (*api.InvocResult, error)
}
```

**Impact**: Any code that implements this interface must add the new method.

### 5.1 Interface Implementations

| Implementation        | Location            | Status  |
| --------------------- | ------------------- | ------- |
| `*stmgr.StateManager` | chain/stmgr/call.go | Updated |

**Note**: The interface is only used within the eth module; `stmgr.StateManager` is the only production implementation.

---

## 6. Affected Downstream Functions

### 6.1 Direct Consumers of Modified Code

| Function         | File                 | Impact                                                       |
| ---------------- | -------------------- | ------------------------------------------------------------ |
| `EthCall`        | node/impl/eth/gas.go | **CHANGED** - Uses `ApplyOnStateWithGasSkipSenderValidation` |
| `EthEstimateGas` | node/impl/eth/gas.go | **CHANGED** - Uses `GasEstimateGasLimitSkipSenderValidation` |

### 6.2 New Functions (Skip Sender Validation Path)

| Function                                     | File                           | Purpose                          |
| -------------------------------------------- | ------------------------------ | -------------------------------- |
| `CallWithGasSkipSenderValidation`            | chain/stmgr/call.go            | Gas estimation with skip sender  |
| `ApplyOnStateWithGasSkipSenderValidation`    | chain/stmgr/call.go            | eth_call with skip sender        |
| `GasEstimateCallWithGasSkipSenderValidation` | node/impl/gasutils/gasutils.go | Wrapper for skip sender call     |
| `GasEstimateGasLimitSkipSenderValidation`    | node/impl/gasutils/gasutils.go | Gas limit estimation skip sender |
| `ApplyMessageSkipSenderValidation`           | chain/vm/vmi.go (interface)    | VM interface method              |

### 6.3 Unchanged Consumers (via callInternal with skipSenderValidation=false)

| Function                             | File                             | skipSenderValidation               |
| ------------------------------------ | -------------------------------- | ---------------------------------- |
| `StateManager.Call`                  | chain/stmgr/call.go              | false (via CallOnState)            |
| `StateManager.CallWithGas`           | chain/stmgr/call.go              | false                              |
| `StateManager.CallAtStateAndVersion` | chain/stmgr/call.go              | false                              |
| `GasEstimateCallWithGas`             | node/impl/gasutils/gasutils.go   | false (via CallWithGas)            |
| `GasEstimateGasLimit`                | node/impl/gasutils/gasutils.go   | false (via GasEstimateCallWithGas) |
| `lotus-shed gas-estimation`          | cmd/lotus-shed/gas-estimation.go | false                              |

---

## 7. Security Analysis

### 7.1 State Isolation

**Verified**: All state modifications occur on a buffered blockstore:

```go
// line 170 in call.go
buffStore := blockstore.NewTieredBstore(sm.cs.StateBlockstore(), blockstore.NewMemorySync())
```

The `NewMemorySync()` tier ensures:

- Writes go to memory, not persisted storage
- No consensus-critical state is modified
- Changes are discarded after the call completes

### 7.2 Actor Code Substitution

**Concern**: Contract actors have their code changed to EthAccount.

```go
modifiedActor := &types.Actor{
    Code:    ethAcctCid,  // Changed from original code
    Head:    vm.EmptyObjectCid,
    // ...
}
```

**Mitigation**:

- Only affects simulation state
- Necessary for FVM to accept the actor as a valid sender
- Original state is not modified

### 7.3 Implicit Message Execution

**Concern**: `ApplyImplicitMessage` bypasses normal validation.

**Mitigation**:

- Only used for RPC simulation, not for real transaction processing
- This is the intended behavior for `eth_call`/`eth_estimateGas`
- Matches Geth's `SkipAccountChecks` behavior

---

## 8. Potential Issues Identified

### 8.1 GasCosts Nil Check

**Issue**: `ApplyImplicitMessage` may return `ret` with `GasCosts == nil`.

**Fix Applied**: Added null check at line 254:

```go
if ret != nil && ret.GasCosts != nil {
    gasInfo = MakeMsgGasCost(msg, ret)
}
```

**Recommendation**: This fix is correct and sufficient.

### 8.2 Error Handling in Actor Creation

**Issue**: Multiple error paths during synthetic actor creation.

**Review**:

- `actorstypes.VersionForNetwork` - proper error handling
- `actors.GetActorCodeID` - returns `ok` bool, checked
- `RegisterNewAddress` - proper error handling
- `SetActor` - proper error handling
- `Flush` - proper error handling
- `newVM` - proper error handling

**Verdict**: Error handling is comprehensive.

---

## 9. Test Coverage

### 9.1 New Tests (itests/eth_skip_sender_test.go)

| Test                                       | Scenario                                 | Status |
| ------------------------------------------ | ---------------------------------------- | ------ |
| `TestEthCallFromContractAddress`           | eth_call from deployed contract          | PASS   |
| `TestEthCallFromNonExistentAddress`        | eth_call from address that doesn't exist | PASS   |
| `TestEthEstimateGasFromContract`           | eth_estimateGas from contract            | PASS   |
| `TestEthEstimateGasFromNonExistentAddress` | eth_estimateGas from non-existent        | PASS   |
| `TestEthCallFromEOAStillWorks`             | Regression test for normal EOA           | PASS   |

### 9.2 Existing Tests That May Be Affected

| Test File                   | Functions Tested        | Risk                      |
| --------------------------- | ----------------------- | ------------------------- |
| `itests/eth_api_test.go`    | EthCall, EthEstimateGas | Medium - behavior changed |
| `itests/fevm_test.go`       | Various EVM tests       | Low                       |
| `chain/stmgr/forks_test.go` | CallWithGas             | Low - uses false flag     |

### 9.3 Recommended Additional Testing

1. **Run full ETH test suite**:

   ```bash
   go test -v ./itests -run "TestETH|TestEth" -count=1
   ```

2. **Run FEVM tests**:

   ```bash
   go test -v ./itests -run TestFEVM -count=1
   ```

3. **Verify no regression in gas estimation**:
   ```bash
   go test -v ./itests -run "TestGas" -count=1
   ```

---

## 10. Conclusion

The implementation is **sound and safe for production use** with the following caveats:

1. **Run full test suite** before merging to ensure no regressions
2. The changes are properly isolated to simulation code paths
3. State modifications are ephemeral (buffered blockstore)
4. Error handling is comprehensive
5. Behavior matches Geth's implementation

### Approval Status: **CONDITIONALLY APPROVED**

Condition: Full ETH API test suite passes without regressions.

---

## Appendix A: Dependency Graph

```
eth_call (RPC)
    │
    └─▶ ethGas.EthCall
            │
            └─▶ ethGas.applyMessage
                    │
                    └─▶ StateManager.ApplyOnStateWithGasSkipSenderValidation
                            │
                            └─▶ StateManager.callInternal (skipSenderValidation=true)
                                    │
                                    └─▶ vmi.ApplyMessageSkipSenderValidation
                                            │
                                            ├─▶ LegacyVM: skips nonce/balance checks
                                            └─▶ FVM: delegates to ApplyMessage

eth_estimateGas (RPC)
    │
    └─▶ ethGas.EthEstimateGas
            │
            └─▶ gasutils.GasEstimateGasLimitSkipSenderValidation
                    │
                    └─▶ gasutils.GasEstimateCallWithGasSkipSenderValidation
                            │
                            └─▶ StateManager.CallWithGasSkipSenderValidation
                                    │
                                    └─▶ StateManager.callInternal (skipSenderValidation=true)
                                            │
                                            └─▶ vmi.ApplyMessageSkipSenderValidation
```
