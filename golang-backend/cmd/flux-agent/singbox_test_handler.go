package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter/endpoint"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/adapter/outbound"
	serviceAdapter "github.com/sagernet/sing-box/adapter/service"
	"github.com/sagernet/sing-box/dns"
	dnsLocal "github.com/sagernet/sing-box/dns/transport/local"
	"github.com/sagernet/sing-box/option"
	protocolAnytls "github.com/sagernet/sing-box/protocol/anytls"
	protocolDirect "github.com/sagernet/sing-box/protocol/direct"
	protocolHttp "github.com/sagernet/sing-box/protocol/http"
	protocolHysteria2 "github.com/sagernet/sing-box/protocol/hysteria2"
	protocolShadowsocks "github.com/sagernet/sing-box/protocol/shadowsocks"
	protocolSocks "github.com/sagernet/sing-box/protocol/socks"
	protocolTrojan "github.com/sagernet/sing-box/protocol/trojan"
	protocolTuic "github.com/sagernet/sing-box/protocol/tuic"
	protocolVless "github.com/sagernet/sing-box/protocol/vless"
	protocolVmess "github.com/sagernet/sing-box/protocol/vmess"
	protocolWireguard "github.com/sagernet/sing-box/protocol/wireguard"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type singboxTestReq struct {
	RequestID string                 `json:"requestId"`
	Mode      string                 `json:"mode"` // connect | speed
	URL       string                 `json:"url"`
	Duration  int                    `json:"duration"`
	Outbound  map[string]interface{} `json:"outbound"`
}

func runSingboxTest(req singboxTestReq) map[string]any {
	result := map[string]any{
		"success": false,
		"mode":    req.Mode,
	}
	log.Printf("{\"event\":\"singbox_test_stage\",\"stage\":\"start\",\"mode\":%q}", req.Mode)
	outbound := req.Outbound
	if outbound == nil {
		result["message"] = "missing outbound"
		return result
	}
	logOutboundSummary(outbound)
	result["outbound"] = outboundSummary(outbound)
	outbound["tag"] = "proxy"

	cfg := map[string]interface{}{
		"log": map[string]interface{}{
			"disabled": true,
		},
		"outbounds": []interface{}{
			outbound,
			map[string]interface{}{"type": "direct", "tag": "DIRECT"},
		},
		"route": map[string]interface{}{
			"final": "proxy",
		},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		result["message"] = err.Error()
		return result
	}
	log.Printf("{\"event\":\"singbox_test_stage\",\"stage\":\"config_built\"}")
	ctx := singboxContext()
	var opts option.Options
	if err := opts.UnmarshalJSONContext(ctx, raw); err != nil {
		result["message"] = err.Error()
		result["hint"] = classifySingboxError(err, outbound)
		return result
	}
	log.Printf("{\"event\":\"singbox_test_stage\",\"stage\":\"options_unmarshaled\"}")
	sb, err := box.New(box.Options{Options: opts, Context: ctx})
	if err != nil {
		result["message"] = err.Error()
		result["hint"] = classifySingboxError(err, outbound)
		return result
	}
	log.Printf("{\"event\":\"singbox_test_stage\",\"stage\":\"box_created\"}")
	if err := sb.Start(); err != nil {
		_ = sb.Close()
		result["message"] = err.Error()
		result["hint"] = classifySingboxError(err, outbound)
		return result
	}
	log.Printf("{\"event\":\"singbox_test_stage\",\"stage\":\"box_started\"}")
	defer sb.Close()
	ob, ok := sb.Outbound().Outbound("proxy")
	if !ok {
		result["message"] = "proxy outbound not found"
		return result
	}
	log.Printf("{\"event\":\"singbox_test_stage\",\"stage\":\"outbound_ready\"}")

	transport := &http.Transport{
		DisableCompression: true,
		TLSClientConfig:    &tls.Config{},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, portStr, err := net.SplitHostPort(addr)
			if err != nil {
				dest := M.ParseSocksaddr(addr)
				return ob.DialContext(ctx, network, dest)
			}
			dest := M.ParseSocksaddrHostPortStr(host, portStr)
			return ob.DialContext(ctx, network, dest)
		},
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   12 * time.Second,
	}
	log.Printf("{\"event\":\"singbox_test_stage\",\"stage\":\"http_client_ready\"}")

	url := strings.TrimSpace(req.URL)
	if url == "" {
		url = "http://www.google.com/generate_204"
	}

	switch strings.ToLower(req.Mode) {
	case "speed":
		dur := req.Duration
		if dur <= 0 {
			dur = 3
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(dur+8)*time.Second)
		defer cancel()
		reqHTTP, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		reqHTTP.Header.Set("Accept-Encoding", "identity")
		log.Printf("{\"event\":\"singbox_test_stage\",\"stage\":\"http_request_start\"}")
		start := time.Now()
		resp, err := client.Do(reqHTTP)
		if err != nil {
			result["message"] = err.Error()
			result["hint"] = classifySingboxError(err, outbound)
			return result
		}
		log.Printf("{\"event\":\"singbox_test_stage\",\"stage\":\"http_response_ready\",\"status\":%d}", resp.StatusCode)
		defer resp.Body.Close()
		buf := make([]byte, 32*1024)
		var total int64
		for {
			if time.Since(start) >= time.Duration(dur)*time.Second {
				break
			}
			n, err := resp.Body.Read(buf)
			if n > 0 {
				total += int64(n)
			}
			if err != nil {
				if err == io.EOF {
					break
				}
				result["message"] = err.Error()
				result["hint"] = classifySingboxError(err, outbound)
				return result
			}
		}
		elapsed := time.Since(start)
		if elapsed <= 0 {
			elapsed = time.Duration(dur) * time.Second
		}
		mbps := float64(total*8) / (elapsed.Seconds() * 1e6)
		result["success"] = true
		result["speedMbps"] = mbps
		return result
	default:
		log.Printf("{\"event\":\"singbox_test_stage\",\"stage\":\"http_request_start\"}")
		start := time.Now()
		resp, err := client.Get(url)
		lat := time.Since(start).Milliseconds()
		if err != nil {
			result["message"] = err.Error()
			result["hint"] = classifySingboxError(err, outbound)
			return result
		}
		log.Printf("{\"event\":\"singbox_test_stage\",\"stage\":\"http_response_ready\",\"status\":%d}", resp.StatusCode)
		_, _ = io.CopyN(io.Discard, resp.Body, 4096)
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			result["message"] = fmt.Sprintf("http status %d", resp.StatusCode)
			result["hint"] = "目标返回非 2xx"
			result["latencyMs"] = float64(lat)
			return result
		}
		result["success"] = true
		result["latencyMs"] = float64(lat)
		return result
	}
}

func logOutboundSummary(outbound map[string]interface{}) {
	if outbound == nil {
		return
	}
	typ, _ := outbound["type"].(string)
	server, _ := outbound["server"].(string)
	if server == "" {
		server, _ = outbound["address"].(string)
	}
	port := outbound["server_port"]
	if port == nil {
		port = outbound["port"]
	}
	sni, _ := outbound["sni"].(string)
	if sni == "" {
		if tlsCfg, ok := outbound["tls"].(map[string]interface{}); ok {
			if v, ok2 := tlsCfg["server_name"].(string); ok2 {
				sni = v
			}
		}
	}
	log.Printf("{\"event\":\"singbox_test_outbound\",\"type\":%q,\"server\":%q,\"port\":%v,\"sni\":%q}", typ, server, port, sni)
}

func outboundSummary(outbound map[string]interface{}) map[string]any {
	if outbound == nil {
		return nil
	}
	typ, _ := outbound["type"].(string)
	server, _ := outbound["server"].(string)
	if server == "" {
		server, _ = outbound["address"].(string)
	}
	port := outbound["server_port"]
	if port == nil {
		port = outbound["port"]
	}
	sni, _ := outbound["sni"].(string)
	if sni == "" {
		if tlsCfg, ok := outbound["tls"].(map[string]interface{}); ok {
			if v, ok2 := tlsCfg["server_name"].(string); ok2 {
				sni = v
			}
		}
	}
	return map[string]any{
		"type":   typ,
		"server": server,
		"port":   port,
		"sni":    sni,
	}
}

func classifySingboxError(err error, outbound map[string]interface{}) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "connection refused") {
		return "入口端口未监听或被防火墙拒绝"
	}
	if strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "context deadline exceeded") {
		return "入口端口不可达/被防火墙拦截/路由不通"
	}
	if strings.Contains(msg, "no such host") || strings.Contains(msg, "name or service not known") {
		return "入口地址解析失败"
	}
	if strings.Contains(msg, "tls required") || strings.Contains(msg, "tls") && strings.Contains(msg, "handshake") {
		return "TLS/SNI 配置不正确或服务端不支持"
	}
	if strings.Contains(msg, "authentication") || strings.Contains(msg, "auth failed") {
		return "认证失败（可能是密码/用户名/UUID 不正确）"
	}
	if strings.Contains(msg, "invalid uuid") {
		return "UUID 格式不正确"
	}
	if strings.Contains(msg, "decrypt") || strings.Contains(msg, "cipher") {
		return "加密方式或密码不匹配"
	}
	if strings.Contains(msg, "unsupported") || strings.Contains(msg, "unknown") {
		return "协议参数不被 sing-box 支持"
	}
	if strings.Contains(msg, "failed to create session") {
		return "会话建立失败（入口不可达或协议不匹配）"
	}
	if outbound != nil {
		if typ, _ := outbound["type"].(string); typ != "" {
			return fmt.Sprintf("连接失败（请检查协议与参数: %s）", typ)
		}
	}
	return "连接失败，请检查协议/参数/端口"
}

var (
	singboxCtxOnce sync.Once
	singboxCtx     context.Context
)

func singboxContext() context.Context {
	singboxCtxOnce.Do(func() {
		outboundRegistry := outbound.NewRegistry()
		protocolDirect.RegisterOutbound(outboundRegistry)
		protocolAnytls.RegisterOutbound(outboundRegistry)
		protocolShadowsocks.RegisterOutbound(outboundRegistry)
		protocolVmess.RegisterOutbound(outboundRegistry)
		protocolVless.RegisterOutbound(outboundRegistry)
		protocolTrojan.RegisterOutbound(outboundRegistry)
		protocolHysteria2.RegisterOutbound(outboundRegistry)
		protocolTuic.RegisterOutbound(outboundRegistry)
		protocolSocks.RegisterOutbound(outboundRegistry)
		protocolHttp.RegisterOutbound(outboundRegistry)
		protocolWireguard.RegisterOutbound(outboundRegistry)

		inboundRegistry := inbound.NewRegistry()
		endpointRegistry := endpoint.NewRegistry()
		serviceRegistry := serviceAdapter.NewRegistry()
		dnsRegistry := dns.NewTransportRegistry()
		dnsLocal.RegisterTransport(dnsRegistry)

		ctx := service.ContextWithDefaultRegistry(context.Background())
		singboxCtx = box.Context(ctx, inboundRegistry, outboundRegistry, endpointRegistry, dnsRegistry, serviceRegistry)
	})
	return singboxCtx
}
