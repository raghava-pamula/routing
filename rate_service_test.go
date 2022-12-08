package main

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/mock"
)

type TradingPairProviderMock struct {
	mock.Mock
}

func (f *TradingPairProviderMock) GetTradingPair(ctx context.Context, tokenA common.Address, tokenB common.Address) (common.Address, error) {
	args := f.Called(ctx, tokenA, mock.Anything)
	return args.Get(0).(common.Address), args.Error(1)
}

type PoolReservesProviderMock struct {
	mock.Mock
}

func (f *PoolReservesProviderMock) GetPoolReserves(ctx context.Context, pairAddress common.Address) (*big.Int, *big.Int, error) {
	args := f.Called(ctx, pairAddress)
	return args.Get(0).(*big.Int), args.Get(1).(*big.Int), args.Error(2)
}

type TokenDecimalsProviderMock struct {
	mock.Mock
}

func (f *TokenDecimalsProviderMock) GetTokenDecimals(ctx context.Context, tokenAddress common.Address) (uint8, error) {
	f.Called(ctx, tokenAddress)
	return 0, nil
}

func TestToEighteenDecimals(t *testing.T) {
	gotAmount := toEighteenDecimals(common.HexToAddress(USDC), big.NewInt(1), 6)
	wantAmount := big.NewInt(1000000000000)
	if gotAmount.Cmp(wantAmount) != 0 {
		t.Errorf("got %d want %d", gotAmount, wantAmount)
	}

	gotAmount = toEighteenDecimals(common.HexToAddress(USDC), big.NewInt(1), 18)
	wantAmount = big.NewInt(1)
	if gotAmount.Cmp(wantAmount) != 0 {
		t.Errorf("got %d want %d", gotAmount, wantAmount)
	}
}

func TestGetExchangeRate(t *testing.T) {
	ctx := context.Background()
	pairProvider := &TradingPairProviderMock{}
	tokenDecimalsProvider := &TokenDecimalsProviderMock{}
	poolReservesProvider := &PoolReservesProviderMock{}
	exchangeRateProvider := &OnChainExchangeRateProvider{
		pairProvider:          pairProvider,
		poolReservesProvider:  poolReservesProvider,
		tokenDecimalsProvider: tokenDecimalsProvider,
	}

	// Mock the calls to the Providers
	pairProvider.On("GetTradingPair", ctx, common.HexToAddress(WETH), common.HexToAddress(USDC)).Return(common.HexToAddress(WETH_USDC), nil)
	tokenDecimalsProvider.On("GetTokenDecimals", ctx, mock.Anything).Return(uint8(18), nil)
	poolReservesProvider.On("GetPoolReserves", ctx, common.HexToAddress(WETH_USDC)).Return(big.NewInt(95), big.NewInt(19), nil)

	// Call the method being tested
	gotRate, err := exchangeRateProvider.GetExchangeRate(context.Background(), common.HexToAddress(WETH), common.HexToAddress(USDC))
	if err != nil {
		t.Errorf("got error %v", err)
	}

	// Assert that the Providers were called
	pairProvider.AssertCalled(t, "GetTradingPair", ctx, common.HexToAddress(WETH), common.HexToAddress(USDC))
	poolReservesProvider.AssertCalled(t, "GetPoolReserves", ctx, common.HexToAddress(WETH_USDC))
	tokenDecimalsProvider.AssertCalled(t, "GetTokenDecimals", ctx, common.HexToAddress(WETH))
	tokenDecimalsProvider.AssertCalled(t, "GetTokenDecimals", ctx, common.HexToAddress(USDC))

	// Assert that the result is correct
	wantRate := big.NewFloat(5)
	if gotRate.Cmp(wantRate) != 0 {
		t.Errorf("got %d want %d", gotRate, wantRate)
	}
}
