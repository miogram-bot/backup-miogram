#!/bin/bash
# ============================================================
# Miogram — Set Telegram webhooks with secret_token & max_connections=100
# ============================================================

set -e

GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'

IP="65.108.2.13"
PORT="8443"
CERT_PATH="../ssl/cert.pem"

# ============================================================
# 🔐 WEBHOOK_SECRET باید با مقدار داخل فایل‌های .env.* یکسان باشد
# ============================================================
WEBHOOK_SECRET="kjfnIIUnskjdsugn9823u4"

# توکن‌های ربات‌های فارسی
declare -A PERSIAN=(
  ["main"]="8353134695:AAEiIq8fvWdF9fWC012Chz9y343LbRQUFSY"
  ["shard1"]="8890591504:AAGFXDePeIXiZZRDP1NKN2S397pgxmDrfRM"
  ["shard2"]="8664184348:AAHslg3TZ_inZn5mSba_CuWdZLK8WeU5zz8"
  ["shard3"]="8871871205:AAEX6yzHB81mkyAz5efRxhrkK2fcEUxhp1E"
  ["shard4"]="8652176746:AAHVst2LHYL2i4c0QQjAkX6f8x85q8TCU1Q"
  ["shard5"]="8695650666:AAGBW_FwyEh_boVFtsqxDM4oVNpKN0qMUJs"
)

set_webhook() {
  local token=$1 path=$2 name=$3
  local url="https://${IP}:${PORT}${path}"
  echo -e "${BLUE}▶ Setting ${name}${NC}"
  echo -e "  URL: ${url}"
  echo -e "  secret_token: ${WEBHOOK_SECRET}"
  echo -e "  max_connections: 100"
  echo -e "  Certificate: ${CERT_PATH}"

  # ارسال secret_token و max_connections به تلگرام
  local resp http_code body
  resp=$(curl -s -w "\n%{http_code}" \
    -F "url=${url}" \
    -F "secret_token=${WEBHOOK_SECRET}" \
    -F "max_connections=100" \
    -F "certificate=@${CERT_PATH}" \
    "https://api.telegram.org/bot${token}/setWebhook")
  http_code=$(echo "$resp" | tail -n1)
  body=$(echo "$resp" | head -n-1)

  if [ "$http_code" -eq 200 ] && echo "$body" | grep -q '"ok":true'; then
    echo -e "${GREEN}✅ ${name} - Webhook set successfully${NC}"
  else
    echo -e "${RED}❌ ${name} - HTTP ${http_code}: ${body}${NC}"
  fi
  echo ""
}

echo -e "${YELLOW}========================================${NC}"
echo -e "${YELLOW}  Miogram Webhook Setup${NC}"
echo -e "${YELLOW}  ${IP}:${PORT}  (cert: ${CERT_PATH})${NC}"
echo -e "${YELLOW}  secret_token: ${WEBHOOK_SECRET}${NC}"
echo -e "${YELLOW}  max_connections: 100${NC}"
echo -e "${YELLOW}========================================${NC}"

if [ ! -f "$CERT_PATH" ]; then
  echo -e "${RED}❌ Certificate not found at ${CERT_PATH}${NC}"
  echo -e "${RED}   Create ssl/cert.pem first (see task step 1).${NC}"
  exit 1
fi
echo -e "${GREEN}✅ Certificate found${NC}\n"

for bot in "${!PERSIAN[@]}"; do
  set_webhook "${PERSIAN[$bot]}" "/persian/bot/${bot}" "Persian - ${bot}"
done

echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}✅ Done. Verify with ./get_webhooks.sh${NC}"
echo -e "${GREEN}========================================${NC}"
