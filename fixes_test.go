package main

import (
	"testing"
)

func TestOpenURLRejectsNonHTTP(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", `C:\Windows\calc.exe`, "javascript:alert(1)"} {
		if openURL(u) == nil {
			t.Errorf("%q should be refused", u)
		}
	}
}
