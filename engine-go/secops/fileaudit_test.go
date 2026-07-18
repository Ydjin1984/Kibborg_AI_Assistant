package secops

import "testing"

func TestAuditFile_KnownHashes(t *testing.T) {
	fa := AuditFile("hello.txt", []byte("hello"))
	if fa.SHA256 != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Errorf("SHA256=%s", fa.SHA256)
	}
	if fa.SHA1 != "aaf4c61ddcc5e8a2dabede0f3b482cd9aea9434d" {
		t.Errorf("SHA1=%s", fa.SHA1)
	}
	if fa.MD5 != "5d41402abc4b2a76b9719d911017c592" {
		t.Errorf("MD5=%s", fa.MD5)
	}
}

func TestAuditFile_Entropy(t *testing.T) {
	// All identical bytes → entropy 0.
	if fa := AuditFile("zeros", make([]byte, 1024)); fa.Entropy != 0 {
		t.Errorf("энтропия одинаковых байт должна быть 0, got %.3f", fa.Entropy)
	}
	// Every byte value once → maximum entropy 8.0.
	full := make([]byte, 256)
	for i := range full {
		full[i] = byte(i)
	}
	if fa := AuditFile("full", full); fa.Entropy < 7.99 {
		t.Errorf("энтропия равномерных 256 байт ≈8, got %.3f", fa.Entropy)
	}
}

func TestAuditFile_TypeSniff(t *testing.T) {
	if fa := AuditFile("x.exe", []byte{'M', 'Z', 0x90, 0x00}); !fa.Executable {
		t.Errorf("MZ должен определиться как исполняемый, got %+v", fa)
	}
	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A}
	if fa := AuditFile("x.png", png); fa.FileType != "PNG" {
		t.Errorf("ожидался PNG, got %s", fa.FileType)
	}
	if fa := AuditFile("readme", []byte("just some readable text content here")); fa.FileType != "текст/код" {
		t.Errorf("ожидался текст/код, got %s", fa.FileType)
	}
}
