#!/bin/bash
# ============================================================
# Miogram — Get Telegram webhook info (verification)
# ============================================================
# Reports getWebhookInfo for every configured bot. Fill in the
# real tokens below (mirror scripts/set_webhooks.sh).

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'

declare -A PERSIAN=(
  ["main"]="8353134695:AAEiIq8fvWdF9fWC012Chz9y343LbRQUFSY"
  ["shard1"]="8890591504:AAGFXDePeIXiZZRDP1NKN2S397pgxmDrfRM"
  ["shard2"]="8664184348:AAHslg3TZ_inZn5mSba_CuWdZLK8WeU5zz8"
  ["shard3"]="8871871205:AAEX6yzHB81mkyAz5efRxhrkK2fcEUxhp1E"
  ["shard4"]="8652176746:AAHVst2LHYL2i4c0QQjAkX6f8x85q8TCU1Q"
  ["shard5"]="8695650666:AAGBW_FwyEh_boVFtsqxDM4oVNpKN0qMUJs"
)

echo -e "${YELLOW}========================================${NC}"
echo -e "${YELLOW}  Miogram Webhook Status${NC}"
echo -e "${YELLOW}========================================${NC}"

for name in "${!BOTS[@]}"; do
  echo -e "\n${GREEN}${name}:${NC}"
  curl -s "https://api.telegram.org/bot${BOTS[$name]}/getWebhookInfo" | jq '.'
done
