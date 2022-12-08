package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/raghava-pamula/factory"
)

type TradingPairProvider interface {
	GetTradingPair(ctx context.Context, tokenA, tokenB common.Address) (common.Address, error)
}

type OnChainTradingPairProvider struct {
	rpcClient     *ethclient.Client
	factoryCaller factory.FactoryCaller
}

func (f *OnChainTradingPairProvider) GetTradingPair(ctx context.Context, tokenA, tokenB common.Address) (common.Address, error) {
	caller, _ := factory.NewFactoryCaller(common.HexToAddress(FACTORY_ADDRESS), f.rpcClient)
	callOpts := &bind.CallOpts{
		Context: ctx,
		Pending: false,
	}
	pairAddress, err := caller.GetPair(callOpts, tokenA, tokenB)
	if err != nil {
		return common.Address{}, err
	}
	return pairAddress, nil
}

// top tokens provider returns the top tokens on Uniswap V2
type TopTokensProvider interface {
	GetTopTokens(ctx context.Context) ([]common.Address, error)
}

type StaticTopTokensProvider struct {
}

func (s *StaticTopTokensProvider) GetTopTokens(ctx context.Context) ([]common.Address, error) {
	return []common.Address{
		common.HexToAddress(WETH),
		common.HexToAddress(USDC),
		common.HexToAddress(DAI),
		common.HexToAddress(USDT),
		common.HexToAddress(WBTC),
		common.HexToAddress(UNI),
	}, nil
}

type V2Router interface {
	Route(ctx context.Context, amountIn *big.Int, path []common.Address) (*big.Float, error)
}

type OnChainV2Router struct {
	rateProvider          ExchangeRateProvider
	poolProvider          PoolsProvider
	tradingPairProvider   TradingPairProvider
	poolReservesProvider  PoolReservesProvider
	tokenDecimalsProvider TokenDecimalsProvider
}

func (r *OnChainV2Router) Route(ctx context.Context, tokenIn common.Address, tokenOut common.Address, maxHops int) (*big.Float, []common.Address, error) {
	if tokenIn.String() == tokenOut.String() {
		return &big.Float{}, make([]common.Address, 0), errors.New("tokenIn and tokenOut cannot be the same")
	}
	// at least one hop is required to route
	if maxHops == 0 {
		return &big.Float{}, make([]common.Address, 0), errors.New("maxHops cannot be 0")
	}
	// if maxHops is 1, then we can just return the pair rate, if the pair exists
	if maxHops == 1 {
		amountOut, err := r.rateProvider.GetExchangeRate(ctx, tokenIn, tokenOut)
		if err != nil {
			return &big.Float{}, make([]common.Address, 0), err
		}
		path := []common.Address{tokenIn, tokenOut}
		return amountOut, path, nil
	}
	// swaps with more than 5 hops are not supported for performance and gas cost constraints
	if maxHops > 5 {
		return &big.Float{}, make([]common.Address, 0), errors.New("maxHops cannot be greater than 5")
	}

	usedTokens := make(map[string]bool)
	tokens := []common.Address{}
	pools, err := r.poolProvider.GetPools(ctx)
	if err != nil {
		return &big.Float{}, make([]common.Address, 0), err
	}
	for i := 0; i < len(pools); i++ {
		pair := pools[i]
		if !usedTokens[pair.token0.String()] {
			tokens = append(tokens, pair.token0)
			usedTokens[pair.token0.String()] = true
		}
		if !usedTokens[pair.token1.String()] {
			tokens = append(tokens, pair.token1)
			usedTokens[pair.token1.String()] = true
		}
	}

	if !usedTokens[tokenIn.String()] {
		tokens = append(tokens, tokenIn)
	}
	if !usedTokens[tokenOut.String()] {
		tokens = append(tokens, tokenOut)
	}

	tokenInIndex, tokenOutIndex := -1, -1

	for i := 0; i < len(tokens); i++ {
		if tokens[i].String() == tokenIn.String() {
			tokenInIndex = i
		}
		if tokens[i].String() == tokenOut.String() {
			tokenOutIndex = i
		}
	}
	// caches liquidity for V2 Pairs
	reservesCache := map[string][]big.Int{}

	for i := 0; i < len(tokens); i++ {
		for j := 0; j < len(tokens); j++ {
			if i == j {
				continue
			}
			key := tokens[i].String() + tokens[j].String()
			pair, err := r.tradingPairProvider.GetTradingPair(ctx, tokens[i], tokens[j])
			if err != nil {
				return &big.Float{}, make([]common.Address, 0), err
			}
			reservesA, reservesB, err := r.poolReservesProvider.GetPoolReserves(ctx, pair)
			if err != nil {
				return &big.Float{}, make([]common.Address, 0), err
			}
			if tokens[i].String() > tokens[j].String() {
				reservesCache[key] = []big.Int{*reservesB, *reservesA}
			} else {
				reservesCache[key] = []big.Int{*reservesA, *reservesB}
			}
		}
	}

	// init 2d array for floyd warshall
	cachedPossibleOutputs := make([][]*big.Float, maxHops+1)
	prev := make(map[int]map[common.Address]common.Address)
	for i := 0; i < maxHops+1; i++ {
		prev[i] = make(map[common.Address]common.Address)
	}
	bestPrice := &big.Float{}
	numHops := 0
	for i := range cachedPossibleOutputs {
		for _ = range tokens {
			cachedPossibleOutputs[i] = append(cachedPossibleOutputs[i], big.NewFloat(0))
		}
		if i == 0 {
			cachedPossibleOutputs[0][tokenInIndex] = big.NewFloat(1)
			continue
		}
		for input := 0; input < len(tokens); input++ {
			for output := 0; output < len(tokens); output++ {
				// skipping because we can't swap to the same token
				if input == output {
					if cachedPossibleOutputs[i][output].Cmp(cachedPossibleOutputs[i-1][input]) < 0 {
						cachedPossibleOutputs[i][output] = cachedPossibleOutputs[i-1][input]
					}
					continue
				}
				inputAmount := cachedPossibleOutputs[i-1][input]
				if inputAmount.Cmp(big.NewFloat(0)) == 0 {
					continue
				}
				reservesInput, reservesOutput := big.NewInt(0), big.NewInt(0)
				// if the pair exists, then we can use the reserves to calculate the price
				// reserves are cached to avoid multiple calls to the contract
				// reserves are returned from the contract in the lexicographical order of the token addresses
				if tokens[input].String() < tokens[output].String() {
					key := tokens[input].String() + tokens[output].String()
					reserves, _ := reservesCache[key]
					reservesInput, reservesOutput = &reserves[0], &reserves[1]
				} else {
					key := tokens[output].String() + tokens[input].String()
					reserves, _ := reservesCache[key]
					reservesOutput, reservesInput = &reserves[0], &reserves[1]
				}
				decimalsInput, _ := r.tokenDecimalsProvider.GetTokenDecimals(ctx, tokens[input])
				decimalsOutput, _ := r.tokenDecimalsProvider.GetTokenDecimals(ctx, tokens[output])
				tokenInput, _ := inputAmount.Int(&big.Int{})
				rate := calculatePrice(reservesInput, reservesOutput, decimalsInput, decimalsOutput, tokenInput)
				possibleOutputAmount := new(big.Float).Mul(inputAmount, rate)

				// update cached value for cachedPossibleOutputs[i][output]
				if possibleOutputAmount.Cmp(cachedPossibleOutputs[i][output]) > 0 {
					cachedPossibleOutputs[i][output] = possibleOutputAmount
					prev[i][tokens[output]] = tokens[input]
				}
			}
		}
		fmt.Printf("best price with %v hops: %v\n", i, cachedPossibleOutputs[i][tokenOutIndex])
		if bestPrice.Cmp(cachedPossibleOutputs[i][tokenOutIndex]) >= 0 {
			break
		}
		numHops = i + 1
		bestPrice = cachedPossibleOutputs[i][tokenOutIndex]
	}
	path := []common.Address{}
	currentToken := tokens[tokenOutIndex]

	// reconstruct the path by traversing the prev map
	for i := numHops - 1; i >= 0; i-- {
		path = append(path, currentToken)
		token, ok := prev[i][currentToken]
		if !ok {
			break
		}
		currentToken = token
	}
	reverse(path)
	return cachedPossibleOutputs[numHops-1][tokenOutIndex], path, nil
}

func reverse(arr []common.Address) {
	for i := 0; i < len(arr)/2; i++ {
		arr[i], arr[len(arr)-1-i] = arr[len(arr)-1-i], arr[i]
	}
}

type PoolReservesProvider interface {
	// returns reserve0, reserve1 in the order of the lexically sorted token addresses in the pair
	GetPoolReserves(ctx context.Context, pairAddress common.Address) (*big.Int, *big.Int, error)
}

type OnChainPoolReservesProvider struct {
	rpcClient *ethclient.Client
}

type Pool struct {
	token0   common.Address
	token1   common.Address
	contract common.Address
}

type PoolsProvider interface {
	// should return only pools with $500k liquidity or more
	GetPools(ctx context.Context) ([]Pool, error)
}

type OnChainPoolsProvider struct {
	tradingPairProvider TradingPairProvider
	topTokensProvider   TopTokensProvider
}

func (p *OnChainPoolsProvider) GetPools(ctx context.Context) ([]Pool, error) {
	tokens, err := p.topTokensProvider.GetTopTokens(ctx)
	if err != nil {
		return nil, err
	}
	pools := []Pool{}
	for token := range tokens {
		for otherToken := token + 1; otherToken < len(tokens); otherToken++ {
			if tokens[token].String() == tokens[otherToken].String() {
				continue
			}

			pairAddress, err := p.tradingPairProvider.GetTradingPair(ctx, tokens[token], tokens[otherToken])
			if err != nil {
				return nil, err
			}
			pool := Pool{
				token0:   tokens[token],
				token1:   tokens[otherToken],
				contract: pairAddress,
			}
			pools = append(pools, pool)
		}
	}
	return pools, nil
}

type ExchangeRateProvider interface {
	GetExchangeRate(ctx context.Context, tokenA, tokenB common.Address) (*big.Float, error)
}

type OnChainExchangeRateProvider struct {
	pairProvider          TradingPairProvider
	poolReservesProvider  PoolReservesProvider
	tokenDecimalsProvider TokenDecimalsProvider
}

func (f *OnChainExchangeRateProvider) GetExchangeRate(ctx context.Context, tokenA, tokenB common.Address) (*big.Float, error) {
	if tokenA.String() == tokenB.String() {
		return nil, errors.New(fmt.Sprintf("tokenA %v and tokenB %v cannot be the same", tokenA.String(), tokenB))
	}
	pairAddress, _ := f.pairProvider.GetTradingPair(ctx, tokenA, tokenB)
	tokenAMagnitude, _ := new(big.Int).SetString(tokenA.String()[2:], 16)
	tokenBMagnitude, _ := new(big.Int).SetString(tokenB.String()[2:], 16)
	decimalsA, _ := f.tokenDecimalsProvider.GetTokenDecimals(ctx, tokenA)
	decimalsB, _ := f.tokenDecimalsProvider.GetTokenDecimals(ctx, tokenB)
	reserve0, reserve1, err := f.poolReservesProvider.GetPoolReserves(ctx, pairAddress)
	if err != nil {
		return nil, err
	}
	// If tokenA is less than tokenB, then tokenA will be Reserve0
	// Otherwise, tokenB will be Reserve0 and tokenA will be Reserve1
	if tokenAMagnitude.Cmp(tokenBMagnitude) == -1 {
		tokenAReserve := toEighteenDecimals(tokenA, reserve0, decimalsA)
		tokenBReserve := toEighteenDecimals(tokenB, reserve1, decimalsB)
		price := new(big.Float).Quo(new(big.Float).SetInt(tokenBReserve), new(big.Float).SetInt(tokenAReserve))
		return price, nil
	} else if tokenAMagnitude.Cmp(tokenBMagnitude) == 1 {
		tokenAReserve := toEighteenDecimals(tokenA, reserve1, decimalsA)
		tokenBReserve := toEighteenDecimals(tokenB, reserve0, decimalsB)
		price := new(big.Float).Quo(new(big.Float).SetInt(tokenBReserve), new(big.Float).SetInt(tokenAReserve))
		return price, nil
	} else {
		return nil, errors.New("tokenA and tokenB cannot be the same")
	}
}

type TokenDecimalsProvider interface {
	GetTokenDecimals(ctx context.Context, tokenAddress common.Address) (uint8, error)
}

type OnChainTokenDecimalsProvider struct {
	rpcClient *ethclient.Client
}

func (f *OnChainTokenDecimalsProvider) GetTokenDecimals(ctx context.Context, tokenAddress common.Address) (uint8, error) {
	caller, err := NewMainCaller(tokenAddress, f.rpcClient)
	if err != nil {
		return 0, err
	}
	callOpts := &bind.CallOpts{
		Context: ctx,
		Pending: false,
	}
	decimals, err := caller.Decimals(callOpts)
	if err != nil {
		return 0, err
	}
	return decimals, nil
}

func (f *OnChainPoolReservesProvider) GetPoolReserves(ctx context.Context, pairAddress common.Address) (*big.Int, *big.Int, error) {
	caller, err := NewMainCaller(pairAddress, f.rpcClient)
	if err != nil {
		log.Fatal(err)
	}
	callOpts := &bind.CallOpts{
		Context: ctx,
		Pending: false,
	}
	resp, err := caller.GetReserves(callOpts)
	if err != nil {
		return nil, nil, err
	}
	return resp.Reserve0, resp.Reserve1, nil
}

func main() {
	rpcClient := getEthClient()
	factoryCaller, _ := factory.NewFactoryCaller(common.HexToAddress(FACTORY_ADDRESS), rpcClient)
	pairProvider := &OnChainTradingPairProvider{
		factoryCaller: *factoryCaller,
		rpcClient:     rpcClient,
	}
	poolReservesProvider := &OnChainPoolReservesProvider{
		rpcClient: rpcClient,
	}
	tokenDecimalsProvider := &OnChainTokenDecimalsProvider{
		rpcClient: rpcClient,
	}
	exchangeRateProvider := &OnChainExchangeRateProvider{
		pairProvider:          pairProvider,
		poolReservesProvider:  poolReservesProvider,
		tokenDecimalsProvider: tokenDecimalsProvider,
	}
	topTokensProvider := &StaticTopTokensProvider{}
	poolsProvider := &OnChainPoolsProvider{
		tradingPairProvider: pairProvider,
		topTokensProvider:   topTokensProvider,
	}
	router := &OnChainV2Router{
		rateProvider:          exchangeRateProvider,
		poolProvider:          poolsProvider,
		tradingPairProvider:   pairProvider,
		poolReservesProvider:  poolReservesProvider,
		tokenDecimalsProvider: tokenDecimalsProvider,
	}

	fmt.Print("Enter tokenA address: ")
	var tokenAInput string
	fmt.Scanln(&tokenAInput)
	if !common.IsHexAddress(tokenAInput) {
		log.Fatal("Invalid tokenA address")
	}
	tokenA := common.HexToAddress(tokenAInput)

	fmt.Print("Enter tokenB address: ")
	var tokenBInput string
	fmt.Scanln(&tokenBInput)
	if tokenBInput == tokenAInput {
		log.Fatal("tokenA and tokenB cannot be the same")
	}
	if !common.IsHexAddress(tokenBInput) {
		log.Fatal("Invalid tokenB address")
	}
	tokenB := common.HexToAddress(tokenBInput)

	price, _ := exchangeRateProvider.GetExchangeRate(context.Background(), tokenA, tokenB)
	fmt.Println("1", tokenAInput, "token equals", price, tokenBInput, "tokens")
	fmt.Println("routing with multiple hops")
	bestPrice, path, err := router.Route(context.Background(), tokenA, tokenB, 5)
	if err != nil {
		fmt.Println("error routing", err)
	}
	fmt.Println("best price:", bestPrice)
	fmt.Println("best path:", path)
}

func getEthClient() *ethclient.Client {
	client, err := ethclient.Dial(MAINNET_INFURA_RPC)
	if err != nil {
		log.Fatal(err)
	}

	return client
}

func toEighteenDecimals(tokenAddress common.Address, amount *big.Int, decimals uint8) *big.Int {
	if decimals == 18 {
		return amount
	}
	return new(big.Int).Mul(amount, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(18-decimals)), nil))
}

func calculatePrice(reserve0, reserve1 *big.Int, decimalsA, decimalsB uint8, inputAmount *big.Int) *big.Float {
	tokenAReserve := toEighteenDecimals(common.Address{}, reserve0, decimalsA)
	tokenBReserve := toEighteenDecimals(common.Address{}, reserve1, decimalsB)
	price := new(big.Float).Quo(new(big.Float).SetInt(tokenBReserve), new(big.Float).SetInt(tokenAReserve))
	return price
}
