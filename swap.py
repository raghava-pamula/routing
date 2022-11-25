pairs = [('A', 'B', 1), ('B', 'C', 2), ('A', 'C', 1.5), ('B', 'D', 2), ('D', 'A', 3)]
nextCurrencyMap = {}
def initMap():
    for pair in pairs:
        if(not(pair[0] in nextCurrencyMap)):
            nextCurrencyMap[pair[0]] = []
        nextCurrencyMap[pair[0]].append((pair[1], pair[2]))
initMap()
def swap(amountIn, currencyA, currencyB, maxSwaps=3):
    if(currencyA == currencyB):
        return amountIn
    if(maxSwaps == 0):
        return -1
    if(not currencyA in nextCurrencyMap):
        return -1
    bestPrice = -1
    for nextCurrency, tradingRate in nextCurrencyMap[currencyA]:
        amountOut = amountIn * tradingRate
        bestPrice = max(bestPrice, swap(amountOut, nextCurrency, currencyB, maxSwaps-1))
    return bestPrice

print("10 A to B, expected: 10, got: " + str(swap(10, 'A', 'B')))
print("10 A to C, expected: 15, got: " + str(swap(10, 'A', 'C')))
print("10 B to C, expected: 20, got: " + str(swap(10, 'B', 'C')))
print("10 B to A, expected: -1, got: " + str(swap(10, 'B', 'A')))
