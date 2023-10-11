package processing

import (
	"errors"
	"fmt"

	"github.com/multiversx/mx-chain-core-go/core"
	dataBlock "github.com/multiversx/mx-chain-core-go/data/block"
	"github.com/multiversx/mx-chain-go/common"
	"github.com/multiversx/mx-chain-go/config"
	"github.com/multiversx/mx-chain-go/dataRetriever"
	debugFactory "github.com/multiversx/mx-chain-go/debug/factory"
	"github.com/multiversx/mx-chain-go/epochStart"
	metachainEpochStart "github.com/multiversx/mx-chain-go/epochStart/metachain"
	customErrors "github.com/multiversx/mx-chain-go/errors"
	mainFactory "github.com/multiversx/mx-chain-go/factory"
	"github.com/multiversx/mx-chain-go/genesis"
	"github.com/multiversx/mx-chain-go/outport"
	processOutport "github.com/multiversx/mx-chain-go/outport/process"
	factoryOutportProvider "github.com/multiversx/mx-chain-go/outport/process/factory"
	"github.com/multiversx/mx-chain-go/process"
	"github.com/multiversx/mx-chain-go/process/block"
	"github.com/multiversx/mx-chain-go/process/block/cutoff"
	"github.com/multiversx/mx-chain-go/process/block/postprocess"
	"github.com/multiversx/mx-chain-go/process/block/preprocess"
	"github.com/multiversx/mx-chain-go/process/coordinator"
	"github.com/multiversx/mx-chain-go/process/factory"
	"github.com/multiversx/mx-chain-go/process/factory/metachain"
	"github.com/multiversx/mx-chain-go/process/factory/shard"
	"github.com/multiversx/mx-chain-go/process/rewardTransaction"
	"github.com/multiversx/mx-chain-go/process/scToProtocol"
	"github.com/multiversx/mx-chain-go/process/smartContract"
	"github.com/multiversx/mx-chain-go/process/smartContract/builtInFunctions"
	"github.com/multiversx/mx-chain-go/process/smartContract/hooks"
	"github.com/multiversx/mx-chain-go/process/smartContract/hooks/counters"
	"github.com/multiversx/mx-chain-go/process/smartContract/processProxy"
	"github.com/multiversx/mx-chain-go/process/smartContract/processorV2"
	"github.com/multiversx/mx-chain-go/process/smartContract/scrCommon"
	"github.com/multiversx/mx-chain-go/process/throttle"
	"github.com/multiversx/mx-chain-go/process/transaction"
	"github.com/multiversx/mx-chain-go/sharding"
	"github.com/multiversx/mx-chain-go/state"
	"github.com/multiversx/mx-chain-go/state/syncer"
	"github.com/multiversx/mx-chain-go/storage/txcache"
	"github.com/multiversx/mx-chain-go/vm"
	logger "github.com/multiversx/mx-chain-logger-go"
	vmcommon "github.com/multiversx/mx-chain-vm-common-go"
	"github.com/multiversx/mx-chain-vm-common-go/parsers"
)

type blockProcessorAndVmFactories struct {
	blockProcessor         process.BlockProcessor
	vmFactoryForProcessing process.VirtualMachinesContainerFactory
}

func (pcf *processComponentsFactory) newBlockProcessor(
	requestHandler process.RequestHandler,
	forkDetector process.ForkDetector,
	epochStartTrigger epochStart.TriggerHandler,
	bootStorer process.BootStorer,
	validatorStatisticsProcessor process.ValidatorStatisticsProcessor,
	headerValidator process.HeaderConstructionValidator,
	blockTracker process.BlockTracker,
	pendingMiniBlocksHandler process.PendingMiniBlocksHandler,
	wasmVMChangeLocker common.Locker,
	scheduledTxsExecutionHandler process.ScheduledTxsExecutionHandler,
	processedMiniBlocksTracker process.ProcessedMiniBlocksTracker,
	receiptsRepository mainFactory.ReceiptsRepository,
	blockCutoffProcessingHandler cutoff.BlockProcessingCutoffHandler,
	missingTrieNodesNotifier common.MissingTrieNodesNotifier,
) (*blockProcessorAndVmFactories, error) {
	shardCoordinator := pcf.bootstrapComponents.ShardCoordinator()
	if shardCoordinator.SelfId() < shardCoordinator.NumberOfShards() {
		return pcf.newShardBlockProcessor(
			requestHandler,
			forkDetector,
			validatorStatisticsProcessor,
			epochStartTrigger,
			bootStorer,
			headerValidator,
			blockTracker,
			pcf.smartContractParser,
			wasmVMChangeLocker,
			scheduledTxsExecutionHandler,
			processedMiniBlocksTracker,
			receiptsRepository,
			blockCutoffProcessingHandler,
			missingTrieNodesNotifier,
		)
	}
	if shardCoordinator.SelfId() == core.MetachainShardId {
		return pcf.newMetaBlockProcessor(
			requestHandler,
			forkDetector,
			validatorStatisticsProcessor,
			epochStartTrigger,
			bootStorer,
			headerValidator,
			blockTracker,
			pendingMiniBlocksHandler,
			wasmVMChangeLocker,
			scheduledTxsExecutionHandler,
			processedMiniBlocksTracker,
			receiptsRepository,
			blockCutoffProcessingHandler,
		)
	}

	return nil, errors.New("could not create block processor")
}

var log = logger.GetOrCreate("factory")

func (pcf *processComponentsFactory) newShardBlockProcessor(
	requestHandler process.RequestHandler,
	forkDetector process.ForkDetector,
	validatorStatisticsProcessor process.ValidatorStatisticsProcessor,
	epochStartTrigger process.EpochStartTriggerHandler,
	bootStorer process.BootStorer,
	headerValidator process.HeaderConstructionValidator,
	blockTracker process.BlockTracker,
	smartContractParser genesis.InitialSmartContractParser,
	wasmVMChangeLocker common.Locker,
	scheduledTxsExecutionHandler process.ScheduledTxsExecutionHandler,
	processedMiniBlocksTracker process.ProcessedMiniBlocksTracker,
	receiptsRepository mainFactory.ReceiptsRepository,
	blockProcessingCutoffHandler cutoff.BlockProcessingCutoffHandler,
	missingTrieNodesNotifier common.MissingTrieNodesNotifier,
) (*blockProcessorAndVmFactories, error) {
	argsParser := smartContract.NewArgumentParser()

	esdtTransferParser, err := parsers.NewESDTTransferParser(pcf.coreData.InternalMarshalizer())
	if err != nil {
		return nil, err
	}

	mapDNSAddresses, err := smartContractParser.GetDeployedSCAddresses(genesis.DNSType)
	if err != nil {
		return nil, err
	}

	builtInFuncFactory, err := pcf.createBuiltInFunctionContainer(pcf.state.AccountsAdapter(), mapDNSAddresses)
	if err != nil {
		return nil, err
	}

	log.Debug("blockProcessorCreator: enable epoch for sc deploy", "epoch", pcf.epochConfig.EnableEpochs.SCDeployEnableEpoch)
	log.Debug("blockProcessorCreator: enable epoch for ahead of time gas usage", "epoch", pcf.epochConfig.EnableEpochs.AheadOfTimeGasUsageEnableEpoch)
	log.Debug("blockProcessorCreator: enable epoch for repair callback", "epoch", pcf.epochConfig.EnableEpochs.RepairCallbackEnableEpoch)

	var shardFactory VMFactoryCreator

	shardFactory = &ShardVMFactoryImpl{}
	//TODO: refactor this with creators and runTypeComponents
	if pcf.chainRunType == common.ChainRunTypeSovereign {
		shardFactory = &SovereignVMFactoryImpl{
			shardVMFactory: ShardVMFactoryImpl{},
			metaVMFactory:  MetaVMFactoryImpl{},
		}
	}
	vmFactory, err := shardFactory.CreateVMFactory(
		VMFactoryCreatorArgs{
			Accounts:              pcf.state.AccountsAdapter(),
			Notifier:              missingTrieNodesNotifier,
			BuiltInFuncs:          builtInFuncFactory.BuiltInFunctionContainer(),
			EsdtTransferParser:    esdtTransferParser,
			WasmVMChangeLocker:    wasmVMChangeLocker,
			ConfigSCStorage:       pcf.config.SmartContractsStorage,
			NftStorageHandler:     builtInFuncFactory.NFTStorageHandler(),
			GlobalSettingsHandler: builtInFuncFactory.ESDTGlobalSettingsHandler(),
			GasSchedule:           pcf.gasSchedule,
			CoreData:              pcf.coreData,
			Data:                  pcf.data,
			ShardCoordinator:      pcf.bootstrapComponents.ShardCoordinator(),
			WorkingDir:            pcf.flagsConfig.WorkingDir,
			ChainRunType:          pcf.chainRunType,
			VmConfig:              pcf.config.VirtualMachine.Execution,
		},
	)
	if err != nil {
		return nil, err
	}

	vmContainer, err := vmFactory.Create()
	if err != nil {
		return nil, err
	}

	err = shardFactory.AddAdditionalVMs(vmContainer)
	if err != nil {
		return nil, err
	}

	err = builtInFuncFactory.SetPayableHandler(vmFactory.BlockChainHookImpl())
	if err != nil {
		return nil, err
	}

	argsFactory := shard.ArgsNewIntermediateProcessorsContainerFactory{
		ShardCoordinator:        pcf.bootstrapComponents.ShardCoordinator(),
		Marshalizer:             pcf.coreData.InternalMarshalizer(),
		Hasher:                  pcf.coreData.Hasher(),
		PubkeyConverter:         pcf.coreData.AddressPubKeyConverter(),
		Store:                   pcf.data.StorageService(),
		PoolsHolder:             pcf.data.Datapool(),
		EconomicsFee:            pcf.coreData.EconomicsData(),
		EnableEpochsHandler:     pcf.coreData.EnableEpochsHandler(),
		TxExecutionOrderHandler: pcf.txExecutionOrderHandler,
	}

	interimProcFactory, err := shard.NewIntermediateProcessorsContainerFactory(argsFactory)
	if err != nil {
		return nil, err
	}

	interimProcContainer, err := interimProcFactory.Create()
	if err != nil {
		return nil, err
	}

	scForwarder, err := interimProcContainer.Get(dataBlock.SmartContractResultBlock)
	if err != nil {
		return nil, err
	}

	receiptTxInterim, err := interimProcContainer.Get(dataBlock.ReceiptBlock)
	if err != nil {
		return nil, err
	}

	badTxInterim, err := interimProcContainer.Get(dataBlock.InvalidBlock)
	if err != nil {
		return nil, err
	}

	argsTxTypeHandler := coordinator.ArgNewTxTypeHandler{
		PubkeyConverter:     pcf.coreData.AddressPubKeyConverter(),
		ShardCoordinator:    pcf.bootstrapComponents.ShardCoordinator(),
		BuiltInFunctions:    builtInFuncFactory.BuiltInFunctionContainer(),
		ArgumentParser:      parsers.NewCallArgsParser(),
		ESDTTransferParser:  esdtTransferParser,
		EnableEpochsHandler: pcf.coreData.EnableEpochsHandler(),
	}
	txTypeHandler, err := coordinator.NewTxTypeHandler(argsTxTypeHandler)
	if err != nil {
		return nil, err
	}

	gasHandler, err := preprocess.NewGasComputation(
		pcf.coreData.EconomicsData(),
		txTypeHandler,
		pcf.coreData.EnableEpochsHandler(),
	)
	if err != nil {
		return nil, err
	}

	txFeeHandler, err := postprocess.NewFeeAccumulator()
	if err != nil {
		return nil, err
	}

	argsNewScProcessor := scrCommon.ArgsNewSmartContractProcessor{
		VmContainer:         vmContainer,
		ArgsParser:          argsParser,
		Hasher:              pcf.coreData.Hasher(),
		Marshalizer:         pcf.coreData.InternalMarshalizer(),
		AccountsDB:          pcf.state.AccountsAdapter(),
		BlockChainHook:      vmFactory.BlockChainHookImpl(),
		BuiltInFunctions:    builtInFuncFactory.BuiltInFunctionContainer(),
		PubkeyConv:          pcf.coreData.AddressPubKeyConverter(),
		ShardCoordinator:    pcf.bootstrapComponents.ShardCoordinator(),
		ScrForwarder:        scForwarder,
		TxFeeHandler:        txFeeHandler,
		EconomicsFee:        pcf.coreData.EconomicsData(),
		GasHandler:          gasHandler,
		GasSchedule:         pcf.gasSchedule,
		TxLogsProcessor:     pcf.txLogsProcessor,
		TxTypeHandler:       txTypeHandler,
		IsGenesisProcessing: false,
		BadTxForwarder:      badTxInterim,
		EnableRoundsHandler: pcf.coreData.EnableRoundsHandler(),
		EnableEpochsHandler: pcf.coreData.EnableEpochsHandler(),
		VMOutputCacher:      txcache.NewDisabledCache(),
		WasmVMChangeLocker:  wasmVMChangeLocker,
	}

	scProcessorProxy, err := processorV2.CreateSCRProcessor(pcf.chainRunType, argsNewScProcessor)
	if err != nil {
		return nil, err
	}

	rewardsTxProcessor, err := rewardTransaction.NewRewardTxProcessor(
		pcf.state.AccountsAdapter(),
		pcf.coreData.AddressPubKeyConverter(),
		pcf.bootstrapComponents.ShardCoordinator(),
	)
	if err != nil {
		return nil, err
	}

	argsNewTxProcessor := transaction.ArgsNewTxProcessor{
		Accounts:            pcf.state.AccountsAdapter(),
		Hasher:              pcf.coreData.Hasher(),
		PubkeyConv:          pcf.coreData.AddressPubKeyConverter(),
		Marshalizer:         pcf.coreData.InternalMarshalizer(),
		SignMarshalizer:     pcf.coreData.TxMarshalizer(),
		ShardCoordinator:    pcf.bootstrapComponents.ShardCoordinator(),
		ScProcessor:         scProcessorProxy,
		TxFeeHandler:        txFeeHandler,
		TxTypeHandler:       txTypeHandler,
		EconomicsFee:        pcf.coreData.EconomicsData(),
		ReceiptForwarder:    receiptTxInterim,
		BadTxForwarder:      badTxInterim,
		ArgsParser:          argsParser,
		ScrForwarder:        scForwarder,
		EnableRoundsHandler: pcf.coreData.EnableRoundsHandler(),
		EnableEpochsHandler: pcf.coreData.EnableEpochsHandler(),
		GuardianChecker:     pcf.bootstrapComponents.GuardedAccountHandler(),
		TxVersionChecker:    pcf.coreData.TxVersionChecker(),
		TxLogsProcessor:     pcf.txLogsProcessor,
	}
	transactionProcessor, err := transaction.NewTxProcessor(argsNewTxProcessor)
	if err != nil {
		return nil, errors.New("could not create transaction processor: " + err.Error())
	}

	scheduledTxsExecutionHandler.SetTransactionProcessor(transactionProcessor)

	blockSizeThrottler, err := throttle.NewBlockSizeThrottle(
		pcf.config.BlockSizeThrottleConfig.MinSizeInBytes,
		pcf.config.BlockSizeThrottleConfig.MaxSizeInBytes,
	)
	if err != nil {
		return nil, err
	}

	blockSizeComputationHandler, err := preprocess.NewBlockSizeComputation(
		pcf.coreData.InternalMarshalizer(),
		blockSizeThrottler,
		pcf.config.BlockSizeThrottleConfig.MaxSizeInBytes,
	)
	if err != nil {
		return nil, err
	}

	balanceComputationHandler, err := preprocess.NewBalanceComputation()
	if err != nil {
		return nil, err
	}

	argsPreProc := shard.ArgPreProcessorsContainerFactory{
		ShardCoordinator:             pcf.bootstrapComponents.ShardCoordinator(),
		Store:                        pcf.data.StorageService(),
		Marshaller:                   pcf.coreData.InternalMarshalizer(),
		Hasher:                       pcf.coreData.Hasher(),
		DataPool:                     pcf.data.Datapool(),
		PubkeyConverter:              pcf.coreData.AddressPubKeyConverter(),
		Accounts:                     pcf.state.AccountsAdapter(),
		RequestHandler:               requestHandler,
		TxProcessor:                  transactionProcessor,
		ScProcessor:                  scProcessorProxy,
		ScResultProcessor:            scProcessorProxy,
		RewardsTxProcessor:           rewardsTxProcessor,
		EconomicsFee:                 pcf.coreData.EconomicsData(),
		GasHandler:                   gasHandler,
		BlockTracker:                 blockTracker,
		BlockSizeComputation:         blockSizeComputationHandler,
		BalanceComputation:           balanceComputationHandler,
		EnableEpochsHandler:          pcf.coreData.EnableEpochsHandler(),
		TxTypeHandler:                txTypeHandler,
		ScheduledTxsExecutionHandler: scheduledTxsExecutionHandler,
		ProcessedMiniBlocksTracker:   processedMiniBlocksTracker,
		ChainRunType:                 pcf.chainRunType,
		TxExecutionOrderHandler:      pcf.txExecutionOrderHandler,
		TxPreProcessorCreator:        pcf.txPreprocessorCreator,
	}
	preProcFactory, err := shard.NewPreProcessorsContainerFactory(argsPreProc)
	if err != nil {
		return nil, err
	}

	preProcContainer, err := preProcFactory.Create()
	if err != nil {
		return nil, err
	}

	argsDetector := coordinator.ArgsPrintDoubleTransactionsDetector{
		Marshaller:          pcf.coreData.InternalMarshalizer(),
		Hasher:              pcf.coreData.Hasher(),
		EnableEpochsHandler: pcf.coreData.EnableEpochsHandler(),
	}
	doubleTransactionsDetector, err := coordinator.NewPrintDoubleTransactionsDetector(argsDetector)
	if err != nil {
		return nil, err
	}

	argsTransactionCoordinator := coordinator.ArgTransactionCoordinator{
		Hasher:                       pcf.coreData.Hasher(),
		Marshalizer:                  pcf.coreData.InternalMarshalizer(),
		ShardCoordinator:             pcf.bootstrapComponents.ShardCoordinator(),
		Accounts:                     pcf.state.AccountsAdapter(),
		MiniBlockPool:                pcf.data.Datapool().MiniBlocks(),
		RequestHandler:               requestHandler,
		PreProcessors:                preProcContainer,
		InterProcessors:              interimProcContainer,
		GasHandler:                   gasHandler,
		FeeHandler:                   txFeeHandler,
		BlockSizeComputation:         blockSizeComputationHandler,
		BalanceComputation:           balanceComputationHandler,
		EconomicsFee:                 pcf.coreData.EconomicsData(),
		TxTypeHandler:                txTypeHandler,
		TransactionsLogProcessor:     pcf.txLogsProcessor,
		EnableEpochsHandler:          pcf.coreData.EnableEpochsHandler(),
		ScheduledTxsExecutionHandler: scheduledTxsExecutionHandler,
		DoubleTransactionsDetector:   doubleTransactionsDetector,
		ProcessedMiniBlocksTracker:   processedMiniBlocksTracker,
		TxExecutionOrderHandler:      pcf.txExecutionOrderHandler,
	}
	txCoordinator, err := pcf.createTransactionCoordinator(argsTransactionCoordinator)
	if err != nil {
		return nil, err
	}

	outportDataProvider, err := pcf.createOutportDataProvider(txCoordinator, gasHandler)
	if err != nil {
		return nil, err
	}

	scheduledTxsExecutionHandler.SetTransactionCoordinator(txCoordinator)

	accountsDb := make(map[state.AccountsDbIdentifier]state.AccountsAdapter)
	accountsDb[state.UserAccountsState] = pcf.state.AccountsAdapter()
	accountsDb[state.PeerAccountsState] = pcf.state.PeerAccounts()

	argumentsBaseProcessor := block.ArgBaseProcessor{
		CoreComponents:               pcf.coreData,
		DataComponents:               pcf.data,
		BootstrapComponents:          pcf.bootstrapComponents,
		StatusComponents:             pcf.statusComponents,
		StatusCoreComponents:         pcf.statusCoreComponents,
		Config:                       pcf.config,
		PrefsConfig:                  pcf.prefConfigs,
		Version:                      pcf.flagsConfig.Version,
		AccountsDB:                   accountsDb,
		ForkDetector:                 forkDetector,
		NodesCoordinator:             pcf.nodesCoordinator,
		RequestHandler:               requestHandler,
		BlockChainHook:               vmFactory.BlockChainHookImpl(),
		TxCoordinator:                txCoordinator,
		EpochStartTrigger:            epochStartTrigger,
		HeaderValidator:              headerValidator,
		BootStorer:                   bootStorer,
		BlockTracker:                 blockTracker,
		FeeHandler:                   txFeeHandler,
		BlockSizeThrottler:           blockSizeThrottler,
		HistoryRepository:            pcf.historyRepo,
		VMContainersFactory:          vmFactory,
		VmContainer:                  vmContainer,
		GasHandler:                   gasHandler,
		ScheduledTxsExecutionHandler: scheduledTxsExecutionHandler,
		ProcessedMiniBlocksTracker:   processedMiniBlocksTracker,
		ReceiptsRepository:           receiptsRepository,
		OutportDataProvider:          outportDataProvider,
		BlockProcessingCutoffHandler: blockProcessingCutoffHandler,
		ManagedPeersHolder:           pcf.crypto.ManagedPeersHolder(),
	}

	blockProcessor, err := pcf.createBlockProcessor(argumentsBaseProcessor, validatorStatisticsProcessor)
	if err != nil {
		return nil, err
	}

	return &blockProcessorAndVmFactories{
		blockProcessor:         blockProcessor,
		vmFactoryForProcessing: vmFactory,
	}, nil
}

func (pcf *processComponentsFactory) createTransactionCoordinator(
	argsTransactionCoordinator coordinator.ArgTransactionCoordinator,
) (process.TransactionCoordinator, error) {
	transactionCoordinator, err := coordinator.NewTransactionCoordinator(argsTransactionCoordinator)
	if err != nil {
		return nil, err
	}

	switch pcf.chainRunType {
	case common.ChainRunTypeRegular:
		return transactionCoordinator, nil
	case common.ChainRunTypeSovereign:
		return coordinator.NewSovereignChainTransactionCoordinator(transactionCoordinator)
	default:
		return nil, fmt.Errorf("%w type %v", customErrors.ErrUnimplementedChainRunType, pcf.chainRunType)
	}
}

func (pcf *processComponentsFactory) createBlockProcessor(
	argumentsBaseProcessor block.ArgBaseProcessor,
	validatorStatisticsProcessor process.ValidatorStatisticsProcessor,
) (process.BlockProcessor, error) {
	argShardProcessor := block.ArgShardProcessor{
		ArgBaseProcessor: argumentsBaseProcessor,
	}

	shardProcessor, err := block.NewShardProcessor(argShardProcessor)
	if err != nil {
		return nil, errors.New("could not create shard block processor: " + err.Error())
	}

	err = pcf.attachProcessDebugger(shardProcessor, pcf.config.Debug.Process)
	if err != nil {
		return nil, err
	}

	switch pcf.chainRunType {
	case common.ChainRunTypeRegular:
		return shardProcessor, nil
	case common.ChainRunTypeSovereign:
		return block.NewSovereignChainBlockProcessor(
			shardProcessor,
			validatorStatisticsProcessor,
		)
	default:
		return nil, fmt.Errorf("%w type %v", customErrors.ErrUnimplementedChainRunType, pcf.chainRunType)
	}
}

func (pcf *processComponentsFactory) newMetaBlockProcessor(
	requestHandler process.RequestHandler,
	forkDetector process.ForkDetector,
	validatorStatisticsProcessor process.ValidatorStatisticsProcessor,
	epochStartTrigger process.EpochStartTriggerHandler,
	bootStorer process.BootStorer,
	headerValidator process.HeaderConstructionValidator,
	blockTracker process.BlockTracker,
	pendingMiniBlocksHandler process.PendingMiniBlocksHandler,
	wasmVMChangeLocker common.Locker,
	scheduledTxsExecutionHandler process.ScheduledTxsExecutionHandler,
	processedMiniBlocksTracker process.ProcessedMiniBlocksTracker,
	receiptsRepository mainFactory.ReceiptsRepository,
	blockProcessingCutoffhandler cutoff.BlockProcessingCutoffHandler,
) (*blockProcessorAndVmFactories, error) {
	builtInFuncFactory, err := pcf.createBuiltInFunctionContainer(pcf.state.AccountsAdapter(), make(map[string]struct{}))
	if err != nil {
		return nil, err
	}

	argsParser := smartContract.NewArgumentParser()

	var metaFactory VMFactoryCreator
	metaFactory = &MetaVMFactoryImpl{}

	vmFactory, err := metaFactory.CreateVMFactory(
		VMFactoryCreatorArgs{
			Accounts:              pcf.state.AccountsAdapter(),
			BuiltInFuncs:          builtInFuncFactory.BuiltInFunctionContainer(),
			ConfigSCStorage:       pcf.config.SmartContractsStorage,
			NftStorageHandler:     builtInFuncFactory.NFTStorageHandler(),
			GlobalSettingsHandler: builtInFuncFactory.ESDTGlobalSettingsHandler(),
			State:                 pcf.state,
			CoreData:              pcf.coreData,
			Data:                  pcf.data,
			ShardCoordinator:      pcf.bootstrapComponents.ShardCoordinator(),
			WorkingDir:            pcf.flagsConfig.WorkingDir,
			ChainRunType:          pcf.chainRunType,
			VmConfig:              pcf.config.VirtualMachine.Execution,
			Crypto:                pcf.crypto,
		},
	)
	if err != nil {
		return nil, err
	}

	vmContainer, err := vmFactory.Create()
	if err != nil {
		return nil, err
	}

	argsFactory := metachain.ArgsNewIntermediateProcessorsContainerFactory{
		ShardCoordinator:        pcf.bootstrapComponents.ShardCoordinator(),
		Marshalizer:             pcf.coreData.InternalMarshalizer(),
		Hasher:                  pcf.coreData.Hasher(),
		PubkeyConverter:         pcf.coreData.AddressPubKeyConverter(),
		Store:                   pcf.data.StorageService(),
		PoolsHolder:             pcf.data.Datapool(),
		EconomicsFee:            pcf.coreData.EconomicsData(),
		EnableEpochsHandler:     pcf.coreData.EnableEpochsHandler(),
		TxExecutionOrderHandler: pcf.txExecutionOrderHandler,
	}

	interimProcFactory, err := metachain.NewIntermediateProcessorsContainerFactory(argsFactory)
	if err != nil {
		return nil, err
	}

	interimProcContainer, err := interimProcFactory.Create()
	if err != nil {
		return nil, err
	}

	scForwarder, err := interimProcContainer.Get(dataBlock.SmartContractResultBlock)
	if err != nil {
		return nil, err
	}

	badTxForwarder, err := interimProcContainer.Get(dataBlock.InvalidBlock)
	if err != nil {
		return nil, err
	}

	esdtTransferParser, err := parsers.NewESDTTransferParser(pcf.coreData.InternalMarshalizer())
	if err != nil {
		return nil, err
	}

	argsTxTypeHandler := coordinator.ArgNewTxTypeHandler{
		PubkeyConverter:     pcf.coreData.AddressPubKeyConverter(),
		ShardCoordinator:    pcf.bootstrapComponents.ShardCoordinator(),
		BuiltInFunctions:    builtInFuncFactory.BuiltInFunctionContainer(),
		ArgumentParser:      parsers.NewCallArgsParser(),
		ESDTTransferParser:  esdtTransferParser,
		EnableEpochsHandler: pcf.coreData.EnableEpochsHandler(),
	}
	txTypeHandler, err := coordinator.NewTxTypeHandler(argsTxTypeHandler)
	if err != nil {
		return nil, err
	}

	gasHandler, err := preprocess.NewGasComputation(
		pcf.coreData.EconomicsData(),
		txTypeHandler,
		pcf.coreData.EnableEpochsHandler(),
	)
	if err != nil {
		return nil, err
	}

	txFeeHandler, err := postprocess.NewFeeAccumulator()
	if err != nil {
		return nil, err
	}

	enableEpochs := pcf.epochConfig.EnableEpochs
	argsNewScProcessor := scrCommon.ArgsNewSmartContractProcessor{
		VmContainer:         vmContainer,
		ArgsParser:          argsParser,
		Hasher:              pcf.coreData.Hasher(),
		Marshalizer:         pcf.coreData.InternalMarshalizer(),
		AccountsDB:          pcf.state.AccountsAdapter(),
		BlockChainHook:      vmFactory.BlockChainHookImpl(),
		BuiltInFunctions:    builtInFuncFactory.BuiltInFunctionContainer(),
		PubkeyConv:          pcf.coreData.AddressPubKeyConverter(),
		ShardCoordinator:    pcf.bootstrapComponents.ShardCoordinator(),
		ScrForwarder:        scForwarder,
		TxFeeHandler:        txFeeHandler,
		EconomicsFee:        pcf.coreData.EconomicsData(),
		TxTypeHandler:       txTypeHandler,
		GasHandler:          gasHandler,
		GasSchedule:         pcf.gasSchedule,
		TxLogsProcessor:     pcf.txLogsProcessor,
		IsGenesisProcessing: false,
		BadTxForwarder:      badTxForwarder,
		EnableRoundsHandler: pcf.coreData.EnableRoundsHandler(),
		EnableEpochsHandler: pcf.coreData.EnableEpochsHandler(),
		VMOutputCacher:      txcache.NewDisabledCache(),
		WasmVMChangeLocker:  wasmVMChangeLocker,
	}

	scProcessorProxy, err := processProxy.NewSmartContractProcessorProxy(argsNewScProcessor, pcf.epochNotifier)
	if err != nil {
		return nil, err
	}

	argsNewMetaTxProcessor := transaction.ArgsNewMetaTxProcessor{
		Hasher:              pcf.coreData.Hasher(),
		Marshalizer:         pcf.coreData.InternalMarshalizer(),
		Accounts:            pcf.state.AccountsAdapter(),
		PubkeyConv:          pcf.coreData.AddressPubKeyConverter(),
		ShardCoordinator:    pcf.bootstrapComponents.ShardCoordinator(),
		ScProcessor:         scProcessorProxy,
		TxTypeHandler:       txTypeHandler,
		EconomicsFee:        pcf.coreData.EconomicsData(),
		EnableEpochsHandler: pcf.coreData.EnableEpochsHandler(),
		GuardianChecker:     pcf.bootstrapComponents.GuardedAccountHandler(),
		TxVersionChecker:    pcf.coreData.TxVersionChecker(),
	}

	transactionProcessor, err := transaction.NewMetaTxProcessor(argsNewMetaTxProcessor)
	if err != nil {
		return nil, errors.New("could not create transaction processor: " + err.Error())
	}

	scheduledTxsExecutionHandler.SetTransactionProcessor(transactionProcessor)

	blockSizeThrottler, err := throttle.NewBlockSizeThrottle(pcf.config.BlockSizeThrottleConfig.MinSizeInBytes, pcf.config.BlockSizeThrottleConfig.MaxSizeInBytes)
	if err != nil {
		return nil, err
	}

	blockSizeComputationHandler, err := preprocess.NewBlockSizeComputation(
		pcf.coreData.InternalMarshalizer(),
		blockSizeThrottler,
		pcf.config.BlockSizeThrottleConfig.MaxSizeInBytes,
	)
	if err != nil {
		return nil, err
	}

	balanceComputationHandler, err := preprocess.NewBalanceComputation()
	if err != nil {
		return nil, err
	}

	argsPreProc := metachain.ArgPreProcessorsContainerFactory{
		ShardCoordinator:             pcf.bootstrapComponents.ShardCoordinator(),
		Store:                        pcf.data.StorageService(),
		Marshaller:                   pcf.coreData.InternalMarshalizer(),
		Hasher:                       pcf.coreData.Hasher(),
		DataPool:                     pcf.data.Datapool(),
		Accounts:                     pcf.state.AccountsAdapter(),
		RequestHandler:               requestHandler,
		TxProcessor:                  transactionProcessor,
		ScResultProcessor:            scProcessorProxy,
		EconomicsFee:                 pcf.coreData.EconomicsData(),
		GasHandler:                   gasHandler,
		BlockTracker:                 blockTracker,
		PubkeyConverter:              pcf.coreData.AddressPubKeyConverter(),
		BlockSizeComputation:         blockSizeComputationHandler,
		BalanceComputation:           balanceComputationHandler,
		EnableEpochsHandler:          pcf.coreData.EnableEpochsHandler(),
		TxTypeHandler:                txTypeHandler,
		ScheduledTxsExecutionHandler: scheduledTxsExecutionHandler,
		ProcessedMiniBlocksTracker:   processedMiniBlocksTracker,
		TxExecutionOrderHandler:      pcf.txExecutionOrderHandler,
		TxPreProcessorCreator:        pcf.txPreprocessorCreator,
	}
	preProcFactory, err := metachain.NewPreProcessorsContainerFactory(argsPreProc)
	if err != nil {
		return nil, err
	}

	preProcContainer, err := preProcFactory.Create()
	if err != nil {
		return nil, err
	}

	argsDetector := coordinator.ArgsPrintDoubleTransactionsDetector{
		Marshaller:          pcf.coreData.InternalMarshalizer(),
		Hasher:              pcf.coreData.Hasher(),
		EnableEpochsHandler: pcf.coreData.EnableEpochsHandler(),
	}
	doubleTransactionsDetector, err := coordinator.NewPrintDoubleTransactionsDetector(argsDetector)
	if err != nil {
		return nil, err
	}

	argsTransactionCoordinator := coordinator.ArgTransactionCoordinator{
		Hasher:                       pcf.coreData.Hasher(),
		Marshalizer:                  pcf.coreData.InternalMarshalizer(),
		ShardCoordinator:             pcf.bootstrapComponents.ShardCoordinator(),
		Accounts:                     pcf.state.AccountsAdapter(),
		MiniBlockPool:                pcf.data.Datapool().MiniBlocks(),
		RequestHandler:               requestHandler,
		PreProcessors:                preProcContainer,
		InterProcessors:              interimProcContainer,
		GasHandler:                   gasHandler,
		FeeHandler:                   txFeeHandler,
		BlockSizeComputation:         blockSizeComputationHandler,
		BalanceComputation:           balanceComputationHandler,
		EconomicsFee:                 pcf.coreData.EconomicsData(),
		TxTypeHandler:                txTypeHandler,
		TransactionsLogProcessor:     pcf.txLogsProcessor,
		EnableEpochsHandler:          pcf.coreData.EnableEpochsHandler(),
		ScheduledTxsExecutionHandler: scheduledTxsExecutionHandler,
		DoubleTransactionsDetector:   doubleTransactionsDetector,
		ProcessedMiniBlocksTracker:   processedMiniBlocksTracker,
		TxExecutionOrderHandler:      pcf.txExecutionOrderHandler,
	}
	txCoordinator, err := coordinator.NewTransactionCoordinator(argsTransactionCoordinator)
	if err != nil {
		return nil, err
	}

	scheduledTxsExecutionHandler.SetTransactionCoordinator(txCoordinator)

	argsStaking := scToProtocol.ArgStakingToPeer{
		PubkeyConv:          pcf.coreData.ValidatorPubKeyConverter(),
		Hasher:              pcf.coreData.Hasher(),
		Marshalizer:         pcf.coreData.InternalMarshalizer(),
		PeerState:           pcf.state.PeerAccounts(),
		BaseState:           pcf.state.AccountsAdapter(),
		ArgParser:           argsParser,
		CurrTxs:             pcf.data.Datapool().CurrentBlockTxs(),
		RatingsData:         pcf.coreData.RatingsData(),
		EnableEpochsHandler: pcf.coreData.EnableEpochsHandler(),
	}
	smartContractToProtocol, err := scToProtocol.NewStakingToPeer(argsStaking)
	if err != nil {
		return nil, err
	}

	genesisHdr := pcf.data.Blockchain().GetGenesisHeader()
	argsEpochStartData := metachainEpochStart.ArgsNewEpochStartData{
		Marshalizer:         pcf.coreData.InternalMarshalizer(),
		Hasher:              pcf.coreData.Hasher(),
		Store:               pcf.data.StorageService(),
		DataPool:            pcf.data.Datapool(),
		BlockTracker:        blockTracker,
		ShardCoordinator:    pcf.bootstrapComponents.ShardCoordinator(),
		EpochStartTrigger:   epochStartTrigger,
		RequestHandler:      requestHandler,
		GenesisEpoch:        genesisHdr.GetEpoch(),
		EnableEpochsHandler: pcf.coreData.EnableEpochsHandler(),
	}
	epochStartDataCreator, err := metachainEpochStart.NewEpochStartData(argsEpochStartData)
	if err != nil {
		return nil, err
	}

	economicsDataProvider := metachainEpochStart.NewEpochEconomicsStatistics()
	argsEpochEconomics := metachainEpochStart.ArgsNewEpochEconomics{
		Marshalizer:           pcf.coreData.InternalMarshalizer(),
		Hasher:                pcf.coreData.Hasher(),
		Store:                 pcf.data.StorageService(),
		ShardCoordinator:      pcf.bootstrapComponents.ShardCoordinator(),
		RewardsHandler:        pcf.coreData.EconomicsData(),
		RoundTime:             pcf.coreData.RoundHandler(),
		GenesisNonce:          genesisHdr.GetNonce(),
		GenesisEpoch:          genesisHdr.GetEpoch(),
		GenesisTotalSupply:    pcf.coreData.EconomicsData().GenesisTotalSupply(),
		EconomicsDataNotified: economicsDataProvider,
		StakingV2EnableEpoch:  pcf.coreData.EnableEpochsHandler().StakingV2EnableEpoch(),
	}
	epochEconomics, err := metachainEpochStart.NewEndOfEpochEconomicsDataCreator(argsEpochEconomics)
	if err != nil {
		return nil, err
	}

	systemVM, err := vmContainer.Get(factory.SystemVirtualMachine)
	if err != nil {
		return nil, err
	}

	// TODO: in case of changing the minimum node price, make sure to update the staking data provider
	stakingDataProvider, err := metachainEpochStart.NewStakingDataProvider(systemVM, pcf.systemSCConfig.StakingSystemSCConfig.GenesisNodePrice)
	if err != nil {
		return nil, err
	}

	rewardsStorage, err := pcf.data.StorageService().GetStorer(dataRetriever.RewardTransactionUnit)
	if err != nil {
		return nil, err
	}

	miniBlockStorage, err := pcf.data.StorageService().GetStorer(dataRetriever.MiniBlockUnit)
	if err != nil {
		return nil, err
	}

	argsEpochRewards := metachainEpochStart.RewardsCreatorProxyArgs{
		BaseRewardsCreatorArgs: metachainEpochStart.BaseRewardsCreatorArgs{
			ShardCoordinator:              pcf.bootstrapComponents.ShardCoordinator(),
			PubkeyConverter:               pcf.coreData.AddressPubKeyConverter(),
			RewardsStorage:                rewardsStorage,
			MiniBlockStorage:              miniBlockStorage,
			Hasher:                        pcf.coreData.Hasher(),
			Marshalizer:                   pcf.coreData.InternalMarshalizer(),
			DataPool:                      pcf.data.Datapool(),
			ProtocolSustainabilityAddress: pcf.coreData.EconomicsData().ProtocolSustainabilityAddress(),
			NodesConfigProvider:           pcf.nodesCoordinator,
			UserAccountsDB:                pcf.state.AccountsAdapter(),
			EnableEpochsHandler:           pcf.coreData.EnableEpochsHandler(),
			ExecutionOrderHandler:         pcf.txExecutionOrderHandler,
		},
		StakingDataProvider:   stakingDataProvider,
		RewardsHandler:        pcf.coreData.EconomicsData(),
		EconomicsDataProvider: economicsDataProvider,
	}
	epochRewards, err := metachainEpochStart.NewRewardsCreatorProxy(argsEpochRewards)
	if err != nil {
		return nil, err
	}

	validatorInfoStorage, err := pcf.data.StorageService().GetStorer(dataRetriever.UnsignedTransactionUnit)
	if err != nil {
		return nil, err
	}
	argsEpochValidatorInfo := metachainEpochStart.ArgsNewValidatorInfoCreator{
		ShardCoordinator:     pcf.bootstrapComponents.ShardCoordinator(),
		ValidatorInfoStorage: validatorInfoStorage,
		MiniBlockStorage:     miniBlockStorage,
		Hasher:               pcf.coreData.Hasher(),
		Marshalizer:          pcf.coreData.InternalMarshalizer(),
		DataPool:             pcf.data.Datapool(),
		EnableEpochsHandler:  pcf.coreData.EnableEpochsHandler(),
	}
	validatorInfoCreator, err := metachainEpochStart.NewValidatorInfoCreator(argsEpochValidatorInfo)
	if err != nil {
		return nil, err
	}

	outportDataProvider, err := pcf.createOutportDataProvider(txCoordinator, gasHandler)
	if err != nil {
		return nil, err
	}

	accountsDb := make(map[state.AccountsDbIdentifier]state.AccountsAdapter)
	accountsDb[state.UserAccountsState] = pcf.state.AccountsAdapter()
	accountsDb[state.PeerAccountsState] = pcf.state.PeerAccounts()

	argumentsBaseProcessor := block.ArgBaseProcessor{
		CoreComponents:               pcf.coreData,
		DataComponents:               pcf.data,
		BootstrapComponents:          pcf.bootstrapComponents,
		StatusComponents:             pcf.statusComponents,
		StatusCoreComponents:         pcf.statusCoreComponents,
		Config:                       pcf.config,
		PrefsConfig:                  pcf.prefConfigs,
		Version:                      pcf.flagsConfig.Version,
		AccountsDB:                   accountsDb,
		ForkDetector:                 forkDetector,
		NodesCoordinator:             pcf.nodesCoordinator,
		RequestHandler:               requestHandler,
		BlockChainHook:               vmFactory.BlockChainHookImpl(),
		TxCoordinator:                txCoordinator,
		EpochStartTrigger:            epochStartTrigger,
		HeaderValidator:              headerValidator,
		BootStorer:                   bootStorer,
		BlockTracker:                 blockTracker,
		FeeHandler:                   txFeeHandler,
		BlockSizeThrottler:           blockSizeThrottler,
		HistoryRepository:            pcf.historyRepo,
		VMContainersFactory:          vmFactory,
		VmContainer:                  vmContainer,
		GasHandler:                   gasHandler,
		ScheduledTxsExecutionHandler: scheduledTxsExecutionHandler,
		ProcessedMiniBlocksTracker:   processedMiniBlocksTracker,
		ReceiptsRepository:           receiptsRepository,
		OutportDataProvider:          outportDataProvider,
		BlockProcessingCutoffHandler: blockProcessingCutoffhandler,
		ManagedPeersHolder:           pcf.crypto.ManagedPeersHolder(),
	}

	esdtOwnerAddress, err := pcf.coreData.AddressPubKeyConverter().Decode(pcf.systemSCConfig.ESDTSystemSCConfig.OwnerAddress)
	if err != nil {
		return nil, fmt.Errorf("%w while decoding systemSCConfig.ESDTSystemSCConfig.OwnerAddress "+
			"in processComponentsFactory.newMetaBlockProcessor", err)
	}

	argsEpochSystemSC := metachainEpochStart.ArgsNewEpochStartSystemSCProcessing{
		SystemVM:                systemVM,
		UserAccountsDB:          pcf.state.AccountsAdapter(),
		PeerAccountsDB:          pcf.state.PeerAccounts(),
		Marshalizer:             pcf.coreData.InternalMarshalizer(),
		StartRating:             pcf.coreData.RatingsData().StartRating(),
		ValidatorInfoCreator:    validatorStatisticsProcessor,
		EndOfEpochCallerAddress: vm.EndOfEpochAddress,
		StakingSCAddress:        vm.StakingSCAddress,
		ChanceComputer:          pcf.coreData.Rater(),
		EpochNotifier:           pcf.coreData.EpochNotifier(),
		GenesisNodesConfig:      pcf.coreData.GenesisNodesSetup(),
		MaxNodesEnableConfig:    enableEpochs.MaxNodesChangeEnableEpoch,
		StakingDataProvider:     stakingDataProvider,
		NodesConfigProvider:     pcf.nodesCoordinator,
		ShardCoordinator:        pcf.bootstrapComponents.ShardCoordinator(),
		ESDTOwnerAddressBytes:   esdtOwnerAddress,
		EnableEpochsHandler:     pcf.coreData.EnableEpochsHandler(),
	}
	epochStartSystemSCProcessor, err := metachainEpochStart.NewSystemSCProcessor(argsEpochSystemSC)
	if err != nil {
		return nil, err
	}

	arguments := block.ArgMetaProcessor{
		ArgBaseProcessor:             argumentsBaseProcessor,
		SCToProtocol:                 smartContractToProtocol,
		PendingMiniBlocksHandler:     pendingMiniBlocksHandler,
		EpochStartDataCreator:        epochStartDataCreator,
		EpochEconomics:               epochEconomics,
		EpochRewardsCreator:          epochRewards,
		EpochValidatorInfoCreator:    validatorInfoCreator,
		ValidatorStatisticsProcessor: validatorStatisticsProcessor,
		EpochSystemSCProcessor:       epochStartSystemSCProcessor,
	}

	metaProcessor, err := block.NewMetaProcessor(arguments)
	if err != nil {
		return nil, errors.New("could not create meta block processor: " + err.Error())
	}

	err = pcf.attachProcessDebugger(metaProcessor, pcf.config.Debug.Process)
	if err != nil {
		return nil, err
	}

	blockProcessorComponents := &blockProcessorAndVmFactories{
		blockProcessor:         metaProcessor,
		vmFactoryForProcessing: vmFactory,
	}

	return blockProcessorComponents, nil
}

func (pcf *processComponentsFactory) attachProcessDebugger(
	processor mainFactory.ProcessDebuggerSetter,
	configs config.ProcessDebugConfig,
) error {
	processDebugger, err := debugFactory.CreateProcessDebugger(configs)
	if err != nil {
		return err
	}

	return processor.SetProcessDebugger(processDebugger)
}

func (pcf *processComponentsFactory) createOutportDataProvider(
	txCoordinator process.TransactionCoordinator,
	gasConsumedProvider processOutport.GasConsumedProvider,
) (outport.DataProviderOutport, error) {
	txsStorer, err := pcf.data.StorageService().GetStorer(dataRetriever.TransactionUnit)
	if err != nil {
		return nil, err
	}
	mbsStorer, err := pcf.data.StorageService().GetStorer(dataRetriever.MiniBlockUnit)
	if err != nil {
		return nil, err
	}

	return factoryOutportProvider.CreateOutportDataProvider(factoryOutportProvider.ArgOutportDataProviderFactory{
		HasDrivers:             pcf.statusComponents.OutportHandler().HasDrivers(),
		AddressConverter:       pcf.coreData.AddressPubKeyConverter(),
		AccountsDB:             pcf.state.AccountsAdapter(),
		Marshaller:             pcf.coreData.InternalMarshalizer(),
		EsdtDataStorageHandler: pcf.esdtNftStorage,
		TransactionsStorer:     txsStorer,
		ShardCoordinator:       pcf.bootstrapComponents.ShardCoordinator(),
		TxCoordinator:          txCoordinator,
		NodesCoordinator:       pcf.nodesCoordinator,
		GasConsumedProvider:    gasConsumedProvider,
		EconomicsData:          pcf.coreData.EconomicsData(),
		IsImportDBMode:         pcf.importDBConfig.IsImportDBMode,
		Hasher:                 pcf.coreData.Hasher(),
		MbsStorer:              mbsStorer,
		EnableEpochsHandler:    pcf.coreData.EnableEpochsHandler(),
		ExecutionOrderGetter:   pcf.txExecutionOrderHandler,
	})
}

// VMFactoryCreatorArgs is a struct containing the arguments needed for creating a VMFactory
type VMFactoryCreatorArgs struct {
	Accounts              state.AccountsAdapter
	Notifier              common.MissingTrieNodesNotifier
	BuiltInFuncs          vmcommon.BuiltInFunctionContainer
	EsdtTransferParser    vmcommon.ESDTTransferParser
	WasmVMChangeLocker    common.Locker
	ConfigSCStorage       config.StorageConfig
	NftStorageHandler     vmcommon.SimpleESDTNFTStorageHandler
	GlobalSettingsHandler vmcommon.ESDTGlobalSettingsHandler
	GasSchedule           core.GasScheduleNotifier
	CoreData              mainFactory.CoreComponentsHolder
	Data                  mainFactory.DataComponentsHolder
	Crypto                mainFactory.CryptoComponentsHolder
	State                 mainFactory.StateComponentsHolder
	ShardCoordinator      sharding.Coordinator
	WorkingDir            string
	ChainRunType          common.ChainRunType
	VmConfig              config.VirtualMachineConfig
	SystemSCConfig        *config.SystemSmartContractsConfig
}

// VMFactoryCreator is an interface for creating a VMFactory
type VMFactoryCreator interface {
	CreateVMFactory(args VMFactoryCreatorArgs) (process.VirtualMachinesContainerFactory, error)
	AddAdditionalVMs(vmContainer process.VirtualMachinesContainer) error
}

// ShardVMFactoryImpl is an implementation of VMFactoryCreator
type ShardVMFactoryImpl struct {
}

// CreateVMFactory creates a VMFactory
func (svmf *ShardVMFactoryImpl) CreateVMFactory(args VMFactoryCreatorArgs) (process.VirtualMachinesContainerFactory, error) {
	return svmf.createVMFactoryShard(args)
}

// AddAdditionalVMs adds additional VMs to the VMContainer
func (svmf *ShardVMFactoryImpl) AddAdditionalVMs(_ process.VirtualMachinesContainer) error {
	return nil
}

// SovereignVMFactoryImpl is an implementation of VMFactoryCreator
type SovereignVMFactoryImpl struct {
	shardVMFactory ShardVMFactoryImpl
	metaVMFactory  MetaVMFactoryImpl
	metaFactory    process.VirtualMachinesContainerFactory
}

// CreateVMFactory creates a VMFactory
func (svmf *SovereignVMFactoryImpl) CreateVMFactory(args VMFactoryCreatorArgs) (process.VirtualMachinesContainerFactory, error) {
	metaFactory, err := svmf.metaVMFactory.CreateVMFactory(args)
	if err != nil {
		return nil, err
	}
	svmf.metaFactory = metaFactory
	return svmf.shardVMFactory.CreateVMFactory(args)
}

// AddAdditionalVMs adds additional VMs to the VMContainer
func (svmf *SovereignVMFactoryImpl) AddAdditionalVMs(vmContainer process.VirtualMachinesContainer) error {
	vmContainerMeta, err := svmf.metaFactory.Create()
	if err != nil {
		return err
	}

	vmMeta, err := vmContainerMeta.Get(factory.SystemVirtualMachine)
	if err != nil {
		return err
	}

	err = vmContainer.Add(factory.SystemVirtualMachine, vmMeta)
	if err != nil {
		return err
	}

	return nil
}

func (*ShardVMFactoryImpl) createVMFactoryShard(args VMFactoryCreatorArgs) (process.VirtualMachinesContainerFactory, error) {
	counter, err := counters.NewUsageCounter(args.EsdtTransferParser)
	if err != nil {
		return nil, err
	}

	argsHook := hooks.ArgBlockChainHook{
		Accounts:                 args.Accounts,
		PubkeyConv:               args.CoreData.AddressPubKeyConverter(),
		StorageService:           args.Data.StorageService(),
		BlockChain:               args.Data.Blockchain(),
		ShardCoordinator:         args.ShardCoordinator,
		Marshalizer:              args.CoreData.InternalMarshalizer(),
		Uint64Converter:          args.CoreData.Uint64ByteSliceConverter(),
		BuiltInFunctions:         args.BuiltInFuncs,
		DataPool:                 args.Data.Datapool(),
		CompiledSCPool:           args.Data.Datapool().SmartContracts(),
		WorkingDir:               args.WorkingDir,
		NFTStorageHandler:        args.NftStorageHandler,
		GlobalSettingsHandler:    args.GlobalSettingsHandler,
		EpochNotifier:            args.CoreData.EpochNotifier(),
		EnableEpochsHandler:      args.CoreData.EnableEpochsHandler(),
		NilCompiledSCStore:       false,
		ConfigSCStorage:          args.ConfigSCStorage,
		GasSchedule:              args.GasSchedule,
		Counter:                  counter,
		MissingTrieNodesNotifier: args.Notifier,
	}

	blockChainHookImpl, err := hooks.CreateBlockChainHook(args.ChainRunType, argsHook)
	if err != nil {
		return nil, err
	}

	argsNewVMFactory := shard.ArgVMContainerFactory{
		BlockChainHook:      blockChainHookImpl,
		BuiltInFunctions:    argsHook.BuiltInFunctions,
		Config:              args.VmConfig,
		BlockGasLimit:       args.CoreData.EconomicsData().MaxGasLimitPerBlock(args.ShardCoordinator.SelfId()),
		GasSchedule:         args.GasSchedule,
		EpochNotifier:       args.CoreData.EpochNotifier(),
		EnableEpochsHandler: args.CoreData.EnableEpochsHandler(),
		WasmVMChangeLocker:  args.WasmVMChangeLocker,
		ESDTTransferParser:  args.EsdtTransferParser,
		Hasher:              args.CoreData.Hasher(),
	}

	return shard.NewVMContainerFactory(argsNewVMFactory)
}

// MetaVMFactoryImpl is an implementation of VMFactoryCreator
type MetaVMFactoryImpl struct {
}

// CreateVMFactory creates a VMFactory
func (mvf *MetaVMFactoryImpl) CreateVMFactory(args VMFactoryCreatorArgs) (process.VirtualMachinesContainerFactory, error) {
	return mvf.createVMFactoryMeta(args)
}

// AddAdditionalVMs adds additional VMs to the VMContainer
func (svmf *MetaVMFactoryImpl) AddAdditionalVMs(vmContainer process.VirtualMachinesContainer) error {
	return nil
}

func (pcf *MetaVMFactoryImpl) createVMFactoryMeta(args VMFactoryCreatorArgs) (process.VirtualMachinesContainerFactory, error) {
	argsHook := hooks.ArgBlockChainHook{
		Accounts:                 args.Accounts,
		PubkeyConv:               args.CoreData.AddressPubKeyConverter(),
		StorageService:           args.Data.StorageService(),
		BlockChain:               args.Data.Blockchain(),
		ShardCoordinator:         args.ShardCoordinator,
		Marshalizer:              args.CoreData.InternalMarshalizer(),
		Uint64Converter:          args.CoreData.Uint64ByteSliceConverter(),
		BuiltInFunctions:         args.BuiltInFuncs,
		DataPool:                 args.Data.Datapool(),
		CompiledSCPool:           args.Data.Datapool().SmartContracts(),
		ConfigSCStorage:          args.ConfigSCStorage,
		WorkingDir:               args.WorkingDir,
		NFTStorageHandler:        args.NftStorageHandler,
		GlobalSettingsHandler:    args.GlobalSettingsHandler,
		EpochNotifier:            args.CoreData.EpochNotifier(),
		EnableEpochsHandler:      args.CoreData.EnableEpochsHandler(),
		NilCompiledSCStore:       false,
		GasSchedule:              args.GasSchedule,
		Counter:                  counters.NewDisabledCounter(),
		MissingTrieNodesNotifier: syncer.NewMissingTrieNodesNotifier(),
	}

	blockChainHookImpl, err := hooks.CreateBlockChainHook(args.ChainRunType, argsHook)
	if err != nil {
		return nil, err
	}

	argsNewVMContainer := metachain.ArgsNewVMContainerFactory{
		BlockChainHook:      blockChainHookImpl,
		PubkeyConv:          argsHook.PubkeyConv,
		Economics:           args.CoreData.EconomicsData(),
		MessageSignVerifier: args.Crypto.MessageSignVerifier(),
		GasSchedule:         args.GasSchedule,
		NodesConfigProvider: args.CoreData.GenesisNodesSetup(),
		Hasher:              args.CoreData.Hasher(),
		Marshalizer:         args.CoreData.InternalMarshalizer(),
		SystemSCConfig:      args.SystemSCConfig,
		ValidatorAccountsDB: args.State.PeerAccounts(),
		UserAccountsDB:      args.State.AccountsAdapter(),
		ChanceComputer:      args.CoreData.Rater(),
		ShardCoordinator:    args.ShardCoordinator,
		EnableEpochsHandler: args.CoreData.EnableEpochsHandler(),
	}
	return metachain.NewVMContainerFactory(argsNewVMContainer)
}

func (pcf *processComponentsFactory) createBuiltInFunctionContainer(
	accounts state.AccountsAdapter,
	mapDNSAddresses map[string]struct{},
) (vmcommon.BuiltInFunctionFactory, error) {
	convertedAddresses, err := mainFactory.DecodeAddresses(
		pcf.coreData.AddressPubKeyConverter(),
		pcf.config.BuiltInFunctions.AutomaticCrawlerAddresses,
	)
	if err != nil {
		return nil, err
	}

	convertedDNSV2Addresses, err := mainFactory.DecodeAddresses(
		pcf.coreData.AddressPubKeyConverter(),
		pcf.config.BuiltInFunctions.DNSV2Addresses,
	)
	if err != nil {
		return nil, err
	}

	mapDNSV2Addresses := make(map[string]struct{})
	for _, address := range convertedDNSV2Addresses {
		mapDNSV2Addresses[string(address)] = struct{}{}
	}

	argsBuiltIn := builtInFunctions.ArgsCreateBuiltInFunctionContainer{
		GasSchedule:               pcf.gasSchedule,
		MapDNSAddresses:           mapDNSAddresses,
		MapDNSV2Addresses:         mapDNSV2Addresses,
		Marshalizer:               pcf.coreData.InternalMarshalizer(),
		Accounts:                  accounts,
		ShardCoordinator:          pcf.bootstrapComponents.ShardCoordinator(),
		EpochNotifier:             pcf.coreData.EpochNotifier(),
		EnableEpochsHandler:       pcf.coreData.EnableEpochsHandler(),
		GuardedAccountHandler:     pcf.bootstrapComponents.GuardedAccountHandler(),
		AutomaticCrawlerAddresses: convertedAddresses,
		MaxNumNodesInTransferRole: pcf.config.BuiltInFunctions.MaxNumAddressesInTransferRole,
	}

	return builtInFunctions.CreateBuiltInFunctionsFactory(argsBuiltIn)
}
