#!/usr/bin/env bash
# Lightweight Bash-based Diagnose Agent for network-panel
# Requirements: websocat, jq, nc(netcat), iperf3, ping

set -euo pipefail

SCHEME="${SCHEME:-ws}"         # ws or wss
ADDR="${ADDR:-}"               # panel ip:port
SECRET="${SECRET:-}"           # node secret
VERSION="bash-agent-1.0"
COUNT_DEFAULT=3
TIMEOUT_MS_DEFAULT=1500

usage() {
  cat <<EOF
Usage: $0 [-a addr:port] [-s secret] [-S ws|wss]
If omitted, reads from /etc/gost/config.json {addr, secret}.
EOF
}

while getopts ":a:s:S:h" opt; do
  case "$opt" in
    a) ADDR="$OPTARG";;
    s) SECRET="$OPTARG";;
    S) SCHEME="$OPTARG";;
    h) usage; exit 0;;
    *) usage; exit 1;;
  esac
done

CONFIG="/etc/gost/config.json"
if [[ -z "${ADDR}" || -z "${SECRET}" ]]; then
  if [[ -f "$CONFIG" ]]; then
    ADDR=${ADDR:-$(jq -r '.addr // empty' "$CONFIG")}
    SECRET=${SECRET:-$(jq -r '.secret // empty' "$CONFIG")}
  fi
fi

if [[ -z "${ADDR}" || -z "${SECRET}" ]]; then
  echo "ERROR: addr/secret missing. Provide via args or $CONFIG" >&2
  exit 1
fi

if ! command -v websocat >/dev/null 2>&1; then
  echo "ERROR: websocat not found (required)" >&2; exit 1
fi
if ! command -v jq >/dev/null 2>&1; then
  echo "ERROR: jq not found (required)" >&2; exit 1
fi

WS_URL="${SCHEME}://${ADDR}/system-info?type=1&secret=${SECRET}&version=${VERSION}"

log() { printf "[%s] %s\n" "$(date '+%F %T')" "$*" >&2; }

is_ipv6() { [[ "$1" == *:* && "$1" != \[*\]* ]]; }

json_escape() { jq -R -s @json; }

run_tcp() {
  local host="$1" port="$2" count="$3" timeout_ms="$4"
  local succ=0 sum_ms=0 i=0
  local to_sec=$(( (timeout_ms + 999) / 1000 ))
  local nc_cmd=(nc -z -v -w "$to_sec")
  is_ipv6 "$host" && nc_cmd=(nc -6 -z -v -w "$to_sec")
  while (( i < count )); do
    local start end
    start=$(date +%s%3N)
    if "${nc_cmd[@]}" "$host" "$port" >/dev/null 2>&1; then
      end=$(date +%s%3N)
      sum_ms=$(( sum_ms + end - start ))
      succ=$(( succ + 1 ))
    fi
    i=$(( i + 1 ))
  done
  local avg=0 loss=100
  if (( succ > 0 )); then
    avg=$(( sum_ms / succ ))
    loss=$(( (count - succ) * 100 / count ))
  fi
  echo "$avg" "$loss" "$succ"
}

run_icmp() {
  local host="$1" count="$2" timeout_ms="$3"
  local ping_cmd=(ping -c "$count" -W $(( (timeout_ms + 999)/1000 )) "$host")
  is_ipv6 "$host" && ping_cmd=(ping -6 -c "$count" -W $(( (timeout_ms + 999)/1000 )) "$host")
  local out; out=$("${ping_cmd[@]}" 2>/dev/null || true)
  # parse loss
  local loss_line; loss_line=$(printf "%s" "$out" | grep -E "(packet loss|received,.*loss)" || true)
  local loss=100
  if [[ -n "$loss_line" ]]; then
    loss=$(printf "%s" "$loss_line" | grep -Eo "[0-9.]+%" | tr -d '%')
  fi
  # parse avg
  local rtt_line; rtt_line=$(printf "%s" "$out" | grep -E "rtt|round-trip" || true)
  local avg=0
  if [[ -n "$rtt_line" ]]; then
    # format: min/avg/max/mdev = a/b/c/d ms
    avg=$(printf "%s" "$rtt_line" | awk -F'/' '{print $(NF-2)}' | awk '{print int($1+0)}')
  fi
  echo "$avg" "$loss"
}

pick_free_port() {
  local p
  for _ in {1..20}; do
    p=$(shuf -i 20000-40000 -n 1)
    if ! nc -z localhost "$p" >/dev/null 2>&1; then
      echo "$p"; return 0
    fi
  done
  echo 5201
}

run_iperf3_server() {
  local port="$1"
  command -v iperf3 >/dev/null 2>&1 || { echo 0; return; }
  if [[ "$port" -eq 0 ]]; then port=$(pick_free_port); fi
  # start daemonized server
  iperf3 -s -D -p "$port" >/dev/null 2>&1 || true
  sleep 1
  echo "$port"
}

run_iperf3_client_bw() {
  local host="$1" port="$2" duration="$3"
  command -v iperf3 >/dev/null 2>&1 || { echo 0; return; }
  local out; out=$(iperf3 -J -R -c "$host" -p "$port" -t "$duration" 2>/dev/null || true)
  if [[ -z "$out" ]]; then echo 0; return; fi
  # prefer received in reverse mode
  local bps; bps=$(printf "%s" "$out" | jq -r '(.end.sum_received.bits_per_second // .end.sum_sent.bits_per_second // 0)')
  if [[ "$bps" == "null" || -z "$bps" ]]; then echo 0; else printf "%.2f" "$(awk -v b="$bps" 'BEGIN{print b/1000000}')"; fi
}

send_reply() {
  local req_id="$1" payload_json="$2"
  printf '{"type":"DiagnoseResult","requestId":%s,"data":%s}\n' "$(printf '%s' "$req_id" | json_escape)" "$payload_json"
}

connect_ws() {
  log "connecting to $WS_URL"
  coproc WS ( websocat -t --ping-interval=30 "$WS_URL" )
  exec 3>&${WS[1]} 4<&${WS[0]}
  log "connected"
  local buf="" depth=0 in_str=0 esc=0
  while :; do
    IFS= read -r -n 1 -u 4 ch || break
    buf+="$ch"
    # JSON framing
    if (( in_str == 1 )); then
      if [[ "$esc" -eq 1 ]]; then esc=0; continue; fi
      if [[ "$ch" == "\\" ]]; then esc=1; continue; fi
      if [[ "$ch" == '"' ]]; then in_str=0; fi
    else
      if [[ "$ch" == '"' ]]; then in_str=1; fi
      if [[ "$ch" == '{' ]]; then depth=$((depth+1)); fi
      if [[ "$ch" == '}' ]]; then depth=$((depth-1)); fi
    fi
    if (( depth == 0 )) && [[ "$buf" == *}* ]]; then
      local line="$buf"; buf=""
      local typ; typ=$(printf '%s' "$line" | jq -r '.type // empty' 2>/dev/null || true)
      if [[ "$typ" == "Diagnose" ]]; then
        log "recv Diagnose: $line"
        local req_id mode host port count timeoutMs ctxjson
        req_id=$(printf '%s' "$line" | jq -r '.data.requestId // ""')
        mode=$(printf '%s' "$line" | jq -r '.data.mode // empty')
        host=$(printf '%s' "$line" | jq -r '.data.host // empty')
        port=$(printf '%s' "$line" | jq -r '.data.port // 0')
        count=$(printf '%s' "$line" | jq -r '.data.count // empty')
        timeoutMs=$(printf '%s' "$line" | jq -r '.data.timeoutMs // empty')
        ctxjson=$(printf '%s' "$line" | jq -c '.data.ctx // {}')
        [[ -z "$count" || "$count" == "null" ]] && count=$COUNT_DEFAULT
        [[ -z "$timeoutMs" || "$timeoutMs" == "null" ]] && timeoutMs=$TIMEOUT_MS_DEFAULT
        if [[ "$mode" == "icmp" ]]; then
          read -r avg loss < <(run_icmp "$host" "$count" "$timeoutMs")
          local ok=false msg="ok"; (( $(printf '%.0f' "$loss") < 100 )) && ok=true || msg="unreachable"
          local data_json; data_json=$(jq -n --argjson s "$ok" --arg avg "$avg" --arg loss "$loss" --arg m "$msg" --argjson ctx "$ctxjson" '{success:$s, averageTime:($avg|tonumber), packetLoss:($loss|tonumber), message:$m, ctx:$ctx}')
          log "send DiagnoseResult icmp: $data_json"
          send_reply "$req_id" "$data_json" >&3
        elif [[ "$mode" == "iperf3" ]]; then
          local is_server; is_server=$(printf '%s' "$line" | jq -r '.data.server // false')
          local is_client; is_client=$(printf '%s' "$line" | jq -r '.data.client // false')
          if [[ "$is_server" == "true" ]]; then
            local chosen; chosen=$(run_iperf3_server "$port")
            local data_json; data_json=$(jq -n --argjson s true --arg p "$chosen" --argjson ctx "$ctxjson" '{success:$s, port:($p|tonumber), message:"server started", ctx:$ctx}')
            log "send DiagnoseResult iperf3-server: $data_json"
            send_reply "$req_id" "$data_json" >&3
          elif [[ "$is_client" == "true" ]]; then
            local duration; duration=$(printf '%s' "$line" | jq -r '.data.duration // 5')
            local bw; bw=$(run_iperf3_client_bw "$host" "$port" "$duration")
            local ok=false; [[ "$bw" != "0" ]] && ok=true
            local data_json; data_json=$(jq -n --argjson s "$ok" --arg bw "$bw" --argjson ctx "$ctxjson" '{success:$s, bandwidthMbps:($bw|tonumber), ctx:$ctx}')
            log "send DiagnoseResult iperf3-client: $data_json"
            send_reply "$req_id" "$data_json" >&3
          else
            send_reply "$req_id" '{"success":false,"message":"unknown iperf3 mode"}' >&3
          fi
        else
          read -r avg loss succ < <(run_tcp "$host" "$port" "$count" "$timeoutMs")
          local ok=false msg="connect fail"; (( succ > 0 )) && ok=true && msg="ok"
          local data_json; data_json=$(jq -n --argjson s "$ok" --arg avg "$avg" --arg loss "$loss" --arg m "$msg" --argjson ctx "$ctxjson" '{success:$s, averageTime:($avg|tonumber), packetLoss:($loss|tonumber), message:$m, ctx:$ctx}')
          log "send DiagnoseResult tcp: $data_json"
          send_reply "$req_id" "$data_json" >&3
        fi
      fi
    fi
  done
}

while true; do
  connect_ws || true
  log "disconnected; retry in 3s"
  sleep 3
done
