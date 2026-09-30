package auth

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMasterSecretGeneratedOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")

	s1, err := MasterSecret(path)
	if err != nil {
		t.Fatalf("first MasterSecret: %v", err)
	}
	if len(s1) != 32 {
		t.Fatalf("secret length: want 32, got %d", len(s1))
	}

	// file created with 0600
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("master key perms: want 0600, got %o", perm)
	}

	// second load returns the same secret
	s2, err := MasterSecret(path)
	if err != nil {
		t.Fatalf("second MasterSecret: %v", err)
	}
	if !bytes.Equal(s1, s2) {
		t.Fatal("master secret should be stable across loads")
	}
}

func TestMasterSecretRejectsWrongSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := MasterSecret(path); err == nil {
		t.Fatal("expected error for wrong-size secret")
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	master := make([]byte, 32)
	for i := range master {
		master[i] = byte(i)
	}
	c, err := NewCipher(master, PurposeProviderKeys)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}

	for _, pt := range [][]byte{
		[]byte("sk-proj-abcdef0123456789"),
		[]byte(""),
		bytes.Repeat([]byte("x"), 4096),
	} {
		env, err := c.Encrypt(pt)
		if err != nil {
			t.Fatalf("Encrypt: %v", err)
		}
		got, err := c.Decrypt(env)
		if err != nil {
			t.Fatalf("Decrypt: %v", err)
		}
		if !bytes.Equal(got, pt) {
			t.Fatalf("round-trip mismatch: want %q, got %q", pt, got)
		}
	}
}

func TestEncryptIsRandomized(t *testing.T) {
	master := make([]byte, 32)
	c, _ := NewCipher(master, PurposeProviderKeys)
	e1, _ := c.Encrypt([]byte("same plaintext"))
	e2, _ := c.Encrypt([]byte("same plaintext"))
	if bytes.Equal(e1, e2) {
		t.Fatal("nonce reuse: identical ciphertexts for identical plaintext")
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	master1 := bytes.Repeat([]byte{1}, 32)
	master2 := bytes.Repeat([]byte{2}, 32)
	c1, _ := NewCipher(master1, PurposeProviderKeys)
	c2, _ := NewCipher(master2, PurposeProviderKeys)

	env, _ := c1.Encrypt([]byte("secret material"))
	if _, err := c2.Decrypt(env); err == nil {
		t.Fatal("decrypt with wrong key must fail")
	}
}

func TestDecryptTamperFails(t *testing.T) {
	master := make([]byte, 32)
	c, _ := NewCipher(master, PurposeProviderKeys)
	env, _ := c.Encrypt([]byte("secret material"))

	// flip a bit in the ciphertext body
	tampered := make([]byte, len(env))
	copy(tampered, env)
	tampered[len(tampered)-1] ^= 0xFF
	if _, err := c.Decrypt(tampered); err == nil {
		t.Fatal("tampered envelope must fail authentication")
	}
}

func TestDecryptRejectsMalformed(t *testing.T) {
	master := make([]byte, 32)
	c, _ := NewCipher(master, PurposeProviderKeys)

	if _, err := c.Decrypt(nil); err == nil {
		t.Fatal("nil envelope must fail")
	}
	if _, err := c.Decrypt([]byte{0x02, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}); err == nil {
		t.Fatal("unknown version must fail")
	}
}

func TestPurposesAreIsolated(t *testing.T) {
	master := make([]byte, 32)
	ck, _ := NewCipher(master, PurposeProviderKeys)
	other, _ := NewCipher(master, Purpose("onegate/other/v1"))
	env, _ := ck.Encrypt([]byte("data"))
	if _, err := other.Decrypt(env); err == nil {
		t.Fatal("keys from different purposes must not decrypt each other")
	}
}

func TestMaskKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"short", "*****"},
		{"12345678", "********"},
		{"sk-proj-abcdefgh", "sk-p…efgh"},
	}
	for _, tc := range cases {
		if got := MaskKey(tc.in); got != tc.want {
			t.Errorf("MaskKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if strings.Contains(MaskKey("sk-proj-abcdefgh"), "abc") {
		t.Fatal("mask must not leak middle of the key")
	}
}
