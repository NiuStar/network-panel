package controller

import (
	"encoding/json"
	"errors"
	"strings"
)

// requiredAnyTLSCertificatePin validates leaf-certificate SHA-256 trust, not a
// uTLS client fingerprint. It never discovers trust from the network.
func requiredAnyTLSCertificatePin(params map[string]interface{}) (string, error) {
	var pin string
	var walk func(map[string]interface{}, bool) error
	var walkValue func(interface{}) error
	walkValue = func(value interface{}) error {
		switch nested := value.(type) {
		case map[string]interface{}:
			return walk(nested, false)
		case []interface{}:
			for _, entry := range nested {
				if err := walkValue(entry); err != nil {
					return err
				}
			}
		}
		return nil
	}
	walk = func(values map[string]interface{}, root bool) error {
		for key, value := range values {
			key = strings.ToLower(strings.TrimSpace(key))
			key = strings.NewReplacer("-", "", "_", "", " ", "").Replace(key)
			switch key {
			case "skipcertverify", "skipverify", "skiptlsverify", "insecure", "allowinsecure", "tlsinsecure", "insecureskipverify", "tlsverificationdisabled":
				// Only explicit false is safe; malformed values cannot enable a bypass.
				safe := false
				if b, ok := value.(bool); ok {
					safe = !b
				}
				if s, ok := value.(string); ok {
					safe = strings.EqualFold(strings.TrimSpace(s), "false")
				}
				if !safe {
					return errors.New("AnyTLS 不允许跳过证书验证")
				}
			case "tls", "verify", "verifycertificate", "certverify":
				if disabled, ok := value.(bool); ok && !disabled {
					return errors.New("AnyTLS 不允许关闭 TLS 或证书验证")
				}
			case "certsha256", "fingerprintsha256", "servercertfingerprintsha256", "certificatefingerprint", "fingerprint":
				raw, ok := value.(string)
				candidate := normalizeAnyTLSCertSHA256Pin(raw)
				if !root || !ok || candidate == "" || (pin != "" && pin != candidate) {
					return errors.New("AnyTLS 证书 SHA-256 指纹无效或冲突")
				}
				pin = candidate
			case "clash", "clashmeta", "singbox", "surge", "uri", "url", "shadowrocket", "loon", "qx", "quantumultx", "stash":
				return errors.New("AnyTLS 不允许原始客户端配置覆盖证书信任")
			}
			if err := walkValue(value); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(params, true); err != nil {
		return "", err
	}
	if pin == "" {
		return "", errors.New("AnyTLS 必须提供可信来源的叶证书 SHA-256 指纹 (cert-sha256)")
	}
	return pin, nil
}

// buildPinnedAnyTLSAgentOutbound is the private agent probe wire contract, NOT
// sing-box SDK JSON. Keep the pin here even when that SDK cannot export pins.
func buildPinnedAnyTLSAgentOutbound(item subProxy, params map[string]interface{}) (map[string]interface{}, error) {
	pin, err := requiredAnyTLSCertificatePin(params)
	if err != nil {
		return nil, err
	}
	password := item.Password
	if password == "" {
		password, _ = params["password"].(string)
	}
	password = anyTLSCredentialPassword(password, params)
	if password == "" {
		return nil, errors.New("AnyTLS 缺少密码")
	}
	out := map[string]interface{}{
		"type": "anytls", "server": item.Server, "server_port": item.Port,
		"password": password, "cert-sha256": pin,
	}
	tls := map[string]interface{}{"enabled": true, "insecure": false}
	for _, key := range []string{"sni", "server_name"} {
		if sni := strings.TrimSpace(paramString(params, key)); sni != "" {
			tls["server_name"] = sni
			break
		}
	}
	out["tls"] = tls
	return out, nil
}

// Only external client nodes require a caller-supplied pin. Managed server
// bootstrap issues its leaf through the persistent controller authority.
func validateExternalAnyTLSConfig(protocol *string, raw json.RawMessage) error {
	var params map[string]interface{}
	proto := normalizeProtocol(ptrString(protocol))
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			if proto == "anytls" {
				return errors.New("AnyTLS 配置必须是 JSON 对象")
			}
			return nil
		}
	}
	if proto == "" {
		if value, ok := params["protocol"].(string); ok {
			proto = normalizeProtocol(value)
		}
		if proto == "" {
			if value, ok := params["type"].(string); ok {
				proto = normalizeProtocol(value)
			}
		}
	}
	if proto != "anytls" {
		return nil
	}
	_, err := requiredAnyTLSCertificatePin(params)
	return err
}
