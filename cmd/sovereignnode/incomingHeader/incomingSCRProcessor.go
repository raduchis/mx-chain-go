package incomingHeader

import (
	"encoding/hex"
	"fmt"
	"math/big"

	"github.com/multiversx/mx-chain-core-go/core"
	"github.com/multiversx/mx-chain-core-go/data"
	"github.com/multiversx/mx-chain-core-go/data/smartContractResult"
	"github.com/multiversx/mx-chain-core-go/hashing"
	"github.com/multiversx/mx-chain-core-go/marshal"
	"github.com/multiversx/mx-chain-go/process"
)

const (
	minTopicsInEvent      = 4
	numTransferTopics     = 3
	minNumEventDataTokens = 4
)

type eventData struct {
	nonce                uint64
	functionCallWithArgs []byte
	gasLimit             uint64
}

type scrInfo struct {
	scr  *smartContractResult.SmartContractResult
	hash []byte
}

type scrProcessor struct {
	txPool     TransactionPool
	marshaller marshal.Marshalizer
	hasher     hashing.Hasher
}

func (sp *scrProcessor) createIncomingSCRs(events []data.EventHandler) ([]*scrInfo, error) {
	scrs := make([]*scrInfo, 0, len(events))

	for idx, event := range events {
		oldTopics := event.GetTopics()

		topics := make([][]byte, 0)
		topics = append(topics, oldTopics[1])
		convertedTopics := ConvertTopics(oldTopics[2])
		topics = append(topics, convertedTopics...)

		// TODO: Check each param validity (e.g. check that topic[0] == valid address)
		if len(topics) < minTopicsInEvent || len(topics[1:])%numTransferTopics != 0 {
			log.Error("incomingHeaderHandler.createIncomingSCRs",
				"error", errInvalidNumTopicsIncomingEvent,
				"num topics", len(topics),
				"topics", topics)
			return nil, fmt.Errorf("%w at event idx = %d; num topics = %d",
				errInvalidNumTopicsIncomingEvent, idx, len(topics))
		}

		receivedEventData, err := getEventData(event.GetData())
		if err != nil {
			return nil, err
		}

		scrData := createSCRData(topics)
		scrData = append(scrData, receivedEventData.functionCallWithArgs...)
		scr := &smartContractResult.SmartContractResult{
			Nonce:          receivedEventData.nonce,
			OriginalTxHash: nil, // TODO:  Implement this in MX-14321 task
			RcvAddr:        topics[0],
			SndAddr:        core.ESDTSCAddress,
			Data:           scrData,
			Value:          big.NewInt(0),
			GasLimit:       receivedEventData.gasLimit,
		}

		hash, err := core.CalculateHash(sp.marshaller, sp.hasher, scr)
		if err != nil {
			return nil, err
		}

		scrs = append(scrs, &scrInfo{
			scr:  scr,
			hash: hash,
		})
	}

	return scrs, nil
}

func getEventData(data []byte) (*eventData, error) {
	if len(data) == 0 {
		return nil, errEmptyLogData
	}

	//tokens := strings.Split(string(data), "@")
	//numTokens := len(tokens)
	//if numTokens < minNumEventDataTokens {
	//	return nil, fmt.Errorf("%w, expected min num tokens: %d, received num tokens: %d",
	//		errInvalidNumTokensOnLogData, minNumEventDataTokens, numTokens)
	//}
	//
	//// TODO: Add validity checks
	//eventNonce := big.NewInt(0).SetBytes([]byte(tokens[0]))
	//gasLimit := big.NewInt(0).SetBytes([]byte(tokens[numTokens-1]))
	//
	//functionCallWithArgs := []byte("@" + tokens[1])
	//for i := 2; i < numTokens-1; i++ {
	//	functionCallWithArgs = append(functionCallWithArgs, []byte("@"+tokens[i])...)
	//}

	// TODO: Add validity checks
	eventNonce := big.NewInt(0).SetBytes(data[0:8])
	gasLimit := big.NewInt(0)

	return &eventData{
		nonce:                eventNonce.Uint64(),
		gasLimit:             gasLimit.Uint64(),
		functionCallWithArgs: make([]byte, 0),
	}, nil
}

func createSCRData(topics [][]byte) []byte {
	numTokensToTransfer := len(topics[1:]) / numTransferTopics
	numTokensToTransferBytes := big.NewInt(int64(numTokensToTransfer)).Bytes()

	ret := []byte(core.BuiltInFunctionMultiESDTNFTTransfer +
		"@" + hex.EncodeToString(numTokensToTransferBytes))

	for idx := 1; idx < len(topics[1:]); idx += 3 {
		transfer := []byte("@" +
			hex.EncodeToString(topics[idx]) + // tokenID
			"@" + hex.EncodeToString(topics[idx+1]) + //nonce
			"@" + hex.EncodeToString(topics[idx+2])) //value

		ret = append(ret, transfer...)
	}

	return ret
}

func (sp *scrProcessor) addSCRsToPool(scrs []*scrInfo) {
	cacheID := process.ShardCacherIdentifier(core.MainChainShardId, core.SovereignChainShardId)

	for _, scrData := range scrs {
		sp.txPool.AddData(scrData.hash, scrData.scr, scrData.scr.Size(), cacheID)
	}
}

func ConvertTopics(topicArray []byte) [][]byte {
	var topics [][]byte
	stillToDo := true
	for stillToDo {
		if len(topicArray) < 20 {
			stillToDo = false
			break
		}
		parsedBytes, nrBytes := parseBytes(topicArray)
		topics = append(topics, parsedBytes...)
		topicArray = topicArray[nrBytes:]
	}

	return topics
}

func parseBytes(array []byte) ([][]byte, int64) {
	parsedArray := make([][]byte, 0)
	nrBytes := int64(0)
	tokenIdentifierSize := big.NewInt(0).SetBytes(array[:4]).Int64()
	nrBytes += 4
	tokenIdentifier := array[nrBytes : nrBytes+tokenIdentifierSize]
	parsedArray = append(parsedArray, tokenIdentifier)
	nrBytes += tokenIdentifierSize
	tokenNonce := array[nrBytes : nrBytes+8]
	parsedArray = append(parsedArray, tokenNonce)
	nrBytes += 8
	quantitySize := big.NewInt(0).SetBytes(array[nrBytes : nrBytes+4]).Int64()
	nrBytes += 4
	quantity := array[nrBytes : nrBytes+quantitySize]
	nrBytes += quantitySize
	parsedArray = append(parsedArray, quantity)
	return parsedArray, nrBytes
}
