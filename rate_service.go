package main

import (
	"context"
	"log"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

func main() {
	// todo
}

func getEthClient() *ethclient.Client {
	client, err := ethclient.Dial("https://mainnet.infura.io/v3/c75a0117c6cd4c84a4a8bf62ac9979e7")
	if err != nil {
		log.Fatal(err)
	}

	return client
}

func getExchangeRate(ctx context.Context, tokenAddress common.Address) (float64, error) {
	v2RouterAddress := common.HexToAddress("0x7a250d5630B4cF539739dF2C5dAcb4c659F2488D")
	client := getEthClient()
	callMsg := ethereum.CallMsg{
		From:     common.HexToAddress("0x0000000000000000000000000000000000000000"),
		To:       &tokenAddress,
		Gas:      0,
		GasPrice: big.NewInt(0),
		Value:    big.NewInt(0),
		Data:     nil,
	}
	client.CallContract(ctx)
	v2RouterInstance, err := v2Router.NewV2Router(v2RouterAddress, client)
	v2RouterInstance.swapExactTokensForETH()
}
