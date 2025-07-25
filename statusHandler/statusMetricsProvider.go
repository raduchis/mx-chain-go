package statusHandler

import (
	"fmt"
	"strings"
	"sync"

	"github.com/multiversx/mx-chain-go/common"
)

// statusMetrics will handle displaying at /node/details all metrics already collected for other status handlers
type statusMetrics struct {
	uint64Metrics       map[string]uint64
	mutUint64Operations sync.RWMutex

	stringMetrics       map[string]string
	mutStringOperations sync.RWMutex

	int64Metrics       map[string]int64
	mutInt64Operations sync.RWMutex
}

// NewStatusMetrics will return an instance of the struct
func NewStatusMetrics() *statusMetrics {
	return &statusMetrics{
		uint64Metrics: make(map[string]uint64),
		stringMetrics: make(map[string]string),
		int64Metrics:  make(map[string]int64),
	}
}

// IsInterfaceNil returns true if there is no value under the interface
func (sm *statusMetrics) IsInterfaceNil() bool {
	return sm == nil
}

// Increment method increment a metric
func (sm *statusMetrics) Increment(key string) {
	sm.mutUint64Operations.Lock()
	defer sm.mutUint64Operations.Unlock()

	value, ok := sm.uint64Metrics[key]
	if !ok {
		return
	}

	value++
	sm.uint64Metrics[key] = value
}

// AddUint64 method increase a metric with a specific value
func (sm *statusMetrics) AddUint64(key string, val uint64) {
	sm.mutUint64Operations.Lock()
	defer sm.mutUint64Operations.Unlock()

	value, ok := sm.uint64Metrics[key]
	if !ok {
		return
	}

	value += val
	sm.uint64Metrics[key] = value
}

// Decrement method - decrement a metric
func (sm *statusMetrics) Decrement(key string) {
	sm.mutUint64Operations.Lock()
	defer sm.mutUint64Operations.Unlock()

	value, ok := sm.uint64Metrics[key]
	if !ok {
		return
	}

	if value == 0 {
		return
	}

	value--
	sm.uint64Metrics[key] = value
}

// SetInt64Value method - sets an int64 value for a key
func (sm *statusMetrics) SetInt64Value(key string, value int64) {
	sm.mutInt64Operations.Lock()
	defer sm.mutInt64Operations.Unlock()

	sm.int64Metrics[key] = value
}

// SetUInt64Value method - sets an uint64 value for a key
func (sm *statusMetrics) SetUInt64Value(key string, value uint64) {
	sm.mutUint64Operations.Lock()
	defer sm.mutUint64Operations.Unlock()

	sm.uint64Metrics[key] = value
}

// SetStringValue method - sets a string value for a key
func (sm *statusMetrics) SetStringValue(key string, value string) {
	sm.mutStringOperations.Lock()
	defer sm.mutStringOperations.Unlock()

	sm.stringMetrics[key] = value
}

// Close method - won't do anything
func (sm *statusMetrics) Close() {
}

// StatusMetricsMapWithoutP2P will return the non-p2p metrics in a map
func (sm *statusMetrics) StatusMetricsMapWithoutP2P() (map[string]interface{}, error) {
	metrics, err := sm.getMetricsWithoutP2P()
	if err != nil {
		return nil, err
	}

	// remove these metrics, since they are computed at call time and would return 0 otherwise
	delete(metrics, common.MetricNoncesPassedInCurrentEpoch)
	delete(metrics, common.MetricRoundsPassedInCurrentEpoch)

	// remove these metrics, since they are returned through the /node/bootstrapstatus endpoint
	delete(metrics, common.MetricTrieSyncNumReceivedBytes)
	delete(metrics, common.MetricTrieSyncNumProcessedNodes)

	return metrics, nil
}

func (sm *statusMetrics) getMetricsWithoutP2P() (map[string]interface{}, error) {
	return sm.getMetricsWithKeyFilterMutexProtected(func(input string) bool {
		return !strings.Contains(input, "_p2p_")
	}), nil
}

// StatusP2pMetricsMap will return the p2p metrics in a map
func (sm *statusMetrics) StatusP2pMetricsMap() (map[string]interface{}, error) {
	return sm.getMetricsWithKeyFilterMutexProtected(func(input string) bool {
		return strings.Contains(input, "_p2p_")
	}), nil
}

func (sm *statusMetrics) getMetricsWithKeyFilterMutexProtected(filterFunc func(input string) bool) map[string]interface{} {
	statusMetricsMap := make(map[string]interface{})

	sm.mutUint64Operations.RLock()
	for key, value := range sm.uint64Metrics {
		if !filterFunc(key) {
			continue
		}

		statusMetricsMap[key] = value
	}
	sm.mutUint64Operations.RUnlock()

	sm.mutStringOperations.RLock()
	for key, value := range sm.stringMetrics {
		if !filterFunc(key) {
			continue
		}

		statusMetricsMap[key] = value
	}
	sm.mutStringOperations.RUnlock()

	sm.mutInt64Operations.RLock()
	for key, value := range sm.int64Metrics {
		if !filterFunc(key) {
			continue
		}

		statusMetricsMap[key] = value
	}
	sm.mutInt64Operations.RUnlock()

	return statusMetricsMap
}

// StatusMetricsWithoutP2PPrometheusString returns the metrics in a string format which respects prometheus style
func (sm *statusMetrics) StatusMetricsWithoutP2PPrometheusString() (string, error) {
	metrics, err := sm.getMetricsWithoutP2P()
	if err != nil {
		return "", err
	}

	sm.mutUint64Operations.RLock()
	shardID := sm.uint64Metrics[common.MetricShardId]
	sm.mutUint64Operations.RUnlock()

	stringBuilder := strings.Builder{}
	for key, value := range metrics {
		sm.addPrometheusMetricToStringBuilder(&stringBuilder, shardID, key, value)
	}

	return stringBuilder.String(), nil
}

func (sm *statusMetrics) addPrometheusMetricToStringBuilder(builder *strings.Builder, shardID uint64, key string, value interface{}) {
	// only numeric values are accepted for prometheus. return if the value is not int64 or uint64
	switch value.(type) {
	case int64, uint64:
	default:
		return
	}

	if key == common.MetricNoncesPassedInCurrentEpoch {
		sm.mutUint64Operations.RLock()
		value = computeDelta(sm.uint64Metrics[common.MetricNonce], sm.uint64Metrics[common.MetricNonceAtEpochStart])
		sm.mutUint64Operations.RUnlock()
	}
	if key == common.MetricRoundsPassedInCurrentEpoch {
		sm.mutUint64Operations.RLock()
		value = computeDelta(sm.uint64Metrics[common.MetricCurrentRound], sm.uint64Metrics[common.MetricRoundAtEpochStart])
		sm.mutUint64Operations.RUnlock()
	}
	builder.WriteString(fmt.Sprintf("%s{%s=\"%d\"} %v\n", key, common.MetricShardId, shardID, value))
}

// EconomicsMetrics returns the economics related metrics
func (sm *statusMetrics) EconomicsMetrics() (map[string]interface{}, error) {
	economicsMetrics := make(map[string]interface{})

	sm.mutStringOperations.RLock()
	economicsMetrics[common.MetricTotalSupply] = sm.stringMetrics[common.MetricTotalSupply]
	economicsMetrics[common.MetricTotalFees] = sm.stringMetrics[common.MetricTotalFees]
	economicsMetrics[common.MetricDevRewardsInEpoch] = sm.stringMetrics[common.MetricDevRewardsInEpoch]
	economicsMetrics[common.MetricInflation] = sm.stringMetrics[common.MetricInflation]
	sm.mutStringOperations.RUnlock()

	sm.mutUint64Operations.RLock()
	economicsMetrics[common.MetricEpochForEconomicsData] = sm.uint64Metrics[common.MetricEpochForEconomicsData]
	sm.mutUint64Operations.RUnlock()

	return economicsMetrics, nil
}

// ConfigMetrics will return metrics related to current configuration
func (sm *statusMetrics) ConfigMetrics() (map[string]interface{}, error) {
	configMetrics := make(map[string]interface{})

	sm.mutUint64Operations.RLock()
	configMetrics[common.MetricNumShardsWithoutMetachain] = sm.uint64Metrics[common.MetricNumShardsWithoutMetachain]
	configMetrics[common.MetricNumNodesPerShard] = sm.uint64Metrics[common.MetricNumNodesPerShard]
	configMetrics[common.MetricNumMetachainNodes] = sm.uint64Metrics[common.MetricNumMetachainNodes]
	configMetrics[common.MetricShardConsensusGroupSize] = sm.uint64Metrics[common.MetricShardConsensusGroupSize]
	configMetrics[common.MetricMetaConsensusGroupSize] = sm.uint64Metrics[common.MetricMetaConsensusGroupSize]
	configMetrics[common.MetricMinGasPrice] = sm.uint64Metrics[common.MetricMinGasPrice]
	configMetrics[common.MetricMinGasLimit] = sm.uint64Metrics[common.MetricMinGasLimit]
	configMetrics[common.MetricExtraGasLimitGuardedTx] = sm.uint64Metrics[common.MetricExtraGasLimitGuardedTx]
	configMetrics[common.MetricExtraGasLimitRelayedTx] = sm.uint64Metrics[common.MetricExtraGasLimitRelayedTx]
	configMetrics[common.MetricMaxGasPerTransaction] = sm.uint64Metrics[common.MetricMaxGasPerTransaction]
	configMetrics[common.MetricRoundDuration] = sm.uint64Metrics[common.MetricRoundDuration]
	configMetrics[common.MetricStartTime] = sm.uint64Metrics[common.MetricStartTime]
	configMetrics[common.MetricDenomination] = sm.uint64Metrics[common.MetricDenomination]
	configMetrics[common.MetricMinTransactionVersion] = sm.uint64Metrics[common.MetricMinTransactionVersion]
	configMetrics[common.MetricRoundsPerEpoch] = sm.uint64Metrics[common.MetricRoundsPerEpoch]
	configMetrics[common.MetricGasPerDataByte] = sm.uint64Metrics[common.MetricGasPerDataByte]
	sm.mutUint64Operations.RUnlock()

	sm.mutStringOperations.RLock()
	configMetrics[common.MetricRewardsTopUpGradientPoint] = sm.stringMetrics[common.MetricRewardsTopUpGradientPoint]
	configMetrics[common.MetricChainId] = sm.stringMetrics[common.MetricChainId]
	configMetrics[common.MetricLatestTagSoftwareVersion] = sm.stringMetrics[common.MetricLatestTagSoftwareVersion]
	configMetrics[common.MetricTopUpFactor] = sm.stringMetrics[common.MetricTopUpFactor]
	configMetrics[common.MetricGasPriceModifier] = sm.stringMetrics[common.MetricGasPriceModifier]
	configMetrics[common.MetricAdaptivity] = sm.stringMetrics[common.MetricAdaptivity]
	configMetrics[common.MetricHysteresis] = sm.stringMetrics[common.MetricHysteresis]
	sm.mutStringOperations.RUnlock()

	return configMetrics, nil
}

// EnableEpochsMetrics will return metrics related to activation epochs
func (sm *statusMetrics) EnableEpochsMetrics() (map[string]interface{}, error) {
	enableEpochsMetrics := make(map[string]interface{})

	sm.mutUint64Operations.RLock()
	enableEpochsMetrics[common.MetricScDeployEnableEpoch] = sm.uint64Metrics[common.MetricScDeployEnableEpoch]
	enableEpochsMetrics[common.MetricBuiltInFunctionsEnableEpoch] = sm.uint64Metrics[common.MetricBuiltInFunctionsEnableEpoch]
	enableEpochsMetrics[common.MetricRelayedTransactionsEnableEpoch] = sm.uint64Metrics[common.MetricRelayedTransactionsEnableEpoch]
	enableEpochsMetrics[common.MetricPenalizedTooMuchGasEnableEpoch] = sm.uint64Metrics[common.MetricPenalizedTooMuchGasEnableEpoch]
	enableEpochsMetrics[common.MetricSwitchJailWaitingEnableEpoch] = sm.uint64Metrics[common.MetricSwitchJailWaitingEnableEpoch]
	enableEpochsMetrics[common.MetricSwitchHysteresisForMinNodesEnableEpoch] = sm.uint64Metrics[common.MetricSwitchHysteresisForMinNodesEnableEpoch]
	enableEpochsMetrics[common.MetricBelowSignedThresholdEnableEpoch] = sm.uint64Metrics[common.MetricBelowSignedThresholdEnableEpoch]
	enableEpochsMetrics[common.MetricTransactionSignedWithTxHashEnableEpoch] = sm.uint64Metrics[common.MetricTransactionSignedWithTxHashEnableEpoch]
	enableEpochsMetrics[common.MetricMetaProtectionEnableEpoch] = sm.uint64Metrics[common.MetricMetaProtectionEnableEpoch]
	enableEpochsMetrics[common.MetricAheadOfTimeGasUsageEnableEpoch] = sm.uint64Metrics[common.MetricAheadOfTimeGasUsageEnableEpoch]
	enableEpochsMetrics[common.MetricGasPriceModifierEnableEpoch] = sm.uint64Metrics[common.MetricGasPriceModifierEnableEpoch]
	enableEpochsMetrics[common.MetricRepairCallbackEnableEpoch] = sm.uint64Metrics[common.MetricRepairCallbackEnableEpoch]
	enableEpochsMetrics[common.MetricBlockGasAndFreeRecheckEnableEpoch] = sm.uint64Metrics[common.MetricBlockGasAndFreeRecheckEnableEpoch]
	enableEpochsMetrics[common.MetricStakingV2EnableEpoch] = sm.uint64Metrics[common.MetricStakingV2EnableEpoch]
	enableEpochsMetrics[common.MetricStakeEnableEpoch] = sm.uint64Metrics[common.MetricStakeEnableEpoch]
	enableEpochsMetrics[common.MetricDoubleKeyProtectionEnableEpoch] = sm.uint64Metrics[common.MetricDoubleKeyProtectionEnableEpoch]
	enableEpochsMetrics[common.MetricEsdtEnableEpoch] = sm.uint64Metrics[common.MetricEsdtEnableEpoch]
	enableEpochsMetrics[common.MetricGovernanceEnableEpoch] = sm.uint64Metrics[common.MetricGovernanceEnableEpoch]
	enableEpochsMetrics[common.MetricDelegationManagerEnableEpoch] = sm.uint64Metrics[common.MetricDelegationManagerEnableEpoch]
	enableEpochsMetrics[common.MetricDelegationSmartContractEnableEpoch] = sm.uint64Metrics[common.MetricDelegationSmartContractEnableEpoch]
	enableEpochsMetrics[common.MetricIncrementSCRNonceInMultiTransferEnableEpoch] = sm.uint64Metrics[common.MetricIncrementSCRNonceInMultiTransferEnableEpoch]
	enableEpochsMetrics[common.MetricBalanceWaitingListsEnableEpoch] = sm.uint64Metrics[common.MetricBalanceWaitingListsEnableEpoch]
	enableEpochsMetrics[common.MetricCorrectLastUnjailedEnableEpoch] = sm.uint64Metrics[common.MetricCorrectLastUnjailedEnableEpoch]
	enableEpochsMetrics[common.MetricReturnDataToLastTransferEnableEpoch] = sm.uint64Metrics[common.MetricReturnDataToLastTransferEnableEpoch]
	enableEpochsMetrics[common.MetricSenderInOutTransferEnableEpoch] = sm.uint64Metrics[common.MetricSenderInOutTransferEnableEpoch]
	enableEpochsMetrics[common.MetricRelayedTransactionsV2EnableEpoch] = sm.uint64Metrics[common.MetricRelayedTransactionsV2EnableEpoch]
	enableEpochsMetrics[common.MetricUnbondTokensV2EnableEpoch] = sm.uint64Metrics[common.MetricUnbondTokensV2EnableEpoch]
	enableEpochsMetrics[common.MetricSaveJailedAlwaysEnableEpoch] = sm.uint64Metrics[common.MetricSaveJailedAlwaysEnableEpoch]
	enableEpochsMetrics[common.MetricValidatorToDelegationEnableEpoch] = sm.uint64Metrics[common.MetricValidatorToDelegationEnableEpoch]
	enableEpochsMetrics[common.MetricReDelegateBelowMinCheckEnableEpoch] = sm.uint64Metrics[common.MetricReDelegateBelowMinCheckEnableEpoch]
	enableEpochsMetrics[common.MetricScheduledMiniBlocksEnableEpoch] = sm.uint64Metrics[common.MetricScheduledMiniBlocksEnableEpoch]
	enableEpochsMetrics[common.MetricESDTMultiTransferEnableEpoch] = sm.uint64Metrics[common.MetricESDTMultiTransferEnableEpoch]
	enableEpochsMetrics[common.MetricGlobalMintBurnDisableEpoch] = sm.uint64Metrics[common.MetricGlobalMintBurnDisableEpoch]
	enableEpochsMetrics[common.MetricESDTTransferRoleEnableEpoch] = sm.uint64Metrics[common.MetricESDTTransferRoleEnableEpoch]
	enableEpochsMetrics[common.MetricComputeRewardCheckpointEnableEpoch] = sm.uint64Metrics[common.MetricComputeRewardCheckpointEnableEpoch]
	enableEpochsMetrics[common.MetricSCRSizeInvariantCheckEnableEpoch] = sm.uint64Metrics[common.MetricSCRSizeInvariantCheckEnableEpoch]
	enableEpochsMetrics[common.MetricBackwardCompSaveKeyValueEnableEpoch] = sm.uint64Metrics[common.MetricBackwardCompSaveKeyValueEnableEpoch]
	enableEpochsMetrics[common.MetricESDTNFTCreateOnMultiShardEnableEpoch] = sm.uint64Metrics[common.MetricESDTNFTCreateOnMultiShardEnableEpoch]
	enableEpochsMetrics[common.MetricMetaESDTSetEnableEpoch] = sm.uint64Metrics[common.MetricMetaESDTSetEnableEpoch]
	enableEpochsMetrics[common.MetricAddTokensToDelegationEnableEpoch] = sm.uint64Metrics[common.MetricAddTokensToDelegationEnableEpoch]
	enableEpochsMetrics[common.MetricMultiESDTTransferFixOnCallBackOnEnableEpoch] = sm.uint64Metrics[common.MetricMultiESDTTransferFixOnCallBackOnEnableEpoch]
	enableEpochsMetrics[common.MetricOptimizeGasUsedInCrossMiniBlocksEnableEpoch] = sm.uint64Metrics[common.MetricOptimizeGasUsedInCrossMiniBlocksEnableEpoch]
	enableEpochsMetrics[common.MetricCorrectFirstQueuedEpoch] = sm.uint64Metrics[common.MetricCorrectFirstQueuedEpoch]
	enableEpochsMetrics[common.MetricCorrectJailedNotUnstakedEmptyQueueEpoch] = sm.uint64Metrics[common.MetricCorrectJailedNotUnstakedEmptyQueueEpoch]
	enableEpochsMetrics[common.MetricFixOOGReturnCodeEnableEpoch] = sm.uint64Metrics[common.MetricFixOOGReturnCodeEnableEpoch]
	enableEpochsMetrics[common.MetricRemoveNonUpdatedStorageEnableEpoch] = sm.uint64Metrics[common.MetricRemoveNonUpdatedStorageEnableEpoch]
	enableEpochsMetrics[common.MetricDeleteDelegatorAfterClaimRewardsEnableEpoch] = sm.uint64Metrics[common.MetricDeleteDelegatorAfterClaimRewardsEnableEpoch]
	enableEpochsMetrics[common.MetricOptimizeNFTStoreEnableEpoch] = sm.uint64Metrics[common.MetricOptimizeNFTStoreEnableEpoch]
	enableEpochsMetrics[common.MetricCreateNFTThroughExecByCallerEnableEpoch] = sm.uint64Metrics[common.MetricCreateNFTThroughExecByCallerEnableEpoch]
	enableEpochsMetrics[common.MetricStopDecreasingValidatorRatingWhenStuckEnableEpoch] = sm.uint64Metrics[common.MetricStopDecreasingValidatorRatingWhenStuckEnableEpoch]
	enableEpochsMetrics[common.MetricFrontRunningProtectionEnableEpoch] = sm.uint64Metrics[common.MetricFrontRunningProtectionEnableEpoch]
	enableEpochsMetrics[common.MetricIsPayableBySCEnableEpoch] = sm.uint64Metrics[common.MetricIsPayableBySCEnableEpoch]
	enableEpochsMetrics[common.MetricCleanUpInformativeSCRsEnableEpoch] = sm.uint64Metrics[common.MetricCleanUpInformativeSCRsEnableEpoch]
	enableEpochsMetrics[common.MetricStorageAPICostOptimizationEnableEpoch] = sm.uint64Metrics[common.MetricStorageAPICostOptimizationEnableEpoch]
	enableEpochsMetrics[common.MetricTransformToMultiShardCreateEnableEpoch] = sm.uint64Metrics[common.MetricTransformToMultiShardCreateEnableEpoch]
	enableEpochsMetrics[common.MetricESDTRegisterAndSetAllRolesEnableEpoch] = sm.uint64Metrics[common.MetricESDTRegisterAndSetAllRolesEnableEpoch]
	enableEpochsMetrics[common.MetricDoNotReturnOldBlockInBlockchainHookEnableEpoch] = sm.uint64Metrics[common.MetricDoNotReturnOldBlockInBlockchainHookEnableEpoch]
	enableEpochsMetrics[common.MetricAddFailedRelayedTxToInvalidMBsDisableEpoch] = sm.uint64Metrics[common.MetricAddFailedRelayedTxToInvalidMBsDisableEpoch]
	enableEpochsMetrics[common.MetricSCRSizeInvariantOnBuiltInResultEnableEpoch] = sm.uint64Metrics[common.MetricSCRSizeInvariantOnBuiltInResultEnableEpoch]
	enableEpochsMetrics[common.MetricCheckCorrectTokenIDForTransferRoleEnableEpoch] = sm.uint64Metrics[common.MetricCheckCorrectTokenIDForTransferRoleEnableEpoch]
	enableEpochsMetrics[common.MetricDisableExecByCallerEnableEpoch] = sm.uint64Metrics[common.MetricDisableExecByCallerEnableEpoch]
	enableEpochsMetrics[common.MetricFailExecutionOnEveryAPIErrorEnableEpoch] = sm.uint64Metrics[common.MetricFailExecutionOnEveryAPIErrorEnableEpoch]
	enableEpochsMetrics[common.MetricManagedCryptoAPIsEnableEpoch] = sm.uint64Metrics[common.MetricManagedCryptoAPIsEnableEpoch]
	enableEpochsMetrics[common.MetricRefactorContextEnableEpoch] = sm.uint64Metrics[common.MetricRefactorContextEnableEpoch]
	enableEpochsMetrics[common.MetricCheckFunctionArgumentEnableEpoch] = sm.uint64Metrics[common.MetricCheckFunctionArgumentEnableEpoch]
	enableEpochsMetrics[common.MetricCheckExecuteOnReadOnlyEnableEpoch] = sm.uint64Metrics[common.MetricCheckExecuteOnReadOnlyEnableEpoch]
	enableEpochsMetrics[common.MetricMiniBlockPartialExecutionEnableEpoch] = sm.uint64Metrics[common.MetricMiniBlockPartialExecutionEnableEpoch]
	enableEpochsMetrics[common.MetricESDTMetadataContinuousCleanupEnableEpoch] = sm.uint64Metrics[common.MetricESDTMetadataContinuousCleanupEnableEpoch]
	enableEpochsMetrics[common.MetricFixAsyncCallBackArgsListEnableEpoch] = sm.uint64Metrics[common.MetricFixAsyncCallBackArgsListEnableEpoch]
	enableEpochsMetrics[common.MetricFixOldTokenLiquidityEnableEpoch] = sm.uint64Metrics[common.MetricFixOldTokenLiquidityEnableEpoch]
	enableEpochsMetrics[common.MetricRuntimeMemStoreLimitEnableEpoch] = sm.uint64Metrics[common.MetricRuntimeMemStoreLimitEnableEpoch]
	enableEpochsMetrics[common.MetricRuntimeCodeSizeFixEnableEpoch] = sm.uint64Metrics[common.MetricRuntimeCodeSizeFixEnableEpoch]
	enableEpochsMetrics[common.MetricSetSenderInEeiOutputTransferEnableEpoch] = sm.uint64Metrics[common.MetricSetSenderInEeiOutputTransferEnableEpoch]
	enableEpochsMetrics[common.MetricRefactorPeersMiniBlocksEnableEpoch] = sm.uint64Metrics[common.MetricRefactorPeersMiniBlocksEnableEpoch]
	enableEpochsMetrics[common.MetricSCProcessorV2EnableEpoch] = sm.uint64Metrics[common.MetricSCProcessorV2EnableEpoch]
	enableEpochsMetrics[common.MetricMaxBlockchainHookCountersEnableEpoch] = sm.uint64Metrics[common.MetricMaxBlockchainHookCountersEnableEpoch]
	enableEpochsMetrics[common.MetricWipeSingleNFTLiquidityDecreaseEnableEpoch] = sm.uint64Metrics[common.MetricWipeSingleNFTLiquidityDecreaseEnableEpoch]
	enableEpochsMetrics[common.MetricAlwaysSaveTokenMetaDataEnableEpoch] = sm.uint64Metrics[common.MetricAlwaysSaveTokenMetaDataEnableEpoch]
	enableEpochsMetrics[common.MetricCleanUpInformativeSCRsEnableEpoch] = sm.uint64Metrics[common.MetricCleanUpInformativeSCRsEnableEpoch]
	enableEpochsMetrics[common.MetricSetGuardianEnableEpoch] = sm.uint64Metrics[common.MetricSetGuardianEnableEpoch]
	enableEpochsMetrics[common.MetricSetScToScLogEventEnableEpoch] = sm.uint64Metrics[common.MetricSetScToScLogEventEnableEpoch]
	enableEpochsMetrics[common.MetricRelayedNonceFixEnableEpoch] = sm.uint64Metrics[common.MetricRelayedNonceFixEnableEpoch]
	enableEpochsMetrics[common.MetricDeterministicSortOnValidatorsInfoEnableEpoch] = sm.uint64Metrics[common.MetricDeterministicSortOnValidatorsInfoEnableEpoch]
	enableEpochsMetrics[common.MetricKeepExecOrderOnCreatedSCRsEnableEpoch] = sm.uint64Metrics[common.MetricKeepExecOrderOnCreatedSCRsEnableEpoch]
	enableEpochsMetrics[common.MetricMultiClaimOnDelegationEnableEpoch] = sm.uint64Metrics[common.MetricMultiClaimOnDelegationEnableEpoch]
	enableEpochsMetrics[common.MetricChangeUsernameEnableEpoch] = sm.uint64Metrics[common.MetricChangeUsernameEnableEpoch]
	enableEpochsMetrics[common.MetricAutoBalanceDataTriesEnableEpoch] = sm.uint64Metrics[common.MetricAutoBalanceDataTriesEnableEpoch]
	enableEpochsMetrics[common.MetricMigrateDataTrieEnableEpoch] = sm.uint64Metrics[common.MetricMigrateDataTrieEnableEpoch]
	enableEpochsMetrics[common.MetricConsistentTokensValuesLengthCheckEnableEpoch] = sm.uint64Metrics[common.MetricConsistentTokensValuesLengthCheckEnableEpoch]
	enableEpochsMetrics[common.MetricFixDelegationChangeOwnerOnAccountEnableEpoch] = sm.uint64Metrics[common.MetricFixDelegationChangeOwnerOnAccountEnableEpoch]
	enableEpochsMetrics[common.MetricDynamicGasCostForDataTrieStorageLoadEnableEpoch] = sm.uint64Metrics[common.MetricDynamicGasCostForDataTrieStorageLoadEnableEpoch]
	enableEpochsMetrics[common.MetricNFTStopCreateEnableEpoch] = sm.uint64Metrics[common.MetricNFTStopCreateEnableEpoch]
	enableEpochsMetrics[common.MetricChangeOwnerAddressCrossShardThroughSCEnableEpoch] = sm.uint64Metrics[common.MetricChangeOwnerAddressCrossShardThroughSCEnableEpoch]
	enableEpochsMetrics[common.MetricFixGasRemainingForSaveKeyValueBuiltinFunctionEnableEpoch] = sm.uint64Metrics[common.MetricFixGasRemainingForSaveKeyValueBuiltinFunctionEnableEpoch]
	enableEpochsMetrics[common.MetricCurrentRandomnessOnSortingEnableEpoch] = sm.uint64Metrics[common.MetricCurrentRandomnessOnSortingEnableEpoch]
	enableEpochsMetrics[common.MetricStakeLimitsEnableEpoch] = sm.uint64Metrics[common.MetricStakeLimitsEnableEpoch]
	enableEpochsMetrics[common.MetricStakingV4Step1EnableEpoch] = sm.uint64Metrics[common.MetricStakingV4Step1EnableEpoch]
	enableEpochsMetrics[common.MetricStakingV4Step2EnableEpoch] = sm.uint64Metrics[common.MetricStakingV4Step2EnableEpoch]
	enableEpochsMetrics[common.MetricStakingV4Step3EnableEpoch] = sm.uint64Metrics[common.MetricStakingV4Step3EnableEpoch]
	enableEpochsMetrics[common.MetricCleanupAuctionOnLowWaitingListEnableEpoch] = sm.uint64Metrics[common.MetricCleanupAuctionOnLowWaitingListEnableEpoch]
	enableEpochsMetrics[common.MetricAlwaysMergeContextsInEEIEnableEpoch] = sm.uint64Metrics[common.MetricAlwaysMergeContextsInEEIEnableEpoch]
	enableEpochsMetrics[common.MetricDynamicESDTEnableEpoch] = sm.uint64Metrics[common.MetricDynamicESDTEnableEpoch]
	enableEpochsMetrics[common.MetricEGLDInMultiTransferEnableEpoch] = sm.uint64Metrics[common.MetricEGLDInMultiTransferEnableEpoch]
	enableEpochsMetrics[common.MetricCryptoOpcodesV2EnableEpoch] = sm.uint64Metrics[common.MetricCryptoOpcodesV2EnableEpoch]
	enableEpochsMetrics[common.MetricMultiESDTNFTTransferAndExecuteByUserEnableEpoch] = sm.uint64Metrics[common.MetricMultiESDTNFTTransferAndExecuteByUserEnableEpoch]
	enableEpochsMetrics[common.MetricFixRelayedMoveBalanceToNonPayableSCEnableEpoch] = sm.uint64Metrics[common.MetricFixRelayedMoveBalanceToNonPayableSCEnableEpoch]
	enableEpochsMetrics[common.MetricRelayedTransactionsV3EnableEpoch] = sm.uint64Metrics[common.MetricRelayedTransactionsV3EnableEpoch]
	enableEpochsMetrics[common.MetricRelayedTransactionsV3FixESDTTransferEnableEpoch] = sm.uint64Metrics[common.MetricRelayedTransactionsV3FixESDTTransferEnableEpoch]
	enableEpochsMetrics[common.MetricCheckBuiltInCallOnTransferValueAndFailEnableRound] = sm.uint64Metrics[common.MetricCheckBuiltInCallOnTransferValueAndFailEnableRound]
	enableEpochsMetrics[common.MetricMaskVMInternalDependenciesErrorsEnableEpoch] = sm.uint64Metrics[common.MetricMaskVMInternalDependenciesErrorsEnableEpoch]
	enableEpochsMetrics[common.MetricFixBackTransferOPCODEEnableEpoch] = sm.uint64Metrics[common.MetricFixBackTransferOPCODEEnableEpoch]
	enableEpochsMetrics[common.MetricValidationOnGobDecodeEnableEpoch] = sm.uint64Metrics[common.MetricValidationOnGobDecodeEnableEpoch]
	enableEpochsMetrics[common.MetricBarnardOpcodesEnableEpoch] = sm.uint64Metrics[common.MetricBarnardOpcodesEnableEpoch]
	enableEpochsMetrics[common.MetricAutomaticActivationOfNodesDisableEpoch] = sm.uint64Metrics[common.MetricAutomaticActivationOfNodesDisableEpoch]
	enableEpochsMetrics[common.MetricFixGetBalanceEnableEpoch] = sm.uint64Metrics[common.MetricFixGetBalanceEnableEpoch]

	numNodesChangeConfig := sm.uint64Metrics[common.MetricMaxNodesChangeEnableEpoch+"_count"]

	nodesChangeConfig := make([]map[string]interface{}, 0)
	for i := uint64(0); i < numNodesChangeConfig; i++ {
		maxNodesChangeConfig := make(map[string]interface{})

		epochEnable := fmt.Sprintf("%s%d%s", common.MetricMaxNodesChangeEnableEpoch, i, common.EpochEnableSuffix)
		maxNodesChangeConfig[common.MetricEpochEnable] = sm.uint64Metrics[epochEnable]

		maxNumNodes := fmt.Sprintf("%s%d%s", common.MetricMaxNodesChangeEnableEpoch, i, common.MaxNumNodesSuffix)
		maxNodesChangeConfig[common.MetricMaxNumNodes] = sm.uint64Metrics[maxNumNodes]

		nodesToShufflePerShard := fmt.Sprintf("%s%d%s", common.MetricMaxNodesChangeEnableEpoch, i, common.NodesToShufflePerShardSuffix)
		maxNodesChangeConfig[common.MetricNodesToShufflePerShard] = sm.uint64Metrics[nodesToShufflePerShard]

		nodesChangeConfig = append(nodesChangeConfig, maxNodesChangeConfig)
	}
	enableEpochsMetrics[common.MetricMaxNodesChangeEnableEpoch] = nodesChangeConfig
	sm.mutUint64Operations.RUnlock()

	return enableEpochsMetrics, nil
}

// NetworkMetrics will return metrics related to current configuration
func (sm *statusMetrics) NetworkMetrics() (map[string]interface{}, error) {
	networkMetrics := make(map[string]interface{})

	sm.saveUint64NetworkMetricsInMap(networkMetrics)
	sm.saveStringNetworkMetricsInMap(networkMetrics)

	return networkMetrics, nil
}

func (sm *statusMetrics) saveUint64NetworkMetricsInMap(networkMetrics map[string]interface{}) {
	sm.mutUint64Operations.RLock()
	defer sm.mutUint64Operations.RUnlock()

	currentRound := sm.uint64Metrics[common.MetricCurrentRound]
	roundNumberAtEpochStart := sm.uint64Metrics[common.MetricRoundAtEpochStart]

	currentNonce := sm.uint64Metrics[common.MetricNonce]
	nonceAtEpochStart := sm.uint64Metrics[common.MetricNonceAtEpochStart]
	networkMetrics[common.MetricNonce] = currentNonce
	networkMetrics[common.MetricBlockTimestamp] = sm.uint64Metrics[common.MetricBlockTimestamp]
	networkMetrics[common.MetricBlockTimestampMs] = sm.uint64Metrics[common.MetricBlockTimestampMs]
	networkMetrics[common.MetricHighestFinalBlock] = sm.uint64Metrics[common.MetricHighestFinalBlock]
	networkMetrics[common.MetricCurrentRound] = currentRound
	networkMetrics[common.MetricRoundAtEpochStart] = roundNumberAtEpochStart
	networkMetrics[common.MetricNonceAtEpochStart] = nonceAtEpochStart
	networkMetrics[common.MetricEpochNumber] = sm.uint64Metrics[common.MetricEpochNumber]
	networkMetrics[common.MetricRoundsPerEpoch] = sm.uint64Metrics[common.MetricRoundsPerEpoch]
	networkMetrics[common.MetricRoundsPassedInCurrentEpoch] = computeDelta(currentRound, roundNumberAtEpochStart)
	networkMetrics[common.MetricNoncesPassedInCurrentEpoch] = computeDelta(currentNonce, nonceAtEpochStart)
}

func (sm *statusMetrics) saveStringNetworkMetricsInMap(networkMetrics map[string]interface{}) {
	sm.mutStringOperations.RLock()
	defer sm.mutStringOperations.RUnlock()

	crossCheckValue := sm.stringMetrics[common.MetricCrossCheckBlockHeight]
	if len(crossCheckValue) > 0 {
		networkMetrics[common.MetricCrossCheckBlockHeight] = crossCheckValue
	}
}

// RatingsMetrics will return metrics related to current configuration
func (sm *statusMetrics) RatingsMetrics() (map[string]interface{}, error) {
	ratingsMetrics := make(map[string]interface{})

	sm.mutUint64Operations.RLock()
	ratingsMetrics[common.MetricRatingsGeneralStartRating] = sm.uint64Metrics[common.MetricRatingsGeneralStartRating]
	ratingsMetrics[common.MetricRatingsGeneralMaxRating] = sm.uint64Metrics[common.MetricRatingsGeneralMaxRating]
	ratingsMetrics[common.MetricRatingsGeneralMinRating] = sm.uint64Metrics[common.MetricRatingsGeneralMinRating]

	numSelectionChances := sm.uint64Metrics[common.MetricRatingsGeneralSelectionChances+"_count"]
	selectionChances := make([]map[string]uint64, 0)
	for i := uint64(0); i < numSelectionChances; i++ {
		selectionChance := make(map[string]uint64)
		maxThresholdStr := fmt.Sprintf("%s%d%s", common.MetricRatingsGeneralSelectionChances, i, common.SelectionChancesMaxThresholdSuffix)
		selectionChance[common.MetricSelectionChancesMaxThreshold] = sm.uint64Metrics[maxThresholdStr]
		chancePercentStr := fmt.Sprintf("%s%d%s", common.MetricRatingsGeneralSelectionChances, i, common.SelectionChancesChancePercentSuffix)
		selectionChance[common.MetricSelectionChancesChancePercent] = sm.uint64Metrics[chancePercentStr]
		selectionChances = append(selectionChances, selectionChance)
	}
	ratingsMetrics[common.MetricRatingsGeneralSelectionChances] = selectionChances

	ratingsMetrics[common.MetricRatingsShardChainHoursToMaxRatingFromStartRating] = sm.uint64Metrics[common.MetricRatingsShardChainHoursToMaxRatingFromStartRating]
	ratingsMetrics[common.MetricRatingsMetaChainHoursToMaxRatingFromStartRating] = sm.uint64Metrics[common.MetricRatingsMetaChainHoursToMaxRatingFromStartRating]
	ratingsMetrics[common.MetricRatingsPeerHonestyDecayUpdateIntervalInSeconds] = sm.uint64Metrics[common.MetricRatingsPeerHonestyDecayUpdateIntervalInSeconds]
	sm.mutUint64Operations.RUnlock()

	sm.mutStringOperations.RLock()
	ratingsMetrics[common.MetricRatingsGeneralSignedBlocksThreshold] = sm.stringMetrics[common.MetricRatingsGeneralSignedBlocksThreshold]
	ratingsMetrics[common.MetricRatingsShardChainProposerValidatorImportance] = sm.stringMetrics[common.MetricRatingsShardChainProposerValidatorImportance]
	ratingsMetrics[common.MetricRatingsShardChainProposerDecreaseFactor] = sm.stringMetrics[common.MetricRatingsShardChainProposerDecreaseFactor]
	ratingsMetrics[common.MetricRatingsShardChainValidatorDecreaseFactor] = sm.stringMetrics[common.MetricRatingsShardChainValidatorDecreaseFactor]
	ratingsMetrics[common.MetricRatingsShardChainConsecutiveMissedBlocksPenalty] = sm.stringMetrics[common.MetricRatingsShardChainConsecutiveMissedBlocksPenalty]
	ratingsMetrics[common.MetricRatingsMetaChainProposerValidatorImportance] = sm.stringMetrics[common.MetricRatingsMetaChainProposerValidatorImportance]
	ratingsMetrics[common.MetricRatingsMetaChainProposerDecreaseFactor] = sm.stringMetrics[common.MetricRatingsMetaChainProposerDecreaseFactor]
	ratingsMetrics[common.MetricRatingsMetaChainValidatorDecreaseFactor] = sm.stringMetrics[common.MetricRatingsMetaChainValidatorDecreaseFactor]
	ratingsMetrics[common.MetricRatingsMetaChainConsecutiveMissedBlocksPenalty] = sm.stringMetrics[common.MetricRatingsMetaChainConsecutiveMissedBlocksPenalty]
	ratingsMetrics[common.MetricRatingsPeerHonestyDecayCoefficient] = sm.stringMetrics[common.MetricRatingsPeerHonestyDecayCoefficient]
	ratingsMetrics[common.MetricRatingsPeerHonestyMaxScore] = sm.stringMetrics[common.MetricRatingsPeerHonestyMaxScore]
	ratingsMetrics[common.MetricRatingsPeerHonestyMinScore] = sm.stringMetrics[common.MetricRatingsPeerHonestyMinScore]
	ratingsMetrics[common.MetricRatingsPeerHonestyBadPeerThreshold] = sm.stringMetrics[common.MetricRatingsPeerHonestyBadPeerThreshold]
	ratingsMetrics[common.MetricRatingsPeerHonestyUnitValue] = sm.stringMetrics[common.MetricRatingsPeerHonestyUnitValue]
	sm.mutStringOperations.RUnlock()

	return ratingsMetrics, nil
}

// BootstrapMetrics returns the metrics available during bootstrap
func (sm *statusMetrics) BootstrapMetrics() (map[string]interface{}, error) {
	bootstrapMetrics := make(map[string]interface{})

	sm.mutUint64Operations.RLock()
	bootstrapMetrics[common.MetricTrieSyncNumReceivedBytes] = sm.uint64Metrics[common.MetricTrieSyncNumReceivedBytes]
	bootstrapMetrics[common.MetricTrieSyncNumProcessedNodes] = sm.uint64Metrics[common.MetricTrieSyncNumProcessedNodes]
	bootstrapMetrics[common.MetricShardId] = sm.uint64Metrics[common.MetricShardId]
	sm.mutUint64Operations.RUnlock()

	sm.mutStringOperations.RLock()
	bootstrapMetrics[common.MetricGatewayMetricsEndpoint] = sm.stringMetrics[common.MetricGatewayMetricsEndpoint]
	sm.mutStringOperations.RUnlock()

	return bootstrapMetrics, nil
}

func computeDelta(biggerNum uint64, lowerNum uint64) uint64 {
	if biggerNum >= lowerNum {
		return biggerNum - lowerNum
	}

	return 0
}
