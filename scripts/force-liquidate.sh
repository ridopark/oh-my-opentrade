#!/bin/bash

# Configuration - Update these with your Paper keys
API_KEY="PK5XDG5CFUABPGU465IPQVTIHM"
API_SECRET="2EqxsBLpcbK3vFotJ9UAjnXSR3u77Kmogint5Houi7Ev"
BASE_URL="https://paper-api.alpaca.markets"

SYMBOL=$1

if [ -z "$SYMBOL" ]; then
    echo "Usage: ./liquidate.sh <SYMBOL> (e.g., ./liquidate.sh BTCUSD)"
    exit 1
fi

echo "--- Forcing Liquidation for $SYMBOL ---"

# 1. Cancel all open orders for this symbol to release share 'locks'
echo "Step 1: Canceling open orders..."
curl -s -X DELETE "$BASE_URL/v2/orders?symbol=$SYMBOL" \
    -H "APCA-API-KEY-ID: $API_KEY" \
    -H "APCA-API-SECRET-KEY: $API_SECRET"

# 2. Liquidate the actual position
echo "Step 2: Closing position..."
curl -s -X DELETE "$BASE_URL/v2/positions/$SYMBOL" \
    -H "APCA-API-KEY-ID: $API_KEY" \
    -H "APCA-API-SECRET-KEY: $API_SECRET" \
    -H "Content-Type: application/json"

echo "Done. Check your dashboard."