package auth

import "testing"

func TestPasswordHash(t *testing.T) {
	hash, err := HashPassword("pass-1234")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(hash, "pass-1234") || CheckPassword(hash, "wrong") {
		t.Fatal("password check mismatch")
	}
	long := string(make([]byte, 200))
	hash, err = HashPassword(long)
	if err != nil || !CheckPassword(hash, long) {
		t.Fatalf("long password failed: %v", err)
	}
}
