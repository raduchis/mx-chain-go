package incomingHeader_test

import (
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/multiversx/mx-chain-go/sovereignnode/incomingHeader"
	"github.com/stretchr/testify/require"
)

func TestNewTopicsConverter(t *testing.T) {
	t.Parallel()

	topicsConverter := incomingHeader.NewESDTTokenPaymentTopicConverter()

	require.False(t, topicsConverter.IsInterfaceNil())
}

func TestEsdtTokenPaymentTopicConverter_Convert1Topic(t *testing.T) {
	t.Parallel()

	topicsConverter := incomingHeader.NewESDTTokenPaymentTopicConverter()
	topicBytes, _ := hex.DecodeString("0000000f4153485745474c442d39336465373100000000000000000000000164")
	newBytes := topicsConverter.ConvertTopics(topicBytes)
	require.Equal(t, 3, len(newBytes))

	expectedTokenIdentifier := "ASHWEGLD-93de71"
	tokenIdentifier := string(newBytes[0])
	require.Equal(t, expectedTokenIdentifier, tokenIdentifier)

	expectedNonce := 0
	tokenNonce := int(big.NewInt(0).SetBytes(newBytes[1]).Int64())
	require.Equal(t, expectedNonce, tokenNonce)

	expectedQuantity := 100
	tokenQuantity := int(big.NewInt(0).SetBytes(newBytes[2]).Int64())
	require.Equal(t, expectedQuantity, tokenQuantity)
}

func TestEsdtTokenPaymentTopicConverter_Convert2Topic(t *testing.T) {
	t.Parallel()

	topicsConverter := incomingHeader.NewESDTTokenPaymentTopicConverter()
	topicBytes, _ := hex.DecodeString("0000000c5745474c442d30316534396400000000000000000000000201280000001155544b5745474c44464c2d38666361303600000000000000010000000164")
	newBytes := topicsConverter.ConvertTopics(topicBytes)
	require.Equal(t, 6, len(newBytes))

	expectedTokenIdentifier := "WEGLD-01e49d"
	tokenIdentifier := string(newBytes[0])
	require.Equal(t, expectedTokenIdentifier, tokenIdentifier)

	expectedNonce := 0
	tokenNonce := int(big.NewInt(0).SetBytes(newBytes[1]).Int64())
	require.Equal(t, expectedNonce, tokenNonce)

	expectedQuantity := 296
	tokenQuantity := int(big.NewInt(0).SetBytes(newBytes[2]).Int64())
	require.Equal(t, expectedQuantity, tokenQuantity)

	expectedTokenIdentifier2 := "UTKWEGLDFL-8fca06"
	tokenIdentifier2 := string(newBytes[3])
	require.Equal(t, expectedTokenIdentifier2, tokenIdentifier2)

	expectedNonce2 := 1
	tokenNonce2 := int(big.NewInt(0).SetBytes(newBytes[4]).Int64())
	require.Equal(t, expectedNonce2, tokenNonce2)

	expectedQuantity2 := 100
	tokenQuantity2 := int(big.NewInt(0).SetBytes(newBytes[5]).Int64())
	require.Equal(t, expectedQuantity2, tokenQuantity2)
}
