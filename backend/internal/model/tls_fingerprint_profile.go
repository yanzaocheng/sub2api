// Package model 定义服务层使用的数据模型。
package model

import (
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

// TLSFingerprintProfile TLS 指纹配置模板
// 包含完整的 ClientHello 参数，用于模拟特定客户端的 TLS 握手特征
type TLSFingerprintProfile struct {
	ID                  int64     `json:"id"`
	Name                string    `json:"name"`
	Description         *string   `json:"description"`
	EnableGREASE        bool      `json:"enable_grease"`
	CipherSuites        []uint16  `json:"cipher_suites"`
	Curves              []uint16  `json:"curves"`
	PointFormats        []uint16  `json:"point_formats"`
	SignatureAlgorithms []uint16  `json:"signature_algorithms"`
	ALPNProtocols       []string  `json:"alpn_protocols"`
	SupportedVersions   []uint16  `json:"supported_versions"`
	KeyShareGroups      []uint16  `json:"key_share_groups"`
	PSKModes            []uint16  `json:"psk_modes"`
	Extensions          []uint16  `json:"extensions"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// Validate 验证模板配置的有效性
func (p *TLSFingerprintProfile) Validate() error {
	if p == nil {
		return &ValidationError{Field: "profile", Message: "profile is required"}
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return &ValidationError{Field: "name", Message: "name is required"}
	}
	if len(p.Name) > 128 {
		return &ValidationError{Field: "name", Message: "name must be at most 128 characters"}
	}
	if err := validateUint16List("cipher_suites", p.CipherSuites, false, false); err != nil {
		return err
	}
	if err := validateUint16List("curves", p.Curves, false, false); err != nil {
		return err
	}
	if err := validateUint16List("point_formats", p.PointFormats, true, true); err != nil {
		return err
	}
	for _, pointFormat := range p.PointFormats {
		if pointFormat > 2 {
			return &ValidationError{Field: "point_formats", Message: fmt.Sprintf("unsupported point format %d", pointFormat)}
		}
	}
	if err := validateUint16List("signature_algorithms", p.SignatureAlgorithms, false, false); err != nil {
		return err
	}
	if err := validateUint16List("supported_versions", p.SupportedVersions, false, false); err != nil {
		return err
	}
	for _, version := range p.SupportedVersions {
		if version != 0x0301 && version != 0x0302 && version != 0x0303 && version != 0x0304 {
			return &ValidationError{Field: "supported_versions", Message: fmt.Sprintf("unsupported TLS version 0x%04x", version)}
		}
	}
	if err := validateUint16List("key_share_groups", p.KeyShareGroups, false, false); err != nil {
		return err
	}
	if err := validateUint16List("psk_modes", p.PSKModes, true, true); err != nil {
		return err
	}
	for _, mode := range p.PSKModes {
		if mode > 1 {
			return &ValidationError{Field: "psk_modes", Message: fmt.Sprintf("unsupported PSK mode %d", mode)}
		}
	}
	if err := validateUint16List("extensions", p.Extensions, false, true); err != nil {
		return err
	}
	seenALPN := make(map[string]struct{}, len(p.ALPNProtocols))
	for _, rawProtocol := range p.ALPNProtocols {
		protocol := strings.TrimSpace(rawProtocol)
		if rawProtocol != protocol || protocol == "" || len(protocol) > 255 || strings.ContainsAny(protocol, "\r\n") {
			return &ValidationError{Field: "alpn_protocols", Message: fmt.Sprintf("invalid protocol %q", protocol)}
		}
		if _, ok := seenALPN[protocol]; ok {
			return &ValidationError{Field: "alpn_protocols", Message: fmt.Sprintf("duplicate protocol %q", protocol)}
		}
		seenALPN[protocol] = struct{}{}
	}
	return nil
}

func validateUint16List(field string, values []uint16, byteSized, allowZero bool) *ValidationError {
	seen := make(map[uint16]struct{}, len(values))
	for _, value := range values {
		if value == 0 && !allowZero {
			return &ValidationError{Field: field, Message: "values must be non-zero"}
		}
		if byteSized && value > 255 {
			return &ValidationError{Field: field, Message: fmt.Sprintf("value %d exceeds 255", value)}
		}
		if _, ok := seen[value]; ok {
			return &ValidationError{Field: field, Message: fmt.Sprintf("duplicate value %d", value)}
		}
		seen[value] = struct{}{}
	}
	return nil
}

// ToTLSProfile 将领域模型转换为运行时使用的 tlsfingerprint.Profile
// 空切片字段会在 dialer 中 fallback 到内置默认值
func (p *TLSFingerprintProfile) ToTLSProfile() *tlsfingerprint.Profile {
	return &tlsfingerprint.Profile{
		Name:                p.Name,
		EnableGREASE:        p.EnableGREASE,
		CipherSuites:        p.CipherSuites,
		Curves:              p.Curves,
		PointFormats:        p.PointFormats,
		SignatureAlgorithms: p.SignatureAlgorithms,
		ALPNProtocols:       p.ALPNProtocols,
		SupportedVersions:   p.SupportedVersions,
		KeyShareGroups:      p.KeyShareGroups,
		PSKModes:            p.PSKModes,
		Extensions:          p.Extensions,
	}
}
