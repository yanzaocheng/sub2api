package tlsfingerprint

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	utls "github.com/refraction-networking/utls"
)

// Capture the bytes emitted by the real dialer without an external server,
// certificate trust changes, or a proxy that could replace the ClientHello.
func captureDialerHello(t *testing.T, profile *Profile) []byte {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.SetReadDeadline(time.Now().Add(5 * time.Second))
	dialer := NewDialer(profile, func(context.Context, string, string) (net.Conn, error) {
		return client, nil
	})
	done := make(chan error, 1)
	go func() {
		conn, err := dialer.DialTLSContext(ctx, "tcp", "api.anthropic.com:443")
		if conn != nil {
			conn.Close()
		}
		done <- err
	}()
	var handshake []byte
	for {
		header := make([]byte, 5)
		if _, err := io.ReadFull(server, header); err != nil {
			t.Fatalf("read TLS record header: %v", err)
		}
		if header[0] != 22 {
			t.Fatalf("expected handshake record, got %d", header[0])
		}
		body := make([]byte, binary.BigEndian.Uint16(header[3:]))
		if _, err := io.ReadFull(server, body); err != nil {
			t.Fatalf("read TLS record body: %v", err)
		}
		handshake = append(handshake, body...)
		if len(handshake) >= 4 {
			size := int(handshake[1])<<16 | int(handshake[2])<<8 | int(handshake[3])
			if len(handshake) >= size+4 {
				server.Close()
				if err := <-done; err == nil {
					t.Fatal("expected handshake to stop after ClientHello capture")
				}
				if handshake[0] != 1 {
					t.Fatalf("expected ClientHello, got %d", handshake[0])
				}
				return handshake[4 : size+4]
			}
		}
	}
}

type helloWireReader struct {
	t *testing.T
	b []byte
}

func (r *helloWireReader) take(n int) []byte {
	r.t.Helper()
	if n > len(r.b) {
		r.t.Fatal("truncated ClientHello")
	}
	b := r.b[:n]
	r.b = r.b[n:]
	return b
}

func (r *helloWireReader) u8() int     { return int(r.take(1)[0]) }
func (r *helloWireReader) u16() uint16 { return binary.BigEndian.Uint16(r.take(2)) }
func (r *helloWireReader) vector16() *helloWireReader {
	return &helloWireReader{t: r.t, b: r.take(int(r.u16()))}
}
func (r *helloWireReader) numbers() []uint16 {
	var values []uint16
	for len(r.b) > 0 {
		value := r.u16()
		if !isGREASEValue(value) {
			values = append(values, value)
		}
	}
	return values
}

func fingerprintNumbers(values []uint16, hex bool) string {
	parts := make([]string, len(values))
	for i, value := range values {
		if hex {
			parts[i] = fmt.Sprintf("%04x", value)
		} else {
			parts[i] = fmt.Sprint(value)
		}
	}
	if hex {
		return strings.Join(parts, ",")
	}
	return strings.Join(parts, "-")
}

func TestDefaultDialerMatchesClaudeBunFingerprint(t *testing.T) {
	for _, profile := range []*Profile{nil, {Name: "empty-default"}} {
		name := "nil-profile"
		if profile != nil {
			name = profile.Name
		}
		t.Run(name, func(t *testing.T) {
			r := &helloWireReader{t: t, b: captureDialerHello(t, profile)}
			version := r.u16()
			r.take(32)     // random
			r.take(r.u8()) // session ID
			ciphers := r.vector16().numbers()
			r.take(r.u8()) // compression methods
			exts := r.vector16()
			var extensions, groups, points, signatures, versions, shares, shareSizes []uint16
			var alpn []string
			for len(exts.b) > 0 {
				id := exts.u16()
				data := exts.vector16()
				if isGREASEValue(id) {
					t.Fatalf("default ClientHello contains GREASE extension %04x", id)
				}
				extensions = append(extensions, id)
				switch id {
				case 10:
					groups = data.vector16().numbers()
				case 11:
					for _, point := range data.take(data.u8()) {
						points = append(points, uint16(point))
					}
				case 13:
					signatures = data.vector16().numbers()
				case 16:
					protocols := data.vector16()
					for len(protocols.b) > 0 {
						alpn = append(alpn, string(protocols.take(protocols.u8())))
					}
				case 43:
					versions = (&helloWireReader{t: t, b: data.take(data.u8())}).numbers()
				case 51:
					keyShares := data.vector16()
					for len(keyShares.b) > 0 {
						shares = append(shares, keyShares.u16())
						size := keyShares.u16()
						shareSizes = append(shareSizes, size)
						keyShares.take(int(size))
					}
				}
			}
			ja3Raw := fmt.Sprintf("%d,%s,%s,%s,%s", version, fingerprintNumbers(ciphers, false), fingerprintNumbers(extensions, false), fingerprintNumbers(groups, false), fingerprintNumbers(points, false))
			ja3 := fmt.Sprintf("%x", md5.Sum([]byte(ja3Raw)))
			if ja3 != "1523504b38f0fae0d881d4b6554aac1b" {
				t.Fatalf("JA3 mismatch: %s (%s)", ja3, ja3Raw)
			}
			if !reflect.DeepEqual(groups, []uint16{4588, 29, 23, 24}) || !reflect.DeepEqual(shares, []uint16{4588, 29}) || !reflect.DeepEqual(shareSizes, []uint16{1216, 32}) {
				t.Fatalf("MLKEM groups/shares/sizes mismatch: %v / %v / %v", groups, shares, shareSizes)
			}
			if !reflect.DeepEqual(alpn, []string{"http/1.1"}) || !reflect.DeepEqual(versions, []uint16{772, 771}) {
				t.Fatalf("ALPN/versions mismatch: %v / %v", alpn, versions)
			}
			var sortedExtensions []uint16
			for _, id := range extensions {
				if id != 0 && id != 16 {
					sortedExtensions = append(sortedExtensions, id)
				}
			}
			sort.Slice(ciphers, func(i, j int) bool { return ciphers[i] < ciphers[j] })
			sort.Slice(sortedExtensions, func(i, j int) bool { return sortedExtensions[i] < sortedExtensions[j] })
			cipherHash := fmt.Sprintf("%x", sha256.Sum256([]byte(fingerprintNumbers(ciphers, true))))[:12]
			extensionHash := fmt.Sprintf("%x", sha256.Sum256([]byte(fingerprintNumbers(sortedExtensions, true)+"_"+fingerprintNumbers(signatures, true))))[:12]
			ja4 := fmt.Sprintf("t13d%02d%02dh1_%s_%s", len(ciphers), len(extensions), cipherHash, extensionHash)
			if ja4 != "t13d1713h1_5b57614c22b0_6a3d802a7139" {
				t.Fatalf("JA4 mismatch: %s", ja4)
			}
			t.Logf("JA3=%s JA4=%s", ja3, ja4)
		})
	}
}

func TestLegacyProfileKeepsX25519KeyShare(t *testing.T) {
	spec := buildClientHelloSpecFromProfile(&Profile{Curves: []uint16{29, 23, 24}})
	for _, extension := range spec.Extensions {
		if shares, ok := extension.(*utls.KeyShareExtension); ok {
			if len(shares.KeyShares) != 1 || shares.KeyShares[0].Group != utls.X25519 {
				t.Fatalf("legacy profile key shares: %v", shares.KeyShares)
			}
			return
		}
	}
	t.Fatal("missing key_share extension")
}
