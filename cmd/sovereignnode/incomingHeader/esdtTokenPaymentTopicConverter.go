package incomingHeader

import "math/big"

// esdtTokenPaymentTopicConverter is a struct that will convert topics from a byte array to a 2D byte array
// The topics are encoded as follows:
// 1. The first 4 bytes represent the size of the token identifier
// 2. The next n bytes represent the token identifier
// 3. The next 8 bytes represent the token nonce
// 4. The next 4 bytes represent the size of the quantity
// 5. The next n bytes represent the quantity
type esdtTokenPaymentTopicConverter struct {
}

// NewESDTTokenPaymentTopicConverter creates a new instance of esdtTokenPaymentTopicConverter
func NewESDTTokenPaymentTopicConverter() *esdtTokenPaymentTopicConverter {
	return &esdtTokenPaymentTopicConverter{}
}

// ConvertTopics converts a byte array to a 2D byte array
func (tc *esdtTokenPaymentTopicConverter) ConvertTopics(topicArray []byte) [][]byte {
	var topics [][]byte
	stillToDo := true
	for stillToDo {
		parsedBytes, nrBytes := parseEsdtTokenPayment(topicArray)
		if nrBytes == 0 {
			stillToDo = false
			break
		}
		topics = append(topics, parsedBytes...)
		topicArray = topicArray[nrBytes:]
	}

	return topics
}

// IsInterfaceNil returns true if there is no value under the interface
func (tc *esdtTokenPaymentTopicConverter) IsInterfaceNil() bool {
	return tc == nil
}

func parseEsdtTokenPayment(array []byte) ([][]byte, int64) {
	if len(array) < 16 {
		return nil, 0
	}
	// identifier
	parsedArray := make([][]byte, 0)
	nrBytes := int64(0)
	tokenIdentifierSize := big.NewInt(0).SetBytes(array[:4]).Int64()
	nrBytes += 4
	tokenIdentifier := array[nrBytes : nrBytes+tokenIdentifierSize]

	// nonce
	parsedArray = append(parsedArray, tokenIdentifier)
	nrBytes += tokenIdentifierSize
	tokenNonce := array[nrBytes : nrBytes+8]
	parsedArray = append(parsedArray, tokenNonce)
	nrBytes += 8

	// quantity
	quantitySize := big.NewInt(0).SetBytes(array[nrBytes : nrBytes+4]).Int64()
	nrBytes += 4
	quantity := array[nrBytes : nrBytes+quantitySize]
	nrBytes += quantitySize
	parsedArray = append(parsedArray, quantity)
	return parsedArray, nrBytes
}
